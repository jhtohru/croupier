//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/auth"
	"github.com/jhtohru/croupier/internal/postgres"
	"github.com/jhtohru/croupier/internal/wager"
)

func keycloakIssuerURL() string {
	if v := os.Getenv("KEYCLOAK_ISSUER_URL"); v != "" {
		return v
	}
	return "http://localhost:8080/realms/croupier"
}

// fetchRealToken performs a real client_credentials grant against the real
// Keycloak realm from deploy/keycloak/realm-export.json — no mock IdP
// anywhere in this file, same bar as the rest of this test.
func fetchRealToken(t *testing.T, clientID, clientSecret string) string {
	t.Helper()
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	resp, err := http.PostForm(keycloakIssuerURL()+"/protocol/openid-connect/token", form)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEmpty(t, body.AccessToken)
	return body.AccessToken
}

// testServer wires a real Postgres-backed, real-Keycloak-verified Server
// exactly the way cmd/croupier (Fase 10) eventually will. No mocks anywhere
// in this file: same real-infrastructure bar as internal/postgres's and
// internal/sqs's integration tests.
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	verifier, err := auth.NewVerifier(ctx, keycloakIssuerURL())
	require.NoError(t, err)

	return NewServer(Deps{
		WalletCreator:          app.NewWalletCreator(wallets, wagers, outbox, txManager),
		WalletGetter:           app.NewWalletGetter(wallets),
		WalletReconciler:       app.NewWalletReconciler(wallets, txManager),
		WalletLedgerLister:     app.NewWalletLedgerLister(wallets),
		WagerSubmitter:         app.NewWagerSubmitter(wallets, wagers, outbox, txManager),
		WagerTransactionGetter: app.NewWagerTransactionGetter(wagers),
		Ready:                  func(ctx context.Context) error { return pool.Ping(ctx) },
		Auth:                   verifier,
	})
}

// doAuthed and doRequestNoAuth build a request against a real Keycloak
// token (or no token at all) — doRequest (from wallets_test.go) always
// attaches the fake stubTokenVerifier's testBearerToken, which is no good
// here since this file verifies against a real IdP.
func doAuthed(t *testing.T, srv *Server, token, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := newBareRequest(t, method, target, body)
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

func doRequestNoAuth(t *testing.T, srv *Server, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := newBareRequest(t, method, target, body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

// doWagerSubmission is doAuthed specialized for POST /wagering/transactions,
// which requires an Idempotency-Key header (challenge spec §9) — providerID
// here is the identity behind token, needed to build the expected
// "{providerId}:{externalTransactionId}" value.
func doWagerSubmission(t *testing.T, srv *Server, token, providerID string, req submitWagerTransactionRequest) *httptest.ResponseRecorder {
	t.Helper()
	r := newBareRequest(t, http.MethodPost, "/wagering/transactions", req)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Idempotency-Key", providerID+":"+req.ExternalTransactionID)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

func newBareRequest(t *testing.T, method, target string, body any) *http.Request {
	t.Helper()
	if body == nil {
		return httptest.NewRequest(method, target, nil)
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	return httptest.NewRequest(method, target, strings.NewReader(string(b)))
}

// TestWagerLifecycleOverHTTP exercises the whole HTTP surface against real
// Postgres and a real Keycloak-issued token end to end: create a wallet
// (internal-service), submit a winning BET/WIN pair as a real provider
// would (provider-a's own client_credentials token), then confirm the
// wallet, the ledger, and reconciliation all agree — proving httpapi wires
// into internal/app, internal/postgres and internal/auth correctly
// together, not just that each layer works in isolation.
func TestWagerLifecycleOverHTTP(t *testing.T) {
	srv := testServer(t)
	internalToken := fetchRealToken(t, "internal-service", "internal-service-secret")
	providerToken := fetchRealToken(t, "provider-a", "provider-a-secret")
	const providerID = "provider-a"

	rec := doAuthed(t, srv, internalToken, http.MethodGet, "/health/ready", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	playerID := uuid.New()
	rec = doAuthed(t, srv, internalToken, http.MethodPost, "/wallets", createWalletRequest{
		PlayerID:       playerID,
		InitialBalance: mustMoney(t, "100.00"),
	})
	require.Equal(t, http.StatusCreated, rec.Code)
	var created walletResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	rec = doAuthed(t, srv, internalToken, http.MethodGet, "/wallets/"+created.ID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code)

	// A provider token must never reach a wallet route, even a read one.
	rec = doAuthed(t, srv, providerToken, http.MethodGet, "/wallets/"+created.ID.String(), nil)
	require.Equal(t, http.StatusForbidden, rec.Code)

	betExtID := "bet-" + uuid.New().String()
	rec = doWagerSubmission(t, srv, providerToken, providerID, submitWagerTransactionRequest{
		ExternalTransactionID: betExtID,
		PlayerID:              playerID, WalletID: created.ID, RoundID: "round-1", GameID: "game-1",
		Kind: wager.KindBet, Money: mustMoney(t, "30.00"),
	})
	require.Equal(t, http.StatusOK, rec.Code)
	var betResp submitWagerTransactionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &betResp))
	assert.Equal(t, wager.TxStatusProcessed, betResp.Status)
	assert.Equal(t, mustMoney(t, "70.00"), betResp.Balance)

	// providerId isn't in the flat submission response (spec §9's example
	// shape has no room for it) — confirmed via a lookup instead, which does
	// carry it, straight from the token, never a body field.
	rec = doAuthed(t, srv, internalToken, http.MethodGet, "/wagering/transactions/"+betResp.TransactionID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var betTx wagerTransactionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &betTx))
	assert.Equal(t, providerID, betTx.ProviderID)

	// Idempotent replay of the exact same submission must not move the
	// balance again — same content, same key, same observed result.
	rec = doWagerSubmission(t, srv, providerToken, providerID, submitWagerTransactionRequest{
		ExternalTransactionID: betExtID,
		PlayerID:              playerID, WalletID: created.ID, RoundID: "round-1", GameID: "game-1",
		Kind: wager.KindBet, Money: mustMoney(t, "30.00"),
	})
	require.Equal(t, http.StatusOK, rec.Code)
	var replayResp submitWagerTransactionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &replayResp))
	assert.True(t, replayResp.IdempotentReplay)
	assert.Equal(t, mustMoney(t, "70.00"), replayResp.Balance)

	winExtID := "win-" + uuid.New().String()
	rec = doWagerSubmission(t, srv, providerToken, providerID, submitWagerTransactionRequest{
		ExternalTransactionID: winExtID,
		PlayerID:              playerID, WalletID: created.ID, RoundID: "round-1", GameID: "game-1",
		Kind: wager.KindWin, Money: mustMoney(t, "50.00"),
	})
	require.Equal(t, http.StatusOK, rec.Code)
	var winResp submitWagerTransactionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &winResp))
	assert.Equal(t, mustMoney(t, "120.00"), winResp.Balance)

	// Internal id lookup is internal-service-only.
	rec = doAuthed(t, srv, internalToken, http.MethodGet, "/wagering/transactions/"+betResp.TransactionID.String(), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	rec = doAuthed(t, srv, providerToken, http.MethodGet, "/wagering/transactions/"+betResp.TransactionID.String(), nil)
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Provider-facing lookup: provider-a may read its own transaction...
	rec = doAuthed(t, srv, providerToken, http.MethodGet, "/providers/"+providerID+"/wagering/transactions/"+winExtID, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var byProvider wagerTransactionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &byProvider))
	assert.Equal(t, winResp.TransactionID, byProvider.ID)

	// ...but provider-b, a real, distinct authenticated identity, may not.
	providerBToken := fetchRealToken(t, "provider-b", "provider-b-secret")
	rec = doAuthed(t, srv, providerBToken, http.MethodGet, "/providers/"+providerID+"/wagering/transactions/"+winExtID, nil)
	require.Equal(t, http.StatusForbidden, rec.Code)

	rec = doAuthed(t, srv, internalToken, http.MethodGet, "/wallets/"+created.ID.String()+"/ledger", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var ledger listLedgerResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ledger))
	// OPENING credit + BET debit + WIN credit — the idempotent replay adds
	// no fourth entry.
	assert.Len(t, ledger.Entries, 3)

	rec = doAuthed(t, srv, internalToken, http.MethodPost, "/wallets/"+created.ID.String()+"/reconciliation", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var recon reconciliationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &recon))
	assert.True(t, recon.Consistent)
	assert.Equal(t, mustMoney(t, "120.00"), recon.StoredBalance)

	// No token at all is rejected outright.
	rec = doRequestNoAuth(t, srv, http.MethodGet, "/wallets/"+created.ID.String(), nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
