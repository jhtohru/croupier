package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/outbox"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

var (
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrWalletAlreadyExists = errors.New("wallet already exists")
)

// TxManager coordinates atomicity across repositories: repository calls made
// inside fn run within the same underlying SQL transaction.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

type WalletRepository interface {
	FindByID(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error)
	FindByPlayerAndCurrency(ctx context.Context, playerID uuid.UUID, currency money.Currency) (*wallet.Wallet, error)
	Save(ctx context.Context, w *wallet.Wallet) error
	SaveLedgerEntry(ctx context.Context, e *wallet.LedgerEntry) error
}

type WagerRepository interface {
	Save(ctx context.Context, tx *wager.Transaction) error
}

type OutboxRepository interface {
	SaveAll(ctx context.Context, entries ...*outbox.Entry) error
}
