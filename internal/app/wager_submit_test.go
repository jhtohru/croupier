package app

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

type submitTestEnv struct {
	wallets   *fakeWalletRepository
	wagers    *fakeWagerRepository
	outbox    *fakeOutboxRepository
	submitter *WagerSubmitter
}

func newSubmitTestEnv(t *testing.T, initialBalance int64) (submitTestEnv, *wallet.Wallet) {
	t.Helper()

	wallets := newFakeWalletRepository()
	wagers := &fakeWagerRepository{}
	outboxRepo := &fakeOutboxRepository{}
	submitter := NewWagerSubmitter(wallets, wagers, outboxRepo, fakeTxManager{})

	balance, err := money.FromMinorUnits("BRL", initialBalance)
	if err != nil {
		t.Fatal(err)
	}
	w, err := wallet.New(uuid.New(), balance)
	if err != nil {
		t.Fatal(err)
	}
	if err := wallets.Save(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	return submitTestEnv{wallets: wallets, wagers: wagers, outbox: outboxRepo, submitter: submitter}, w
}

func mustSubmit(t *testing.T, env submitTestEnv, input SubmitWagerTransactionInput) *SubmitWagerTransactionResult {
	t.Helper()
	if input.CorrelationID == "" {
		input.CorrelationID = "test-correlation-id"
	}
	result, err := env.submitter.Submit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustAmount(t *testing.T, minorUnits int64) money.Money {
	t.Helper()
	m, err := money.FromMinorUnits("BRL", minorUnits)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWagerSubmitterSubmit(t *testing.T) {
	t.Run("bet success", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 10000)

		result := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "bet-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustAmount(t, 3000),
		})

		assert.Equal(t, wager.TxStatusProcessed, result.Transaction.Status())
		assert.Equal(t, mustAmount(t, 7000), result.Balance)
		assert.False(t, result.IdempotentReplay)
		assert.Len(t, env.wallets.ledger, 1)
		assert.Equal(t, wallet.DirectionDebit, env.wallets.ledger[0].Direction())
		assert.Len(t, env.outbox.entries, 2)
	})

	t.Run("bet insufficient balance", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 100)

		result := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "bet-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustAmount(t, 300),
		})

		assert.Equal(t, wager.TxStatusRejected, result.Transaction.Status())
		assert.Equal(t, FailureCodeInsufficientBalance, result.Transaction.FailureCode())
		assert.Equal(t, mustAmount(t, 100), result.Balance)
		assert.Empty(t, env.wallets.ledger)
	})

	t.Run("win success", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 1000)

		result := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "win-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindWin, Amount: mustAmount(t, 500),
		})

		assert.Equal(t, wager.TxStatusProcessed, result.Transaction.Status())
		assert.Equal(t, mustAmount(t, 1500), result.Balance)
		assert.Equal(t, wallet.DirectionCredit, env.wallets.ledger[0].Direction())
	})

	t.Run("loss success, no wallet movement", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 1000)

		result := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "loss-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindLoss, Amount: mustAmount(t, 0),
		})

		assert.Equal(t, wager.TxStatusProcessed, result.Transaction.Status())
		assert.Equal(t, mustAmount(t, 1000), result.Balance)
		assert.Empty(t, env.wallets.ledger)
		assert.Len(t, env.outbox.entries, 1) // only WagerTransactionProcessed, no balance change
	})

	t.Run("loss with mismatched currency is rejected", func(t *testing.T) {
		// Challenge spec §7.23: "LOSS continua exigindo a moeda da
		// carteira" — LOSS never touches the wallet's balance, so this
		// can't rely on Wallet.Credit/Debit's own currency check like
		// every other kind does; it needs its own.
		env, w := newSubmitTestEnv(t, 1000)
		amount, err := money.Zero("USD")
		require.NoError(t, err)

		_, err = env.submitter.Submit(context.Background(), SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "loss-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindLoss, Amount: amount, CorrelationID: "test-correlation-id",
		})

		assert.ErrorIs(t, err, money.ErrCurrencyMismatch)
	})

	t.Run("refund success", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 10000)

		betResult := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "bet-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustAmount(t, 3000),
		})

		refID := "bet-1"
		result := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "refund-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRefund, Amount: mustAmount(t, 3000), ReferenceExternalTransactionID: &refID,
		})

		assert.Equal(t, wager.TxStatusProcessed, result.Transaction.Status())
		if assert.NotNil(t, result.Transaction.ReferenceTransactionID()) {
			assert.Equal(t, betResult.Transaction.ID(), *result.Transaction.ReferenceTransactionID())
		}
		assert.Equal(t, mustAmount(t, 10000), result.Balance)
	})

	t.Run("refund reference not found becomes pending", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 10000)
		refID := "missing-bet"

		result := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "refund-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRefund, Amount: mustAmount(t, 3000), ReferenceExternalTransactionID: &refID,
		})

		assert.Equal(t, wager.TxStatusPendingReference, result.Transaction.Status())
		assert.Equal(t, mustAmount(t, 10000), result.Balance)
		assert.Empty(t, env.wallets.ledger)
	})

	t.Run("refund with mismatched reference is rejected", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 10000)

		mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "bet-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustAmount(t, 3000),
		})

		refID := "bet-1"
		result := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "refund-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRefund, Amount: mustAmount(t, 999), ReferenceExternalTransactionID: &refID, // amount doesn't match the BET
		})

		assert.Equal(t, wager.TxStatusRejected, result.Transaction.Status())
		assert.Equal(t, FailureCodeInvalidReference, result.Transaction.FailureCode())
	})

	t.Run("duplicate refund on same reference is rejected", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 10000)

		mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "bet-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustAmount(t, 3000),
		})
		refID := "bet-1"
		mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "refund-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRefund, Amount: mustAmount(t, 3000), ReferenceExternalTransactionID: &refID,
		})

		result := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "refund-2",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRefund, Amount: mustAmount(t, 3000), ReferenceExternalTransactionID: &refID,
		})

		assert.Equal(t, wager.TxStatusRejected, result.Transaction.Status())
		assert.Equal(t, FailureCodeDuplicateReversal, result.Transaction.FailureCode())
	})

	t.Run("rollback after a processed refund on the same reference is rejected", func(t *testing.T) {
		// Challenge spec §7.26: a REFUND and a ROLLBACK on the same BET must
		// not both succeed, or the same debit gets returned twice. This is
		// the cross-kind case — FindReversal must reject regardless of
		// which reversal kind came first.
		env, w := newSubmitTestEnv(t, 10000)

		mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "bet-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustAmount(t, 3000),
		})
		refID := "bet-1"
		mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "refund-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRefund, Amount: mustAmount(t, 3000), ReferenceExternalTransactionID: &refID,
		})

		result := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "rollback-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRollback, Amount: mustAmount(t, 3000), ReferenceExternalTransactionID: &refID,
		})

		assert.Equal(t, wager.TxStatusRejected, result.Transaction.Status())
		assert.Equal(t, FailureCodeDuplicateReversal, result.Transaction.FailureCode())
	})

	t.Run("rollback of a bet credits back", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 10000)

		mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "bet-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustAmount(t, 3000),
		})

		refID := "bet-1"
		result := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "rollback-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRollback, Amount: mustAmount(t, 3000), ReferenceExternalTransactionID: &refID,
		})

		assert.Equal(t, wager.TxStatusProcessed, result.Transaction.Status())
		assert.Equal(t, mustAmount(t, 10000), result.Balance)
	})

	t.Run("rollback of a win debits back", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 1000)

		mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "win-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindWin, Amount: mustAmount(t, 500),
		})

		refID := "win-1"
		result := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "rollback-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRollback, Amount: mustAmount(t, 500), ReferenceExternalTransactionID: &refID,
		})

		assert.Equal(t, wager.TxStatusProcessed, result.Transaction.Status())
		assert.Equal(t, mustAmount(t, 1000), result.Balance)
	})

	t.Run("rollback exceeding balance uses a distinct failure code from bet", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 1000)

		mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "win-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindWin, Amount: mustAmount(t, 500),
		}) // balance now 1500
		mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "bet-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustAmount(t, 1400),
		}) // balance now 100

		refID := "win-1"
		result := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "rollback-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRollback, Amount: mustAmount(t, 500), ReferenceExternalTransactionID: &refID,
		}) // rolling back the 500 credit needs to debit 500, but only 100 is left

		assert.Equal(t, wager.TxStatusRejected, result.Transaction.Status())
		assert.Equal(t, FailureCodeReversalExceedsBalance, result.Transaction.FailureCode())
		assert.NotEqual(t, FailureCodeInsufficientBalance, result.Transaction.FailureCode())
	})

	t.Run("idempotent replay returns the original balance, not the current one", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 10000)

		input := SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "bet-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustAmount(t, 3000),
		}
		first := mustSubmit(t, env, input)

		mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "win-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindWin, Amount: mustAmount(t, 2000),
		}) // moves the wallet balance further

		replay := mustSubmit(t, env, input)

		assert.True(t, replay.IdempotentReplay)
		assert.Equal(t, first.Transaction.ID(), replay.Transaction.ID())
		assert.Equal(t, first.Balance, replay.Balance)
		assert.Len(t, env.wagers.transactions, 2) // bet + win only, replay didn't create a third
	})

	t.Run("idempotency conflict on same key with different content", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 10000)

		input := SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "bet-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustAmount(t, 3000),
		}
		mustSubmit(t, env, input)

		input.Amount = mustAmount(t, 999)
		result, err := env.submitter.Submit(context.Background(), input)
		assert.ErrorIs(t, err, ErrIdempotencyConflict)
		assert.Nil(t, result)
	})
}

// raceyWagerRepository simulates the real unique-constraint race Postgres
// enforces: the first Save for a given (providerId, externalTransactionId)
// pretends a concurrent Submit for the exact same key already committed its
// own row moments earlier, exactly like WagerRepository.Save translates a
// real "wager_transactions_provider_external_unique" violation.
type raceyWagerRepository struct {
	*fakeWagerRepository
	winner    *wager.Transaction
	triggered bool
}

func (r *raceyWagerRepository) Save(ctx context.Context, tx *wager.Transaction) error {
	if !r.triggered && tx.ProviderID() == r.winner.ProviderID() && tx.ExternalTransactionID() == r.winner.ExternalTransactionID() {
		r.triggered = true
		r.fakeWagerRepository.transactions = append(r.fakeWagerRepository.transactions, r.winner)
		return ErrWagerTransactionAlreadyExists
	}
	return r.fakeWagerRepository.Save(ctx, tx)
}

// TestWagerSubmitterSubmitRetriesAsReplayOnInsertRace exercises the Fase 12
// mandatory scenario's failure mode directly and deterministically (the
// integration-level version, TestWagerSubmitterConcurrentDuplicateSubmissions
// in internal/postgres, proves it under real concurrency against real
// Postgres — this test isolates just Submit's retry control flow): losing
// the race to insert (providerId, externalTransactionId) must produce a
// clean idempotent replay against the winner, never a raw error.
func TestWagerSubmitterSubmitRetriesAsReplayOnInsertRace(t *testing.T) {
	wallets := newFakeWalletRepository()
	outboxRepo := &fakeOutboxRepository{}

	balance := mustAmount(t, 10000)
	w, err := wallet.New(uuid.New(), balance)
	require.NoError(t, err)
	require.NoError(t, wallets.Save(context.Background(), w))

	winnerInput := SubmitWagerTransactionInput{
		ProviderID: "provider-a", ExternalTransactionID: "bet-1",
		PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
		Kind: wager.KindBet, Amount: mustAmount(t, 3000),
		CorrelationID: "test-correlation-id",
	}
	winner, err := wager.NewTransaction(wager.NewTransactionInput{
		ProviderID: winnerInput.ProviderID, ExternalTransactionID: winnerInput.ExternalTransactionID,
		PlayerID: winnerInput.PlayerID, WalletID: winnerInput.WalletID,
		RoundID: winnerInput.RoundID, GameID: winnerInput.GameID,
		Kind: winnerInput.Kind, Amount: winnerInput.Amount,
	})
	require.NoError(t, err)
	require.NoError(t, winner.MarkProcessed())
	entry, err := wallet.NewLedgerEntry(wallet.NewLedgerEntryInput{
		WalletID: w.ID(), TransactionID: winner.ID(), Direction: wallet.DirectionDebit,
		Amount: winnerInput.Amount, BalanceBefore: balance, BalanceAfter: mustAmount(t, 7000),
	})
	require.NoError(t, err)
	require.NoError(t, wallets.SaveLedgerEntry(context.Background(), entry))

	wagers := &raceyWagerRepository{fakeWagerRepository: &fakeWagerRepository{}, winner: winner}
	submitter := NewWagerSubmitter(wallets, wagers, outboxRepo, fakeTxManager{})

	// Same content as winnerInput — a genuine race between two identical
	// submissions, not a conflict.
	result, err := submitter.Submit(context.Background(), winnerInput)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IdempotentReplay)
	assert.Equal(t, winner.ID(), result.Transaction.ID())
	assert.Equal(t, mustAmount(t, 7000), result.Balance) // the winner's observed balance, not a second debit
	assert.True(t, wagers.triggered)
}
