package app

import (
	"context"

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

type fakeOutboxRepository struct {
	entries []*outbox.Entry
}

func (r *fakeOutboxRepository) SaveAll(ctx context.Context, entries ...*outbox.Entry) error {
	r.entries = append(r.entries, entries...)
	return nil
}

type fakeTxManager struct{}

func (fakeTxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}
