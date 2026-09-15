package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/inbox"
)

type InboxRepository struct {
	pool *pgxpool.Pool
}

func NewInboxRepository(pool *pgxpool.Pool) *InboxRepository {
	return &InboxRepository{pool: pool}
}

func (r *InboxRepository) FindByConsumerAndMessage(ctx context.Context, consumerName, messageID string) (*inbox.Inbox, error) {
	const q = `SELECT consumer_name, message_id, payload_hash, completed_at, created_at FROM inbox WHERE consumer_name = $1 AND message_id = $2`
	var (
		gotConsumer, gotMessage string
		payloadHash             []byte
		completedAt             *time.Time
		createdAt               time.Time
	)
	err := dbFor(ctx, r.pool).QueryRow(ctx, q, consumerName, messageID).Scan(&gotConsumer, &gotMessage, &payloadHash, &completedAt, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrInboxEntryNotFound
	}
	if err != nil {
		return nil, err
	}
	var hash [32]byte
	copy(hash[:], payloadHash)
	return inbox.FromPersistence(gotConsumer, gotMessage, hash, completedAt, createdAt), nil
}

// Save upserts by (consumer_name, message_id) — the write path for both a
// brand-new inbox entry and marking one already loaded as completed.
// completed_at is the only column that legitimately changes after creation
// (mirrors WagerRepository.Save's reasoning: only re-write what the domain
// actually mutates).
func (r *InboxRepository) Save(ctx context.Context, i *inbox.Inbox) error {
	const q = `
		INSERT INTO inbox (consumer_name, message_id, payload_hash, completed_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (consumer_name, message_id) DO UPDATE SET
			completed_at = EXCLUDED.completed_at
	`
	hash := i.PayloadHash()
	_, err := dbFor(ctx, r.pool).Exec(ctx, q, i.ConsumerName(), i.MessageID(), hash[:], i.CompletedAt(), i.CreatedAt())
	return err
}
