package wallet

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jhtohru/croupier/internal/money"
)

var (
	ErrInsufficientBalance    = errors.New("insufficient balance")
	ErrNegativeInitialBalance = errors.New("negative initial balance")
	ErrNonPositiveAmount      = errors.New("non-positive amount")
)

type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	version   int64
	balance   money.Money
	createdAt time.Time
	updatedAt time.Time
}

func New(
	playerID uuid.UUID,
	initialBalance money.Money,
) (*Wallet, error) {
	if initialBalance.IsNegative() {
		return nil, ErrNegativeInitialBalance
	}
	now := time.Now()
	return &Wallet{
		id:        uuid.New(),
		playerID:  playerID,
		version:   1,
		balance:   initialBalance,
		createdAt: now,
		updatedAt: now,
	}, nil
}

func FromPersistence(
	id uuid.UUID,
	playerID uuid.UUID,
	version int64,
	balance money.Money,
	createdAt time.Time,
	updatedAt time.Time,
) *Wallet {
	return &Wallet{
		id:        id,
		playerID:  playerID,
		version:   version,
		balance:   balance,
		createdAt: createdAt,
		updatedAt: updatedAt,
	}
}

func (w *Wallet) Credit(amount money.Money) error {
	if !amount.IsPositive() {
		return ErrNonPositiveAmount
	}
	balance, err := w.balance.Add(amount)
	if err != nil {
		return err
	}
	w.balance = balance
	w.version++
	return nil
}

func (w *Wallet) Debit(amount money.Money) error {
	if !amount.IsPositive() {
		return ErrNonPositiveAmount
	}
	balance, err := w.balance.Subtract(amount)
	if err != nil {
		return err
	}
	if balance.IsNegative() {
		return ErrInsufficientBalance
	}
	w.balance = balance
	w.version++
	return nil
}

func (w *Wallet) ID() uuid.UUID {
	return w.id
}

func (w *Wallet) PlayerID() uuid.UUID {
	return w.playerID
}

func (w *Wallet) Version() int64 {
	return w.version
}

func (w *Wallet) Balance() money.Money {
	return w.balance
}

func (w *Wallet) CreatedAt() time.Time {
	return w.createdAt
}

func (w *Wallet) UpdatedAt() time.Time {
	return w.updatedAt
}
