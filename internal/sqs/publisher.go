package sqs

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/outbox"
)

// eventEnvelope is the wire shape of every message Publisher sends — the
// same envelope regardless of event type, with the type-specific fields
// (already JSON from internal/app/events.go) nested under Data. EventID is
// the outbox entry's own id, unchanged across retries: a consumer reading it
// from the message body (not just from SQS's own MessageDeduplicationId,
// which isn't visible in the body) can recognize a republish after a crash
// as the same logical event, satisfying "republicação preservando eventId."
//
// CorrelationID/CausationID/Version match the challenge spec §11 literally
// ("O envelope deve conter eventId, eventType, aggregateId, correlationId,
// causationId opcional, occurredAt, version e data tipado") — all three are
// decided once, at event-construction time (internal/app/events.go, via
// outbox.Entry), not by Publisher; this envelope just carries them onto the
// wire unchanged. CausationID is omitted from the JSON entirely when nil,
// matching "causationId opcional."
type eventEnvelope struct {
	EventID       uuid.UUID       `json:"eventId"`
	AggregateType string          `json:"aggregateType"`
	AggregateID   uuid.UUID       `json:"aggregateId"`
	EventType     string          `json:"eventType"`
	Version       int             `json:"version"`
	OccurredAt    time.Time       `json:"occurredAt"`
	CorrelationID string          `json:"correlationId"`
	CausationID   *string         `json:"causationId,omitempty"`
	Data          json.RawMessage `json:"data"`
}

// sender is the one *sqs.Client method Publisher calls — consumer-defined,
// same reasoning as receiveDeleter in consumer.go, so Publish is unit-
// testable against a fake instead of requiring LocalStack.
type sender interface {
	SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

type Publisher struct {
	client   sender
	queueURL string
}

func NewPublisher(client *sqs.Client, queueURL string) *Publisher {
	return &Publisher{client: client, queueURL: queueURL}
}

// Publish sends entry as one FIFO message. MessageGroupId is the entry's
// aggregateId, so events about the same wallet/transaction are delivered in
// order relative to each other while different aggregates still parallelize;
// MessageDeduplicationId is the entry's own id, so a redelivery within SQS's
// dedup window doesn't need any extra bookkeeping on our side.
func (p *Publisher) Publish(ctx context.Context, entry *outbox.Entry) error {
	body, err := json.Marshal(eventEnvelope{
		EventID:       entry.ID(),
		AggregateType: entry.AggregateType(),
		AggregateID:   entry.AggregateID(),
		EventType:     entry.EventType(),
		Version:       entry.Version(),
		OccurredAt:    entry.OccurredAt(),
		CorrelationID: entry.CorrelationID(),
		CausationID:   entry.CausationID(),
		Data:          entry.Payload(),
	})
	if err != nil {
		return err
	}
	_, err = p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(p.queueURL),
		MessageBody:            aws.String(string(body)),
		MessageGroupId:         aws.String(entry.AggregateID().String()),
		MessageDeduplicationId: aws.String(entry.ID().String()),
	})
	return err
}
