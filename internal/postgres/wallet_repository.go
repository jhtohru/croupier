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
	"github.com/jhtohru/croupier/internal/wallet"
)

type WalletRepository struct {
	pool *pgxpool.Pool
}

func NewWalletRepository(pool *pgxpool.Pool) *WalletRepository {
	return &WalletRepository{pool: pool}
}

func (r *WalletRepository) FindByID(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	q := `SELECT id, player_id, currency, balance, version, created_at, updated_at FROM wallets WHERE id = $1`
	if _, insideTx := txFromContext(ctx); insideTx {
		q += " FOR UPDATE"
	}
	w, err := scanWallet(dbFor(ctx, r.pool).QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrWalletNotFound
	}
	return w, err
}

func (r *WalletRepository) FindByPlayerAndCurrency(ctx context.Context, playerID uuid.UUID, currency money.Currency) (*wallet.Wallet, error) {
	q := `SELECT id, player_id, currency, balance, version, created_at, updated_at FROM wallets WHERE player_id = $1 AND currency = $2`
	if _, insideTx := txFromContext(ctx); insideTx {
		q += " FOR UPDATE"
	}
	w, err := scanWallet(dbFor(ctx, r.pool).QueryRow(ctx, q, playerID, string(currency)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrWalletNotFound
	}
	return w, err
}

// Save upserts by id (the write path for both a brand-new wallet and an
// update to one already loaded via FindByID). A concurrent Create for the
// same (player_id, currency) hits the wallets_player_currency_unique
// constraint instead — that's the actual duplicate-wallet guarantee, not a
// pre-check, since two concurrent inserts can both pass a pre-check.
func (r *WalletRepository) Save(ctx context.Context, w *wallet.Wallet) error {
	const q = `
		INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET
			balance = EXCLUDED.balance,
			version = EXCLUDED.version,
			updated_at = EXCLUDED.updated_at
	`
	_, err := dbFor(ctx, r.pool).Exec(ctx, q,
		w.ID(), w.PlayerID(), string(w.Balance().Currency()), w.Balance().Amount(), w.Version(), w.CreatedAt(), w.UpdatedAt(),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "wallets_player_currency_unique" {
			return app.ErrWalletAlreadyExists
		}
		return err
	}
	return nil
}

func (r *WalletRepository) SaveLedgerEntry(ctx context.Context, e *wallet.LedgerEntry) error {
	const q = `
		INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount, balance_before, balance_after, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := dbFor(ctx, r.pool).Exec(ctx, q,
		e.ID(), e.WalletID(), e.TransactionID(), string(e.Direction()),
		e.Amount().Amount(), e.BalanceBefore().Amount(), e.BalanceAfter().Amount(), e.CreatedAt(),
	)
	return err
}

const ledgerEntrySelect = `
	SELECT le.id, le.wallet_id, le.transaction_id, le.direction, le.amount, le.balance_before, le.balance_after, le.created_at, w.currency
	FROM wallet_ledger_entries le
	JOIN wallets w ON w.id = le.wallet_id
`

func (r *WalletRepository) FindLedgerEntryByTransactionID(ctx context.Context, transactionID uuid.UUID) (*wallet.LedgerEntry, error) {
	q := ledgerEntrySelect + " WHERE le.transaction_id = $1"
	e, err := scanLedgerEntry(dbFor(ctx, r.pool).QueryRow(ctx, q, transactionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrLedgerEntryNotFound
	}
	return e, err
}

func (r *WalletRepository) AllLedgerEntries(ctx context.Context, walletID uuid.UUID) ([]*wallet.LedgerEntry, error) {
	q := ledgerEntrySelect + " WHERE le.wallet_id = $1 ORDER BY le.created_at, le.id"
	rows, err := dbFor(ctx, r.pool).Query(ctx, q, walletID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*wallet.LedgerEntry
	for rows.Next() {
		e, err := scanLedgerEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (r *WalletRepository) ListLedgerEntries(ctx context.Context, walletID uuid.UUID, cursor *uuid.UUID, limit int) ([]*wallet.LedgerEntry, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if cursor == nil {
		q := ledgerEntrySelect + " WHERE le.wallet_id = $1 ORDER BY le.created_at, le.id LIMIT $2"
		rows, err = dbFor(ctx, r.pool).Query(ctx, q, walletID, limit)
	} else {
		q := ledgerEntrySelect + `
			WHERE le.wallet_id = $1 AND (le.created_at, le.id) > (
				SELECT created_at, id FROM wallet_ledger_entries WHERE id = $2
			)
			ORDER BY le.created_at, le.id
			LIMIT $3
		`
		rows, err = dbFor(ctx, r.pool).Query(ctx, q, walletID, *cursor, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*wallet.LedgerEntry
	for rows.Next() {
		e, err := scanLedgerEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanWallet(row scanner) (*wallet.Wallet, error) {
	var (
		id, playerID         uuid.UUID
		currency             string
		balance, version     int64
		createdAt, updatedAt time.Time
	)
	if err := row.Scan(&id, &playerID, &currency, &balance, &version, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	balanceMoney, err := money.FromMinorUnits(currency, balance)
	if err != nil {
		return nil, err
	}
	return wallet.FromPersistence(id, playerID, version, balanceMoney, createdAt, updatedAt), nil
}

func scanLedgerEntry(row scanner) (*wallet.LedgerEntry, error) {
	var (
		id, walletID, transactionID         uuid.UUID
		direction                           string
		amount, balanceBefore, balanceAfter int64
		createdAt                           time.Time
		currency                            string
	)
	if err := row.Scan(&id, &walletID, &transactionID, &direction, &amount, &balanceBefore, &balanceAfter, &createdAt, &currency); err != nil {
		return nil, err
	}
	amountMoney, err := money.FromMinorUnits(currency, amount)
	if err != nil {
		return nil, err
	}
	beforeMoney, err := money.FromMinorUnits(currency, balanceBefore)
	if err != nil {
		return nil, err
	}
	afterMoney, err := money.FromMinorUnits(currency, balanceAfter)
	if err != nil {
		return nil, err
	}
	return wallet.LedgerEntryFromPersistence(
		id, walletID, transactionID, wallet.Direction(direction), amountMoney, beforeMoney, afterMoney, createdAt,
	), nil
}
