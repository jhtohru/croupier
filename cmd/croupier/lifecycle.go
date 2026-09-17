package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/httpapi"
	croupiersqs "github.com/jhtohru/croupier/internal/sqs"
)

// registerPostgresPool ties the pool's lifetime to fx's own, closing it only
// after every other OnStop hook has run — registered first in main.go's
// fx.Invoke list, so (OnStop runs in reverse registration order) its own
// OnStop runs last, once HTTP handlers and background workers still using
// the pool have all finished. Satisfies the challenge spec §4's "fechamento
// das dependências após a finalização dos componentes que as utilizam" —
// previously the pool was never explicitly closed at all.
func registerPostgresPool(lc fx.Lifecycle, pool *pgxpool.Pool) {
	lc.Append(fx.Hook{
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})
}

// registerHTTPServer starts the HTTP server on OnStart and gives it up to
// cfg.ShutdownTimeout to finish in-flight requests on OnStop
// (http.Server.Shutdown) before the process moves on regardless. Binding the
// listener synchronously in OnStart (instead of inside the goroutine, via a
// plain ListenAndServe) means a port-already-in-use error fails fx's own
// startup instead of surfacing later as a silent, already-backgrounded
// goroutine's log line.
func registerHTTPServer(lc fx.Lifecycle, srv *httpapi.Server, cfg *Config) {
	server := &http.Server{
		Handler: srv,
		// Challenge spec §6.0.10: "Operações de I/O devem... respeitar...
		// timeout" — ctx propagation alone (already true everywhere) only
		// covers cancellation; without these, a slow or hung client could
		// hold a connection (and the goroutine serving it) open
		// indefinitely regardless of anything the handler does. Generous
		// on purpose (this app has no large uploads/downloads or
		// long-polling routes) — these exist to bound worst-case
		// connection lifetime, not to be tuned per endpoint.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ln, err := net.Listen("tcp", ":"+cfg.AppPort)
			if err != nil {
				return fmt.Errorf("http server: %w", err)
			}
			go func() {
				if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					slog.Error("http server exited unexpectedly", "error", err)
				}
			}()
			slog.Info("http server listening", "port", cfg.AppPort)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			shutdownCtx, cancel := context.WithTimeout(ctx, cfg.ShutdownTimeout)
			defer cancel()
			return server.Shutdown(shutdownCtx)
		},
	})
}

// registerBackgroundLoop wires a cancellable goroutine into fx's lifecycle:
// OnStart launches run in the background; OnStop cancels its context and
// waits (bounded by cfg.ShutdownTimeout) for it to actually return, so a
// worker mid-processing gets a real chance to finish instead of being
// abandoned the instant the process starts exiting.
//
// If run returns an error other than context cancellation, it's restarted
// after an exponential backoff instead of being left dead for the rest of
// the process's life — this is what satisfies the challenge spec §3's
// "indisponibilidade temporária do... SQS" for internal/sqs.Consumer.Run in
// particular: a ReceiveMessage failure during a LocalStack/SQS blip used to
// end Run permanently (its own doc comment even said a supervisor would
// handle restarts, but until this, none did). The other three loops
// registered through this function (outbox worker, pending-reference
// resolver, DLQ poller) already swallow their own per-tick errors and never
// hit this path in practice, but gain the same safety net for free.
func registerBackgroundLoop(lc fx.Lifecycle, cfg *Config, name string, run func(ctx context.Context) error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				backoff := time.Second
				const maxBackoff = 30 * time.Second
				for {
					err := run(ctx)
					if err == nil || errors.Is(err, context.Canceled) {
						return
					}
					slog.Error("background loop exited with error, restarting", "name", name, "error", err, "backoff", backoff)
					select {
					case <-ctx.Done():
						return
					case <-time.After(backoff):
					}
					if backoff *= 2; backoff > maxBackoff {
						backoff = maxBackoff
					}
				}
			}()
			slog.Info("background loop started", "name", name)
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()
			select {
			case <-done:
				return nil
			case <-time.After(cfg.ShutdownTimeout):
				return fmt.Errorf("%s: did not stop within %s", name, cfg.ShutdownTimeout)
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})
}

func registerConsumer(lc fx.Lifecycle, cfg *Config, consumer *croupiersqs.Consumer) {
	registerBackgroundLoop(lc, cfg, "sqs-consumer", consumer.Run)
}

// registerPendingReferenceResolver polls for PENDING_REFERENCE transactions
// whose retry schedule is due, draining everything currently due before
// waiting for the next tick — same shape as registerOutboxWorker below.
func registerPendingReferenceResolver(lc fx.Lifecycle, cfg *Config, resolver *app.PendingReferenceResolver) {
	registerBackgroundLoop(lc, cfg, "pending-reference-resolver", func(ctx context.Context) error {
		ticker := time.NewTicker(cfg.PendingReferencePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				const batchLimit = 50
				if _, err := resolver.ResolveDue(ctx, time.Now(), batchLimit); err != nil {
					slog.Error("pending reference resolver tick failed", "error", err)
				}
			}
		}
	})
}

// registerDLQDepthPoller reports the dead-letter queue's depth on a timer
// (Fase 11) — same shape as the other two pollers above, just simpler: one
// GetQueueAttributes call per tick, no batching/draining needed.
func registerDLQDepthPoller(lc fx.Lifecycle, cfg *Config, poller *dlqDepthPoller) {
	registerBackgroundLoop(lc, cfg, "dlq-depth-poller", func(ctx context.Context) error {
		ticker := time.NewTicker(cfg.DLQDepthPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				if err := poller.poll(ctx); err != nil {
					slog.Error("dlq depth poll failed", "error", err)
				}
			}
		}
	})
}

func registerOutboxWorker(lc fx.Lifecycle, cfg *Config, worker *app.OutboxWorker) {
	registerBackgroundLoop(lc, cfg, "outbox-worker", func(ctx context.Context) error {
		ticker := time.NewTicker(cfg.OutboxPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				for {
					processed, err := worker.RunOnce(ctx)
					if err != nil {
						slog.Error("outbox worker tick failed", "error", err)
						break
					}
					if !processed {
						break
					}
				}
			}
		}
	})
}
