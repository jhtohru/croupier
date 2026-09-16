package sqs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/inbox"
	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wager"
)

// wagerSubmitter is the one method Consumer needs from app.WagerSubmitter —
// consumer-defined, same pattern as internal/httpapi's server.go, so tests
// use a stub instead of a real WagerSubmitter/Postgres.
type wagerSubmitter interface {
	Submit(ctx context.Context, input app.SubmitWagerTransactionInput) (*app.SubmitWagerTransactionResult, error)
}

// receiveDeleter is the subset of *sqs.Client's methods Consumer calls —
// consumer-defined so Run/processMessage can be unit-tested against a fake
// queue instead of requiring LocalStack for every test. *sqs.Client already
// satisfies this with no adapter needed.
type receiveDeleter interface {
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

// wagerTransactionRequestedType is the only inbound event type this queue
// carries today (challenge spec §10's example envelope) — a message with
// any other type is rejected as unsupported rather than silently guessed at.
const wagerTransactionRequestedType = "WagerTransactionRequested"

// wagerTransactionEnvelope is the inbound message's wire shape — challenge
// spec §10: {messageId, type, occurredAt, data}. messageId here is the
// envelope's own field, distinct from (and, per the spec, authoritative
// over) *sqs.Client's transport-level msg.MessageId — see handle below for
// why both exist and which one this consumer treats as the durable identity.
type wagerTransactionEnvelope struct {
	MessageID  string                  `json:"messageId"`
	Type       string                  `json:"type"`
	OccurredAt time.Time               `json:"occurredAt"`
	Data       wagerTransactionMessage `json:"data"`
}

// wagerTransactionMessage is the envelope's "data" shape — the same fields
// httpapi's submitWagerTransactionRequest carries (both ingestion paths feed
// the identical app.SubmitWagerTransactionInput), plus idempotencyKey, which
// plays here exactly the role the Idempotency-Key HTTP header plays for the
// HTTP path: a client-supplied consistency check against
// providerId:externalTransactionId (the actual identity), not a second
// source of truth — see handle's validation below and the equivalent
// reasoning in internal/httpapi/wagering.go.
type wagerTransactionMessage struct {
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	IdempotencyKey                 string      `json:"idempotencyKey"`
	PlayerID                       uuid.UUID   `json:"playerId"`
	WalletID                       uuid.UUID   `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           wager.Kind  `json:"kind"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID *string     `json:"referenceExternalTransactionId,omitempty"`
}

// metricsRecorder is the one method Consumer needs from a metrics backend
// (Fase 11) — consumer-defined, same pattern as receiveDeleter above;
// internal/metrics.Registry satisfies this structurally. A nil
// metricsRecorder is a safe no-op, so every existing call site (including
// consumer_test.go/integration_test.go) keeps compiling unchanged.
type metricsRecorder interface {
	ObserveWagerSubmission(kind, outcome string)
	ObserveSQSMessage(consumer, result string)
}

type Consumer struct {
	client       receiveDeleter
	queueURL     string
	consumerName string
	submitter    wagerSubmitter
	inbox        app.InboxRepository
	txManager    app.TxManager
	waitTime     int32
	maxMessages  int32
	metrics      metricsRecorder
}

// withinTx runs fn, wrapped in c.txManager.WithinTx when one is configured.
// A nil txManager (every unit test in this package, which exercises handle
// against fakes that don't need real atomicity) is a safe no-op passthrough
// — same nil-is-fine convention as metricsRecorder above.
func (c *Consumer) withinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if c.txManager == nil {
		return fn(ctx)
	}
	return c.txManager.WithinTx(ctx, fn)
}

// ConsumerOption customizes a Consumer built by NewConsumer — same variadic-
// option reasoning as OutboxWorkerOption in internal/app.
type ConsumerOption func(*Consumer)

func WithMetrics(m metricsRecorder) ConsumerOption {
	return func(c *Consumer) { c.metrics = m }
}

func NewConsumer(client *sqs.Client, queueURL, consumerName string, submitter wagerSubmitter, inboxRepo app.InboxRepository, txManager app.TxManager, opts ...ConsumerOption) *Consumer {
	c := &Consumer{
		client:       client,
		queueURL:     queueURL,
		consumerName: consumerName,
		submitter:    submitter,
		inbox:        inboxRepo,
		txManager:    txManager,
		waitTime:     20,
		maxMessages:  10,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Run polls the queue until ctx is cancelled, processing each batch
// sequentially (simpler than a worker pool, and it naturally avoids two
// goroutines racing to create the same Inbox row within one batch — not a
// correctness requirement since Save upserts either way, just simpler to
// reason about). ctx is checked before every ReceiveMessage call, so
// cancellation (graceful shutdown, Fase 10's SIGTERM handling) stops pulling
// new messages promptly; a message already being processed always finishes
// first (see processMessage) rather than being abandoned mid-write.
//
// A ReceiveMessage/network error propagates up rather than being retried in
// a loop here — cmd/croupier's registerBackgroundLoop is the supervisor that
// restarts Run after a backoff when this happens (Fase 11: satisfies the
// challenge spec's "indisponibilidade temporária do... SQS"), so this
// package doesn't invent its own retry policy for that.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		out, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(c.queueURL),
			MaxNumberOfMessages: c.maxMessages,
			WaitTimeSeconds:     c.waitTime,
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		for _, msg := range out.Messages {
			c.processMessage(ctx, msg)
		}
	}
}

// processMessage never lets a handling failure propagate into deleting the
// message — the only way a message leaves the queue is a fully successful
// handle, so a crash or error at any point (before or after Submit) means
// SQS redelivers it after the visibility timeout, and the queue's redrive
// policy eventually routes a message that keeps failing to the DLQ. No
// message is ever deleted before Submit's underlying transaction commits.
func (c *Consumer) processMessage(ctx context.Context, msg types.Message) {
	messageID := aws.ToString(msg.MessageId)
	// SQS has no correlationId of its own — this stands in as the one
	// identifier tying every log line about processing this specific
	// delivery together (Fase 11); it's per-delivery, not per-messageId, so
	// a redelivery of the same message gets a fresh value on purpose.
	correlationID := uuid.NewString()
	if err := c.handle(ctx, msg, correlationID); err != nil {
		slog.ErrorContext(ctx, "sqs consumer: message processing failed, leaving for redelivery",
			"consumer", c.consumerName, "messageId", messageID, "correlationId", correlationID, "error", err)
		if c.metrics != nil {
			c.metrics.ObserveSQSMessage(c.consumerName, "error")
		}
		return
	}
	if c.metrics != nil {
		c.metrics.ObserveSQSMessage(c.consumerName, "success")
	}
	if _, err := c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.queueURL),
		ReceiptHandle: msg.ReceiptHandle,
	}); err != nil {
		// The work is already durably done at this point — a failed delete
		// only risks a harmless redelivery (caught by the Inbox check below
		// on the next attempt), not reprocessing.
		slog.ErrorContext(ctx, "sqs consumer: failed to delete processed message",
			"consumer", c.consumerName, "messageId", messageID, "correlationId", correlationID, "error", err)
	}
}

func (c *Consumer) handle(ctx context.Context, msg types.Message, correlationID string) error {
	sqsMessageID := aws.ToString(msg.MessageId)
	body := aws.ToString(msg.Body)

	var envelope wagerTransactionEnvelope
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return fmt.Errorf("sqs consumer: malformed message envelope (sqs messageId %s): %w", sqsMessageID, err)
	}
	if envelope.Type != wagerTransactionRequestedType {
		return fmt.Errorf("sqs consumer: unsupported message type %q (sqs messageId %s)", envelope.Type, sqsMessageID)
	}
	if envelope.MessageID == "" {
		return fmt.Errorf("sqs consumer: message envelope missing messageId (sqs messageId %s)", sqsMessageID)
	}
	req := envelope.Data
	if want := req.ProviderID + ":" + req.ExternalTransactionID; req.IdempotencyKey != want {
		// Same reasoning as internal/httpapi's Idempotency-Key check: the
		// real identity is (providerId, externalTransactionId), never
		// overridden by a client-supplied key — a mismatch (including an
		// altogether missing key, which never equals "provider:external")
		// is malformed input, not a transient failure, so this is never
		// worth redelivering as-is.
		return fmt.Errorf("sqs consumer: idempotencyKey does not match providerId:externalTransactionId (messageId %s)", envelope.MessageID)
	}

	// messageId here is the envelope's own field, not sqsMessageID — per
	// challenge spec §10 ("Use o messageId do envelope como identidade
	// durável da mensagem"), that's the durable identity for inbox
	// dedup/hash-on-redelivery, deliberately distinct from whatever SQS
	// itself assigns at the transport level.
	messageID := envelope.MessageID
	hash := sha256.Sum256([]byte(body))

	entry, err := c.inbox.FindByConsumerAndMessage(ctx, c.consumerName, messageID)
	if err != nil && !errors.Is(err, app.ErrInboxEntryNotFound) {
		return err
	}
	if entry != nil {
		switch {
		case entry.PayloadHash() != hash:
			return fmt.Errorf("sqs consumer: messageId %s redelivered with different content than first seen", messageID)
		case entry.IsCompleted():
			// Pure redelivery of work we already finished — this is exactly
			// the "interrupted after commit, before delete" recovery case:
			// ack (delete, back in processMessage) without calling Submit
			// again.
			return nil
		}
	}

	// Challenge spec §6.5.5 ("o registro da inbox e a conclusão durável do
	// tratamento devem compartilhar a transação SQL das alterações de
	// domínio, do ledger e dos eventos correspondentes"): the inbox
	// row (new or being marked completed), Submit's wallet/ledger/wager/
	// outbox writes, and the final completed save all commit together or
	// not at all — TxManager.WithinTx is reentrant (see postgres.TxManager),
	// so Submit's own internal WithinTx joins this same transaction instead
	// of opening a second, unrelated one. A crash anywhere in this block
	// leaves nothing committed; redelivery starts over from entry == nil,
	// exactly as if this attempt had never happened.
	var result *app.SubmitWagerTransactionResult
	err = c.withinTx(ctx, func(ctx context.Context) error {
		if entry == nil {
			entry, err = inbox.New(inbox.NewInput{ConsumerName: c.consumerName, MessageID: messageID, PayloadHash: hash})
			if err != nil {
				return err
			}
			if err := c.inbox.Save(ctx, entry); err != nil {
				return err
			}
		}
		// entry already existed but wasn't completed: a previous attempt
		// crashed between Submit and MarkCompleted. Retrying Submit is
		// safe regardless — it's independently idempotent on
		// providerId:externalTransactionId (Fase 5), Inbox is a second,
		// cheaper layer of dedup on top, not the correctness mechanism
		// itself.
		var submitErr error
		result, submitErr = c.submitter.Submit(ctx, app.SubmitWagerTransactionInput{
			ProviderID:                     req.ProviderID,
			ExternalTransactionID:          req.ExternalTransactionID,
			PlayerID:                       req.PlayerID,
			WalletID:                       req.WalletID,
			RoundID:                        req.RoundID,
			GameID:                         req.GameID,
			Kind:                           req.Kind,
			Amount:                         req.Money,
			ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
			CorrelationID:                  correlationID,
		})
		if submitErr != nil {
			return submitErr
		}
		if err := entry.MarkCompleted(); err != nil {
			return err
		}
		return c.inbox.Save(ctx, entry)
	})
	if err != nil {
		return err
	}

	outcome := wagerOutcome(result)
	if c.metrics != nil {
		c.metrics.ObserveWagerSubmission(string(req.Kind), outcome)
	}
	// No amount/currency here, per Fase 11's "sem payloads financeiros
	// completos" — just enough to trace one submission across HTTP/SQS/logs.
	slog.InfoContext(ctx, "sqs consumer: wager transaction submitted",
		"consumer", c.consumerName, "messageId", messageID, "correlationId", correlationID,
		"providerId", req.ProviderID, "externalTransactionId", req.ExternalTransactionID,
		"walletId", req.WalletID, "outcome", outcome)
	return nil
}

// wagerOutcome classifies a submission result for logs/metrics — mirrors
// internal/httpapi's identical helper (kept separate rather than shared,
// same reasoning as this package's own copy of the request/response DTOs:
// internal/sqs has no reason to depend on internal/httpapi's unexported
// types for three lines of logic).
func wagerOutcome(result *app.SubmitWagerTransactionResult) string {
	if result.IdempotentReplay {
		return "replay"
	}
	if result.Transaction == nil {
		return "unknown"
	}
	return strings.ToLower(string(result.Transaction.Status()))
}
