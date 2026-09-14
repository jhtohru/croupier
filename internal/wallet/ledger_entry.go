package wallet

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jhtohru/croupier/internal/money"
)

var (
	ErrInvalidInput        = errors.New("invalid input")
	ErrInvalidDirection    = errors.New("invalid direction")
	ErrNegativeBalance     = errors.New("negative balance")
	ErrInconsistentBalance = errors.New("inconsistent balance")
)

type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

type LedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

type NewLedgerEntryInput struct {
	WalletID      uuid.UUID
	TransactionID uuid.UUID
	Direction     Direction
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
}

func NewLedgerEntry(input NewLedgerEntryInput) (*LedgerEntry, error) {
	if input.WalletID == uuid.Nil || input.TransactionID == uuid.Nil {
		return nil, ErrInvalidInput
	}
	if !input.Amount.IsPositive() {
		return nil, ErrNonPositiveAmount
	}
	if input.BalanceBefore.IsNegative() || input.BalanceAfter.IsNegative() {
		return nil, ErrNegativeBalance
	}

	var expectedAfter money.Money
	var err error
	switch input.Direction {
	case DirectionDebit:
		expectedAfter, err = input.BalanceBefore.Subtract(input.Amount)
	case DirectionCredit:
		expectedAfter, err = input.BalanceBefore.Add(input.Amount)
	default:
		return nil, ErrInvalidDirection
	}
	if err != nil {
		return nil, err
	}
	if ok, err := expectedAfter.Equal(input.BalanceAfter); err != nil || !ok {
		return nil, ErrInconsistentBalance
	}

	return &LedgerEntry{
		id:            uuid.New(),
		walletID:      input.WalletID,
		transactionID: input.TransactionID,
		direction:     input.Direction,
		amount:        input.Amount,
		balanceBefore: input.BalanceBefore,
		balanceAfter:  input.BalanceAfter,
		createdAt:     time.Now(),
	}, nil
}

func LedgerEntryFromPersistence(
	id uuid.UUID,
	walletID uuid.UUID,
	transactionID uuid.UUID,
	direction Direction,
	amount money.Money,
	balanceBefore money.Money,
	balanceAfter money.Money,
	createdAt time.Time,
) *LedgerEntry {
	return &LedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     createdAt,
	}
}

func (e LedgerEntry) ID() uuid.UUID {
	return e.id
}

func (e LedgerEntry) WalletID() uuid.UUID {
	return e.walletID
}

func (e LedgerEntry) TransactionID() uuid.UUID {
	return e.transactionID
}

func (e LedgerEntry) Direction() Direction {
	return e.direction
}

func (e LedgerEntry) Amount() money.Money {
	return e.amount
}

func (e LedgerEntry) BalanceBefore() money.Money {
	return e.balanceBefore
}

func (e LedgerEntry) BalanceAfter() money.Money {
	return e.balanceAfter
}

func (e LedgerEntry) CreatedAt() time.Time {
	return e.createdAt
}
