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
	version       int
	payload       []byte
	occurredAt    time.Time
	correlationID string
	causationID   *string
	status        Status
	retryCount    int
	nextSendAt    time.Time
	createdAt     time.Time
}

// NewEntryInput.CorrelationID is required — every event traces back to
// whatever HTTP request or SQS delivery caused it (challenge spec §11: the
// event envelope "deve conter ... correlationId"). CausationID is optional
// (spec: "causationId opcional") and, when set, names the specific sibling
// event that directly caused this one — e.g. WalletBalanceChanged's
// causationId is the WagerTransactionProcessed event emitted in the same
// call, not just the same request-level correlationId every event in that
// call already shares. Version is decided by the event's own constructor
// (internal/app/events.go), not by the caller, matching "Tipo e versão devem
// ser definidos pelo construtor do evento."
type NewEntryInput struct {
	AggregateType string
	AggregateID   uuid.UUID
	EventType     string
	Version       int
	Payload       []byte
	OccurredAt    time.Time
	CorrelationID string
	CausationID   *string
}

func NewEntry(input NewEntryInput) (*Entry, error) {
	if input.AggregateType == "" ||
		input.AggregateID == uuid.Nil ||
		input.EventType == "" ||
		input.Version <= 0 ||
		len(input.Payload) == 0 ||
		input.OccurredAt.IsZero() ||
		input.CorrelationID == "" {
		return nil, ErrInvalidInput
	}
	now := time.Now().UTC()
	return &Entry{
		id:            uuid.New(),
		aggregateType: input.AggregateType,
		aggregateID:   input.AggregateID,
		eventType:     input.EventType,
		version:       input.Version,
		payload:       input.Payload,
		occurredAt:    input.OccurredAt,
		correlationID: input.CorrelationID,
		causationID:   input.CausationID,
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
	version int,
	payload []byte,
	occurredAt time.Time,
	correlationID string,
	causationID *string,
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
		version:       version,
		payload:       payload,
		occurredAt:    occurredAt,
		correlationID: correlationID,
		causationID:   causationID,
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

func (e Entry) Version() int {
	return e.version
}

func (e Entry) Payload() []byte {
	return e.payload
}

func (e Entry) OccurredAt() time.Time {
	return e.occurredAt
}

func (e Entry) CorrelationID() string {
	return e.correlationID
}

func (e Entry) CausationID() *string {
	return e.causationID
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
