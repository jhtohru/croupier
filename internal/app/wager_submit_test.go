package app

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

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
