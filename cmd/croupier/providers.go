package main

import (
	"context"
	"fmt"
	"strconv"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	awssqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/auth"
	"github.com/jhtohru/croupier/internal/httpapi"
	"github.com/jhtohru/croupier/internal/metrics"
	"github.com/jhtohru/croupier/internal/postgres"
	croupiersqs "github.com/jhtohru/croupier/internal/sqs"
)

// provideMetrics builds the one *metrics.Registry the whole process shares —
// every collector it owns is registered against a private prometheus
// registry (see internal/metrics), so this is safe to construct exactly
// once here and hand to every provider below that wants to report something.
func provideMetrics() *metrics.Registry {
	return metrics.New()
}

func providePostgresPool(cfg *Config) (*pgxpool.Pool, error) {
	return pgxpool.New(context.Background(), cfg.PostgresDSN)
}

// The five functions below exist only to declare their return type as the
// app package's own interface instead of the concrete *postgres.XxxYyy type
// postgres.NewXxxYyy actually returns — that's what lets fx wire them
// straight into app.NewWalletCreator and friends, whose parameters are
// declared in terms of those same interfaces. Providing the concrete type
// directly would leave fx with no record that it satisfies the interface
// the next provider down the chain asks for.
func provideWalletRepository(pool *pgxpool.Pool) app.WalletRepository {
	return postgres.NewWalletRepository(pool)
}

func provideWagerRepository(pool *pgxpool.Pool) app.WagerRepository {
	return postgres.NewWagerRepository(pool)
}

func provideOutboxRepository(pool *pgxpool.Pool) app.OutboxRepository {
	return postgres.NewOutboxRepository(pool)
}

func provideInboxRepository(pool *pgxpool.Pool) app.InboxRepository {
	return postgres.NewInboxRepository(pool)
}

func provideTxManager(pool *pgxpool.Pool) app.TxManager {
	return postgres.NewTxManager(pool)
}

func provideSQSClient(cfg *Config) (*awssqs.Client, error) {
	return croupiersqs.NewClient(context.Background(), cfg.AWSRegion, cfg.SQSEndpoint)
}

// resolveQueueURL is called once per queue at startup (from the two
// providers below) — cheap, and simpler than threading a shared cache
// through the fx graph for two calls that only ever happen at process start.
func resolveQueueURL(client *awssqs.Client, queueName string) (string, error) {
	out, err := client.GetQueueUrl(context.Background(), &awssqs.GetQueueUrlInput{QueueName: &queueName})
	if err != nil {
		return "", fmt.Errorf("resolving queue URL for %q: %w", queueName, err)
	}
	return *out.QueueUrl, nil
}

func provideOutboxPublisher(client *awssqs.Client, cfg *Config) (app.OutboxPublisher, error) {
	url, err := resolveQueueURL(client, cfg.WalletEventsQueueName)
	if err != nil {
		return nil, err
	}
	return croupiersqs.NewPublisher(client, url), nil
}

func provideConsumer(client *awssqs.Client, submitter *app.WagerSubmitter, inboxRepo app.InboxRepository, cfg *Config, reg *metrics.Registry) (*croupiersqs.Consumer, error) {
	url, err := resolveQueueURL(client, cfg.WagerTransactionsQueueName)
	if err != nil {
		return nil, err
	}
	return croupiersqs.NewConsumer(client, url, cfg.SQSConsumerName, submitter, inboxRepo, croupiersqs.WithMetrics(reg)), nil
}

func providePendingReferenceResolver(wagers app.WagerRepository, submitter *app.WagerSubmitter, cfg *Config, reg *metrics.Registry) *app.PendingReferenceResolver {
	return app.NewPendingReferenceResolver(wagers, submitter, cfg.PendingReferenceMaxAttempts, cfg.PendingReferenceTTL, cfg.PendingReferenceBackoffBase, app.WithPendingReferenceMetrics(reg))
}

func provideOutboxWorker(outboxRepo app.OutboxRepository, publisher app.OutboxPublisher, txManager app.TxManager, cfg *Config, reg *metrics.Registry) *app.OutboxWorker {
	return app.NewOutboxWorker(outboxRepo, publisher, txManager, cfg.OutboxBackoffBase, app.WithOutboxMetrics(reg))
}

// dlqDepthPoller reads a queue's ApproximateNumberOfMessages and reports it
// as a metric (Fase 11's DLQ observability) — this is deliberately just a
// gauge, not a test that a message actually reached the DLQ end-to-end
// (that gap is documented in ARCHITECTURE.md → "Limitações, interpretações
// e trabalho incompleto"): polling depth is cheap, reliable, and enough to
// alert an operator that something is landing there, which is what this
// metric is for.
type dlqDepthPoller struct {
	client   *awssqs.Client
	queueURL string
	queue    string
	metrics  *metrics.Registry
}

func (p *dlqDepthPoller) poll(ctx context.Context) error {
	out, err := p.client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl:       &p.queueURL,
		AttributeNames: []awssqstypes.QueueAttributeName{awssqstypes.QueueAttributeNameApproximateNumberOfMessages},
	})
	if err != nil {
		return fmt.Errorf("dlq depth poller: %w", err)
	}
	depth, err := strconv.ParseFloat(out.Attributes[string(awssqstypes.QueueAttributeNameApproximateNumberOfMessages)], 64)
	if err != nil {
		return fmt.Errorf("dlq depth poller: parsing ApproximateNumberOfMessages: %w", err)
	}
	p.metrics.SetDLQDepth(p.queue, depth)
	return nil
}

func provideDLQDepthPoller(client *awssqs.Client, cfg *Config, reg *metrics.Registry) (*dlqDepthPoller, error) {
	url, err := resolveQueueURL(client, cfg.WagerTransactionsDLQName)
	if err != nil {
		return nil, err
	}
	return &dlqDepthPoller{client: client, queueURL: url, queue: cfg.WagerTransactionsDLQName, metrics: reg}, nil
}

func provideAuthVerifier(cfg *Config) (*auth.Verifier, error) {
	return auth.NewVerifier(context.Background(), cfg.KeycloakIssuerURL)
}

// readyChecker aggregates every dependency GET /health/ready needs to
// confirm — Postgres and, since Fase 8 exists now, SQS too (a single cheap
// GetQueueAttributes call; reaching it at all proves the endpoint and
// credentials are usable, which is what readiness means here).
type readyChecker struct {
	pool        *pgxpool.Pool
	sqsClient   *awssqs.Client
	sqsQueueURL string
}

func (c *readyChecker) check(ctx context.Context) error {
	if err := c.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	if _, err := c.sqsClient.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{QueueUrl: &c.sqsQueueURL}); err != nil {
		return fmt.Errorf("sqs: %w", err)
	}
	return nil
}

func provideReadyChecker(pool *pgxpool.Pool, client *awssqs.Client, cfg *Config) (*readyChecker, error) {
	url, err := resolveQueueURL(client, cfg.WagerTransactionsQueueName)
	if err != nil {
		return nil, err
	}
	return &readyChecker{pool: pool, sqsClient: client, sqsQueueURL: url}, nil
}

func provideHTTPServer(
	walletCreator *app.WalletCreator,
	walletGetter *app.WalletGetter,
	walletReconciler *app.WalletReconciler,
	walletLedgerLister *app.WalletLedgerLister,
	wagerSubmitter *app.WagerSubmitter,
	wagerTransactionGetter *app.WagerTransactionGetter,
	authVerifier *auth.Verifier,
	ready *readyChecker,
	reg *metrics.Registry,
) *httpapi.Server {
	return httpapi.NewServer(httpapi.Deps{
		WalletCreator:          walletCreator,
		WalletGetter:           walletGetter,
		WalletReconciler:       walletReconciler,
		WalletLedgerLister:     walletLedgerLister,
		WagerSubmitter:         wagerSubmitter,
		WagerTransactionGetter: wagerTransactionGetter,
		Auth:                   authVerifier,
		Ready:                  ready.check,
		Metrics:                reg,
		MetricsHandler:         reg.Handler(),
	})
}
