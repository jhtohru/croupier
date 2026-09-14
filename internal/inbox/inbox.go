package inbox

import (
	"errors"
	"time"
)

var (
	ErrInvalidInput     = errors.New("invalid input")
	ErrAlreadyCompleted = errors.New("already completed")
)

type Inbox struct {
	consumerName string
	messageID    string
	payloadHash  [32]byte
	completedAt  *time.Time
	createdAt    time.Time
}

type NewInput struct {
	ConsumerName string
	MessageID    string
	PayloadHash  [32]byte
}

func New(input NewInput) (*Inbox, error) {
	if input.ConsumerName == "" || input.MessageID == "" {
		return nil, ErrInvalidInput
	}
	return &Inbox{
		consumerName: input.ConsumerName,
		messageID:    input.MessageID,
		payloadHash:  input.PayloadHash,
		createdAt:    time.Now(),
	}, nil
}

func FromPersistence(
	consumerName string,
	messageID string,
	payloadHash [32]byte,
	completedAt *time.Time,
	createdAt time.Time,
) *Inbox {
	return &Inbox{
		consumerName: consumerName,
		messageID:    messageID,
		payloadHash:  payloadHash,
		completedAt:  completedAt,
		createdAt:    createdAt,
	}
}

func (i *Inbox) MarkCompleted() error {
	if i.completedAt != nil {
		return ErrAlreadyCompleted
	}
	now := time.Now()
	i.completedAt = &now
	return nil
}

func (i Inbox) ConsumerName() string {
	return i.consumerName
}

func (i Inbox) MessageID() string {
	return i.messageID
}

func (i Inbox) PayloadHash() [32]byte {
	return i.payloadHash
}

func (i Inbox) CompletedAt() *time.Time {
	return i.completedAt
}

func (i Inbox) IsCompleted() bool {
	return i.completedAt != nil
}

func (i Inbox) CreatedAt() time.Time {
	return i.createdAt
}
