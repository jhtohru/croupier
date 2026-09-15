//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/postgres"
	"github.com/jhtohru/croupier/internal/wager"
)

// testServer wires a real Postgres-backed Server exactly the way cmd/croupier
// (Fase 10) eventually will, minus auth (Fase 9) — see the limitation noted
// on NewServer. No mocks anywhere in this file: same real-infrastructure
// bar as internal/postgres/integration_test.go.
func testServer(t *testing.T) *Server {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://croupier:croupier@localhost:5432/croupier?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Ping(context.Background()))

	wallets := postgres.NewWalletRepository(pool)
	wagers := postgres.NewWagerRepository(pool)
	outbox := postgres.NewOutboxRepository(pool)
	txManager := postgres.NewTxManager(pool)

	return NewServer(Deps{
		WalletCreator:          app.NewWalletCreator(wallets, wagers, outbox, txManager),
		WalletGetter:           app.NewWalletGetter(wallets),
		WalletReconciler:       app.NewWalletReconciler(wallets),
		WalletLedgerLister:     app.NewWalletLedgerLister(wallets),
		WagerSubmitter:         app.NewWagerSubmitter(wallets, wagers, outbox, txManager),
		WagerTransactionGetter: app.NewWagerTransactionGetter(wagers),
		Ready:                  func(ctx context.Context) error { return pool.Ping(ctx) },
	})
}

// TestWagerLifecycleOverHTTP exercises the whole HTTP surface against real
// Postgres end to end: create a wallet with an opening balance, submit a
// winning BET/WIN pair over the same routes a real provider would use, then
// confirm the wallet, the ledger, and reconciliation all agree — proving the
// httpapi layer wires into internal/app/internal/postgres correctly, not
// just that each layer works in isolation.
func TestWagerLifecycleOverHTTP(t *testing.T) {
	srv := testServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/health/ready", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	playerID := uuid.New()
	rec = doRequest(t, srv, http.MethodPost, "/wallets", createWalletRequest{
		PlayerID:       playerID,
		InitialBalance: mustMoney(t, "100.00"),
	})
	require.Equal(t, http.StatusCreated, rec.Code)
	var created walletResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	rec = doRequest(t, srv, http.MethodGet, "/wallets/"+created.ID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code)

	providerID := "provider-" + uuid.New().String()
	betExtID := "bet-" + uuid.New().String()
	rec = doRequest(t, srv, http.MethodPost, "/wagering/transactions", submitWagerTransactionRequest{
		ProviderID: providerID, ExternalTransactionID: betExtID,
		PlayerID: playerID, WalletID: created.ID, RoundID: "round-1", GameID: "game-1",
		Kind: wager.KindBet, Amount: mustMoney(t, "30.00"),
	})
	require.Equal(t, http.StatusOK, rec.Code)
	var betResp submitWagerTransactionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &betResp))
	assert.Equal(t, wager.TxStatusProcessed, betResp.Transaction.Status)
	assert.Equal(t, mustMoney(t, "70.00"), betResp.Balance)

	// Idempotent replay of the exact same submission must not move the
	// balance again — same content, same key, same observed result.
	rec = doRequest(t, srv, http.MethodPost, "/wagering/transactions", submitWagerTransactionRequest{
		ProviderID: providerID, ExternalTransactionID: betExtID,
		PlayerID: playerID, WalletID: created.ID, RoundID: "round-1", GameID: "game-1",
		Kind: wager.KindBet, Amount: mustMoney(t, "30.00"),
	})
	require.Equal(t, http.StatusOK, rec.Code)
	var replayResp submitWagerTransactionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &replayResp))
	assert.True(t, replayResp.IdempotentReplay)
	assert.Equal(t, mustMoney(t, "70.00"), replayResp.Balance)

	winExtID := "win-" + uuid.New().String()
	rec = doRequest(t, srv, http.MethodPost, "/wagering/transactions", submitWagerTransactionRequest{
		ProviderID: providerID, ExternalTransactionID: winExtID,
		PlayerID: playerID, WalletID: created.ID, RoundID: "round-1", GameID: "game-1",
		Kind: wager.KindWin, Amount: mustMoney(t, "50.00"),
	})
	require.Equal(t, http.StatusOK, rec.Code)
	var winResp submitWagerTransactionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &winResp))
	assert.Equal(t, mustMoney(t, "120.00"), winResp.Balance)

	rec = doRequest(t, srv, http.MethodGet, "/wagering/transactions/"+betResp.Transaction.ID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = doRequest(t, srv, http.MethodGet, "/providers/"+providerID+"/wagering/transactions/"+winExtID, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var byProvider wagerTransactionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &byProvider))
	assert.Equal(t, winResp.Transaction.ID, byProvider.ID)

	rec = doRequest(t, srv, http.MethodGet, "/wallets/"+created.ID.String()+"/ledger", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var ledger listLedgerResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ledger))
	// OPENING credit + BET debit + WIN credit — the idempotent replay adds
	// no fourth entry.
	assert.Len(t, ledger.Entries, 3)

	rec = doRequest(t, srv, http.MethodPost, "/wallets/"+created.ID.String()+"/reconciliation", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var recon reconciliationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &recon))
	assert.True(t, recon.Consistent)
	assert.Equal(t, mustMoney(t, "120.00"), recon.StoredBalance)
}
