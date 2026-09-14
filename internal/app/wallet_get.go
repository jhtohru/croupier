package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/wallet"
)

type WalletGetter struct {
	wallets WalletRepository
}

func NewWalletGetter(wallets WalletRepository) *WalletGetter {
	return &WalletGetter{wallets: wallets}
}

func (wg *WalletGetter) Get(ctx context.Context, walletID uuid.UUID) (*wallet.Wallet, error) {
	return wg.wallets.FindByID(ctx, walletID)
}
