package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/wager"
)

func TestPendingReferenceResolverResolveDue(t *testing.T) {
	t.Run("resolves once the reference exists", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 10000)
		missingRefID := "bet-1"
		parked := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "refund-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRefund, Amount: mustAmount(t, 3000), ReferenceExternalTransactionID: &missingRefID,
		})
		require.Equal(t, wager.TxStatusPendingReference, parked.Transaction.Status())

		bet := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: missingRefID,
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustAmount(t, 3000),
		})
		require.Equal(t, wager.TxStatusProcessed, bet.Transaction.Status())

		resolver := NewPendingReferenceResolver(env.wagers, env.submitter, 5, time.Hour, time.Second)
		n, err := resolver.ResolveDue(context.Background(), time.Now(), 10)
		require.NoError(t, err)
		assert.Equal(t, 1, n)

		refund, err := env.wagers.FindByID(context.Background(), parked.Transaction.ID())
		require.NoError(t, err)
		assert.Equal(t, wager.TxStatusProcessed, refund.Status())
		if assert.NotNil(t, refund.ReferenceTransactionID()) {
			assert.Equal(t, bet.Transaction.ID(), *refund.ReferenceTransactionID())
		}
		finalWallet, err := env.wallets.FindByID(context.Background(), w.ID())
		require.NoError(t, err)
		assert.Equal(t, mustAmount(t, 10000), finalWallet.Balance()) // BET debited 3000, REFUND credited it back
	})

	t.Run("still not found reschedules with backoff", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 10000)
		missingRefID := "never-arrives"
		parked := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "refund-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRefund, Amount: mustAmount(t, 3000), ReferenceExternalTransactionID: &missingRefID,
		})
		require.Equal(t, wager.TxStatusPendingReference, parked.Transaction.Status())
		outboxEntriesBefore := len(env.outbox.entries)

		resolver := NewPendingReferenceResolver(env.wagers, env.submitter, 5, time.Hour, time.Second)
		now := time.Now()
		n, err := resolver.ResolveDue(context.Background(), now, 10)
		require.NoError(t, err)
		assert.Equal(t, 1, n)

		still, err := env.wagers.FindByID(context.Background(), parked.Transaction.ID())
		require.NoError(t, err)
		assert.Equal(t, wager.TxStatusPendingReference, still.Status())
		assert.Equal(t, 1, env.wagers.attempts[parked.Transaction.ID()])
		assert.WithinDuration(t, now.Add(time.Second), env.wagers.nextRetryAt[parked.Transaction.ID()], time.Millisecond)
		// A retry that changes nothing shouldn't re-announce PENDING_REFERENCE.
		assert.Equal(t, outboxEntriesBefore, len(env.outbox.entries))
	})

	t.Run("gives up after max attempts", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 10000)
		missingRefID := "never-arrives"
		parked := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "refund-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRefund, Amount: mustAmount(t, 3000), ReferenceExternalTransactionID: &missingRefID,
		})

		// Long backoffBase on purpose: each loop iteration forces the item due
		// again by resetting its schedule directly, instead of racing real
		// backoff delays against wall-clock time.Now() calls a few lines apart.
		resolver := NewPendingReferenceResolver(env.wagers, env.submitter, 2, 24*time.Hour, time.Hour)
		ctx := context.Background()
		for i := 0; i < 2; i++ {
			n, err := resolver.ResolveDue(ctx, time.Now(), 10)
			require.NoError(t, err)
			require.Equal(t, 1, n)
			env.wagers.nextRetryAt[parked.Transaction.ID()] = time.Time{} // force due for the next iteration
		}

		n, err := resolver.ResolveDue(ctx, time.Now(), 10)
		require.NoError(t, err)
		assert.Equal(t, 1, n)

		expired, err := env.wagers.FindByID(ctx, parked.Transaction.ID())
		require.NoError(t, err)
		assert.Equal(t, wager.TxStatusRejected, expired.Status())
		assert.Equal(t, FailureCodeReferenceNotFound, expired.FailureCode())
	})

	t.Run("gives up after ttl even under the attempt limit", func(t *testing.T) {
		env, w := newSubmitTestEnv(t, 10000)
		missingRefID := "never-arrives"
		parked := mustSubmit(t, env, SubmitWagerTransactionInput{
			ProviderID: "provider-a", ExternalTransactionID: "refund-1",
			PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindRefund, Amount: mustAmount(t, 3000), ReferenceExternalTransactionID: &missingRefID,
		})

		resolver := NewPendingReferenceResolver(env.wagers, env.submitter, 100, time.Minute, time.Second)
		future := parked.Transaction.CreatedAt().Add(2 * time.Minute)
		n, err := resolver.ResolveDue(context.Background(), future, 10)
		require.NoError(t, err)
		assert.Equal(t, 1, n)

		expired, err := env.wagers.FindByID(context.Background(), parked.Transaction.ID())
		require.NoError(t, err)
		assert.Equal(t, wager.TxStatusRejected, expired.Status())
		assert.Equal(t, FailureCodeReferenceNotFound, expired.FailureCode())
	})
}
