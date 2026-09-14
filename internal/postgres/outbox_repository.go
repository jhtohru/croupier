package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jhtohru/croupier/internal/outbox"
)

type OutboxRepository struct {
	pool *pgxpool.Pool
}

func NewOutboxRepository(pool *pgxpool.Pool) *OutboxRepository {
	return &OutboxRepository{pool: pool}
}

func (r *OutboxRepository) SaveAll(ctx context.Context, entries ...*outbox.Entry) error {
	const q = `
		INSERT INTO outbox (id, aggregate_type, aggregate_id, event_type, payload, occurred_at, status, retry_count, next_send_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	db := dbFor(ctx, r.pool)
	for _, e := range entries {
		_, err := db.Exec(ctx, q,
			e.ID(), e.AggregateType(), e.AggregateID(), e.EventType(), e.Payload(),
			e.OccurredAt(), string(e.Status()), e.RetryCount(), e.NextSendAt(), e.CreatedAt(),
		)
		if err != nil {
			return err
		}
	}
	return nil
}
