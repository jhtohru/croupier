package app

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/jhtohru/croupier/internal/wallet"
)

func TestWalletLedgerListerList(t *testing.T) {
	t.Run("paginates with cursor", func(t *testing.T) {
		wallets := newFakeWalletRepository()
		walletID := uuid.New()
		var entryIDs []uuid.UUID
		for i := 0; i < 5; i++ {
			amount := mustAmount(t, int64(100*(i+1)))
			entry, err := wallet.NewLedgerEntry(wallet.NewLedgerEntryInput{
				WalletID: walletID, TransactionID: uuid.New(), Direction: wallet.DirectionCredit,
				Amount: amount, BalanceBefore: mustAmount(t, 0), BalanceAfter: amount,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := wallets.SaveLedgerEntry(context.Background(), entry); err != nil {
				t.Fatal(err)
			}
			entryIDs = append(entryIDs, entry.ID())
		}

		l := NewWalletLedgerLister(wallets)

		page1, err := l.List(context.Background(), ListWalletLedgerInput{WalletID: walletID, Limit: 2})
		assert.NoError(t, err)
		if assert.Len(t, page1, 2) {
			assert.Equal(t, entryIDs[0], page1[0].ID())
			assert.Equal(t, entryIDs[1], page1[1].ID())
		}

		cursor := page1[1].ID()
		page2, err := l.List(context.Background(), ListWalletLedgerInput{WalletID: walletID, Cursor: &cursor, Limit: 2})
		assert.NoError(t, err)
		if assert.Len(t, page2, 2) {
			assert.Equal(t, entryIDs[2], page2[0].ID())
			assert.Equal(t, entryIDs[3], page2[1].ID())
		}

		lastCursor := page2[1].ID()
		page3, err := l.List(context.Background(), ListWalletLedgerInput{WalletID: walletID, Cursor: &lastCursor, Limit: 2})
		assert.NoError(t, err)
		if assert.Len(t, page3, 1) {
			assert.Equal(t, entryIDs[4], page3[0].ID())
		}
	})

	t.Run("scoped to the wallet", func(t *testing.T) {
		wallets := newFakeWalletRepository()
		otherWalletID := uuid.New()
		entry, err := wallet.NewLedgerEntry(wallet.NewLedgerEntryInput{
			WalletID: otherWalletID, TransactionID: uuid.New(), Direction: wallet.DirectionCredit,
			Amount: mustAmount(t, 100), BalanceBefore: mustAmount(t, 0), BalanceAfter: mustAmount(t, 100),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := wallets.SaveLedgerEntry(context.Background(), entry); err != nil {
			t.Fatal(err)
		}

		l := NewWalletLedgerLister(wallets)
		entries, err := l.List(context.Background(), ListWalletLedgerInput{WalletID: uuid.New(), Limit: 10})
		assert.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("caps the limit to the default", func(t *testing.T) {
		wallets := newFakeWalletRepository()
		l := NewWalletLedgerLister(wallets)
		entries, err := l.List(context.Background(), ListWalletLedgerInput{WalletID: uuid.New(), Limit: 0})
		assert.NoError(t, err)
		assert.Empty(t, entries)
	})
}
