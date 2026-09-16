package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/wager"
)

func mustTransaction(t *testing.T, input wager.NewTransactionInput) *wager.Transaction {
	t.Helper()
	tx, err := wager.NewTransaction(input)
	require.NoError(t, err)
	return tx
}

func betInput(t *testing.T) wager.NewTransactionInput {
	t.Helper()
	return wager.NewTransactionInput{
		ProviderID: "provider-a", ExternalTransactionID: "ext-1",
		PlayerID: uuid.New(), WalletID: uuid.New(), RoundID: "round-1", GameID: "game-1",
		Kind: wager.KindBet, Amount: mustMoney(t, "80.00"),
	}
}

// requestFromTx builds a submitWagerTransactionRequest matching tx — the
// request type itself has no providerId field (it comes from the caller's
// token, see submitWagerTransaction), so there's nothing to set for it here.
func requestFromTx(tx *wager.Transaction) submitWagerTransactionRequest {
	return submitWagerTransactionRequest{
		ExternalTransactionID: tx.ExternalTransactionID(),
		PlayerID:              tx.PlayerID(),
		WalletID:              tx.WalletID(),
		RoundID:               tx.RoundID(),
		GameID:                tx.GameID(),
		Kind:                  tx.Kind(),
		Money:                 tx.Amount(),
	}
}

// doWagerRequest builds a POST /wagering/transactions request with a valid
// (matching) Idempotency-Key header set — that header is mandatory (spec
// §9), so every test exercising a path past that check needs one; tests
// about the header itself (below) build the request manually instead.
func doWagerRequest(t *testing.T, srv *Server, req submitWagerTransactionRequest, providerID string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(req)
	require.NoError(t, err)
	r := newJSONRequest(t, http.MethodPost, "/wagering/transactions", b)
	r.Header.Set("Idempotency-Key", providerID+":"+req.ExternalTransactionID)
	return recordRequest(srv, r)
}

func TestSubmitWagerTransaction(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		tx := mustTransaction(t, betInput(t))
		submitter := &stubWagerSubmitter{result: &app.SubmitWagerTransactionResult{
			Transaction: tx, Balance: mustMoney(t, "20.00"),
		}}
		srv := NewServer(Deps{Auth: providerAuth(tx.ProviderID()), WagerSubmitter: submitter})

		rec := doWagerRequest(t, srv, requestFromTx(tx), tx.ProviderID())

		require.Equal(t, http.StatusOK, rec.Code)
		var got submitWagerTransactionResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, tx.ID(), got.TransactionID)
		assert.Equal(t, tx.Status(), got.Status)
		// providerId came from the token, not the request body — there's no
		// field for it in submitWagerTransactionRequest at all.
		assert.Equal(t, tx.ProviderID(), submitter.gotInput.ProviderID)
	})

	t.Run("business rejection includes failureCode in the response body", func(t *testing.T) {
		// Challenge spec §7.36: "Toda rejeição deve fornecer um failureCode
		// estável e documentado" — the submission response (not just the
		// separate GET) must carry it.
		tx := mustTransaction(t, betInput(t))
		require.NoError(t, tx.MarkRejected(wager.FailureCode("INSUFFICIENT_BALANCE")))
		submitter := &stubWagerSubmitter{result: &app.SubmitWagerTransactionResult{
			Transaction: tx, Balance: mustMoney(t, "20.00"),
		}}
		srv := NewServer(Deps{Auth: providerAuth(tx.ProviderID()), WagerSubmitter: submitter})

		rec := doWagerRequest(t, srv, requestFromTx(tx), tx.ProviderID())

		require.Equal(t, http.StatusOK, rec.Code)
		var got submitWagerTransactionResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, wager.TxStatusRejected, got.Status)
		assert.Equal(t, wager.FailureCode("INSUFFICIENT_BALANCE"), got.FailureCode)
	})

	t.Run("missing Idempotency-Key maps to 400", func(t *testing.T) {
		submitter := &stubWagerSubmitter{}
		srv := NewServer(Deps{Auth: providerAuth("provider-a"), WagerSubmitter: submitter})

		rec := doRequest(t, srv, http.MethodPost, "/wagering/transactions", submitWagerTransactionRequest{
			ExternalTransactionID: "ext-1",
			PlayerID:              uuid.New(), WalletID: uuid.New(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Money: mustMoney(t, "80.00"),
		})

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("Idempotency-Key mismatch maps to 400", func(t *testing.T) {
		submitter := &stubWagerSubmitter{}
		srv := NewServer(Deps{Auth: providerAuth("provider-a"), WagerSubmitter: submitter})

		b, err := json.Marshal(submitWagerTransactionRequest{
			ExternalTransactionID: "ext-1",
			PlayerID:              uuid.New(), WalletID: uuid.New(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Money: mustMoney(t, "80.00"),
		})
		require.NoError(t, err)
		r := newJSONRequest(t, http.MethodPost, "/wagering/transactions", b)
		r.Header.Set("Idempotency-Key", "provider-a:some-other-id")
		rec := recordRequest(srv, r)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("matching Idempotency-Key is accepted", func(t *testing.T) {
		tx := mustTransaction(t, betInput(t))
		submitter := &stubWagerSubmitter{result: &app.SubmitWagerTransactionResult{Transaction: tx, Balance: mustMoney(t, "20.00")}}
		srv := NewServer(Deps{Auth: providerAuth(tx.ProviderID()), WagerSubmitter: submitter})

		rec := doWagerRequest(t, srv, requestFromTx(tx), tx.ProviderID())

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("idempotency conflict maps to 409", func(t *testing.T) {
		submitter := &stubWagerSubmitter{err: app.ErrIdempotencyConflict}
		srv := NewServer(Deps{Auth: providerAuth("provider-a"), WagerSubmitter: submitter})
		rec := doWagerRequest(t, srv, submitWagerTransactionRequest{
			ExternalTransactionID: "ext-1",
			PlayerID:              uuid.New(), WalletID: uuid.New(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Money: mustMoney(t, "80.00"),
		}, "provider-a")
		assert.Equal(t, http.StatusConflict, rec.Code)
	})

	t.Run("invalid kind maps to 400", func(t *testing.T) {
		submitter := &stubWagerSubmitter{err: wager.ErrInvalidKind}
		srv := NewServer(Deps{Auth: providerAuth("provider-a"), WagerSubmitter: submitter})
		rec := doWagerRequest(t, srv, submitWagerTransactionRequest{
			ExternalTransactionID: "ext-1",
			PlayerID:              uuid.New(), WalletID: uuid.New(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindOpening, Money: mustMoney(t, "80.00"),
		}, "provider-a")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestGetWagerTransaction(t *testing.T) {
	tx := mustTransaction(t, betInput(t))

	t.Run("by internal id, internal-service only", func(t *testing.T) {
		srv := NewServer(Deps{Auth: internalAuth(), WagerTransactionGetter: stubWagerTransactionGetter{tx: tx}})
		rec := doRequest(t, srv, http.MethodGet, "/wagering/transactions/"+tx.ID().String(), nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var got wagerTransactionResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, tx.ID(), got.ID)
	})

	t.Run("not found maps to 404", func(t *testing.T) {
		srv := NewServer(Deps{Auth: internalAuth(), WagerTransactionGetter: stubWagerTransactionGetter{err: app.ErrWagerTransactionNotFound}})
		rec := doRequest(t, srv, http.MethodGet, "/wagering/transactions/"+uuid.New().String(), nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("by provider and external id, own provider", func(t *testing.T) {
		srv := NewServer(Deps{Auth: providerAuth(tx.ProviderID()), WagerTransactionGetter: stubWagerTransactionGetter{tx: tx}})
		rec := doRequest(t, srv, http.MethodGet, "/providers/"+tx.ProviderID()+"/wagering/transactions/"+tx.ExternalTransactionID(), nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var got wagerTransactionResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, tx.ID(), got.ID)
	})

	t.Run("by provider and external id, another provider's path maps to 403", func(t *testing.T) {
		srv := NewServer(Deps{Auth: providerAuth("provider-b"), WagerTransactionGetter: stubWagerTransactionGetter{tx: tx}})
		rec := doRequest(t, srv, http.MethodGet, "/providers/"+tx.ProviderID()+"/wagering/transactions/"+tx.ExternalTransactionID(), nil)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("internal-service may look up any provider's transaction", func(t *testing.T) {
		srv := NewServer(Deps{Auth: internalAuth(), WagerTransactionGetter: stubWagerTransactionGetter{tx: tx}})
		rec := doRequest(t, srv, http.MethodGet, "/providers/"+tx.ProviderID()+"/wagering/transactions/"+tx.ExternalTransactionID(), nil)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}
