package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wager"
)

type WagerRepository struct {
	pool *pgxpool.Pool
}

func NewWagerRepository(pool *pgxpool.Pool) *WagerRepository {
	return &WagerRepository{pool: pool}
}

const wagerTransactionSelect = `
	SELECT id, status, kind, provider_id, external_transaction_id, round_id, game_id,
	       player_id, wallet_id, currency, amount, reference_external_transaction_id,
	       reference_transaction_id, failure_code, created_at, updated_at
	FROM wager_transactions
`

func (r *WagerRepository) Save(ctx context.Context, tx *wager.Transaction) error {
	const q = `
		INSERT INTO wager_transactions (
			id, status, kind, provider_id, external_transaction_id, round_id, game_id,
			player_id, wallet_id, currency, amount, reference_external_transaction_id,
			reference_transaction_id, failure_code, payload_hash, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			reference_transaction_id = EXCLUDED.reference_transaction_id,
			failure_code = EXCLUDED.failure_code,
			updated_at = EXCLUDED.updated_at
	`
	payloadHash := tx.PayloadHash()
	var failureCode *string
	if tx.FailureCode() != "" {
		s := string(tx.FailureCode())
		failureCode = &s
	}
	_, err := dbFor(ctx, r.pool).Exec(ctx, q,
		tx.ID(), string(tx.Status()), string(tx.Kind()), nullableString(tx.ProviderID()), nullableString(tx.ExternalTransactionID()),
		nullableString(tx.RoundID()), nullableString(tx.GameID()), tx.PlayerID(), tx.WalletID(),
		string(tx.Amount().Currency()), tx.Amount().Amount(), tx.ReferenceExternalTransactionID(),
		tx.ReferenceTransactionID(), failureCode, payloadHash[:], tx.CreatedAt(), tx.UpdatedAt(),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "wager_transactions_provider_external_unique" {
			// Lost a race: some other concurrent Save for this exact
			// (providerId, externalTransactionId) — necessarily a different
			// tx.ID(), since ON CONFLICT (id) above only catches a retry of
			// this same row — committed first. WagerSubmitter.Submit catches
			// this and retries as a plain idempotent replay.
			return app.ErrWagerTransactionAlreadyExists
		}
		return err
	}
	return nil
}

func (r *WagerRepository) FindByID(ctx context.Context, id uuid.UUID) (*wager.Transaction, error) {
	q := wagerTransactionSelect + " WHERE id = $1"
	tx, err := scanWagerTransaction(dbFor(ctx, r.pool).QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrWagerTransactionNotFound
	}
	return tx, err
}

func (r *WagerRepository) FindByProviderAndExternalID(ctx context.Context, providerID, externalTransactionID string) (*wager.Transaction, error) {
	q := wagerTransactionSelect + " WHERE provider_id = $1 AND external_transaction_id = $2"
	tx, err := scanWagerTransaction(dbFor(ctx, r.pool).QueryRow(ctx, q, providerID, externalTransactionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrWagerTransactionNotFound
	}
	return tx, err
}

func (r *WagerRepository) FindReversal(ctx context.Context, referencedTransactionID uuid.UUID, kind wager.Kind) (*wager.Transaction, error) {
	q := wagerTransactionSelect + " WHERE reference_transaction_id = $1 AND kind = $2 AND status = $3"
	tx, err := scanWagerTransaction(dbFor(ctx, r.pool).QueryRow(ctx, q, referencedTransactionID, string(kind), string(wager.TxStatusProcessed)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrWagerTransactionNotFound
	}
	return tx, err
}

func (r *WagerRepository) FindDuePendingReferences(ctx context.Context, now time.Time, limit int) ([]app.PendingReferenceRetry, error) {
	q := `
		SELECT id, status, kind, provider_id, external_transaction_id, round_id, game_id,
		       player_id, wallet_id, currency, amount, reference_external_transaction_id,
		       reference_transaction_id, failure_code, created_at, updated_at, pending_reference_attempts
		FROM wager_transactions
		WHERE status = $1 AND (pending_reference_next_retry_at IS NULL OR pending_reference_next_retry_at <= $2)
		ORDER BY created_at
		LIMIT $3
	`
	rows, err := dbFor(ctx, r.pool).Query(ctx, q, string(wager.TxStatusPendingReference), now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []app.PendingReferenceRetry
	for rows.Next() {
		tx, attempts, err := scanWagerTransactionWithAttempts(rows)
		if err != nil {
			return nil, err
		}
		due = append(due, app.PendingReferenceRetry{Transaction: tx, Attempts: attempts})
	}
	return due, rows.Err()
}

func (r *WagerRepository) ScheduleNextPendingReferenceRetry(ctx context.Context, id uuid.UUID, nextRetryAt time.Time) error {
	const q = `
		UPDATE wager_transactions
		SET pending_reference_attempts = pending_reference_attempts + 1,
		    pending_reference_next_retry_at = $2
		WHERE id = $1 AND status = $3
	`
	_, err := dbFor(ctx, r.pool).Exec(ctx, q, id, nextRetryAt, string(wager.TxStatusPendingReference))
	return err
}

// nullableString maps the domain's zero-value-means-absent convention
// (empty string, used for OPENING's provider/external/round/game fields) to
// SQL NULL — the schema stores these as nullable so multiple OPENING rows
// don't collide on the (provider_id, external_transaction_id) uniqueness.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// scanWagerTransactionWithAttempts scans a row shaped like
// FindDuePendingReferences's query — the same columns as scanWagerTransaction
// plus pending_reference_attempts, which lives outside the domain type (see
// app.PendingReferenceRetry) so it comes back as a separate return value.
func scanWagerTransactionWithAttempts(row scanner) (*wager.Transaction, int, error) {
	var (
		id, playerID, walletID                             uuid.UUID
		status, kind, currency                             string
		providerID, externalTransactionID, roundID, gameID *string
		amount                                             int64
		referenceExternalTransactionID                     *string
		referenceTransactionID                             *uuid.UUID
		failureCode                                        *string
		createdAt, updatedAt                               time.Time
		attempts                                           int
	)
	if err := row.Scan(
		&id, &status, &kind, &providerID, &externalTransactionID, &roundID, &gameID,
		&playerID, &walletID, &currency, &amount, &referenceExternalTransactionID,
		&referenceTransactionID, &failureCode, &createdAt, &updatedAt, &attempts,
	); err != nil {
		return nil, 0, err
	}
	amountMoney, err := money.FromMinorUnits(currency, amount)
	if err != nil {
		return nil, 0, err
	}
	var fc wager.FailureCode
	if failureCode != nil {
		fc = wager.FailureCode(*failureCode)
	}
	tx := wager.TransactionFromPersistence(wager.TransactionFromPersistenceInput{
		ID:                             id,
		Status:                         wager.TxStatus(status),
		Kind:                           wager.Kind(kind),
		ProviderID:                     stringOrEmpty(providerID),
		ExternalTransactionID:          stringOrEmpty(externalTransactionID),
		RoundID:                        stringOrEmpty(roundID),
		GameID:                         stringOrEmpty(gameID),
		PlayerID:                       playerID,
		WalletID:                       walletID,
		Amount:                         amountMoney,
		ReferenceExternalTransactionID: referenceExternalTransactionID,
		ReferenceTransactionID:         referenceTransactionID,
		FailureCode:                    fc,
		CreatedAt:                      createdAt,
		UpdatedAt:                      updatedAt,
	})
	return tx, attempts, nil
}

func scanWagerTransaction(row scanner) (*wager.Transaction, error) {
	var (
		id, playerID, walletID                             uuid.UUID
		status, kind, currency                             string
		providerID, externalTransactionID, roundID, gameID *string
		amount                                             int64
		referenceExternalTransactionID                     *string
		referenceTransactionID                             *uuid.UUID
		failureCode                                        *string
		createdAt, updatedAt                               time.Time
	)
	if err := row.Scan(
		&id, &status, &kind, &providerID, &externalTransactionID, &roundID, &gameID,
		&playerID, &walletID, &currency, &amount, &referenceExternalTransactionID,
		&referenceTransactionID, &failureCode, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	amountMoney, err := money.FromMinorUnits(currency, amount)
	if err != nil {
		return nil, err
	}
	var fc wager.FailureCode
	if failureCode != nil {
		fc = wager.FailureCode(*failureCode)
	}
	return wager.TransactionFromPersistence(wager.TransactionFromPersistenceInput{
		ID:                             id,
		Status:                         wager.TxStatus(status),
		Kind:                           wager.Kind(kind),
		ProviderID:                     stringOrEmpty(providerID),
		ExternalTransactionID:          stringOrEmpty(externalTransactionID),
		RoundID:                        stringOrEmpty(roundID),
		GameID:                         stringOrEmpty(gameID),
		PlayerID:                       playerID,
		WalletID:                       walletID,
		Amount:                         amountMoney,
		ReferenceExternalTransactionID: referenceExternalTransactionID,
		ReferenceTransactionID:         referenceTransactionID,
		FailureCode:                    fc,
		CreatedAt:                      createdAt,
		UpdatedAt:                      updatedAt,
	}), nil
}

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
