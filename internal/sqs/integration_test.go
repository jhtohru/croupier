//go:build integration

package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/outbox"
	"github.com/jhtohru/croupier/internal/postgres"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

func testSQSClient(t *testing.T) *awssqs.Client {
	t.Helper()
	endpoint := os.Getenv("SQS_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:4566"
	}
	client, err := NewClient(context.Background(), "us-east-1", endpoint)
	require.NoError(t, err)
	return client
}

// createTestFIFOQueue creates a throwaway FIFO queue for one test run and
// deletes it on cleanup, instead of sharing deploy/localstack/init-queues.sh's
// long-lived queues across the whole suite.
//
// This isn't just isolation hygiene: sharing one queue across every run in
// this session made the suite badly flaky, and PurgeQueue (tried first, as
// the obvious fix) made it worse, not better — PurgeQueue is asynchronous
// even on real AWS ("the deletion typically completes within 60 seconds",
// per the API's own docs), and a purge immediately followed by a send+
// receive in the same process reliably produced a queue that would accept
// SendMessage but then never actually deliver that message on any
// subsequent ReceiveMessage, confirmed by direct observation of LocalStack's
// own request logs (SendMessage 200, then ReceiveMessage 200 with an empty
// result, repeated for the rest of the test's timeout). A dedicated queue
// per run has no history to purge and sidesteps the whole class of problem.
func createTestFIFOQueue(t *testing.T, client *awssqs.Client, namePrefix string) string {
	t.Helper()
	name := namePrefix + "-" + strings.ReplaceAll(uuid.New().String(), "-", "") + ".fifo"
	out, err := client.CreateQueue(context.Background(), &awssqs.CreateQueueInput{
		QueueName: aws.String(name),
		Attributes: map[string]string{
			"FifoQueue":                 "true",
			"ContentBasedDeduplication": "true",
		},
	})
	require.NoError(t, err)
	queueURL := aws.ToString(out.QueueUrl)
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(context.Background(), &awssqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
	})
	return queueURL
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://croupier:croupier@localhost:5432/croupier?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Ping(context.Background()))
	return pool
}

// TestConsumerConsumesRealSQSMessage publishes a wager-transaction message
// the way an external provider would (raw SendMessage, no internal/app
// involved), runs the real Consumer against it, and confirms the wallet
// moved — proving the whole chain (LocalStack SQS -> Consumer -> Inbox ->
// WagerSubmitter -> Postgres) works together, not just each piece alone.
func TestConsumerConsumesRealSQSMessage(t *testing.T) {
	client := testSQSClient(t)
	queueURL := createTestFIFOQueue(t, client, "wager-transactions-test")
	pool := testPool(t)

	walletRepo := postgres.NewWalletRepository(pool)
	wagerRepo := postgres.NewWagerRepository(pool)
	outboxRepo := postgres.NewOutboxRepository(pool)
	txManager := postgres.NewTxManager(pool)
	submitter := app.NewWagerSubmitter(walletRepo, wagerRepo, outboxRepo, txManager)
	inboxRepo := postgres.NewInboxRepository(pool)

	balance, err := money.FromMinorUnits("BRL", 10000)
	require.NoError(t, err)
	w, err := wallet.New(uuid.New(), balance)
	require.NoError(t, err)
	require.NoError(t, walletRepo.Save(context.Background(), w))

	providerID, externalTransactionID := "provider-"+uuid.New().String(), "ext-"+uuid.New().String()
	body, err := json.Marshal(wagerTransactionEnvelope{
		MessageID:  uuid.NewString(),
		Type:       wagerTransactionRequestedType,
		OccurredAt: time.Now().UTC(),
		Data: wagerTransactionMessage{
			ProviderID: providerID, ExternalTransactionID: externalTransactionID,
			IdempotencyKey: providerID + ":" + externalTransactionID,
			PlayerID:       w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Money: mustMoney(t, "30.00"),
		},
	})
	require.NoError(t, err)

	_, err = client.SendMessage(context.Background(), &awssqs.SendMessageInput{
		QueueUrl:       aws.String(queueURL),
		MessageBody:    aws.String(string(body)),
		MessageGroupId: aws.String(w.ID().String()),
	})
	require.NoError(t, err)

	consumer := NewConsumer(client, queueURL, "wager-transactions-consumer-test", submitter, inboxRepo, txManager)
	consumer.waitTime = 2

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	runErr := make(chan error, 1)
	go func() { runErr <- consumer.Run(ctx) }()
	// Stop the consumer and wait for its goroutine to actually exit before
	// this test's other cleanups (notably testPool's pool.Close) run —
	// Cleanups run in LIFO order, so registering this after testPool(t) was
	// already called means it runs first.
	t.Cleanup(func() {
		cancel()
		<-runErr
	})

	require.Eventually(t, func() bool {
		got, err := walletRepo.FindByID(context.Background(), w.ID())
		require.NoError(t, err)
		return got.Balance() == mustMoney(t, "70.00")
	}, 15*time.Second, 100*time.Millisecond, "wallet balance never reflected the consumed BET")
}

// TestConsumerRedeliveryAfterCommitBeforeDelete is the mandatory
// "interromper consumer após commit e antes da remoção da mensagem →
// verificar redelivery" scenario against real SQS, going further than the
// fake-based TestConsumerHandle/redelivery_of_already-completed_work_does_not_resubmit:
// here the "redelivery" is a real SQS ReceiveMessage returning the same
// message a second time (via ChangeMessageVisibility instead of waiting out
// a real 30s visibility timeout), not something the test constructs by
// hand.
func TestConsumerRedeliveryAfterCommitBeforeDelete(t *testing.T) {
	client := testSQSClient(t)
	queueURL := createTestFIFOQueue(t, client, "wager-transactions-redelivery-test")
	pool := testPool(t)

	walletRepo := postgres.NewWalletRepository(pool)
	wagerRepo := postgres.NewWagerRepository(pool)
	outboxRepo := postgres.NewOutboxRepository(pool)
	txManager := postgres.NewTxManager(pool)
	submitter := app.NewWagerSubmitter(walletRepo, wagerRepo, outboxRepo, txManager)
	inboxRepo := postgres.NewInboxRepository(pool)

	balance, err := money.FromMinorUnits("BRL", 10000)
	require.NoError(t, err)
	w, err := wallet.New(uuid.New(), balance)
	require.NoError(t, err)
	require.NoError(t, walletRepo.Save(context.Background(), w))

	providerID, externalTransactionID := "provider-"+uuid.New().String(), "ext-"+uuid.New().String()
	body, err := json.Marshal(wagerTransactionEnvelope{
		MessageID:  uuid.NewString(),
		Type:       wagerTransactionRequestedType,
		OccurredAt: time.Now().UTC(),
		Data: wagerTransactionMessage{
			ProviderID: providerID, ExternalTransactionID: externalTransactionID,
			IdempotencyKey: providerID + ":" + externalTransactionID,
			PlayerID:       w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Money: mustMoney(t, "30.00"),
		},
	})
	require.NoError(t, err)
	_, err = client.SendMessage(context.Background(), &awssqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(body)), MessageGroupId: aws.String(w.ID().String()),
	})
	require.NoError(t, err)

	consumer := NewConsumer(client, queueURL, "redelivery-test-consumer", submitter, inboxRepo, txManager)

	// First delivery: receive it directly (bypassing consumer.Run's loop, so
	// the test controls exactly what happens between receipt and delete) and
	// hand it to handle — the real commit, real Postgres included, but
	// deliberately never deleted from the queue afterward.
	out, err := client.ReceiveMessage(context.Background(), &awssqs.ReceiveMessageInput{
		QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 10,
	})
	require.NoError(t, err)
	require.Len(t, out.Messages, 1)
	msg := out.Messages[0]

	require.NoError(t, consumer.handle(context.Background(), msg, "test-correlation-id"))

	got, err := walletRepo.FindByID(context.Background(), w.ID())
	require.NoError(t, err)
	assert.Equal(t, mustMoney(t, "70.00"), got.Balance())

	// Simulate "crashed before delete": force the message visible again
	// immediately instead of waiting out a real visibility timeout.
	_, err = client.ChangeMessageVisibility(context.Background(), &awssqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(queueURL), ReceiptHandle: msg.ReceiptHandle, VisibilityTimeout: 0,
	})
	require.NoError(t, err)

	redelivered, err := client.ReceiveMessage(context.Background(), &awssqs.ReceiveMessageInput{
		QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 10,
	})
	require.NoError(t, err)
	require.Len(t, redelivered.Messages, 1)
	assert.Equal(t, aws.ToString(msg.MessageId), aws.ToString(redelivered.Messages[0].MessageId))

	require.NoError(t, consumer.handle(context.Background(), redelivered.Messages[0], "test-correlation-id"))

	final, err := walletRepo.FindByID(context.Background(), w.ID())
	require.NoError(t, err)
	assert.Equal(t, mustMoney(t, "70.00"), final.Balance(), "redelivery must not process the bet a second time")

	entries, err := walletRepo.AllLedgerEntries(context.Background(), w.ID())
	require.NoError(t, err)
	assert.Len(t, entries, 1, "exactly one debit despite two deliveries")

	_, _ = client.DeleteMessage(context.Background(), &awssqs.DeleteMessageInput{
		QueueUrl: aws.String(queueURL), ReceiptHandle: redelivered.Messages[0].ReceiptHandle,
	})
}

// TestPublisherAndOutboxWorkerOverRealSQS creates a wallet with a positive
// opening balance (which queues real outbox entries the normal way, via
// app.WalletCreator), drains them with a real OutboxWorker publishing
// through a real Publisher, and confirms the events actually arrive on a
// real SQS queue with the right eventId/eventType.
func TestPublisherAndOutboxWorkerOverRealSQS(t *testing.T) {
	client := testSQSClient(t)
	queueURL := createTestFIFOQueue(t, client, "wallet-events-test")
	pool := testPool(t)

	walletRepo := postgres.NewWalletRepository(pool)
	wagerRepo := postgres.NewWagerRepository(pool)
	outboxRepo := postgres.NewOutboxRepository(pool)
	txManager := postgres.NewTxManager(pool)

	creator := app.NewWalletCreator(walletRepo, wagerRepo, outboxRepo, txManager)
	balance, err := money.FromMinorUnits("BRL", 5000)
	require.NoError(t, err)
	w, err := creator.Create(context.Background(), app.CreateWalletInput{PlayerID: uuid.New(), InitialBalance: balance, CorrelationID: "test-correlation-id"})
	require.NoError(t, err)

	// Create's WagerTransactionProcessed event is keyed by the OPENING
	// transaction's own id, not the wallet's — only WalletBalanceChanged
	// uses the wallet as its aggregate. The ledger entry Create just wrote
	// is the only place that transaction id is exposed from here.
	entries, err := walletRepo.AllLedgerEntries(context.Background(), w.ID())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	openingTxID := entries[0].TransactionID()

	publisher := NewPublisher(client, queueURL)
	worker := app.NewOutboxWorker(outboxRepo, publisher, txManager, time.Second)

	// Drain the whole PENDING backlog in Postgres, not just a fixed handful
	// of iterations: this session's other integration tests have been
	// creating outbox entries all along with nothing to publish them until
	// this test, so there can be a large backlog older than this run's own
	// two entries (FindDueForUpdate always serves the oldest first). This is
	// backlog in Postgres's outbox table, unrelated to the fresh SQS queue
	// above — same reasoning as internal/postgres's drainOutboxBacklog.
	const maxIterations = 10000
	for i := 0; i < maxIterations; i++ {
		processed, err := worker.RunOnce(context.Background())
		require.NoError(t, err)
		if !processed {
			break
		}
	}

	seen := map[uuid.UUID]bool{}
	var publishedEventTypes []string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && len(publishedEventTypes) < 2 {
		out, err := client.ReceiveMessage(context.Background(), &awssqs.ReceiveMessageInput{
			QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 2,
		})
		require.NoError(t, err)
		for _, msg := range out.Messages {
			var envelope eventEnvelope
			require.NoError(t, json.Unmarshal([]byte(aws.ToString(msg.Body)), &envelope))
			relevant := envelope.AggregateID == w.ID() || envelope.AggregateID == openingTxID
			if relevant && !seen[envelope.EventID] {
				seen[envelope.EventID] = true
				publishedEventTypes = append(publishedEventTypes, envelope.EventType)
			}
			_, _ = client.DeleteMessage(context.Background(), &awssqs.DeleteMessageInput{
				QueueUrl: aws.String(queueURL), ReceiptHandle: msg.ReceiptHandle,
			})
		}
	}

	assert.ElementsMatch(t, []string{"WagerTransactionProcessed", "WalletBalanceChanged"}, publishedEventTypes)
}

// TestOutboxRecoveryAfterAbandonedPublish is the mandatory "dois publishers
// disputando o mesmo outbox" scenario: publisher 1 claims an entry, really
// publishes it to real SQS, then "crashes" (its transaction is rolled back
// instead of committed — the row lock from FindDueForUpdate is released,
// exactly as it would be if the process had actually died here, per
// ARCHITECTURE.md's "Mensageria (SQS)"). Publisher 2 then picks up the same
// still-PENDING entry and completes normally. At-least-once delivery means
// the message may genuinely reach the queue twice (or SQS FIFO's own
// dedup — same MessageDeduplicationId within its window — may collapse it
// to once; either is fine) — what must never happen is a second, distinct
// eventId for the same logical event.
func TestOutboxRecoveryAfterAbandonedPublish(t *testing.T) {
	client := testSQSClient(t)
	queueURL := createTestFIFOQueue(t, client, "outbox-recovery-test")
	pool := testPool(t)

	outboxRepo := postgres.NewOutboxRepository(pool)
	txManager := postgres.NewTxManager(pool)
	publisher := NewPublisher(client, queueURL)

	// Drain any PENDING backlog this session's other tests left in Postgres
	// first, so FindDueForUpdate below is guaranteed to claim the entry this
	// test itself creates, not some unrelated leftover row — same reasoning
	// as internal/postgres's drainOutboxBacklog.
	drainWorker := app.NewOutboxWorker(outboxRepo, publisher, txManager, time.Second)
	for i := 0; i < 10000; i++ {
		processed, err := drainWorker.RunOnce(context.Background())
		require.NoError(t, err)
		if !processed {
			break
		}
	}

	entry, err := outbox.NewEntry(outbox.NewEntryInput{
		AggregateType: "Wallet", AggregateID: uuid.New(),
		EventType: "WalletBalanceChanged", Version: 1, Payload: []byte(`{"test":true}`), OccurredAt: time.Now(),
		CorrelationID: "test-correlation-id",
	})
	require.NoError(t, err)
	require.NoError(t, outboxRepo.SaveAll(context.Background(), entry))

	// Publisher 1: claims the row, really publishes it, then "crashes" —
	// the forced error means WithinTx rolls back, so the claim (and any
	// status change) never actually takes effect; the entry stays PENDING.
	errSimulatedCrash := errors.New("simulated crash before commit")
	txErr := txManager.WithinTx(context.Background(), func(ctx context.Context) error {
		claimed, err := outboxRepo.FindDueForUpdate(ctx)
		require.NoError(t, err)
		require.Equal(t, entry.ID(), claimed.ID())
		require.NoError(t, publisher.Publish(ctx, claimed))
		return errSimulatedCrash
	})
	require.ErrorIs(t, txErr, errSimulatedCrash)

	// Publisher 2: the entry is still PENDING, so a fresh worker claims and
	// completes it normally.
	worker := app.NewOutboxWorker(outboxRepo, publisher, txManager, time.Second)
	processed, err := worker.RunOnce(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)

	// The drain step above just published this session's entire unrelated
	// backlog to this same fresh queue too — filter down to deliveries of
	// this test's own entry specifically, same reasoning as
	// TestPublisherAndOutboxWorkerOverRealSQS filtering by aggregateId.
	deliveriesOfThisEvent := 0
	otherEventIDsSeen := map[uuid.UUID]bool{}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, err := client.ReceiveMessage(context.Background(), &awssqs.ReceiveMessageInput{
			QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 2,
		})
		require.NoError(t, err)
		if len(out.Messages) == 0 {
			break
		}
		for _, msg := range out.Messages {
			var envelope eventEnvelope
			require.NoError(t, json.Unmarshal([]byte(aws.ToString(msg.Body)), &envelope))
			if envelope.AggregateID == entry.AggregateID() {
				if envelope.EventID == entry.ID() {
					deliveriesOfThisEvent++
				} else {
					otherEventIDsSeen[envelope.EventID] = true
				}
			}
			_, _ = client.DeleteMessage(context.Background(), &awssqs.DeleteMessageInput{
				QueueUrl: aws.String(queueURL), ReceiptHandle: msg.ReceiptHandle,
			})
		}
	}

	assert.GreaterOrEqual(t, deliveriesOfThisEvent, 1, "expected at least the successful publish to land on the queue")
	assert.Empty(t, otherEventIDsSeen, "no other eventId should ever be minted for this same logical event")
}
