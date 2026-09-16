package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/inbox"
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
	// ErrWagerTransactionAlreadyExists means a Save lost a race to insert
	// (providerId, externalTransactionId) — another concurrent Submit for
	// the exact same key already committed its row first. WagerSubmitter is
	// the only caller that should ever see this: it catches it and retries
	// as an ordinary idempotent replay against the winner's row, so no
	// caller of Submit ever observes this error directly (see the Fase 12
	// note in TODO.md — 50 concurrent identical submissions must resolve to
	// one processed transaction and 49 clean replays, not 49 errors).
	ErrWagerTransactionAlreadyExists = errors.New("wager transaction already exists")
	ErrIdempotencyConflict           = errors.New("idempotency conflict")
	ErrInboxEntryNotFound            = errors.New("inbox entry not found")
	ErrOutboxEntryNotFound           = errors.New("outbox entry not found")
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
	// FindDueForUpdate returns the single oldest PENDING entry whose
	// next_send_at is due, row-locked (SELECT ... FOR UPDATE SKIP LOCKED) so
	// concurrent OutboxWorker instances never grab the same entry — call
	// only inside TxManager.WithinTx. Returns ErrOutboxEntryNotFound when
	// nothing is due; the lock (and the entry, if the caller's transaction
	// never commits — a crash mid-publish included) is released back to
	// other workers as soon as the transaction ends, so no separate lease/
	// claim bookkeeping is needed for recovering abandoned work.
	FindDueForUpdate(ctx context.Context) (*outbox.Entry, error)
}

// InboxRepository backs SQS consumer deduplication: (consumerName,
// messageId) identifies one delivery attempt's worth of work.
type InboxRepository interface {
	FindByConsumerAndMessage(ctx context.Context, consumerName, messageID string) (*inbox.Inbox, error)
	Save(ctx context.Context, i *inbox.Inbox) error
}
