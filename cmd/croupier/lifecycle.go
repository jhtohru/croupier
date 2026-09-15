package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/httpapi"
	croupiersqs "github.com/jhtohru/croupier/internal/sqs"
)

// registerHTTPServer starts the HTTP server on OnStart and gives it up to
// cfg.ShutdownTimeout to finish in-flight requests on OnStop
// (http.Server.Shutdown) before the process moves on regardless. Binding the
// listener synchronously in OnStart (instead of inside the goroutine, via a
// plain ListenAndServe) means a port-already-in-use error fails fx's own
// startup instead of surfacing later as a silent, already-backgrounded
// goroutine's log line.
func registerHTTPServer(lc fx.Lifecycle, srv *httpapi.Server, cfg *Config) {
	server := &http.Server{Handler: srv}
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
func registerBackgroundLoop(lc fx.Lifecycle, cfg *Config, name string, run func(ctx context.Context) error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Error("background loop exited with error", "name", name, "error", err)
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
