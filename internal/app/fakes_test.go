package app

import (
	"context"

	"github.com/google/uuid"

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

func walletKey(playerID uuid.UUID, currency string) string {
	return playerID.String() + ":" + currency
}

func (r *fakeWalletRepository) FindByID(ctx context.Context, id uuid.UUID) (*wallet.Wallet, error) {
	w, ok := r.byID[id]
	if !ok {
		return nil, ErrWalletNotFound
	}
	return w, nil
}

func (r *fakeWalletRepository) FindByPlayerAndCurrency(ctx context.Context, playerID uuid.UUID, currency string) (*wallet.Wallet, error) {
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

type fakeWagerRepository struct {
	transactions []*wager.Transaction
}

func (r *fakeWagerRepository) Save(ctx context.Context, tx *wager.Transaction) error {
	r.transactions = append(r.transactions, tx)
	return nil
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
