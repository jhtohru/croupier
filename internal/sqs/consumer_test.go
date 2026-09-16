package sqs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/inbox"
	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wager"
)

func sha256Sum(s string) [32]byte {
	return sha256.Sum256([]byte(s))
}

func newUncompletedInbox(consumerName, messageID string, hash [32]byte) (*inbox.Inbox, error) {
	return inbox.New(inbox.NewInput{ConsumerName: consumerName, MessageID: messageID, PayloadHash: hash})
}

func mustMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.New("BRL", amount)
	require.NoError(t, err)
	return m
}

func testMessageBody(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(wagerTransactionMessage{
		ProviderID: "provider-a", ExternalTransactionID: "ext-1",
		PlayerID: uuid.New(), WalletID: uuid.New(), RoundID: "round-1", GameID: "game-1",
		Kind: wager.KindBet, Amount: mustMoney(t, "10.00"),
	})
	require.NoError(t, err)
	return string(b)
}

func TestConsumerHandle(t *testing.T) {
	const consumerName = "wager-transactions-consumer"

	t.Run("first delivery submits and marks inbox completed", func(t *testing.T) {
		submitter := &fakeWagerSubmitter{}
		inboxRepo := newFakeInboxRepository()
		c := &Consumer{consumerName: consumerName, submitter: submitter, inbox: inboxRepo}

		body := testMessageBody(t)
		msg := types.Message{MessageId: aws.String("msg-1"), Body: aws.String(body), ReceiptHandle: aws.String("rh-1")}

		err := c.handle(context.Background(), msg, "test-correlation-id")

		require.NoError(t, err)
		require.Len(t, submitter.calls, 1)
		assert.Equal(t, "provider-a", submitter.calls[0].ProviderID)
		entry, err := inboxRepo.FindByConsumerAndMessage(context.Background(), consumerName, "msg-1")
		require.NoError(t, err)
		assert.True(t, entry.IsCompleted())
	})

	t.Run("redelivery of already-completed work does not resubmit", func(t *testing.T) {
		submitter := &fakeWagerSubmitter{}
		inboxRepo := newFakeInboxRepository()
		c := &Consumer{consumerName: consumerName, submitter: submitter, inbox: inboxRepo}

		body := testMessageBody(t)
		msg := types.Message{MessageId: aws.String("msg-1"), Body: aws.String(body), ReceiptHandle: aws.String("rh-1")}

		require.NoError(t, c.handle(context.Background(), msg, "test-correlation-id"))
		require.Len(t, submitter.calls, 1)

		// Simulates exactly the "interrupted after commit, before delete"
		// scenario: SQS redelivers the same message, Submit must not run again.
		err := c.handle(context.Background(), msg, "test-correlation-id")

		require.NoError(t, err)
		assert.Len(t, submitter.calls, 1)
	})

	t.Run("retry after a crash before MarkCompleted resubmits (safe: Submit is itself idempotent)", func(t *testing.T) {
		submitter := &fakeWagerSubmitter{}
		inboxRepo := newFakeInboxRepository()
		c := &Consumer{consumerName: consumerName, submitter: submitter, inbox: inboxRepo}
		body := testMessageBody(t)
		msg := types.Message{MessageId: aws.String("msg-1"), Body: aws.String(body), ReceiptHandle: aws.String("rh-1")}

		// Pre-seed an inbox row that was created but never completed — as if
		// a previous attempt died between creating it and MarkCompleted.
		hash := sha256Sum(body)
		entry, err := newUncompletedInbox(consumerName, "msg-1", hash)
		require.NoError(t, err)
		require.NoError(t, inboxRepo.Save(context.Background(), entry))

		err = c.handle(context.Background(), msg, "test-correlation-id")

		require.NoError(t, err)
		assert.Len(t, submitter.calls, 1)
		got, err := inboxRepo.FindByConsumerAndMessage(context.Background(), consumerName, "msg-1")
		require.NoError(t, err)
		assert.True(t, got.IsCompleted())
	})

	t.Run("same messageId with different content is a permanent error", func(t *testing.T) {
		submitter := &fakeWagerSubmitter{}
		inboxRepo := newFakeInboxRepository()
		c := &Consumer{consumerName: consumerName, submitter: submitter, inbox: inboxRepo}

		first := types.Message{MessageId: aws.String("msg-1"), Body: aws.String(testMessageBody(t)), ReceiptHandle: aws.String("rh-1")}
		require.NoError(t, c.handle(context.Background(), first, "test-correlation-id"))

		different := types.Message{MessageId: aws.String("msg-1"), Body: aws.String(testMessageBody(t) + " "), ReceiptHandle: aws.String("rh-2")}
		err := c.handle(context.Background(), different, "test-correlation-id")

		assert.Error(t, err)
		assert.Len(t, submitter.calls, 1) // the second, mismatched body never reaches Submit
	})

	t.Run("malformed body never reaches Submit", func(t *testing.T) {
		submitter := &fakeWagerSubmitter{}
		inboxRepo := newFakeInboxRepository()
		c := &Consumer{consumerName: consumerName, submitter: submitter, inbox: inboxRepo}

		msg := types.Message{MessageId: aws.String("msg-1"), Body: aws.String("not json"), ReceiptHandle: aws.String("rh-1")}
		err := c.handle(context.Background(), msg, "test-correlation-id")

		assert.Error(t, err)
		assert.Empty(t, submitter.calls)
	})

	t.Run("Submit failure leaves inbox uncompleted", func(t *testing.T) {
		submitter := &fakeWagerSubmitter{err: errors.New("db unavailable")}
		inboxRepo := newFakeInboxRepository()
		c := &Consumer{consumerName: consumerName, submitter: submitter, inbox: inboxRepo}

		msg := types.Message{MessageId: aws.String("msg-1"), Body: aws.String(testMessageBody(t)), ReceiptHandle: aws.String("rh-1")}
		err := c.handle(context.Background(), msg, "test-correlation-id")

		assert.Error(t, err)
		got, err := inboxRepo.FindByConsumerAndMessage(context.Background(), consumerName, "msg-1")
		require.NoError(t, err)
		assert.False(t, got.IsCompleted())
	})
}

func TestConsumerProcessMessageDeletesOnlyOnSuccess(t *testing.T) {
	t.Run("success deletes the message", func(t *testing.T) {
		submitter := &fakeWagerSubmitter{}
		inboxRepo := newFakeInboxRepository()
		client := &fakeReceiveDeleter{}
		c := &Consumer{consumerName: "c", submitter: submitter, inbox: inboxRepo, client: client}

		msg := types.Message{MessageId: aws.String("msg-1"), Body: aws.String(testMessageBody(t)), ReceiptHandle: aws.String("rh-1")}
		c.processMessage(context.Background(), msg)

		assert.Equal(t, []string{"rh-1"}, client.deletedHandle)
	})

	t.Run("failure never deletes the message", func(t *testing.T) {
		submitter := &fakeWagerSubmitter{err: errors.New("db unavailable")}
		inboxRepo := newFakeInboxRepository()
		client := &fakeReceiveDeleter{}
		c := &Consumer{consumerName: "c", submitter: submitter, inbox: inboxRepo, client: client}

		msg := types.Message{MessageId: aws.String("msg-1"), Body: aws.String(testMessageBody(t)), ReceiptHandle: aws.String("rh-1")}
		c.processMessage(context.Background(), msg)

		assert.Empty(t, client.deletedHandle)
	})
}

func TestConsumerRunStopsOnContextCancellation(t *testing.T) {
	submitter := &fakeWagerSubmitter{}
	inboxRepo := newFakeInboxRepository()
	client := &fakeReceiveDeleter{receiveOut: &awssqs.ReceiveMessageOutput{}}
	c := &Consumer{consumerName: "c", submitter: submitter, inbox: inboxRepo, client: client, waitTime: 0, maxMessages: 10}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.Run(ctx)

	assert.ErrorIs(t, err, context.Canceled)
	// Cancelled before the first ReceiveMessage — never polled at all.
	assert.Equal(t, 0, client.receiveCalls)
}
