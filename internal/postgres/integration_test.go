//go:build integration

package postgres_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/postgres"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

// testPool connects to a real Postgres — TEST_DATABASE_URL, or the
// docker-compose default. Migrations (internal/postgres/migrations) must
// already be applied; see README.md.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://croupier:croupier@localhost:5432/croupier?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Ping(context.Background()))
	return pool
}

func TestWalletRepositoryRoundTrip(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewWalletRepository(pool)

	balance, err := money.FromMinorUnits("BRL", 1000)
	require.NoError(t, err)
	w, err := wallet.New(uuid.New(), balance)
	require.NoError(t, err)

	require.NoError(t, repo.Save(context.Background(), w))

	got, err := repo.FindByID(context.Background(), w.ID())
	require.NoError(t, err)
	assert.Equal(t, w.ID(), got.ID())
	assert.Equal(t, w.PlayerID(), got.PlayerID())
	assert.Equal(t, w.Balance(), got.Balance())
	assert.Equal(t, w.Version(), got.Version())

	byOwner, err := repo.FindByPlayerAndCurrency(context.Background(), w.PlayerID(), w.Balance().Currency())
	require.NoError(t, err)
	assert.Equal(t, w.ID(), byOwner.ID())

	_, err = repo.FindByID(context.Background(), uuid.New())
	assert.ErrorIs(t, err, app.ErrWalletNotFound)
}

func TestWagerRepositoryRoundTrip(t *testing.T) {
	pool := testPool(t)
	walletRepo := postgres.NewWalletRepository(pool)
	wagerRepo := postgres.NewWagerRepository(pool)

	balance, err := money.FromMinorUnits("BRL", 1000)
	require.NoError(t, err)
	w, err := wallet.New(uuid.New(), balance)
	require.NoError(t, err)
	require.NoError(t, walletRepo.Save(context.Background(), w))

	amount, err := money.FromMinorUnits("BRL", 500)
	require.NoError(t, err)
	tx, err := wager.NewTransaction(wager.NewTransactionInput{
		ProviderID: "provider-a", ExternalTransactionID: uuid.New().String(),
		PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
		Kind: wager.KindBet, Amount: amount,
	})
	require.NoError(t, err)
	require.NoError(t, wagerRepo.Save(context.Background(), tx))

	got, err := wagerRepo.FindByID(context.Background(), tx.ID())
	require.NoError(t, err)
	assert.Equal(t, tx.ID(), got.ID())
	assert.Equal(t, tx.Kind(), got.Kind())
	assert.Equal(t, tx.Amount(), got.Amount())
	assert.Equal(t, tx.ProviderID(), got.ProviderID())

	byProvider, err := wagerRepo.FindByProviderAndExternalID(context.Background(), tx.ProviderID(), tx.ExternalTransactionID())
	require.NoError(t, err)
	assert.Equal(t, tx.ID(), byProvider.ID())

	_, err = wagerRepo.FindByProviderAndExternalID(context.Background(), "provider-a", "does-not-exist")
	assert.ErrorIs(t, err, app.ErrWagerTransactionNotFound)
}

func TestWagerRepositoryTwoOpeningTransactionsCoexist(t *testing.T) {
	pool := testPool(t)
	walletRepo := postgres.NewWalletRepository(pool)
	wagerRepo := postgres.NewWagerRepository(pool)

	balance, err := money.FromMinorUnits("BRL", 1000)
	require.NoError(t, err)
	w, err := wallet.New(uuid.New(), balance)
	require.NoError(t, err)
	require.NoError(t, walletRepo.Save(context.Background(), w))

	for i := 0; i < 2; i++ {
		opening, err := wager.NewOpeningTransaction(wager.NewOpeningInput{
			PlayerID: w.PlayerID(), WalletID: w.ID(), Amount: balance,
		})
		require.NoError(t, err)
		// Two OPENING rows both have NULL provider_id/external_transaction_id
		// — must not collide on the unique constraint.
		require.NoError(t, wagerRepo.Save(context.Background(), opening))
	}
}

// TestWagerSubmitterConcurrentBets is the mandatory scenario: a wallet with
// 100.00 BRL receives two concurrent 80.00 BRL bets. Exactly one must be
// processed, the other rejected for insufficient balance, final balance
// 20.00 BRL, a single ledger debit.
func TestWagerSubmitterConcurrentBets(t *testing.T) {
	pool := testPool(t)
	walletRepo := postgres.NewWalletRepository(pool)
	wagerRepo := postgres.NewWagerRepository(pool)
	outboxRepo := postgres.NewOutboxRepository(pool)
	txManager := postgres.NewTxManager(pool)

	balance, err := money.FromMinorUnits("BRL", 10000)
	require.NoError(t, err)
	w, err := wallet.New(uuid.New(), balance)
	require.NoError(t, err)
	require.NoError(t, walletRepo.Save(context.Background(), w))

	submitter := app.NewWagerSubmitter(walletRepo, wagerRepo, outboxRepo, txManager)

	betAmount, err := money.FromMinorUnits("BRL", 8000)
	require.NoError(t, err)

	var wg sync.WaitGroup
	results := make([]*app.SubmitWagerTransactionResult, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = submitter.Submit(context.Background(), app.SubmitWagerTransactionInput{
				ProviderID: "provider-a", ExternalTransactionID: uuid.New().String(),
				PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
				Kind: wager.KindBet, Amount: betAmount,
			})
		}(i)
	}
	wg.Wait()

	processed, rejected := 0, 0
	for i := 0; i < 2; i++ {
		require.NoError(t, errs[i])
		require.NotNil(t, results[i])
		switch results[i].Transaction.Status() {
		case wager.TxStatusProcessed:
			processed++
		case wager.TxStatusRejected:
			rejected++
			assert.Equal(t, app.FailureCodeInsufficientBalance, results[i].Transaction.FailureCode())
		default:
			t.Fatalf("unexpected status %s", results[i].Transaction.Status())
		}
	}
	assert.Equal(t, 1, processed)
	assert.Equal(t, 1, rejected)

	final, err := walletRepo.FindByID(context.Background(), w.ID())
	require.NoError(t, err)
	wantBalance, err := money.FromMinorUnits("BRL", 2000)
	require.NoError(t, err)
	assert.Equal(t, wantBalance, final.Balance())

	entries, err := walletRepo.AllLedgerEntries(context.Background(), w.ID())
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, wallet.DirectionDebit, entries[0].Direction())
}

// TestPendingReferenceResolverRealPostgres exercises the whole
// PENDING_REFERENCE retry path against real Postgres: a REFUND submitted
// before its BET exists parks, a first ResolveDue leaves it parked and
// schedules a future retry (so it must NOT be picked up again immediately),
// and once the BET arrives a due ResolveDue resolves it and moves the wallet.
func TestPendingReferenceResolverRealPostgres(t *testing.T) {
	pool := testPool(t)
	walletRepo := postgres.NewWalletRepository(pool)
	wagerRepo := postgres.NewWagerRepository(pool)
	outboxRepo := postgres.NewOutboxRepository(pool)
	txManager := postgres.NewTxManager(pool)

	balance, err := money.FromMinorUnits("BRL", 10000)
	require.NoError(t, err)
	w, err := wallet.New(uuid.New(), balance)
	require.NoError(t, err)
	require.NoError(t, walletRepo.Save(context.Background(), w))

	submitter := app.NewWagerSubmitter(walletRepo, wagerRepo, outboxRepo, txManager)
	resolver := app.NewPendingReferenceResolver(wagerRepo, submitter, 5, time.Hour, time.Minute)

	// Every id below is random per run, not a fixed literal: the integration
	// suite never truncates the database between runs (every other test in
	// this file already relies on that by using uuid.New() throughout), so a
	// fixed (providerId, externalTransactionId) would collide with a row a
	// previous run left behind and fail with ErrIdempotencyConflict instead
	// of testing what this test is actually about.
	providerID := "provider-" + uuid.New().String()
	missingRefID := "bet-" + uuid.New().String()
	refundResult, err := submitter.Submit(context.Background(), app.SubmitWagerTransactionInput{
		ProviderID: providerID, ExternalTransactionID: "refund-" + uuid.New().String(),
		PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
		Kind: wager.KindRefund, Amount: mustMoney(t, 3000), ReferenceExternalTransactionID: &missingRefID,
	})
	require.NoError(t, err)
	require.Equal(t, wager.TxStatusPendingReference, refundResult.Transaction.Status())

	// Not due yet: nothing scheduled means "due now" on first sight, so a
	// resolve attempt right away DOES pick it up — but since the reference
	// still doesn't exist, it must stay parked and get a future retry time
	// instead of an immediate one. ResolveDue's return count isn't asserted
	// here: it counts every due PENDING_REFERENCE row in the whole table,
	// not just this test's own, so it's not test-isolated — checking this
	// transaction's own attempts count directly is.
	_, err = resolver.ResolveDue(context.Background(), time.Now(), 10)
	require.NoError(t, err)
	stillParked, err := wagerRepo.FindByID(context.Background(), refundResult.Transaction.ID())
	require.NoError(t, err)
	assert.Equal(t, wager.TxStatusPendingReference, stillParked.Status())
	attemptsAfterFirstResolve := pendingReferenceAttempts(t, pool, refundResult.Transaction.ID())
	assert.Equal(t, 1, attemptsAfterFirstResolve)

	// Immediately due again must leave this transaction's own attempts count
	// unchanged — the retry was scheduled a minute out.
	_, err = resolver.ResolveDue(context.Background(), time.Now(), 10)
	require.NoError(t, err)
	assert.Equal(t, attemptsAfterFirstResolve, pendingReferenceAttempts(t, pool, refundResult.Transaction.ID()))

	_, err = submitter.Submit(context.Background(), app.SubmitWagerTransactionInput{
		ProviderID: providerID, ExternalTransactionID: missingRefID,
		PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
		Kind: wager.KindBet, Amount: mustMoney(t, 3000),
	})
	require.NoError(t, err)

	_, err = resolver.ResolveDue(context.Background(), time.Now().Add(2*time.Minute), 10)
	require.NoError(t, err)

	resolved, err := wagerRepo.FindByID(context.Background(), refundResult.Transaction.ID())
	require.NoError(t, err)
	assert.Equal(t, wager.TxStatusProcessed, resolved.Status())

	finalWallet, err := walletRepo.FindByID(context.Background(), w.ID())
	require.NoError(t, err)
	assert.Equal(t, balance, finalWallet.Balance()) // BET debited 3000, REFUND credited it back
}

func mustMoney(t *testing.T, minorUnits int64) money.Money {
	t.Helper()
	m, err := money.FromMinorUnits("BRL", minorUnits)
	require.NoError(t, err)
	return m
}

// pendingReferenceAttempts reads a transaction's retry bookkeeping directly
// via SQL — there's no repository method for it (it's not something the app
// layer needs to ask for on its own), but it's the only way to assert on
// this test's own transaction without depending on ResolveDue's return
// count, which reflects every due row in the table, not just this test's.
func pendingReferenceAttempts(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) int {
	t.Helper()
	var attempts int
	err := pool.QueryRow(context.Background(), `SELECT pending_reference_attempts FROM wager_transactions WHERE id = $1`, id).Scan(&attempts)
	require.NoError(t, err)
	return attempts
}
