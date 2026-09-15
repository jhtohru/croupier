package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wallet"
)

func mustMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.New("BRL", amount)
	require.NoError(t, err)
	return m
}

func mustWallet(t *testing.T, playerID uuid.UUID, balance money.Money) *wallet.Wallet {
	t.Helper()
	w, err := wallet.New(playerID, balance)
	require.NoError(t, err)
	return w
}

func doRequest(t *testing.T, srv *Server, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r = httptest.NewRequest(method, target, strings.NewReader(string(b)))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	return recordRequest(srv, r)
}

// newJSONRequest and recordRequest split doRequest's two steps apart for
// tests that need to set a header (e.g. Idempotency-Key) between building
// the request and sending it.
func newJSONRequest(t *testing.T, method, target string, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func recordRequest(srv *Server, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

func TestCreateWallet(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		playerID := uuid.New()
		w := mustWallet(t, playerID, mustMoney(t, "100.00"))
		srv := NewServer(Deps{WalletCreator: stubWalletCreator{wallet: w}})

		rec := doRequest(t, srv, http.MethodPost, "/wallets", createWalletRequest{
			PlayerID:       playerID,
			InitialBalance: mustMoney(t, "100.00"),
		})

		require.Equal(t, http.StatusCreated, rec.Code)
		var got walletResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, w.ID(), got.ID)
		assert.Equal(t, playerID, got.PlayerID)
	})

	t.Run("missing playerId", func(t *testing.T) {
		srv := NewServer(Deps{WalletCreator: stubWalletCreator{}})
		rec := doRequest(t, srv, http.MethodPost, "/wallets", createWalletRequest{
			InitialBalance: mustMoney(t, "0.00"),
		})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("malformed body", func(t *testing.T) {
		srv := NewServer(Deps{WalletCreator: stubWalletCreator{}})
		r := httptest.NewRequest(http.MethodPost, "/wallets", strings.NewReader(`{"playerId": not-json}`))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("already exists maps to 409", func(t *testing.T) {
		srv := NewServer(Deps{WalletCreator: stubWalletCreator{err: app.ErrWalletAlreadyExists}})
		rec := doRequest(t, srv, http.MethodPost, "/wallets", createWalletRequest{
			PlayerID:       uuid.New(),
			InitialBalance: mustMoney(t, "0.00"),
		})
		assert.Equal(t, http.StatusConflict, rec.Code)
	})

	t.Run("negative initial balance maps to 400", func(t *testing.T) {
		srv := NewServer(Deps{WalletCreator: stubWalletCreator{err: wallet.ErrNegativeInitialBalance}})
		rec := doRequest(t, srv, http.MethodPost, "/wallets", createWalletRequest{
			PlayerID:       uuid.New(),
			InitialBalance: mustMoney(t, "0.00"),
		})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestGetWallet(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		w := mustWallet(t, uuid.New(), mustMoney(t, "50.00"))
		srv := NewServer(Deps{WalletGetter: stubWalletGetter{wallet: w}})

		rec := doRequest(t, srv, http.MethodGet, "/wallets/"+w.ID().String(), nil)

		require.Equal(t, http.StatusOK, rec.Code)
		var got walletResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, w.ID(), got.ID)
		assert.Equal(t, w.Balance(), got.Balance)
	})

	t.Run("not found maps to 404", func(t *testing.T) {
		srv := NewServer(Deps{WalletGetter: stubWalletGetter{err: app.ErrWalletNotFound}})
		rec := doRequest(t, srv, http.MethodGet, "/wallets/"+uuid.New().String(), nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("invalid walletId maps to 400", func(t *testing.T) {
		srv := NewServer(Deps{WalletGetter: stubWalletGetter{}})
		rec := doRequest(t, srv, http.MethodGet, "/wallets/not-a-uuid", nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestReconcileWallet(t *testing.T) {
	walletID := uuid.New()
	result := &app.ReconciliationResult{
		WalletID:          walletID,
		StoredBalance:     mustMoney(t, "100.00"),
		CalculatedBalance: mustMoney(t, "100.00"),
		Difference:        mustMoney(t, "0.00"),
		Consistent:        true,
		EntriesChecked:    2,
	}
	srv := NewServer(Deps{WalletReconciler: stubWalletReconciler{result: result}})

	rec := doRequest(t, srv, http.MethodPost, "/wallets/"+walletID.String()+"/reconciliation", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	var got reconciliationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.True(t, got.Consistent)
	assert.Equal(t, 2, got.EntriesChecked)
}

func TestListWalletLedger(t *testing.T) {
	walletID := uuid.New()
	entry, err := wallet.NewLedgerEntry(wallet.NewLedgerEntryInput{
		WalletID:      walletID,
		TransactionID: uuid.New(),
		Direction:     wallet.DirectionCredit,
		Amount:        mustMoney(t, "10.00"),
		BalanceBefore: mustMoney(t, "0.00"),
		BalanceAfter:  mustMoney(t, "10.00"),
	})
	require.NoError(t, err)

	t.Run("default pagination", func(t *testing.T) {
		lister := &stubWalletLedgerLister{entries: []*wallet.LedgerEntry{entry}}
		srv := NewServer(Deps{WalletLedgerLister: lister})

		rec := doRequest(t, srv, http.MethodGet, "/wallets/"+walletID.String()+"/ledger", nil)

		require.Equal(t, http.StatusOK, rec.Code)
		var got listLedgerResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Len(t, got.Entries, 1)
		assert.Equal(t, entry.ID(), got.Entries[0].ID)
		assert.Nil(t, lister.gotInput.Cursor)
		assert.Equal(t, 0, lister.gotInput.Limit)
	})

	t.Run("cursor and limit forwarded", func(t *testing.T) {
		cursor := uuid.New()
		lister := &stubWalletLedgerLister{}
		srv := NewServer(Deps{WalletLedgerLister: lister})

		rec := doRequest(t, srv, http.MethodGet, "/wallets/"+walletID.String()+"/ledger?cursor="+cursor.String()+"&limit=10", nil)

		require.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, lister.gotInput.Cursor)
		assert.Equal(t, cursor, *lister.gotInput.Cursor)
		assert.Equal(t, 10, lister.gotInput.Limit)
	})

	t.Run("invalid cursor maps to 400", func(t *testing.T) {
		lister := &stubWalletLedgerLister{}
		srv := NewServer(Deps{WalletLedgerLister: lister})
		rec := doRequest(t, srv, http.MethodGet, "/wallets/"+walletID.String()+"/ledger?cursor=not-a-uuid", nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("invalid limit maps to 400", func(t *testing.T) {
		lister := &stubWalletLedgerLister{}
		srv := NewServer(Deps{WalletLedgerLister: lister})
		rec := doRequest(t, srv, http.MethodGet, "/wallets/"+walletID.String()+"/ledger?limit=-1", nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}
