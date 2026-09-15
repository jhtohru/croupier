package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/outbox"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

type fakeWalletRepository struct {
	byID    map[uuid.UUID]*wallet.Wallet
	byOwner map[string]*wallet.Wallet
	ledger  []*wallet.LedgerEntry
}

func newFakeWalletRepository() *fakeWalletRepository {
	return &fakeWalletRepository{
		byID:    make(map[uuid.UUID]*wallet.Wallet),
		byOwner: make(map[string]*wallet.Wallet),
	}
}

func walletKey(playerID uuid.UUID, currency money.Currency) string {
	return playerID.String() + ":" + string(currency)
}

func (r *fakeWalletRepository) FindByID(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	w, ok := r.byID[id]
	if !ok {
		return nil, ErrWalletNotFound
	}
	return w, nil
}

func (r *fakeWalletRepository) FindByPlayerAndCurrency(ctx context.Context, playerID uuid.UUID, currency money.Currency) (*wallet.Wallet, error) {
	w, ok := r.byOwner[walletKey(playerID, currency)]
	if !ok {
		return nil, ErrWalletNotFound
	}
	return w, nil
}

func (r *fakeWalletRepository) Save(ctx context.Context, w *wallet.Wallet) error {
	r.byID[w.ID()] = w
	r.byOwner[walletKey(w.PlayerID(), w.Balance().Currency())] = w
	return nil
}

func (r *fakeWalletRepository) SaveLedgerEntry(ctx context.Context, e *wallet.LedgerEntry) error {
	r.ledger = append(r.ledger, e)
	return nil
}

func (r *fakeWalletRepository) FindLedgerEntryByTransactionID(ctx context.Context, transactionID uuid.UUID) (*wallet.LedgerEntry, error) {
	for _, e := range r.ledger {
		if e.TransactionID() == transactionID {
			return e, nil
		}
	}
	return nil, ErrLedgerEntryNotFound
}

func (r *fakeWalletRepository) AllLedgerEntries(ctx context.Context, walletID uuid.UUID) ([]*wallet.LedgerEntry, error) {
	var result []*wallet.LedgerEntry
	for _, e := range r.ledger {
		if e.WalletID() == walletID {
			result = append(result, e)
		}
	}
	return result, nil
}

func (r *fakeWalletRepository) ListLedgerEntries(ctx context.Context, walletID uuid.UUID, cursor *uuid.UUID, limit int) ([]*wallet.LedgerEntry, error) {
	var all []*wallet.LedgerEntry
	for _, e := range r.ledger {
		if e.WalletID() == walletID {
			all = append(all, e)
		}
	}
	start := 0
	if cursor != nil {
		start = len(all)
		for i, e := range all {
			if e.ID() == *cursor {
				start = i + 1
				break
			}
		}
	}
	if start > len(all) {
		start = len(all)
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], nil
}

type fakeWagerRepository struct {
	transactions []*wager.Transaction
	attempts     map[uuid.UUID]int
	nextRetryAt  map[uuid.UUID]time.Time
}

func (r *fakeWagerRepository) Save(ctx context.Context, tx *wager.Transaction) error {
	r.transactions = append(r.transactions, tx)
	return nil
}

func (r *fakeWagerRepository) FindByID(ctx context.Context, id uuid.UUID) (*wager.Transaction, error) {
	for _, tx := range r.transactions {
		if tx.ID() == id {
			return tx, nil
		}
	}
	return nil, ErrWagerTransactionNotFound
}

func (r *fakeWagerRepository) FindByProviderAndExternalID(ctx context.Context, providerID, externalTransactionID string) (*wager.Transaction, error) {
	for _, tx := range r.transactions {
		if tx.ProviderID() == providerID && tx.ExternalTransactionID() == externalTransactionID {
			return tx, nil
		}
	}
	return nil, ErrWagerTransactionNotFound
}

func (r *fakeWagerRepository) FindReversal(ctx context.Context, referencedTransactionID uuid.UUID, kind wager.Kind) (*wager.Transaction, error) {
	for _, tx := range r.transactions {
		if tx.Kind() == kind &&
			tx.Status() == wager.TxStatusProcessed &&
			tx.ReferenceTransactionID() != nil &&
			*tx.ReferenceTransactionID() == referencedTransactionID {
			return tx, nil
		}
	}
	return nil, ErrWagerTransactionNotFound
}

func (r *fakeWagerRepository) FindDuePendingReferences(ctx context.Context, now time.Time, limit int) ([]PendingReferenceRetry, error) {
	var due []PendingReferenceRetry
	for _, tx := range r.transactions {
		if tx.Status() != wager.TxStatusPendingReference {
			continue
		}
		if next, scheduled := r.nextRetryAt[tx.ID()]; scheduled && next.After(now) {
			continue
		}
		due = append(due, PendingReferenceRetry{Transaction: tx, Attempts: r.attempts[tx.ID()]})
		if limit > 0 && len(due) >= limit {
			break
		}
	}
	return due, nil
}

func (r *fakeWagerRepository) ScheduleNextPendingReferenceRetry(ctx context.Context, id uuid.UUID, nextRetryAt time.Time) error {
	if r.attempts == nil {
		r.attempts = make(map[uuid.UUID]int)
	}
	if r.nextRetryAt == nil {
		r.nextRetryAt = make(map[uuid.UUID]time.Time)
	}
	r.attempts[id]++
	r.nextRetryAt[id] = nextRetryAt
	return nil
}

type fakeOutboxRepository struct {
	entries []*outbox.Entry
}

// SaveAll upserts by id, mirroring the real Postgres repository's behavior —
// OutboxWorker calls this again on entries fakeOutboxRepository already
// holds (to advance status/retry_count), and treating that as a fresh
// append would double-count them.
func (r *fakeOutboxRepository) SaveAll(ctx context.Context, entries ...*outbox.Entry) error {
	for _, e := range entries {
		var found bool
		for i, existing := range r.entries {
			if existing.ID() == e.ID() {
				r.entries[i] = e
				found = true
				break
			}
		}
		if !found {
			r.entries = append(r.entries, e)
		}
	}
	return nil
}

func (r *fakeOutboxRepository) FindDueForUpdate(ctx context.Context) (*outbox.Entry, error) {
	now := time.Now()
	for _, e := range r.entries {
		if e.Status() == outbox.StatusPending && !e.NextSendAt().After(now) {
			return e, nil
		}
	}
	return nil, ErrOutboxEntryNotFound
}

type fakeTxManager struct{}

func (fakeTxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}
