package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/outbox"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

var (
	ErrWalletNotFound           = errors.New("wallet not found")
	ErrWalletAlreadyExists      = errors.New("wallet already exists")
	ErrLedgerEntryNotFound      = errors.New("ledger entry not found")
	ErrWagerTransactionNotFound = errors.New("wager transaction not found")
	ErrIdempotencyConflict      = errors.New("idempotency conflict")
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
	FindLedgerEntryByTransactionID(ctx context.Context, transactionID uuid.UUID) (*wallet.LedgerEntry, error)
	// AllLedgerEntries returns every ledger entry for a wallet, unpaginated —
	// used by reconciliation, which needs the complete set to recompute the
	// balance. Not the same access pattern as ListLedgerEntries below.
	AllLedgerEntries(ctx context.Context, walletID uuid.UUID) ([]*wallet.LedgerEntry, error)
	// ListLedgerEntries returns up to limit entries for walletID, ordered by
	// creation, after the entry identified by cursor (nil starts from the
	// beginning) — backs the paginated GET /wallets/:walletId/ledger route.
	ListLedgerEntries(ctx context.Context, walletID uuid.UUID, cursor *uuid.UUID, limit int) ([]*wallet.LedgerEntry, error)
}

type WagerRepository interface {
	Save(ctx context.Context, tx *wager.Transaction) error
	FindByID(ctx context.Context, id uuid.UUID) (*wager.Transaction, error)
	FindByProviderAndExternalID(ctx context.Context, providerID, externalTransactionID string) (*wager.Transaction, error)
	// FindReversal looks up an existing PROCESSED transaction of kind (REFUND
	// or ROLLBACK) already pointing at referencedTransactionID, used to
	// prevent applying the same kind of reversal twice against one reference.
	FindReversal(ctx context.Context, referencedTransactionID uuid.UUID, kind wager.Kind) (*wager.Transaction, error)
	// FindDuePendingReferences returns up to limit PENDING_REFERENCE
	// transactions whose retry schedule is due at or before now — a
	// transaction never scheduled yet counts as immediately due. Used by
	// PendingReferenceResolver.
	FindDuePendingReferences(ctx context.Context, now time.Time, limit int) ([]PendingReferenceRetry, error)
	// ScheduleNextPendingReferenceRetry records another failed resolution
	// attempt for id and when to try again.
	ScheduleNextPendingReferenceRetry(ctx context.Context, id uuid.UUID, nextRetryAt time.Time) error
}

type OutboxRepository interface {
	SaveAll(ctx context.Context, entries ...*outbox.Entry) error
}
