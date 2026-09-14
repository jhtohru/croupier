package app

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

func TestWalletCreatorCreate(t *testing.T) {
	t.Run("already exists", func(t *testing.T) {
		wallets := newFakeWalletRepository()
		playerID := uuid.New()
		balance, err := money.FromMinorUnits("BRL", 1000)
		if err != nil {
			t.Fatal(err)
		}
		existing, err := wallet.New(playerID, balance)
		if err != nil {
			t.Fatal(err)
		}
		if err := wallets.Save(context.Background(), existing); err != nil {
			t.Fatal(err)
		}

		wc := NewWalletCreator(wallets, &fakeWagerRepository{}, &fakeOutboxRepository{}, fakeTxManager{})
		w, err := wc.Create(context.Background(), CreateWalletInput{PlayerID: playerID, InitialBalance: balance})
		assert.ErrorIs(t, err, ErrWalletAlreadyExists)
		assert.Nil(t, w)
	})

	t.Run("zero initial balance", func(t *testing.T) {
		wallets := newFakeWalletRepository()
		wagerRepo := &fakeWagerRepository{}
		outboxRepo := &fakeOutboxRepository{}
		wc := NewWalletCreator(wallets, wagerRepo, outboxRepo, fakeTxManager{})

		playerID := uuid.New()
		zero, err := money.Zero("BRL")
		if err != nil {
			t.Fatal(err)
		}

		w, err := wc.Create(context.Background(), CreateWalletInput{PlayerID: playerID, InitialBalance: zero})
		assert.NoError(t, err)
		if assert.NotNil(t, w) {
			assert.Equal(t, playerID, w.PlayerID())
			assert.Equal(t, zero, w.Balance())
		}
		assert.Empty(t, wagerRepo.transactions)
		assert.Empty(t, wallets.ledger)
		assert.Empty(t, outboxRepo.entries)

		saved, err := wallets.FindByPlayerAndCurrency(context.Background(), playerID, "BRL")
		assert.NoError(t, err)
		assert.Equal(t, w, saved)
	})

	t.Run("positive initial balance", func(t *testing.T) {
		wallets := newFakeWalletRepository()
		wagerRepo := &fakeWagerRepository{}
		outboxRepo := &fakeOutboxRepository{}
		wc := NewWalletCreator(wallets, wagerRepo, outboxRepo, fakeTxManager{})

		playerID := uuid.New()
		balance, err := money.FromMinorUnits("BRL", 3000)
		if err != nil {
			t.Fatal(err)
		}

		w, err := wc.Create(context.Background(), CreateWalletInput{PlayerID: playerID, InitialBalance: balance})
		assert.NoError(t, err)
		if assert.NotNil(t, w) {
			assert.Equal(t, playerID, w.PlayerID())
			assert.Equal(t, balance, w.Balance())
		}

		if assert.Len(t, wagerRepo.transactions, 1) {
			tx := wagerRepo.transactions[0]
			assert.Equal(t, wager.KindOpening, tx.Kind())
			assert.Equal(t, wager.TxStatusProcessed, tx.Status())
			assert.Equal(t, w.ID(), tx.WalletID())
			assert.Equal(t, playerID, tx.PlayerID())
			assert.Equal(t, balance, tx.Amount())
		}

		if assert.Len(t, wallets.ledger, 1) {
			entry := wallets.ledger[0]
			assert.Equal(t, w.ID(), entry.WalletID())
			assert.Equal(t, wagerRepo.transactions[0].ID(), entry.TransactionID())
			assert.Equal(t, wallet.DirectionCredit, entry.Direction())
			assert.Equal(t, balance, entry.Amount())
			assert.Equal(t, balance, entry.BalanceAfter())
		}

		if assert.Len(t, outboxRepo.entries, 2) {
			types := []string{outboxRepo.entries[0].EventType(), outboxRepo.entries[1].EventType()}
			assert.Contains(t, types, EventTypeWagerTransactionProcessed)
			assert.Contains(t, types, EventTypeWalletBalanceChanged)
		}

		saved, err := wallets.FindByPlayerAndCurrency(context.Background(), playerID, "BRL")
		assert.NoError(t, err)
		assert.Equal(t, w, saved)
	})
}
