package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/outbox"
)

func TestPublisherPublish(t *testing.T) {
	entry, err := outbox.NewEntry(outbox.NewEntryInput{
		AggregateType: "Wallet", AggregateID: uuid.New(),
		EventType: "WalletBalanceChanged", Payload: []byte(`{"walletId":"x"}`), OccurredAt: time.Now(),
	})
	require.NoError(t, err)

	t.Run("success", func(t *testing.T) {
		sender := &fakeSender{}
		p := &Publisher{client: sender, queueURL: "http://localstack:4566/000000000000/wallet-events.fifo"}

		err := p.Publish(context.Background(), entry)

		require.NoError(t, err)
		require.Len(t, sender.sent, 1)
		sent := sender.sent[0]
		assert.Equal(t, "http://localstack:4566/000000000000/wallet-events.fifo", aws.ToString(sent.QueueUrl))
		assert.Equal(t, entry.AggregateID().String(), aws.ToString(sent.MessageGroupId))
		assert.Equal(t, entry.ID().String(), aws.ToString(sent.MessageDeduplicationId))

		var envelope eventEnvelope
		require.NoError(t, json.Unmarshal([]byte(aws.ToString(sent.MessageBody)), &envelope))
		assert.Equal(t, entry.ID(), envelope.EventID)
		assert.Equal(t, entry.AggregateType(), envelope.AggregateType)
		assert.Equal(t, entry.EventType(), envelope.EventType)
		assert.JSONEq(t, `{"walletId":"x"}`, string(envelope.Data))
	})

	t.Run("sender error propagates", func(t *testing.T) {
		sender := &fakeSender{err: errors.New("throttled")}
		p := &Publisher{client: sender, queueURL: "q"}

		err := p.Publish(context.Background(), entry)

		assert.Error(t, err)
	})
}
