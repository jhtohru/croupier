//go:build integration

package postgres_test

import (
	"context"
	"os"
	"sync"
	"testing"

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
