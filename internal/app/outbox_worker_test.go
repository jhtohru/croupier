package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/outbox"
)

type fakeOutboxPublisher struct {
	err       error
	published []*outbox.Entry
}

func (p *fakeOutboxPublisher) Publish(ctx context.Context, entry *outbox.Entry) error {
	p.published = append(p.published, entry)
	return p.err
}

func newTestEntry(t *testing.T) *outbox.Entry {
	t.Helper()
	e, err := outbox.NewEntry(outbox.NewEntryInput{
		AggregateType: "Wallet", AggregateID: uuid.New(),
		EventType: "WalletBalanceChanged", Version: 1, Payload: []byte(`{}`), OccurredAt: time.Now(),
		CorrelationID: "test-correlation-id",
	})
	require.NoError(t, err)
	return e
}

func TestOutboxWorkerRunOnce(t *testing.T) {
	t.Run("nothing due", func(t *testing.T) {
		worker := NewOutboxWorker(&fakeOutboxRepository{}, &fakeOutboxPublisher{}, fakeTxManager{}, time.Second)
		processed, err := worker.RunOnce(context.Background())
		require.NoError(t, err)
		assert.False(t, processed)
	})

	t.Run("publishes a due entry", func(t *testing.T) {
		entry := newTestEntry(t)
		outboxRepo := &fakeOutboxRepository{entries: []*outbox.Entry{entry}}
		publisher := &fakeOutboxPublisher{}
		worker := NewOutboxWorker(outboxRepo, publisher, fakeTxManager{}, time.Second)

		processed, err := worker.RunOnce(context.Background())

		require.NoError(t, err)
		assert.True(t, processed)
		require.Len(t, publisher.published, 1)
		assert.Equal(t, entry.ID(), publisher.published[0].ID())
		assert.Equal(t, outbox.StatusPublished, entry.Status())
	})

	t.Run("already published entries are never picked up again", func(t *testing.T) {
		entry := newTestEntry(t)
		entry.MarkPublished()
		outboxRepo := &fakeOutboxRepository{entries: []*outbox.Entry{entry}}
		publisher := &fakeOutboxPublisher{}
		worker := NewOutboxWorker(outboxRepo, publisher, fakeTxManager{}, time.Second)

		processed, err := worker.RunOnce(context.Background())

		require.NoError(t, err)
		assert.False(t, processed)
		assert.Empty(t, publisher.published)
	})

	t.Run("not yet due entries are skipped", func(t *testing.T) {
		entry := newTestEntry(t)
		require.NoError(t, entry.ScheduleRetry(time.Now().Add(time.Hour)))
		outboxRepo := &fakeOutboxRepository{entries: []*outbox.Entry{entry}}
		publisher := &fakeOutboxPublisher{}
		worker := NewOutboxWorker(outboxRepo, publisher, fakeTxManager{}, time.Second)

		processed, err := worker.RunOnce(context.Background())

		require.NoError(t, err)
		assert.False(t, processed)
	})

	t.Run("publish failure reschedules with backoff instead of failing RunOnce", func(t *testing.T) {
		entry := newTestEntry(t)
		outboxRepo := &fakeOutboxRepository{entries: []*outbox.Entry{entry}}
		publisher := &fakeOutboxPublisher{err: errors.New("sqs unavailable")}
		worker := NewOutboxWorker(outboxRepo, publisher, fakeTxManager{}, time.Second)

		before := time.Now()
		processed, err := worker.RunOnce(context.Background())

		require.NoError(t, err)
		assert.True(t, processed)
		assert.Equal(t, outbox.StatusPending, entry.Status())
		assert.Equal(t, 1, entry.RetryCount())
		assert.True(t, entry.NextSendAt().After(before))
	})
}
