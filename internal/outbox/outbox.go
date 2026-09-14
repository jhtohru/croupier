package outbox

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidInput      = errors.New("invalid input")
	ErrInvalidTransition = errors.New("invalid transition")
)

type Status string

const (
	StatusPending   Status = "PENDING"
	StatusPublished Status = "PUBLISHED"
)

type Entry struct {
	id            uuid.UUID
	aggregateType string
	aggregateID   uuid.UUID
	eventType     string
	payload       []byte
	occurredAt    time.Time
	status        Status
	retryCount    int
	nextSendAt    time.Time
	createdAt     time.Time
}

type NewEntryInput struct {
	AggregateType string
	AggregateID   uuid.UUID
	EventType     string
	Payload       []byte
	OccurredAt    time.Time
}

func NewEntry(input NewEntryInput) (*Entry, error) {
	if input.AggregateType == "" ||
		input.AggregateID == uuid.Nil ||
		input.EventType == "" ||
		len(input.Payload) == 0 ||
		input.OccurredAt.IsZero() {
		return nil, ErrInvalidInput
	}
	now := time.Now()
	return &Entry{
		id:            uuid.New(),
		aggregateType: input.AggregateType,
		aggregateID:   input.AggregateID,
		eventType:     input.EventType,
		payload:       input.Payload,
		occurredAt:    input.OccurredAt,
		status:        StatusPending,
		retryCount:    0,
		nextSendAt:    now,
		createdAt:     now,
	}, nil
}

func EntryFromPersistence(
	id uuid.UUID,
	aggregateType string,
	aggregateID uuid.UUID,
	eventType string,
	payload []byte,
	occurredAt time.Time,
	status Status,
	retryCount int,
	nextSendAt time.Time,
	createdAt time.Time,
) *Entry {
	return &Entry{
		id:            id,
		aggregateType: aggregateType,
		aggregateID:   aggregateID,
		eventType:     eventType,
		payload:       payload,
		occurredAt:    occurredAt,
		status:        status,
		retryCount:    retryCount,
		nextSendAt:    nextSendAt,
		createdAt:     createdAt,
	}
}

// MarkPublished records successful publication. It is idempotent: calling it
// on an already-published entry is a no-op, since multiple outbox worker
// instances may race to publish the same entry.
func (e *Entry) MarkPublished() {
	e.status = StatusPublished
}

func (e *Entry) ScheduleRetry(next time.Time) error {
	if e.status != StatusPending {
		return ErrInvalidTransition
	}
	e.retryCount++
	e.nextSendAt = next
	return nil
}

func (e Entry) ID() uuid.UUID {
	return e.id
}

func (e Entry) AggregateType() string {
	return e.aggregateType
}

func (e Entry) AggregateID() uuid.UUID {
	return e.aggregateID
}

func (e Entry) EventType() string {
	return e.eventType
}

func (e Entry) Payload() []byte {
	return e.payload
}

func (e Entry) OccurredAt() time.Time {
	return e.occurredAt
}

func (e Entry) Status() Status {
	return e.status
}

func (e Entry) RetryCount() int {
	return e.retryCount
}

func (e Entry) NextSendAt() time.Time {
	return e.nextSendAt
}

func (e Entry) CreatedAt() time.Time {
	return e.createdAt
}
