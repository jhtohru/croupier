package app

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/jhtohru/croupier/internal/wager"
)

func TestWagerTransactionGetterGet(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		wagers := &fakeWagerRepository{}
		g := NewWagerTransactionGetter(wagers)

		tx, err := g.Get(context.Background(), uuid.New())
		assert.ErrorIs(t, err, ErrWagerTransactionNotFound)
		assert.Nil(t, tx)
	})

	t.Run("success", func(t *testing.T) {
		wagers := &fakeWagerRepository{}
		existing, err := wager.NewOpeningTransaction(wager.NewOpeningInput{
			PlayerID: uuid.New(), WalletID: uuid.New(), Amount: mustAmount(t, 1000),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := wagers.Save(context.Background(), existing); err != nil {
			t.Fatal(err)
		}

		g := NewWagerTransactionGetter(wagers)
		got, err := g.Get(context.Background(), existing.ID())
		assert.NoError(t, err)
		assert.Equal(t, existing, got)
	})
}

func TestWagerTransactionGetterGetByProvider(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		wagers := &fakeWagerRepository{}
		g := NewWagerTransactionGetter(wagers)

		tx, err := g.GetByProvider(context.Background(), "provider-a", "ext-1")
		assert.ErrorIs(t, err, ErrWagerTransactionNotFound)
		assert.Nil(t, tx)
	})

	t.Run("success, isolated by provider", func(t *testing.T) {
		wagers := &fakeWagerRepository{}
		existing, err := wager.NewTransaction(wager.NewTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "bet-1",
			PlayerID: uuid.New(), WalletID: uuid.New(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustAmount(t, 1000),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := wagers.Save(context.Background(), existing); err != nil {
			t.Fatal(err)
		}

		g := NewWagerTransactionGetter(wagers)

		got, err := g.GetByProvider(context.Background(), "provider-a", "bet-1")
		assert.NoError(t, err)
		assert.Equal(t, existing, got)

		// same externalTransactionId, different provider — must not resolve,
		// since externalTransactionId is only unique within a provider.
		got, err = g.GetByProvider(context.Background(), "provider-b", "bet-1")
		assert.ErrorIs(t, err, ErrWagerTransactionNotFound)
		assert.Nil(t, got)
	})
}
