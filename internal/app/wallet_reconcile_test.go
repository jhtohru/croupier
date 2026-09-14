package app

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wallet"
)

func TestWalletReconcilerReconcile(t *testing.T) {
	t.Run("wallet not found", func(t *testing.T) {
		wallets := newFakeWalletRepository()
		r := NewWalletReconciler(wallets)

		result, err := r.Reconcile(context.Background(), uuid.New())
		assert.ErrorIs(t, err, ErrWalletNotFound)
		assert.Nil(t, result)
	})

	t.Run("consistent with no ledger entries", func(t *testing.T) {
		wallets := newFakeWalletRepository()
		zero, err := money.Zero("BRL")
		if err != nil {
			t.Fatal(err)
		}
		w, err := wallet.New(uuid.New(), zero)
		if err != nil {
			t.Fatal(err)
		}
		if err := wallets.Save(context.Background(), w); err != nil {
			t.Fatal(err)
		}

		r := NewWalletReconciler(wallets)
		result, err := r.Reconcile(context.Background(), w.ID())
		assert.NoError(t, err)
		assert.True(t, result.Consistent)
		assert.Equal(t, 0, result.EntriesChecked)
		assert.Equal(t, zero, result.CalculatedBalance)
		assert.Equal(t, zero, result.Difference)
	})

	t.Run("consistent after opening, credit and debit", func(t *testing.T) {
		wallets := newFakeWalletRepository()
		zero, err := money.Zero("BRL")
		if err != nil {
			t.Fatal(err)
		}
		w, err := wallet.New(uuid.New(), zero)
		if err != nil {
			t.Fatal(err)
		}

		// Reconcile sums the ledger from scratch, including OPENING — a
		// wallet built with a nonzero initial balance but no matching entry
		// would be an unrealistic state (WalletCreator.Create always writes
		// one), so the test constructs that entry explicitly too.
		beforeOpening := w.Balance()
		if err := w.Credit(mustAmount(t, 1000)); err != nil {
			t.Fatal(err)
		}
		openingEntry, err := wallet.NewLedgerEntry(wallet.NewLedgerEntryInput{
			WalletID: w.ID(), TransactionID: uuid.New(), Direction: wallet.DirectionCredit,
			Amount: mustAmount(t, 1000), BalanceBefore: beforeOpening, BalanceAfter: w.Balance(),
		})
		if err != nil {
			t.Fatal(err)
		}

		beforeCredit := w.Balance()
		if err := w.Credit(mustAmount(t, 500)); err != nil {
			t.Fatal(err)
		}
		entry1, err := wallet.NewLedgerEntry(wallet.NewLedgerEntryInput{
			WalletID: w.ID(), TransactionID: uuid.New(), Direction: wallet.DirectionCredit,
			Amount: mustAmount(t, 500), BalanceBefore: beforeCredit, BalanceAfter: w.Balance(),
		})
		if err != nil {
			t.Fatal(err)
		}

		beforeDebit := w.Balance()
		if err := w.Debit(mustAmount(t, 200)); err != nil {
			t.Fatal(err)
		}
		entry2, err := wallet.NewLedgerEntry(wallet.NewLedgerEntryInput{
			WalletID: w.ID(), TransactionID: uuid.New(), Direction: wallet.DirectionDebit,
			Amount: mustAmount(t, 200), BalanceBefore: beforeDebit, BalanceAfter: w.Balance(),
		})
		if err != nil {
			t.Fatal(err)
		}

		if err := wallets.Save(context.Background(), w); err != nil {
			t.Fatal(err)
		}
		if err := wallets.SaveLedgerEntry(context.Background(), openingEntry); err != nil {
			t.Fatal(err)
		}
		if err := wallets.SaveLedgerEntry(context.Background(), entry1); err != nil {
			t.Fatal(err)
		}
		if err := wallets.SaveLedgerEntry(context.Background(), entry2); err != nil {
			t.Fatal(err)
		}

		r := NewWalletReconciler(wallets)
		result, err := r.Reconcile(context.Background(), w.ID())
		assert.NoError(t, err)
		assert.True(t, result.Consistent)
		assert.Equal(t, 3, result.EntriesChecked)
		assert.Equal(t, mustAmount(t, 1300), result.CalculatedBalance)
		assert.Equal(t, mustAmount(t, 1300), result.StoredBalance)
	})

	t.Run("inconsistent when stored balance diverges from ledger", func(t *testing.T) {
		wallets := newFakeWalletRepository()
		zero, err := money.Zero("BRL")
		if err != nil {
			t.Fatal(err)
		}
		w, err := wallet.New(uuid.New(), zero)
		if err != nil {
			t.Fatal(err)
		}

		beforeOpening := w.Balance()
		if err := w.Credit(mustAmount(t, 1000)); err != nil {
			t.Fatal(err)
		}
		openingEntry, err := wallet.NewLedgerEntry(wallet.NewLedgerEntryInput{
			WalletID: w.ID(), TransactionID: uuid.New(), Direction: wallet.DirectionCredit,
			Amount: mustAmount(t, 1000), BalanceBefore: beforeOpening, BalanceAfter: w.Balance(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := wallets.Save(context.Background(), w); err != nil {
			t.Fatal(err)
		}
		if err := wallets.SaveLedgerEntry(context.Background(), openingEntry); err != nil {
			t.Fatal(err)
		}

		// A further ledger entry claims a credit happened, but the stored
		// wallet was never actually updated to match it — simulates data
		// corruption that reconciliation is meant to surface, not fix.
		entry, err := wallet.NewLedgerEntry(wallet.NewLedgerEntryInput{
			WalletID: w.ID(), TransactionID: uuid.New(), Direction: wallet.DirectionCredit,
			Amount: mustAmount(t, 500), BalanceBefore: mustAmount(t, 1000), BalanceAfter: mustAmount(t, 1500),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := wallets.SaveLedgerEntry(context.Background(), entry); err != nil {
			t.Fatal(err)
		}

		r := NewWalletReconciler(wallets)
		result, err := r.Reconcile(context.Background(), w.ID())
		assert.NoError(t, err)
		assert.False(t, result.Consistent)
		assert.Equal(t, mustAmount(t, 1000), result.StoredBalance)
		assert.Equal(t, mustAmount(t, 1500), result.CalculatedBalance)
		wantDifference, err := money.FromMinorUnits("BRL", -500)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, wantDifference, result.Difference)
	})
}
