// Command croupier wires internal/app's use cases to real infrastructure
// (Postgres, SQS/LocalStack, Keycloak) and serves the HTTP API — this is the
// only place any of internal/app's concrete constructors, internal/postgres,
// internal/sqs's client, or internal/auth's verifier are wired together;
// every other package only ever depends on the interfaces/ports it needs.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"go.uber.org/fx"

	"github.com/jhtohru/croupier/internal/app"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if err := runMigrations(cfg.PostgresDSN); err != nil {
		slog.Error("failed to apply migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")

	fxApp := fx.New(
		fx.Supply(cfg),
		fx.Provide(
			providePostgresPool,
			provideWalletRepository,
			provideWagerRepository,
			provideOutboxRepository,
			provideInboxRepository,
			provideTxManager,

			app.NewWalletCreator,
			app.NewWalletGetter,
			app.NewWalletReconciler,
			app.NewWalletLedgerLister,
			app.NewWagerSubmitter,
			app.NewWagerTransactionGetter,

			provideSQSClient,
			provideOutboxPublisher,
			provideConsumer,
			providePendingReferenceResolver,
			provideOutboxWorker,

			provideAuthVerifier,
			provideReadyChecker,
			provideHTTPServer,
		),
		fx.Invoke(
			registerHTTPServer,
			registerConsumer,
			registerPendingReferenceResolver,
			registerOutboxWorker,
		),
		fx.StartTimeout(cfg.ShutdownTimeout),
		fx.StopTimeout(cfg.ShutdownTimeout+5*time.Second),
	)

	ctx := context.Background()
	if err := fxApp.Start(ctx); err != nil {
		slog.Error("failed to start", "error", err)
		os.Exit(1)
	}

	<-fxApp.Done() // blocks until SIGINT/SIGTERM (fx registers its own signal handler)

	stopCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout+5*time.Second)
	defer cancel()
	if err := fxApp.Stop(stopCtx); err != nil {
		slog.Error("failed to stop cleanly", "error", err)
		os.Exit(1)
	}
}
