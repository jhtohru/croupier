//go:build integration

// Challenge spec §13.24: "Adicione uma verificação da composição Fx e de seu
// início e encerramento, incluindo liberação de recursos dos workers." Every
// other integration test in this package (and internal/httpapi,
// internal/sqs, ...) wires its own repositories/handlers by hand against
// real infrastructure — none of them ever build the actual fx.App main.go
// assembles, so a startup ordering mistake in newFxApp itself (a missing
// provider, a lifecycle hook registered in the wrong place) could compile
// and pass every other suite while still being broken in the one place that
// matters. This test builds and starts/stops that exact graph.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	croupiersqs "github.com/jhtohru/croupier/internal/sqs"
)

// createTestQueue creates a plain FIFO queue with a unique name and returns
// it — same pattern TestSameOperationOverHTTPAndSQSIsNotDuplicated already
// uses, so this test never touches the real wager-transactions.fifo/
// wallet-events.fifo the docker-compose app service is also consuming from.
func createTestQueue(t *testing.T, client *awssqs.Client, prefix string) string {
	t.Helper()
	name := prefix + "-" + uuid.New().String() + ".fifo"
	out, err := client.CreateQueue(context.Background(), &awssqs.CreateQueueInput{
		QueueName:  aws.String(name),
		Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "true"},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(context.Background(), &awssqs.DeleteQueueInput{QueueUrl: out.QueueUrl})
	})
	return name
}

func TestFxAppStartStopReleasesResources(t *testing.T) {
	cfg, err := LoadConfig()
	require.NoError(t, err)

	// Fresh, disposable everything — this app instance must never share
	// state with the docker-compose app service or another test package.
	cfg.PostgresDSN = testDatabaseURL()
	cfg.SQSEndpoint = testSQSEndpoint()
	cfg.KeycloakIssuerURL = testKeycloakIssuerURL()
	cfg.AppPort = "0" // OS-assigned free port — this test never dials it directly
	cfg.ShutdownTimeout = 5 * time.Second
	cfg.PendingReferencePollInterval = time.Hour // long enough to never fire during this test
	cfg.OutboxPollInterval = time.Hour
	cfg.DLQDepthPollInterval = time.Hour

	sqsClient, err := croupiersqs.NewClient(context.Background(), cfg.AWSRegion, cfg.SQSEndpoint)
	require.NoError(t, err)
	cfg.WagerTransactionsQueueName = createTestQueue(t, sqsClient, "fx-test-wager-transactions")
	cfg.WagerTransactionsDLQName = createTestQueue(t, sqsClient, "fx-test-wager-transactions-dlq")
	cfg.WalletEventsQueueName = createTestQueue(t, sqsClient, "fx-test-wallet-events")

	var pool *pgxpool.Pool
	fxApp := newFxApp(cfg, fx.Populate(&pool))

	startCtx, cancelStart := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelStart()
	require.NoError(t, fxApp.Start(startCtx))
	require.NotNil(t, pool, "fx.Populate should have reached the *pgxpool.Pool the real graph builds")
	assert.NoError(t, pool.Ping(context.Background()), "pool should be usable right after Start")

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelStop()
	require.NoError(t, fxApp.Stop(stopCtx))

	// Challenge spec §10's "fechamento das dependências após a finalização
	// dos componentes que as utilizam" — registerPostgresPool.OnStop closes
	// the pool last, after every worker that might still be using it. A
	// pool that's actually closed refuses new work instead of silently
	// still working.
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelPing()
	assert.Error(t, pool.Ping(pingCtx), "pool should be closed after Stop — resources not released")
}
