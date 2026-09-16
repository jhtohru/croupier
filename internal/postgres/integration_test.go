//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/inbox"
	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/outbox"
	"github.com/jhtohru/croupier/internal/postgres"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

// testPool connects to a real Postgres — TEST_DATABASE_URL, or the
// docker-compose default. Migrations (internal/postgres/migrations) must
// already be applied; see README.md.
func testDSN() string {
	if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return "postgres://croupier:croupier@localhost:5432/croupier?sslmode=disable"
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), testDSN())
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

// TestWagerSubmitterConcurrentDuplicateSubmissions is the other mandatory
// concurrency scenario: 50 parallel requests submitting the exact same bet
// (same providerId:externalTransactionId, same everything) must produce
// exactly one debit — every other caller gets back a clean idempotent
// replay, not an error.
func TestWagerSubmitterConcurrentDuplicateSubmissions(t *testing.T) {
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

	betAmount, err := money.FromMinorUnits("BRL", 3000)
	require.NoError(t, err)
	externalTransactionID := uuid.New().String()

	const n = 50
	var wg sync.WaitGroup
	results := make([]*app.SubmitWagerTransactionResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = submitter.Submit(context.Background(), app.SubmitWagerTransactionInput{
				ProviderID: "provider-a", ExternalTransactionID: externalTransactionID,
				PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
				Kind: wager.KindBet, Amount: betAmount,
				CorrelationID: "test-correlation-id",
			})
		}(i)
	}
	wg.Wait()

	firstProcessed, replays := 0, 0
	var transactionID uuid.UUID
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i], "call %d", i)
		require.NotNil(t, results[i], "call %d", i)
		assert.Equal(t, wager.TxStatusProcessed, results[i].Transaction.Status(), "call %d", i)
		if results[i].IdempotentReplay {
			replays++
		} else {
			firstProcessed++
		}
		if transactionID == uuid.Nil {
			transactionID = results[i].Transaction.ID()
		}
		// All 50 calls must agree on which transaction this was — a second,
		// different id here would mean the unique constraint let two rows
		// through for the same (providerId, externalTransactionId).
		assert.Equal(t, transactionID, results[i].Transaction.ID(), "call %d", i)
	}
	assert.Equal(t, 1, firstProcessed)
	assert.Equal(t, n-1, replays)

	final, err := walletRepo.FindByID(context.Background(), w.ID())
	require.NoError(t, err)
	wantBalance, err := money.FromMinorUnits("BRL", 7000)
	require.NoError(t, err)
	assert.Equal(t, wantBalance, final.Balance())

	entries, err := walletRepo.AllLedgerEntries(context.Background(), w.ID())
	require.NoError(t, err)
	assert.Len(t, entries, 1, "exactly one debit, not one per winning goroutine")
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
				CorrelationID: "test-correlation-id",
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

// TestWalletRepositoryFindByIDLocksPerRowNotGlobally is the mandatory
// "wallets distintas processam em paralelo sem lock global" scenario,
// proven directly against the locking primitive itself rather than via a
// wall-clock timing heuristic on the full Submit flow: hold wallet A's row
// lock open in one transaction, and confirm a concurrent FindByID on wallet
// B — a completely different row — is never blocked behind it.
func TestWalletRepositoryFindByIDLocksPerRowNotGlobally(t *testing.T) {
	pool := testPool(t)
	walletRepo := postgres.NewWalletRepository(pool)
	txManager := postgres.NewTxManager(pool)

	balance, err := money.FromMinorUnits("BRL", 10000)
	require.NoError(t, err)
	walletA, err := wallet.New(uuid.New(), balance)
	require.NoError(t, err)
	require.NoError(t, walletRepo.Save(context.Background(), walletA))
	walletB, err := wallet.New(uuid.New(), balance)
	require.NoError(t, err)
	require.NoError(t, walletRepo.Save(context.Background(), walletB))

	holdingLock := make(chan struct{})
	releaseLock := make(chan struct{})
	txAErr := make(chan error, 1)
	go func() {
		txAErr <- txManager.WithinTx(context.Background(), func(ctx context.Context) error {
			if _, err := walletRepo.FindByID(ctx, walletA.ID()); err != nil {
				return err
			}
			close(holdingLock)
			<-releaseLock
			return nil
		})
	}()

	<-holdingLock // wallet A's row lock is now held open, deliberately

	done := make(chan error, 1)
	go func() {
		done <- txManager.WithinTx(context.Background(), func(ctx context.Context) error {
			_, err := walletRepo.FindByID(ctx, walletB.ID())
			return err
		})
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("locking wallet A's row blocked an unrelated read of wallet B — lock is not scoped per row")
	}

	close(releaseLock)
	require.NoError(t, <-txAErr)
}

// TestConcurrentBetsAcrossMultipleAppInstances is the mandatory "3+
// instâncias independentes replicam os cenários acima" scenario: three
// wholly independent stacks (own *pgxpool.Pool, own repositories, own
// *app.WagerSubmitter — exactly what three separate cmd/croupier processes
// would each have) submit concurrent bets against one shared wallet.
// Correctness has to come from Postgres's own row lock, since these three
// "instances" share no in-process state whatsoever to coordinate through.
func TestConcurrentBetsAcrossMultipleAppInstances(t *testing.T) {
	dsn := testDSN()
	const numInstances = 3

	type instance struct {
		submitter *app.WagerSubmitter
	}
	newInstance := func() instance {
		pool, err := pgxpool.New(context.Background(), dsn)
		require.NoError(t, err)
		t.Cleanup(pool.Close)
		wallets := postgres.NewWalletRepository(pool)
		wagers := postgres.NewWagerRepository(pool)
		outboxRepo := postgres.NewOutboxRepository(pool)
		txManager := postgres.NewTxManager(pool)
		return instance{submitter: app.NewWagerSubmitter(wallets, wagers, outboxRepo, txManager)}
	}

	instances := make([]instance, numInstances)
	for i := range instances {
		instances[i] = newInstance()
	}

	walletRepo := postgres.NewWalletRepository(testPool(t))
	balance, err := money.FromMinorUnits("BRL", 10000)
	require.NoError(t, err)
	w, err := wallet.New(uuid.New(), balance)
	require.NoError(t, err)
	require.NoError(t, walletRepo.Save(context.Background(), w))

	betAmount, err := money.FromMinorUnits("BRL", 8000)
	require.NoError(t, err)

	var wg sync.WaitGroup
	results := make([]*app.SubmitWagerTransactionResult, numInstances)
	errs := make([]error, numInstances)
	for i := 0; i < numInstances; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = instances[i].submitter.Submit(context.Background(), app.SubmitWagerTransactionInput{
				ProviderID: "provider-a", ExternalTransactionID: uuid.New().String(),
				PlayerID: w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
				Kind: wager.KindBet, Amount: betAmount,
				CorrelationID: "test-correlation-id",
			})
		}(i)
	}
	wg.Wait()

	// Only one 80.00 bet fits in a 100.00 wallet — same "one wins, the rest
	// lose" invariant as the two-instance mandatory scenario, just spread
	// across three independent instances instead of two goroutines in one.
	processed, rejected := 0, 0
	for i := 0; i < numInstances; i++ {
		require.NoError(t, errs[i], "instance %d", i)
		switch results[i].Transaction.Status() {
		case wager.TxStatusProcessed:
			processed++
		case wager.TxStatusRejected:
			rejected++
			assert.Equal(t, app.FailureCodeInsufficientBalance, results[i].Transaction.FailureCode())
		default:
			t.Fatalf("instance %d: unexpected status %s", i, results[i].Transaction.Status())
		}
	}
	assert.Equal(t, 1, processed)
	assert.Equal(t, numInstances-1, rejected)

	final, err := walletRepo.FindByID(context.Background(), w.ID())
	require.NoError(t, err)
	wantBalance, err := money.FromMinorUnits("BRL", 2000)
	require.NoError(t, err)
	assert.Equal(t, wantBalance, final.Balance())
}

// drainOutboxBacklog claims and marks published every currently-due PENDING
// entry — the integration suite never truncates the database between runs
// (every test here relies on that, using uuid.New() throughout to avoid
// collisions instead), so earlier tests' own outbox entries would otherwise
// still be sitting there PENDING and due, and FindDueForUpdate has no reason
// to skip them (a real OutboxWorker shouldn't skip old backlog either).
// Draining first is what a real OutboxWorker does anyway — repeatedly call
// RunOnce until nothing's left — so it's also the realistic setup, not just
// a test workaround.
func drainOutboxBacklog(t *testing.T, outboxRepo *postgres.OutboxRepository, txManager *postgres.TxManager) {
	t.Helper()
	const maxIterations = 10000
	for i := 0; i < maxIterations; i++ {
		var found bool
		err := txManager.WithinTx(context.Background(), func(ctx context.Context) error {
			e, err := outboxRepo.FindDueForUpdate(ctx)
			if err != nil {
				if errors.Is(err, app.ErrOutboxEntryNotFound) {
					return nil
				}
				return err
			}
			found = true
			e.MarkPublished()
			return outboxRepo.SaveAll(ctx, e)
		})
		require.NoError(t, err)
		if !found {
			return
		}
	}
	t.Fatalf("drainOutboxBacklog: backlog did not drain within %d iterations", maxIterations)
}

func TestOutboxRepositoryFindDueForUpdate(t *testing.T) {
	pool := testPool(t)
	outboxRepo := postgres.NewOutboxRepository(pool)
	txManager := postgres.NewTxManager(pool)
	drainOutboxBacklog(t, outboxRepo, txManager)

	entry, err := outbox.NewEntry(outbox.NewEntryInput{
		AggregateType: "Wallet", AggregateID: uuid.New(),
		EventType: "WalletBalanceChanged", Version: 1, Payload: []byte(`{}`), OccurredAt: time.Now(),
		CorrelationID: "test-correlation-id",
	})
	require.NoError(t, err)
	require.NoError(t, outboxRepo.SaveAll(context.Background(), entry))

	var claimed *outbox.Entry
	err = txManager.WithinTx(context.Background(), func(ctx context.Context) error {
		var err error
		claimed, err = outboxRepo.FindDueForUpdate(ctx)
		if err != nil {
			return err
		}
		claimed.MarkPublished()
		return outboxRepo.SaveAll(ctx, claimed)
	})
	require.NoError(t, err)
	assert.Equal(t, entry.ID(), claimed.ID())

	// Now PUBLISHED — must never be picked up again.
	err = txManager.WithinTx(context.Background(), func(ctx context.Context) error {
		_, err := outboxRepo.FindDueForUpdate(ctx)
		return err
	})
	assert.ErrorIs(t, err, app.ErrOutboxEntryNotFound)
}

// TestOutboxRepositoryFindDueForUpdateSkipsLockedRows proves the "múltiplos
// publishers" safety claim from TODO.md's Fase 8 against real Postgres, not
// just by reading the SQL: two goroutines each open their own transaction
// and call FindDueForUpdate concurrently against two pending entries — SKIP
// LOCKED must hand them one distinct entry each, never the same one twice.
func TestOutboxRepositoryFindDueForUpdateSkipsLockedRows(t *testing.T) {
	pool := testPool(t)
	outboxRepo := postgres.NewOutboxRepository(pool)
	txManager := postgres.NewTxManager(pool)
	drainOutboxBacklog(t, outboxRepo, txManager)

	var entries []*outbox.Entry
	for i := 0; i < 2; i++ {
		e, err := outbox.NewEntry(outbox.NewEntryInput{
			AggregateType: "Wallet", AggregateID: uuid.New(),
			EventType: "WalletBalanceChanged", Version: 1, Payload: []byte(`{}`), OccurredAt: time.Now(),
			CorrelationID: "test-correlation-id",
		})
		require.NoError(t, err)
		require.NoError(t, outboxRepo.SaveAll(context.Background(), e))
		entries = append(entries, e)
	}

	var wg sync.WaitGroup
	claimedIDs := make([]uuid.UUID, 2)
	claimedSignal := make(chan struct{}, 2)
	holdRelease := make(chan struct{})
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			_ = txManager.WithinTx(context.Background(), func(ctx context.Context) error {
				claimed, err := outboxRepo.FindDueForUpdate(ctx)
				if err != nil {
					return err
				}
				claimedIDs[i] = claimed.ID()
				claimedSignal <- struct{}{}
				<-holdRelease // keep the row lock open until both goroutines have claimed one
				claimed.MarkPublished()
				return outboxRepo.SaveAll(ctx, claimed)
			})
		}(i)
	}
	<-claimedSignal
	<-claimedSignal
	close(holdRelease)
	wg.Wait()

	assert.NotEqual(t, claimedIDs[0], claimedIDs[1])
	assert.ElementsMatch(t, []uuid.UUID{entries[0].ID(), entries[1].ID()}, claimedIDs)
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
		CorrelationID: "test-correlation-id",
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
		CorrelationID: "test-correlation-id",
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

func TestInboxRepositoryRoundTrip(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewInboxRepository(pool)

	hash := [32]byte{1, 2, 3, 4}
	entry, err := inbox.New(inbox.NewInput{
		ConsumerName: "wager-transactions-consumer", MessageID: uuid.New().String(), PayloadHash: hash,
	})
	require.NoError(t, err)
	require.NoError(t, repo.Save(context.Background(), entry))

	got, err := repo.FindByConsumerAndMessage(context.Background(), entry.ConsumerName(), entry.MessageID())
	require.NoError(t, err)
	assert.Equal(t, entry.ConsumerName(), got.ConsumerName())
	assert.Equal(t, entry.MessageID(), got.MessageID())
	assert.Equal(t, entry.PayloadHash(), got.PayloadHash())
	assert.False(t, got.IsCompleted())

	require.NoError(t, got.MarkCompleted())
	require.NoError(t, repo.Save(context.Background(), got))

	completed, err := repo.FindByConsumerAndMessage(context.Background(), entry.ConsumerName(), entry.MessageID())
	require.NoError(t, err)
	assert.True(t, completed.IsCompleted())

	_, err = repo.FindByConsumerAndMessage(context.Background(), "wager-transactions-consumer", "does-not-exist")
	assert.ErrorIs(t, err, app.ErrInboxEntryNotFound)
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
