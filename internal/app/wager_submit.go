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
	// CorrelationID identifies the HTTP request or SQS delivery that caused
	// this submission — required, propagated onto every outbox event this
	// call emits (challenge spec §11's event envelope). Callers already have
	// one: internal/httpapi generates/forwards one per request (Fase 11),
	// internal/sqs.Consumer generates one per delivery.
	CorrelationID string
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

	result, err := ws.process(ctx, candidate, input.CorrelationID)
	if errors.Is(err, ErrWagerTransactionAlreadyExists) {
		// This check-then-insert is inherently racy under real concurrency
		// (the check above and process's own insert aren't one atomic step)
		// — losing that race isn't a real conflict, just a concurrent
		// Submit for the identical key that committed first. Replay against
		// whatever it wrote, exactly like the ordinary path above would
		// have if the check had run a moment later. The mandatory 50-
		// concurrent-identical-submissions scenario (TODO.md, Fase 12)
		// depends on this: one caller gets a fresh PROCESSED result, every
		// other one gets a clean replay, never a raw insert error.
		winner, ferr := ws.wagers.FindByProviderAndExternalID(ctx, input.ProviderID, input.ExternalTransactionID)
		if ferr != nil {
			return nil, ferr
		}
		return ws.replay(ctx, winner, candidate)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
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

func (ws *WagerSubmitter) process(ctx context.Context, tx *wager.Transaction, correlationID string) (*SubmitWagerTransactionResult, error) {
	var referenced *wager.Transaction
	if tx.Kind() == wager.KindRefund || tx.Kind() == wager.KindRollback {
		refExtID := *tx.ReferenceExternalTransactionID()
		var err error
		referenced, err = ws.wagers.FindByProviderAndExternalID(ctx, tx.ProviderID(), refExtID)
		if err != nil && !errors.Is(err, ErrWagerTransactionNotFound) {
			return nil, err
		}
		if referenced == nil {
			return ws.parkPendingReference(ctx, tx, correlationID)
		}
		if err := tx.ValidateReference(*referenced); err != nil {
			return ws.reject(ctx, tx, FailureCodeInvalidReference, correlationID)
		}
		if err := tx.ResolveReference(referenced.ID()); err != nil {
			return nil, err
		}
		// The duplicate-reversal check itself happens below, inside the
		// wallet's row lock, not here — see the comment there for why a
		// check at this point (before any lock is held) isn't enough to
		// actually prevent two concurrent reversals of the same reference
		// from both succeeding.
	}

	if tx.Kind() == wager.KindLoss {
		return ws.finishWithoutMovement(ctx, tx, correlationID)
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

		// REFUND/ROLLBACK: check for a duplicate reversal now that the
		// wallet's row lock is held, not before opening this transaction.
		// A reversal always targets the same wallet as its reference
		// (ValidateReference requires it), so this lock also serializes
		// two concurrent reversals of the same reference — the earlier
		// point in this function only resolves the reference and validates
		// it, it can't actually prevent the race (§7.25/§7.26: two
		// concurrent REFUND/ROLLBACK submissions on the same reference,
		// e.g. one of each kind, must never both succeed — a check before
		// any lock is a classic check-then-act TOCTOU: both could pass it
		// before either commits).
		if tx.Kind() == wager.KindRefund || tx.Kind() == wager.KindRollback {
			duplicate, err := ws.wagers.FindReversal(ctx, referenced.ID())
			if err != nil && !errors.Is(err, ErrWagerTransactionNotFound) {
				return err
			}
			if duplicate != nil {
				if err := tx.MarkRejected(FailureCodeDuplicateReversal); err != nil {
					return err
				}
				event, err := newWagerTransactionRejectedEvent(tx, correlationID)
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
			event, err := newWagerTransactionRejectedEvent(tx, correlationID)
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
		processedEvent, err := newWagerTransactionProcessedEvent(tx, correlationID)
		if err != nil {
			return err
		}
		processedEventID := processedEvent.ID().String()
		balanceChangedEvent, err := newWalletBalanceChangedEvent(w, entry, correlationID, &processedEventID)
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
func (ws *WagerSubmitter) parkPendingReference(ctx context.Context, tx *wager.Transaction, correlationID string) (*SubmitWagerTransactionResult, error) {
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
	event, err := newWagerTransactionPendingReferenceEvent(tx, correlationID)
	if err != nil {
		return nil, err
	}
	return ws.saveWithEvent(ctx, tx, event)
}

func (ws *WagerSubmitter) reject(ctx context.Context, tx *wager.Transaction, code wager.FailureCode, correlationID string) (*SubmitWagerTransactionResult, error) {
	if err := tx.MarkRejected(code); err != nil {
		return nil, err
	}
	event, err := newWagerTransactionRejectedEvent(tx, correlationID)
	if err != nil {
		return nil, err
	}
	return ws.saveWithEvent(ctx, tx, event)
}

func (ws *WagerSubmitter) finishWithoutMovement(ctx context.Context, tx *wager.Transaction, correlationID string) (*SubmitWagerTransactionResult, error) {
	// LOSS never calls Wallet.Credit/Debit (no movement), so it never goes
	// through Money.Add/Subtract's own currency check the way every other
	// kind does — checked explicitly here instead (challenge spec §7.23:
	// "LOSS continua exigindo a moeda da carteira"). Same error
	// (money.ErrCurrencyMismatch) and same 400 mapping (internal/httpapi's
	// validationErrors) as a currency mismatch on any other kind.
	w, err := ws.wallets.FindByID(ctx, tx.WalletID())
	if err != nil {
		return nil, err
	}
	if tx.Amount().Currency() != w.Balance().Currency() {
		return nil, money.ErrCurrencyMismatch
	}

	if err := tx.MarkProcessed(); err != nil {
		return nil, err
	}
	event, err := newWagerTransactionProcessedEvent(tx, correlationID)
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
