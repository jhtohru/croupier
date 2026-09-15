package sqs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

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

// wagerTransactionMessage is the inbound message body shape — the same
// fields httpapi's submitWagerTransactionRequest carries (both ingestion
// paths feed the identical app.SubmitWagerTransactionInput), defined
// separately here since internal/sqs has no reason to depend on
// internal/httpapi's unexported types.
type wagerTransactionMessage struct {
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	PlayerID                       uuid.UUID   `json:"playerId"`
	WalletID                       uuid.UUID   `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           wager.Kind  `json:"kind"`
	Amount                         money.Money `json:"amount"`
	ReferenceExternalTransactionID *string     `json:"referenceExternalTransactionId,omitempty"`
}

type Consumer struct {
	client       receiveDeleter
	queueURL     string
	consumerName string
	submitter    wagerSubmitter
	inbox        app.InboxRepository
	waitTime     int32
	maxMessages  int32
}

func NewConsumer(client *sqs.Client, queueURL, consumerName string, submitter wagerSubmitter, inboxRepo app.InboxRepository) *Consumer {
	return &Consumer{
		client:       client,
		queueURL:     queueURL,
		consumerName: consumerName,
		submitter:    submitter,
		inbox:        inboxRepo,
		waitTime:     20,
		maxMessages:  10,
	}
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
// a loop here — Fase 10's supervisor decides whether and how to restart Run,
// this package doesn't invent its own retry policy for that.
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
	if err := c.handle(ctx, msg); err != nil {
		slog.ErrorContext(ctx, "sqs consumer: message processing failed, leaving for redelivery",
			"consumer", c.consumerName, "messageId", messageID, "error", err)
		return
	}
	if _, err := c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.queueURL),
		ReceiptHandle: msg.ReceiptHandle,
	}); err != nil {
		// The work is already durably done at this point — a failed delete
		// only risks a harmless redelivery (caught by the Inbox check below
		// on the next attempt), not reprocessing.
		slog.ErrorContext(ctx, "sqs consumer: failed to delete processed message",
			"consumer", c.consumerName, "messageId", messageID, "error", err)
	}
}

func (c *Consumer) handle(ctx context.Context, msg types.Message) error {
	messageID := aws.ToString(msg.MessageId)
	body := aws.ToString(msg.Body)
	hash := sha256.Sum256([]byte(body))

	entry, err := c.inbox.FindByConsumerAndMessage(ctx, c.consumerName, messageID)
	if err != nil && !errors.Is(err, app.ErrInboxEntryNotFound) {
		return err
	}
	switch {
	case entry == nil:
		entry, err = inbox.New(inbox.NewInput{ConsumerName: c.consumerName, MessageID: messageID, PayloadHash: hash})
		if err != nil {
			return err
		}
		if err := c.inbox.Save(ctx, entry); err != nil {
			return err
		}
	case entry.PayloadHash() != hash:
		return fmt.Errorf("sqs consumer: messageId %s redelivered with different content than first seen", messageID)
	case entry.IsCompleted():
		// Pure redelivery of work we already finished — this is exactly the
		// "interrupted after commit, before delete" recovery case: ack
		// (delete, back in processMessage) without calling Submit again.
		return nil
	}
	// entry exists but isn't completed: a previous attempt crashed between
	// Submit and MarkCompleted below. Falling through to retry Submit is
	// safe regardless — it's independently idempotent on
	// providerId:externalTransactionId (Fase 5), Inbox is a second,
	// cheaper layer of dedup on top, not the correctness mechanism itself.

	var req wagerTransactionMessage
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return fmt.Errorf("sqs consumer: malformed message body: %w", err)
	}

	if _, err := c.submitter.Submit(ctx, app.SubmitWagerTransactionInput{
		ProviderID:                     req.ProviderID,
		ExternalTransactionID:          req.ExternalTransactionID,
		PlayerID:                       req.PlayerID,
		WalletID:                       req.WalletID,
		RoundID:                        req.RoundID,
		GameID:                         req.GameID,
		Kind:                           req.Kind,
		Amount:                         req.Amount,
		ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
	}); err != nil {
		return err
	}

	if err := entry.MarkCompleted(); err != nil {
		return err
	}
	return c.inbox.Save(ctx, entry)
}
