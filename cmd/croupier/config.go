package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is loaded once from the environment (see .env.example) at process
// start — no config file, no hot reload, matching the rest of this project's
// preference for explicit stdlib over a framework when the stdlib already
// resolves the problem.
type Config struct {
	PostgresDSN string

	SQSEndpoint                string
	AWSRegion                  string
	WagerTransactionsQueueName string
	WalletEventsQueueName      string
	SQSConsumerName            string

	KeycloakIssuerURL string

	AppPort string

	PendingReferencePollInterval time.Duration
	PendingReferenceMaxAttempts  int
	PendingReferenceTTL          time.Duration
	PendingReferenceBackoffBase  time.Duration

	OutboxPollInterval time.Duration
	OutboxBackoffBase  time.Duration

	// ShutdownTimeout bounds both fx's own StopTimeout and how long each
	// background loop (SQS consumer, pending-reference resolver, outbox
	// worker) gets to notice cancellation and return before shutdown gives
	// up on it and moves on — see lifecycle.go.
	ShutdownTimeout time.Duration
}

func LoadConfig() (*Config, error) {
	postgresHost := getenv("POSTGRES_HOST", "localhost")
	postgresPort := getenv("POSTGRES_PORT", "5432")
	postgresUser := getenv("POSTGRES_USER", "croupier")
	postgresPassword := getenv("POSTGRES_PASSWORD", "croupier")
	postgresDB := getenv("POSTGRES_DB", "croupier")

	pendingReferenceMaxAttempts, err := getenvInt("PENDING_REFERENCE_MAX_ATTEMPTS", 10)
	if err != nil {
		return nil, err
	}
	pendingReferencePollInterval, err := getenvDuration("PENDING_REFERENCE_POLL_INTERVAL", 5*time.Second)
	if err != nil {
		return nil, err
	}
	pendingReferenceTTL, err := getenvDuration("PENDING_REFERENCE_TTL", 24*time.Hour)
	if err != nil {
		return nil, err
	}
	pendingReferenceBackoffBase, err := getenvDuration("PENDING_REFERENCE_BACKOFF_BASE", 5*time.Second)
	if err != nil {
		return nil, err
	}
	outboxPollInterval, err := getenvDuration("OUTBOX_POLL_INTERVAL", time.Second)
	if err != nil {
		return nil, err
	}
	outboxBackoffBase, err := getenvDuration("OUTBOX_BACKOFF_BASE", 2*time.Second)
	if err != nil {
		return nil, err
	}
	shutdownTimeout, err := getenvDuration("SHUTDOWN_TIMEOUT", 15*time.Second)
	if err != nil {
		return nil, err
	}

	return &Config{
		PostgresDSN: fmt.Sprintf(
			"postgres://%s:%s@%s:%s/%s?sslmode=disable",
			postgresUser, postgresPassword, postgresHost, postgresPort, postgresDB,
		),

		SQSEndpoint:                getenv("SQS_ENDPOINT", "http://localhost:4566"),
		AWSRegion:                  getenv("AWS_REGION", "us-east-1"),
		WagerTransactionsQueueName: getenv("WAGER_TRANSACTIONS_QUEUE_NAME", "wager-transactions.fifo"),
		WalletEventsQueueName:      getenv("WALLET_EVENTS_QUEUE_NAME", "wallet-events.fifo"),
		SQSConsumerName:            getenv("SQS_CONSUMER_NAME", "wager-transactions-consumer"),

		KeycloakIssuerURL: getenv("KEYCLOAK_ISSUER_URL", "http://localhost:8080/realms/croupier"),

		AppPort: getenv("APP_PORT", "8081"),

		PendingReferencePollInterval: pendingReferencePollInterval,
		PendingReferenceMaxAttempts:  pendingReferenceMaxAttempts,
		PendingReferenceTTL:          pendingReferenceTTL,
		PendingReferenceBackoffBase:  pendingReferenceBackoffBase,

		OutboxPollInterval: outboxPollInterval,
		OutboxBackoffBase:  outboxBackoffBase,

		ShutdownTimeout: shutdownTimeout,
	}, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parsing %s=%q as int: %w", key, raw, err)
	}
	return v, nil
}

func getenvDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parsing %s=%q as duration: %w", key, raw, err)
	}
	return v, nil
}
