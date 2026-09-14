package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wallet"
)

type WalletReconciler struct {
	wallets WalletRepository
}

func NewWalletReconciler(wallets WalletRepository) *WalletReconciler {
	return &WalletReconciler{wallets: wallets}
}

type ReconciliationResult struct {
	WalletID          uuid.UUID
	StoredBalance     money.Money
	CalculatedBalance money.Money
	Difference        money.Money // StoredBalance - CalculatedBalance
	Consistent        bool
	EntriesChecked    int
}

// Reconcile recomputes a wallet's balance from its ledger (append-only,
// including the OPENING credit) and compares it against the stored balance.
// It never mutates the wallet — it only reports whether the two agree.
func (r *WalletReconciler) Reconcile(ctx context.Context, walletID uuid.UUID) (*ReconciliationResult, error) {
	w, err := r.wallets.FindByID(ctx, walletID)
	if err != nil {
		return nil, err
	}

	entries, err := r.wallets.AllLedgerEntries(ctx, walletID)
	if err != nil {
		return nil, err
	}

	calculated := w.Balance().Currency().Zero()
	for _, e := range entries {
		switch e.Direction() {
		case wallet.DirectionCredit:
			calculated, err = calculated.Add(e.Amount())
		case wallet.DirectionDebit:
			calculated, err = calculated.Subtract(e.Amount())
		}
		if err != nil {
			return nil, err
		}
	}

	difference, err := w.Balance().Subtract(calculated)
	if err != nil {
		return nil, err
	}

	return &ReconciliationResult{
		WalletID:          walletID,
		StoredBalance:     w.Balance(),
		CalculatedBalance: calculated,
		Difference:        difference,
		Consistent:        difference.IsZero(),
		EntriesChecked:    len(entries),
	}, nil
}
