package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestWalletRoutesRequireInternalRole(t *testing.T) {
	t.Run("missing Authorization header maps to 401", func(t *testing.T) {
		srv := NewServer(Deps{Auth: internalAuth(), WalletCreator: stubWalletCreator{}})
		r := httptest.NewRequest(http.MethodPost, "/wallets", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("invalid token maps to 401", func(t *testing.T) {
		srv := NewServer(Deps{Auth: stubTokenVerifier{err: errors.New("bad signature")}, WalletCreator: stubWalletCreator{}})
		rec := doRequest(t, srv, http.MethodPost, "/wallets", createWalletRequest{})
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("valid token without internal-service role maps to 403", func(t *testing.T) {
		srv := NewServer(Deps{Auth: providerAuth("provider-a"), WalletCreator: stubWalletCreator{}})
		rec := doRequest(t, srv, http.MethodPost, "/wallets", createWalletRequest{})
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
}

func TestWageringRoutesRequireAuth(t *testing.T) {
	t.Run("missing Authorization header maps to 401", func(t *testing.T) {
		srv := NewServer(Deps{Auth: internalAuth(), WagerSubmitter: &stubWagerSubmitter{}})
		r := httptest.NewRequest(http.MethodPost, "/wagering/transactions", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("provider token is enough on POST /wagering/transactions, no internal-service role needed", func(t *testing.T) {
		srv := NewServer(Deps{Auth: providerAuth("provider-a"), WagerSubmitter: &stubWagerSubmitter{}})
		r := newJSONRequest(t, http.MethodPost, "/wagering/transactions", []byte("not json"))
		rec := recordRequest(srv, r)
		// Reaches the handler (400 for the malformed body) instead of being
		// stopped at 401/403 by the middleware — proves requireAuth, not
		// requireInternalRole, gates this route.
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("GET /wagering/transactions/{id} is internal-only, provider token maps to 403", func(t *testing.T) {
		srv := NewServer(Deps{Auth: providerAuth("provider-a"), WagerTransactionGetter: stubWagerTransactionGetter{}})
		rec := doRequest(t, srv, http.MethodGet, "/wagering/transactions/"+uuid.New().String(), nil)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
}
