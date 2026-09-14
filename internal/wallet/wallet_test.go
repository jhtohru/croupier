package wallet

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/jhtohru/croupier/internal/money"
)

func TestNew(t *testing.T) {
	t.Run("negative initial balance", func(t *testing.T) {
		playerID := uuid.New()
		balance, err := money.FromMinorUnits("BRL", -1)
		if err != nil {
			t.Fatal(err)
		}
		w, err := New(playerID, balance)
		assert.ErrorIs(t, err, ErrNegativeInitialBalance)
		assert.Zero(t, w)
	})

	t.Run("zero initial balance", func(t *testing.T) {
		start := time.Now()
		playerID := uuid.New()
		balance, err := money.FromMinorUnits("BRL", 0)
		if err != nil {
			t.Fatal(err)
		}
		w, err := New(playerID, balance)
		assert.NoError(t, err)
		assert.NotEqual(t, uuid.Nil, w.PlayerID())
		assert.Equal(t, playerID, w.PlayerID())
		assert.Equal(t, int64(1), w.Version())
		assert.Equal(t, balance, w.Balance())
		assert.True(t, w.CreatedAt().After(start))
		assert.True(t, w.CreatedAt().Before(time.Now()))
		assert.True(t, w.UpdatedAt().After(start))
		assert.True(t, w.UpdatedAt().Before(time.Now()))
	})

	t.Run("positive initial balance", func(t *testing.T) {
		start := time.Now()
		playerID := uuid.New()
		balance, err := money.FromMinorUnits("BRL", 3000)
		if err != nil {
			t.Fatal(err)
		}
		w, err := New(playerID, balance)
		assert.NoError(t, err)
		assert.NotEqual(t, uuid.Nil, w.PlayerID())
		assert.Equal(t, playerID, w.PlayerID())
		assert.Equal(t, int64(1), w.Version())
		assert.Equal(t, balance, w.Balance())
		assert.True(t, w.CreatedAt().After(start))
		assert.True(t, w.CreatedAt().Before(time.Now()))
		assert.True(t, w.UpdatedAt().After(start))
		assert.True(t, w.UpdatedAt().Before(time.Now()))
	})
}

func TestFromPersistence(t *testing.T) {
	id := uuid.New()
	playerID := uuid.New()
	version := int64(10)
	balance, err := money.FromMinorUnits("BRL", 10000)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now()
	updatedAt := time.Now().Add(10 * time.Second)
	w := FromPersistence(id, playerID, version, balance, createdAt, updatedAt)
	assert.Equal(t, id, w.ID())
	assert.Equal(t, playerID, w.PlayerID())
	assert.Equal(t, version, w.Version())
	assert.Equal(t, balance, w.Balance())
	assert.Equal(t, createdAt, w.CreatedAt())
	assert.Equal(t, updatedAt, w.UpdatedAt())
}

func TestWalletCredit(t *testing.T) {
	t.Run("currency mismatch", func(t *testing.T) {
		initialBalance, err := money.FromMinorUnits("BRL", 10000)
		if err != nil {
			t.Fatal(err)
		}
		w := newWallet(t, initialBalance)
		initialVersion := w.Version()
		amount, err := money.FromMinorUnits("USD", 500)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Credit(amount)
		assert.ErrorIs(t, err, money.ErrCurrencyMismatch)
		assert.Equal(t, initialBalance, w.Balance())
		assert.Equal(t, initialVersion, w.Version())
	})

	t.Run("negative", func(t *testing.T) {
		initialBalance, err := money.FromMinorUnits("BRL", 10000)
		if err != nil {
			t.Fatal(err)
		}
		w := newWallet(t, initialBalance)
		initialVersion := w.Version()
		amount, err := money.FromMinorUnits("BRL", -500)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Credit(amount)
		assert.ErrorIs(t, err, ErrNonPositiveAmount)
		assert.Equal(t, initialBalance, w.Balance())
		assert.Equal(t, initialVersion, w.Version())
	})

	t.Run("zero", func(t *testing.T) {
		initialBalance, err := money.FromMinorUnits("BRL", 10000)
		if err != nil {
			t.Fatal(err)
		}
		w := newWallet(t, initialBalance)
		initialVersion := w.Version()
		amount, err := money.FromMinorUnits("BRL", 0)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Credit(amount)
		assert.ErrorIs(t, err, ErrNonPositiveAmount)
		assert.Equal(t, initialBalance, w.Balance())
		assert.Equal(t, initialVersion, w.Version())
	})

	t.Run("positive", func(t *testing.T) {
		initialBalance, err := money.FromMinorUnits("BRL", 1000)
		if err != nil {
			t.Fatal(err)
		}
		w := newWallet(t, initialBalance)
		initialVersion := w.Version()
		amount, err := money.FromMinorUnits("BRL", 500)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Credit(amount)
		assert.NoError(t, err)
		wantBalance, err := money.FromMinorUnits("BRL", 1000+500)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, wantBalance, w.Balance())
		assert.Equal(t, initialVersion+1, w.Version())
	})

	t.Run("maximum", func(t *testing.T) {
		initialBalance, err := money.FromMinorUnits("BRL", math.MaxInt64-1)
		if err != nil {
			t.Fatal(err)
		}
		w := newWallet(t, initialBalance)
		initialVersion := w.Version()
		amount, err := money.FromMinorUnits("BRL", 1)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Credit(amount)
		assert.NoError(t, err)
		wantBalance, err := money.FromMinorUnits("BRL", math.MaxInt64)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, wantBalance, w.Balance())
		assert.Equal(t, initialVersion+1, w.Version())
	})

	t.Run("overflow", func(t *testing.T) {
		initialBalance, err := money.FromMinorUnits("BRL", math.MaxInt64)
		if err != nil {
			t.Fatal(err)
		}
		w := newWallet(t, initialBalance)
		initialVersion := w.Version()
		amount, err := money.FromMinorUnits("BRL", 1)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Credit(amount)
		assert.ErrorIs(t, err, money.ErrOverflow)
		assert.Equal(t, initialBalance, w.Balance())
		assert.Equal(t, initialVersion, w.Version())
	})
}

func TestWalletDebit(t *testing.T) {
	t.Run("currency mismatch", func(t *testing.T) {
		initialBalance, err := money.FromMinorUnits("BRL", 10000)
		if err != nil {
			t.Fatal(err)
		}
		w := newWallet(t, initialBalance)
		initialVersion := w.Version()
		amount, err := money.FromMinorUnits("USD", 500)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Debit(amount)
		assert.ErrorIs(t, err, money.ErrCurrencyMismatch)
		assert.Equal(t, initialBalance, w.Balance())
		assert.Equal(t, initialVersion, w.Version())
	})

	t.Run("negative amount", func(t *testing.T) {
		initialBalance, err := money.FromMinorUnits("BRL", 10000)
		if err != nil {
			t.Fatal(err)
		}
		w := newWallet(t, initialBalance)
		initialVersion := w.Version()
		amount, err := money.FromMinorUnits("BRL", -500)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Debit(amount)
		assert.ErrorIs(t, err, ErrNonPositiveAmount)
		assert.Equal(t, initialBalance, w.Balance())
		assert.Equal(t, initialVersion, w.Version())
	})

	t.Run("zero amount", func(t *testing.T) {
		initialBalance, err := money.FromMinorUnits("BRL", 10000)
		if err != nil {
			t.Fatal(err)
		}
		w := newWallet(t, initialBalance)
		initialVersion := w.Version()
		amount, err := money.FromMinorUnits("BRL", 0)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Debit(amount)
		assert.ErrorIs(t, err, ErrNonPositiveAmount)
		assert.Equal(t, initialBalance, w.Balance())
		assert.Equal(t, initialVersion, w.Version())
	})

	t.Run("positive amount", func(t *testing.T) {
		balance, err := money.FromMinorUnits("BRL", 1000)
		if err != nil {
			t.Fatal(err)
		}
		w := newWallet(t, balance)
		initialVersion := w.Version()
		amount, err := money.FromMinorUnits("BRL", 500)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Debit(amount)
		assert.NoError(t, err)
		wantBalance, err := money.FromMinorUnits("BRL", 1000-500)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, wantBalance, w.Balance())
		assert.Equal(t, initialVersion+1, w.Version())
	})

	t.Run("minimum balance", func(t *testing.T) {
		balance, err := money.FromMinorUnits("BRL", 1)
		if err != nil {
			t.Fatal(err)
		}
		w := newWallet(t, balance)
		initialVersion := w.Version()
		amount, err := money.FromMinorUnits("BRL", 1)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Debit(amount)
		assert.NoError(t, err)
		zeroBalance, err := money.Zero("BRL")
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, zeroBalance, w.Balance())
		assert.Equal(t, initialVersion+1, w.Version())
	})

	t.Run("insufficient balance", func(t *testing.T) {
		initialBalance, err := money.FromMinorUnits("BRL", 100)
		if err != nil {
			t.Fatal(err)
		}
		w := newWallet(t, initialBalance)
		initialVersion := w.Version()
		amount, err := money.FromMinorUnits("BRL", 300)
		if err != nil {
			t.Fatal(err)
		}
		err = w.Debit(amount)
		assert.ErrorIs(t, err, ErrInsufficientBalance)
		assert.Equal(t, initialBalance, w.Balance())
		assert.Equal(t, initialVersion, w.Version())
	})
}

func newWallet(t *testing.T, balance money.Money) *Wallet {
	t.Helper()

	return &Wallet{
		id:        uuid.New(),
		playerID:  uuid.New(),
		version:   3,
		balance:   balance,
		createdAt: time.Now(),
		updatedAt: time.Now().Add(10 * time.Second),
	}
}
