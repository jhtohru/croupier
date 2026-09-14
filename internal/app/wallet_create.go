package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

type WalletCreator struct {
	wallets   WalletRepository
	wagers    WagerRepository
	outbox    OutboxRepository
	txManager TxManager
}

func NewWalletCreator(
	wallets WalletRepository,
	wager WagerRepository,
	outbox OutboxRepository,
	txManager TxManager,
) *WalletCreator {
	return &WalletCreator{
		wallets:   wallets,
		wagers:    wager,
		outbox:    outbox,
		txManager: txManager,
	}
}

type CreateWalletInput struct {
	PlayerID       uuid.UUID
	InitialBalance money.Money
}

func (wc *WalletCreator) Create(ctx context.Context, input CreateWalletInput) (*wallet.Wallet, error) {
	existing, err := wc.wallets.FindByPlayerAndCurrency(ctx, input.PlayerID, input.InitialBalance.Currency())
	if err != nil && !errors.Is(err, ErrWalletNotFound) {
		return nil, err
	}
	if existing != nil {
		return nil, ErrWalletAlreadyExists
	}

	w, err := wallet.New(input.PlayerID, input.InitialBalance)
	if err != nil {
		return nil, err
	}

	if input.InitialBalance.IsZero() {
		if err := wc.wallets.Save(ctx, w); err != nil {
			return nil, err
		}
		return w, nil
	}

	tx, err := wager.NewOpeningTransaction(wager.NewOpeningInput{
		PlayerID: input.PlayerID,
		WalletID: w.ID(),
		Amount:   input.InitialBalance,
	})
	if err != nil {
		return nil, err
	}

	zero := input.InitialBalance.Currency().Zero()

	entry, err := wallet.NewLedgerEntry(wallet.NewLedgerEntryInput{
		WalletID:      w.ID(),
		TransactionID: tx.ID(),
		Direction:     wallet.DirectionCredit,
		Amount:        input.InitialBalance,
		BalanceBefore: zero,
		BalanceAfter:  input.InitialBalance,
	})
	if err != nil {
		return nil, err
	}

	processedEvent, err := newWagerTransactionProcessedEvent(tx)
	if err != nil {
		return nil, err
	}
	balanceChangedEvent, err := newWalletBalanceChangedEvent(w, entry)
	if err != nil {
		return nil, err
	}

	err = wc.txManager.WithinTx(ctx, func(ctx context.Context) error {
		if err := wc.wallets.Save(ctx, w); err != nil {
			return err
		}
		if err := wc.wagers.Save(ctx, tx); err != nil {
			return err
		}
		if err := wc.wallets.SaveLedgerEntry(ctx, entry); err != nil {
			return err
		}
		return wc.outbox.SaveAll(ctx, processedEvent, balanceChangedEvent)
	})
	if err != nil {
		return nil, err
	}

	return w, nil
}
