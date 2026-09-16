package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wallet"
)

type WalletReconciler struct {
	wallets   WalletRepository
	txManager TxManager
}

func NewWalletReconciler(wallets WalletRepository, txManager TxManager) *WalletReconciler {
	return &WalletReconciler{wallets: wallets, txManager: txManager}
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
//
// Both reads run inside one WithinTx so they see a consistent snapshot
// (challenge spec §9.35/§9's "compare os valores em uma visão consistente
// dos dados"): FindByID takes the same row-level FOR UPDATE lock it takes
// for every wallet-mutating operation (internal/postgres's convention),
// so it blocks until any in-flight WagerSubmitter.process for this wallet
// has fully committed (or rolled back) — by the time it returns, the
// ledger entries read right after are guaranteed to already reflect
// whatever that writer committed, not a stale in-between state. The lock
// also holds off new writers for this one wallet until Reconcile's
// transaction ends, same as any other operation on it — reconciliation is
// read-only, so that window is exactly as long as computing the sum below.
func (r *WalletReconciler) Reconcile(ctx context.Context, walletID uuid.UUID) (*ReconciliationResult, error) {
	var result *ReconciliationResult
	err := r.txManager.WithinTx(ctx, func(ctx context.Context) error {
		w, err := r.wallets.FindByID(ctx, walletID)
		if err != nil {
			return err
		}

		entries, err := r.wallets.AllLedgerEntries(ctx, walletID)
		if err != nil {
			return err
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
				return err
			}
		}

		difference, err := w.Balance().Subtract(calculated)
		if err != nil {
			return err
		}

		result = &ReconciliationResult{
			WalletID:          walletID,
			StoredBalance:     w.Balance(),
			CalculatedBalance: calculated,
			Difference:        difference,
			Consistent:        difference.IsZero(),
			EntriesChecked:    len(entries),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
