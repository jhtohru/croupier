package wallet

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/jhtohru/croupier/internal/money"
)

func validLedgerEntryInput(t *testing.T, direction Direction) NewLedgerEntryInput {
	t.Helper()

	before, err := money.FromMinorUnits("BRL", 1000)
	if err != nil {
		t.Fatal(err)
	}
	amount, err := money.FromMinorUnits("BRL", 300)
	if err != nil {
		t.Fatal(err)
	}

	var after money.Money
	switch direction {
	case DirectionDebit:
		after, err = before.Subtract(amount)
	case DirectionCredit:
		after, err = before.Add(amount)
	}
	if err != nil {
		t.Fatal(err)
	}

	return NewLedgerEntryInput{
		WalletID:      uuid.New(),
		TransactionID: uuid.New(),
		Direction:     direction,
		Amount:        amount,
		BalanceBefore: before,
		BalanceAfter:  after,
	}
}

func TestNewLedgerEntry(t *testing.T) {
	t.Run("nil wallet id", func(t *testing.T) {
		input := validLedgerEntryInput(t, DirectionDebit)
		input.WalletID = uuid.Nil
		e, err := NewLedgerEntry(input)
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, e)
	})

	t.Run("nil transaction id", func(t *testing.T) {
		input := validLedgerEntryInput(t, DirectionDebit)
		input.TransactionID = uuid.Nil
		e, err := NewLedgerEntry(input)
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, e)
	})

	t.Run("zero amount", func(t *testing.T) {
		input := validLedgerEntryInput(t, DirectionDebit)
		zero, err := money.Zero("BRL")
		if err != nil {
			t.Fatal(err)
		}
		input.Amount = zero
		input.BalanceAfter = input.BalanceBefore
		e, err := NewLedgerEntry(input)
		assert.ErrorIs(t, err, ErrNonPositiveAmount)
		assert.Nil(t, e)
	})

	t.Run("negative amount", func(t *testing.T) {
		input := validLedgerEntryInput(t, DirectionDebit)
		negative, err := money.FromMinorUnits("BRL", -300)
		if err != nil {
			t.Fatal(err)
		}
		input.Amount = negative
		e, err := NewLedgerEntry(input)
		assert.ErrorIs(t, err, ErrNonPositiveAmount)
		assert.Nil(t, e)
	})

	t.Run("negative balance before", func(t *testing.T) {
		input := validLedgerEntryInput(t, DirectionDebit)
		negative, err := money.FromMinorUnits("BRL", -1000)
		if err != nil {
			t.Fatal(err)
		}
		input.BalanceBefore = negative
		e, err := NewLedgerEntry(input)
		assert.ErrorIs(t, err, ErrNegativeBalance)
		assert.Nil(t, e)
	})

	t.Run("negative balance after", func(t *testing.T) {
		input := validLedgerEntryInput(t, DirectionDebit)
		negative, err := money.FromMinorUnits("BRL", -700)
		if err != nil {
			t.Fatal(err)
		}
		input.BalanceAfter = negative
		e, err := NewLedgerEntry(input)
		assert.ErrorIs(t, err, ErrNegativeBalance)
		assert.Nil(t, e)
	})

	t.Run("invalid direction", func(t *testing.T) {
		input := validLedgerEntryInput(t, DirectionDebit)
		input.Direction = Direction("SIDEWAYS")
		e, err := NewLedgerEntry(input)
		assert.ErrorIs(t, err, ErrInvalidDirection)
		assert.Nil(t, e)
	})

	t.Run("currency mismatch", func(t *testing.T) {
		input := validLedgerEntryInput(t, DirectionDebit)
		usd, err := money.FromMinorUnits("USD", 300)
		if err != nil {
			t.Fatal(err)
		}
		input.Amount = usd
		e, err := NewLedgerEntry(input)
		assert.ErrorIs(t, err, money.ErrCurrencyMismatch)
		assert.Nil(t, e)
	})

	t.Run("debit inconsistent balance", func(t *testing.T) {
		input := validLedgerEntryInput(t, DirectionDebit)
		wrong, err := money.FromMinorUnits("BRL", 1000)
		if err != nil {
			t.Fatal(err)
		}
		input.BalanceAfter = wrong
		e, err := NewLedgerEntry(input)
		assert.ErrorIs(t, err, ErrInconsistentBalance)
		assert.Nil(t, e)
	})

	t.Run("credit inconsistent balance", func(t *testing.T) {
		input := validLedgerEntryInput(t, DirectionCredit)
		wrong, err := money.FromMinorUnits("BRL", 1000)
		if err != nil {
			t.Fatal(err)
		}
		input.BalanceAfter = wrong
		e, err := NewLedgerEntry(input)
		assert.ErrorIs(t, err, ErrInconsistentBalance)
		assert.Nil(t, e)
	})

	t.Run("debit success", func(t *testing.T) {
		input := validLedgerEntryInput(t, DirectionDebit)
		e, err := NewLedgerEntry(input)
		assert.NoError(t, err)
		if assert.NotNil(t, e) {
			assert.NotEqual(t, uuid.Nil, e.ID())
			assert.Equal(t, input.WalletID, e.WalletID())
			assert.Equal(t, input.TransactionID, e.TransactionID())
			assert.Equal(t, DirectionDebit, e.Direction())
			assert.Equal(t, input.Amount, e.Amount())
			assert.Equal(t, input.BalanceBefore, e.BalanceBefore())
			assert.Equal(t, input.BalanceAfter, e.BalanceAfter())
		}
	})

	t.Run("credit success", func(t *testing.T) {
		input := validLedgerEntryInput(t, DirectionCredit)
		e, err := NewLedgerEntry(input)
		assert.NoError(t, err)
		if assert.NotNil(t, e) {
			assert.Equal(t, DirectionCredit, e.Direction())
			assert.Equal(t, input.BalanceAfter, e.BalanceAfter())
		}
	})
}

func TestLedgerEntryFromPersistence(t *testing.T) {
	id := uuid.New()
	walletID := uuid.New()
	transactionID := uuid.New()
	before, err := money.FromMinorUnits("BRL", 1000)
	if err != nil {
		t.Fatal(err)
	}
	after, err := money.FromMinorUnits("BRL", 700)
	if err != nil {
		t.Fatal(err)
	}
	amount, err := money.FromMinorUnits("BRL", 300)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now()

	e := LedgerEntryFromPersistence(id, walletID, transactionID, DirectionDebit, amount, before, after, createdAt)

	assert.Equal(t, id, e.ID())
	assert.Equal(t, walletID, e.WalletID())
	assert.Equal(t, transactionID, e.TransactionID())
	assert.Equal(t, DirectionDebit, e.Direction())
	assert.Equal(t, amount, e.Amount())
	assert.Equal(t, before, e.BalanceBefore())
	assert.Equal(t, after, e.BalanceAfter())
	assert.Equal(t, createdAt, e.CreatedAt())
}
