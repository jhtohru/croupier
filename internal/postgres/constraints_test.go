//go:build integration

// Challenge spec §13.15 ("Verifique constraints") and §13.16 ("Verifique
// imutabilidade do ledger"): the schema's CHECK constraints and the
// wallet_ledger_entries immutability trigger were previously only verified
// once, by hand, against a real Postgres (see ARCHITECTURE.md) — never as a
// repeatable go test. This file exercises them directly with raw SQL,
// bypassing internal/wallet's own validation entirely, so a constraint
// that's actually missing or wrong at the schema level would be caught even
// if every Go-level guard happened to also be correct.
package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/postgres"
	"github.com/jhtohru/croupier/internal/wallet"
)

func TestWalletsTableConstraints(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	insertWallet := func(balance, version int64) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at)
			VALUES ($1, $2, 'BRL', $3, $4, now(), now())
		`, uuid.New(), uuid.New(), balance, version)
		return err
	}

	t.Run("wallets_balance_non_negative rejects a negative balance", func(t *testing.T) {
		err := insertWallet(-1, 1)
		require.Error(t, err)
		assert.ErrorContains(t, err, "wallets_balance_non_negative")
	})

	t.Run("wallets_version_positive rejects version zero", func(t *testing.T) {
		err := insertWallet(1000, 0)
		require.Error(t, err)
		assert.ErrorContains(t, err, "wallets_version_positive")
	})

	t.Run("a valid row is accepted", func(t *testing.T) {
		assert.NoError(t, insertWallet(0, 1))
	})
}

func TestWalletLedgerEntriesTableConstraints(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	balance, err := money.FromMinorUnits("BRL", 1000)
	require.NoError(t, err)
	w, err := wallet.New(uuid.New(), balance)
	require.NoError(t, err)
	require.NoError(t, postgres.NewWalletRepository(pool).Save(ctx, w))

	insertEntry := func(direction string, amount, balanceBefore, balanceAfter int64) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount, balance_before, balance_after, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		`, uuid.New(), w.ID(), uuid.New(), direction, amount, balanceBefore, balanceAfter)
		return err
	}

	t.Run("wallet_ledger_entries_amount_positive rejects a zero amount", func(t *testing.T) {
		err := insertEntry("CREDIT", 0, 1000, 1000)
		require.Error(t, err)
		assert.ErrorContains(t, err, "wallet_ledger_entries_amount_positive")
	})

	t.Run("wallet_ledger_entries_balance_consistent rejects a mismatched CREDIT", func(t *testing.T) {
		err := insertEntry("CREDIT", 500, 1000, 1000) // should be 1500
		require.Error(t, err)
		assert.ErrorContains(t, err, "wallet_ledger_entries_balance_consistent")
	})

	t.Run("wallet_ledger_entries_balance_consistent rejects a mismatched DEBIT", func(t *testing.T) {
		err := insertEntry("DEBIT", 500, 1000, 1000) // should be 500
		require.Error(t, err)
		assert.ErrorContains(t, err, "wallet_ledger_entries_balance_consistent")
	})

	t.Run("wallet_ledger_entries_balance_after_non_negative rejects a negative result", func(t *testing.T) {
		err := insertEntry("DEBIT", 1500, 1000, -500)
		require.Error(t, err)
		assert.ErrorContains(t, err, "wallet_ledger_entries_balance_after_non_negative")
	})

	t.Run("a valid entry is accepted, then is immutable", func(t *testing.T) {
		// Challenge spec §13.16 and §5's "o ledger deve ser append-only":
		// wallet_ledger_entries_immutable (a BEFORE UPDATE OR DELETE
		// trigger) must reject both mutation forms, not just one.
		id := uuid.New()
		_, err := pool.Exec(ctx, `
			INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, amount, balance_before, balance_after, created_at)
			VALUES ($1, $2, $3, 'CREDIT', 500, 1000, 1500, now())
		`, id, w.ID(), uuid.New())
		require.NoError(t, err)

		_, err = pool.Exec(ctx, `UPDATE wallet_ledger_entries SET amount = 999 WHERE id = $1`, id)
		require.Error(t, err)
		assert.ErrorContains(t, err, "append-only")

		_, err = pool.Exec(ctx, `DELETE FROM wallet_ledger_entries WHERE id = $1`, id)
		require.Error(t, err)
		assert.ErrorContains(t, err, "append-only")
	})
}
