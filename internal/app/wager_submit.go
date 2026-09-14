package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/outbox"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

type WagerSubmitter struct {
	wallets   WalletRepository
	wagers    WagerRepository
	outbox    OutboxRepository
	txManager TxManager
}

func NewWagerSubmitter(
	wallets WalletRepository,
	wagers WagerRepository,
	outbox OutboxRepository,
	txManager TxManager,
) *WagerSubmitter {
	return &WagerSubmitter{
		wallets:   wallets,
		wagers:    wagers,
		outbox:    outbox,
		txManager: txManager,
	}
}

type SubmitWagerTransactionInput struct {
	ProviderID                     string
	ExternalTransactionID          string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           wager.Kind
	Amount                         money.Money
	ReferenceExternalTransactionID *string
}

type SubmitWagerTransactionResult struct {
	Transaction      *wager.Transaction
	Balance          money.Money
	IdempotentReplay bool
}

// Submit processes a wager transaction submission shared by both the HTTP
// and SQS ingestion paths (Fase 7/8 call this with the same input, giving
// them identical idempotency and business-rule guarantees).
func (ws *WagerSubmitter) Submit(ctx context.Context, input SubmitWagerTransactionInput) (*SubmitWagerTransactionResult, error) {
	candidate, err := wager.NewTransaction(wager.NewTransactionInput{
		ProviderID:                     input.ProviderID,
		ExternalTransactionID:          input.ExternalTransactionID,
		PlayerID:                       input.PlayerID,
		WalletID:                       input.WalletID,
		RoundID:                        input.RoundID,
		GameID:                         input.GameID,
		Kind:                           input.Kind,
		Amount:                         input.Amount,
		ReferenceExternalTransactionID: input.ReferenceExternalTransactionID,
	})
	if err != nil {
		return nil, err
	}

	existing, err := ws.wagers.FindByProviderAndExternalID(ctx, input.ProviderID, input.ExternalTransactionID)
	if err != nil && !errors.Is(err, ErrWagerTransactionNotFound) {
		return nil, err
	}
	if existing != nil {
		return ws.replay(ctx, existing, candidate)
	}

	return ws.process(ctx, candidate)
}

// replay handles a resubmission of an (providerId, externalTransactionId)
// pair that was already recorded: same content is an idempotent no-op that
// returns the balance observed during the original processing (not the
// wallet's current balance, which may have moved since); different content
// is a conflict.
func (ws *WagerSubmitter) replay(ctx context.Context, existing, candidate *wager.Transaction) (*SubmitWagerTransactionResult, error) {
	if existing.PayloadHash() != candidate.PayloadHash() {
		return nil, ErrIdempotencyConflict
	}
	balance, err := ws.balanceAtProcessing(ctx, existing)
	if err != nil {
		return nil, err
	}
	return &SubmitWagerTransactionResult{
		Transaction:      existing,
		Balance:          balance,
		IdempotentReplay: true,
	}, nil
}

func (ws *WagerSubmitter) balanceAtProcessing(ctx context.Context, tx *wager.Transaction) (money.Money, error) {
	entry, err := ws.wallets.FindLedgerEntryByTransactionID(ctx, tx.ID())
	if err != nil && !errors.Is(err, ErrLedgerEntryNotFound) {
		return money.Money{}, err
	}
	if entry != nil {
		return entry.BalanceAfter(), nil
	}
	// No ledger entry exists for kinds that never move the wallet (LOSS) or
	// that never got past PENDING_REFERENCE/REJECTED — the wallet's current
	// balance is the best available answer in that case.
	w, err := ws.wallets.FindByID(ctx, tx.WalletID())
	if err != nil {
		return money.Money{}, err
	}
	return w.Balance(), nil
}

func (ws *WagerSubmitter) process(ctx context.Context, tx *wager.Transaction) (*SubmitWagerTransactionResult, error) {
	var referenced *wager.Transaction
	if tx.Kind() == wager.KindRefund || tx.Kind() == wager.KindRollback {
		refExtID := *tx.ReferenceExternalTransactionID()
		var err error
		referenced, err = ws.wagers.FindByProviderAndExternalID(ctx, tx.ProviderID(), refExtID)
		if err != nil && !errors.Is(err, ErrWagerTransactionNotFound) {
			return nil, err
		}
		if referenced == nil {
			return ws.parkPendingReference(ctx, tx)
		}
		if err := tx.ValidateReference(*referenced); err != nil {
			return ws.reject(ctx, tx, FailureCodeInvalidReference)
		}
		if err := tx.ResolveReference(referenced.ID()); err != nil {
			return nil, err
		}
		duplicate, err := ws.wagers.FindReversal(ctx, referenced.ID(), tx.Kind())
		if err != nil && !errors.Is(err, ErrWagerTransactionNotFound) {
			return nil, err
		}
		if duplicate != nil {
			return ws.reject(ctx, tx, FailureCodeDuplicateReversal)
		}
	}

	if tx.Kind() == wager.KindLoss {
		return ws.finishWithoutMovement(ctx, tx)
	}

	direction, err := movementDirection(tx.Kind(), referenced)
	if err != nil {
		return nil, err
	}

	// The wallet is read, mutated and written inside the same transaction,
	// with FindByID taking a row lock (see internal/postgres's FOR UPDATE
	// convention) — required for the mandatory concurrency scenario (two
	// concurrent BETs against one wallet: the second must see the first's
	// already-applied debit, not a stale balance read before either
	// committed). Everything computed from the pre-lock balance (the
	// rejection branch included) has to happen after the lock is held.
	var result *SubmitWagerTransactionResult
	err = ws.txManager.WithinTx(ctx, func(ctx context.Context) error {
		w, err := ws.wallets.FindByID(ctx, tx.WalletID())
		if err != nil {
			return err
		}

		before := w.Balance()
		if direction == wallet.DirectionDebit {
			err = w.Debit(tx.Amount())
		} else {
			err = w.Credit(tx.Amount())
		}
		if err != nil {
			if !errors.Is(err, wallet.ErrInsufficientBalance) {
				return err
			}
			code := FailureCodeInsufficientBalance
			if tx.Kind() != wager.KindBet {
				code = FailureCodeReversalExceedsBalance
			}
			if err := tx.MarkRejected(code); err != nil {
				return err
			}
			event, err := newWagerTransactionRejectedEvent(tx)
			if err != nil {
				return err
			}
			if err := ws.wagers.Save(ctx, tx); err != nil {
				return err
			}
			if err := ws.outbox.SaveAll(ctx, event); err != nil {
				return err
			}
			result = &SubmitWagerTransactionResult{Transaction: tx, Balance: w.Balance()}
			return nil
		}

		entry, err := wallet.NewLedgerEntry(wallet.NewLedgerEntryInput{
			WalletID:      w.ID(),
			TransactionID: tx.ID(),
			Direction:     direction,
			Amount:        tx.Amount(),
			BalanceBefore: before,
			BalanceAfter:  w.Balance(),
		})
		if err != nil {
			return err
		}
		if err := tx.MarkProcessed(); err != nil {
			return err
		}
		processedEvent, err := newWagerTransactionProcessedEvent(tx)
		if err != nil {
			return err
		}
		balanceChangedEvent, err := newWalletBalanceChangedEvent(w, entry)
		if err != nil {
			return err
		}

		if err := ws.wagers.Save(ctx, tx); err != nil {
			return err
		}
		if err := ws.wallets.Save(ctx, w); err != nil {
			return err
		}
		if err := ws.wallets.SaveLedgerEntry(ctx, entry); err != nil {
			return err
		}
		if err := ws.outbox.SaveAll(ctx, processedEvent, balanceChangedEvent); err != nil {
			return err
		}
		result = &SubmitWagerTransactionResult{Transaction: tx, Balance: w.Balance()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// movementDirection reports which way the wallet moves for kind. ROLLBACK's
// direction depends on what it reverses: undoing a BET (a debit) credits the
// wallet back; undoing a WIN or REFUND (a credit) debits it back.
func movementDirection(kind wager.Kind, referenced *wager.Transaction) (wallet.Direction, error) {
	switch kind {
	case wager.KindBet:
		return wallet.DirectionDebit, nil
	case wager.KindWin, wager.KindRefund:
		return wallet.DirectionCredit, nil
	case wager.KindRollback:
		switch referenced.Kind() {
		case wager.KindBet:
			return wallet.DirectionCredit, nil
		case wager.KindWin, wager.KindRefund:
			return wallet.DirectionDebit, nil
		}
	}
	return "", fmt.Errorf("app: no wallet movement defined for kind %q", kind)
}

// parkPendingReference is called both the first time a reference can't be
// found and, by PendingReferenceResolver, on every subsequent retry that
// still can't find it — MarkPendingReference is idempotent from
// PENDING_REFERENCE for exactly this reason. Only the first call emits the
// PENDING_REFERENCE event and writes the row: a retry that changes nothing
// about tx's persisted state has nothing new to save or announce, and
// re-emitting the same event every retry cycle would spam the outbox without
// telling any consumer anything it doesn't already know. Retry bookkeeping
// (attempt count, next retry time) is the resolver's job, not this method's.
func (ws *WagerSubmitter) parkPendingReference(ctx context.Context, tx *wager.Transaction) (*SubmitWagerTransactionResult, error) {
	alreadyParked := tx.Status() == wager.TxStatusPendingReference
	if err := tx.MarkPendingReference(); err != nil {
		return nil, err
	}
	if alreadyParked {
		w, err := ws.wallets.FindByID(ctx, tx.WalletID())
		if err != nil {
			return nil, err
		}
		return &SubmitWagerTransactionResult{Transaction: tx, Balance: w.Balance()}, nil
	}
	event, err := newWagerTransactionPendingReferenceEvent(tx)
	if err != nil {
		return nil, err
	}
	return ws.saveWithEvent(ctx, tx, event)
}

func (ws *WagerSubmitter) reject(ctx context.Context, tx *wager.Transaction, code wager.FailureCode) (*SubmitWagerTransactionResult, error) {
	if err := tx.MarkRejected(code); err != nil {
		return nil, err
	}
	event, err := newWagerTransactionRejectedEvent(tx)
	if err != nil {
		return nil, err
	}
	return ws.saveWithEvent(ctx, tx, event)
}

func (ws *WagerSubmitter) finishWithoutMovement(ctx context.Context, tx *wager.Transaction) (*SubmitWagerTransactionResult, error) {
	if err := tx.MarkProcessed(); err != nil {
		return nil, err
	}
	event, err := newWagerTransactionProcessedEvent(tx)
	if err != nil {
		return nil, err
	}
	return ws.saveWithEvent(ctx, tx, event)
}

func (ws *WagerSubmitter) saveWithEvent(ctx context.Context, tx *wager.Transaction, event *outbox.Entry) (*SubmitWagerTransactionResult, error) {
	err := ws.txManager.WithinTx(ctx, func(ctx context.Context) error {
		if err := ws.wagers.Save(ctx, tx); err != nil {
			return err
		}
		return ws.outbox.SaveAll(ctx, event)
	})
	if err != nil {
		return nil, err
	}
	w, err := ws.wallets.FindByID(ctx, tx.WalletID())
	if err != nil {
		return nil, err
	}
	return &SubmitWagerTransactionResult{Transaction: tx, Balance: w.Balance()}, nil
}
