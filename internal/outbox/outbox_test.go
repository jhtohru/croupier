package outbox

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func validNewEntryInput() NewEntryInput {
	return NewEntryInput{
		AggregateType: "WagerTransaction",
		AggregateID:   uuid.New(),
		EventType:     "WagerTransactionProcessed",
		Version:       1,
		Payload:       []byte(`{"foo":"bar"}`),
		OccurredAt:    time.Now(),
		CorrelationID: "corr-1",
	}
}

func TestNewEntry(t *testing.T) {
	t.Run("empty aggregate type", func(t *testing.T) {
		input := validNewEntryInput()
		input.AggregateType = ""
		e, err := NewEntry(input)
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, e)
	})

	t.Run("nil aggregate id", func(t *testing.T) {
		input := validNewEntryInput()
		input.AggregateID = uuid.Nil
		e, err := NewEntry(input)
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, e)
	})

	t.Run("empty event type", func(t *testing.T) {
		input := validNewEntryInput()
		input.EventType = ""
		e, err := NewEntry(input)
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, e)
	})

	t.Run("empty payload", func(t *testing.T) {
		input := validNewEntryInput()
		input.Payload = nil
		e, err := NewEntry(input)
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, e)
	})

	t.Run("zero occurred at", func(t *testing.T) {
		input := validNewEntryInput()
		input.OccurredAt = time.Time{}
		e, err := NewEntry(input)
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, e)
	})

	t.Run("zero version", func(t *testing.T) {
		input := validNewEntryInput()
		input.Version = 0
		e, err := NewEntry(input)
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, e)
	})

	t.Run("empty correlation id", func(t *testing.T) {
		input := validNewEntryInput()
		input.CorrelationID = ""
		e, err := NewEntry(input)
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, e)
	})

	t.Run("success", func(t *testing.T) {
		input := validNewEntryInput()
		e, err := NewEntry(input)
		assert.NoError(t, err)
		if assert.NotNil(t, e) {
			assert.NotEqual(t, uuid.Nil, e.ID())
			assert.Equal(t, input.AggregateType, e.AggregateType())
			assert.Equal(t, input.AggregateID, e.AggregateID())
			assert.Equal(t, input.EventType, e.EventType())
			assert.Equal(t, input.Version, e.Version())
			assert.Equal(t, input.Payload, e.Payload())
			assert.Equal(t, input.OccurredAt, e.OccurredAt())
			assert.Equal(t, input.CorrelationID, e.CorrelationID())
			assert.Nil(t, e.CausationID())
			assert.Equal(t, StatusPending, e.Status())
			assert.Equal(t, 0, e.RetryCount())
			assert.False(t, e.NextSendAt().IsZero())
		}
	})

	t.Run("success with causation id", func(t *testing.T) {
		input := validNewEntryInput()
		causationID := "cause-1"
		input.CausationID = &causationID
		e, err := NewEntry(input)
		assert.NoError(t, err)
		if assert.NotNil(t, e) {
			assert.Equal(t, &causationID, e.CausationID())
		}
	})
}

func TestEntryFromPersistence(t *testing.T) {
	id := uuid.New()
	aggregateID := uuid.New()
	occurredAt := time.Now().Add(-time.Hour)
	nextSendAt := time.Now().Add(time.Minute)
	createdAt := time.Now().Add(-time.Hour)

	causationID := "cause-1"
	e := EntryFromPersistence(id, "WagerTransaction", aggregateID, "WagerTransactionProcessed", 1, []byte(`{}`), occurredAt, "corr-1", &causationID, StatusPending, 2, nextSendAt, createdAt)

	assert.Equal(t, id, e.ID())
	assert.Equal(t, "WagerTransaction", e.AggregateType())
	assert.Equal(t, aggregateID, e.AggregateID())
	assert.Equal(t, "WagerTransactionProcessed", e.EventType())
	assert.Equal(t, 1, e.Version())
	assert.Equal(t, []byte(`{}`), e.Payload())
	assert.Equal(t, occurredAt, e.OccurredAt())
	assert.Equal(t, "corr-1", e.CorrelationID())
	assert.Equal(t, &causationID, e.CausationID())
	assert.Equal(t, StatusPending, e.Status())
	assert.Equal(t, 2, e.RetryCount())
	assert.Equal(t, nextSendAt, e.NextSendAt())
	assert.Equal(t, createdAt, e.CreatedAt())
}

func TestEntryMarkPublished(t *testing.T) {
	t.Run("from pending", func(t *testing.T) {
		e := &Entry{status: StatusPending}
		e.MarkPublished()
		assert.Equal(t, StatusPublished, e.status)
	})

	t.Run("idempotent when already published", func(t *testing.T) {
		e := &Entry{status: StatusPublished}
		e.MarkPublished()
		assert.Equal(t, StatusPublished, e.status)
	})
}

func TestEntryScheduleRetry(t *testing.T) {
	t.Run("from pending", func(t *testing.T) {
		e := &Entry{status: StatusPending, retryCount: 1}
		next := time.Now().Add(time.Minute)
		err := e.ScheduleRetry(next)
		assert.NoError(t, err)
		assert.Equal(t, 2, e.retryCount)
		assert.Equal(t, next, e.nextSendAt)
	})

	t.Run("from published", func(t *testing.T) {
		e := &Entry{status: StatusPublished}
		err := e.ScheduleRetry(time.Now())
		assert.ErrorIs(t, err, ErrInvalidTransition)
	})
}
