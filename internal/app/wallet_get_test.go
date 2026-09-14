package app

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wallet"
)

func TestWalletGetterGet(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		wallets := newFakeWalletRepository()
		wg := NewWalletGetter(wallets)

		w, err := wg.Get(context.Background(), uuid.New())
		assert.ErrorIs(t, err, ErrWalletNotFound)
		assert.Nil(t, w)
	})

	t.Run("success", func(t *testing.T) {
		wallets := newFakeWalletRepository()
		balance, err := money.FromMinorUnits("BRL", 1000)
		if err != nil {
			t.Fatal(err)
		}
		existing, err := wallet.New(uuid.New(), balance)
		if err != nil {
			t.Fatal(err)
		}
		if err := wallets.Save(context.Background(), existing); err != nil {
			t.Fatal(err)
		}

		wg := NewWalletGetter(wallets)
		w, err := wg.Get(context.Background(), existing.ID())
		assert.NoError(t, err)
		assert.Equal(t, existing, w)
	})
}
