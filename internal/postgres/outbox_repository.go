package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/outbox"
)

type OutboxRepository struct {
	pool *pgxpool.Pool
}

func NewOutboxRepository(pool *pgxpool.Pool) *OutboxRepository {
	return &OutboxRepository{pool: pool}
}

// SaveAll upserts by id — the write path both for brand-new entries (Fase 5/
// 6 use cases creating events atomically alongside their own writes) and for
// OutboxWorker advancing an existing entry's status/retry_count/next_send_at
// after a publish attempt. aggregate_type/aggregate_id/event_type/payload/
// occurred_at/created_at never change after creation, so only the columns
// that actually do are in the SET clause — same reasoning as
// WagerRepository.Save.
func (r *OutboxRepository) SaveAll(ctx context.Context, entries ...*outbox.Entry) error {
	const q = `
		INSERT INTO outbox (id, aggregate_type, aggregate_id, event_type, version, payload, occurred_at, correlation_id, causation_id, status, retry_count, next_send_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			retry_count = EXCLUDED.retry_count,
			next_send_at = EXCLUDED.next_send_at
	`
	db := dbFor(ctx, r.pool)
	for _, e := range entries {
		_, err := db.Exec(ctx, q,
			e.ID(), e.AggregateType(), e.AggregateID(), e.EventType(), e.Version(), e.Payload(),
			e.OccurredAt(), e.CorrelationID(), e.CausationID(), string(e.Status()), e.RetryCount(), e.NextSendAt(), e.CreatedAt(),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// FindDueForUpdate returns the single oldest due PENDING entry, locked via
// FOR UPDATE SKIP LOCKED so concurrent OutboxWorker instances (or repeated
// calls within the same process) never contend for or double-claim the same
// row — see the interface doc in internal/app/repository.go for why this
// alone is enough to recover abandoned work with no separate lease column.
func (r *OutboxRepository) FindDueForUpdate(ctx context.Context) (*outbox.Entry, error) {
	const q = `
		SELECT id, aggregate_type, aggregate_id, event_type, version, payload, occurred_at, correlation_id, causation_id, status, retry_count, next_send_at, created_at
		FROM outbox
		WHERE status = 'PENDING' AND next_send_at <= now()
		ORDER BY created_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`
	entry, err := scanOutboxEntry(dbFor(ctx, r.pool).QueryRow(ctx, q))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrOutboxEntryNotFound
	}
	return entry, err
}

func scanOutboxEntry(row scanner) (*outbox.Entry, error) {
	var (
		id, aggregateID                   uuid.UUID
		aggregateType, eventType, status  string
		version                           int
		payload                           []byte
		occurredAt, nextSendAt, createdAt time.Time
		correlationID                     string
		causationID                       *string
		retryCount                        int
	)
	if err := row.Scan(&id, &aggregateType, &aggregateID, &eventType, &version, &payload, &occurredAt, &correlationID, &causationID, &status, &retryCount, &nextSendAt, &createdAt); err != nil {
		return nil, err
	}
	return outbox.EntryFromPersistence(id, aggregateType, aggregateID, eventType, version, payload, occurredAt, correlationID, causationID, outbox.Status(status), retryCount, nextSendAt, createdAt), nil
}
