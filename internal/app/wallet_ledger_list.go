package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/wallet"
)

const defaultLedgerListLimit = 50

type WalletLedgerLister struct {
	wallets WalletRepository
}

func NewWalletLedgerLister(wallets WalletRepository) *WalletLedgerLister {
	return &WalletLedgerLister{wallets: wallets}
}

type ListWalletLedgerInput struct {
	WalletID uuid.UUID
	Cursor   *uuid.UUID
	Limit    int
}

func (l *WalletLedgerLister) List(ctx context.Context, input ListWalletLedgerInput) ([]*wallet.LedgerEntry, error) {
	limit := input.Limit
	if limit <= 0 || limit > defaultLedgerListLimit {
		limit = defaultLedgerListLimit
	}
	return l.wallets.ListLedgerEntries(ctx, input.WalletID, input.Cursor, limit)
}
