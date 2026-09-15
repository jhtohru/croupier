package httpapi

import (
	"encoding/json"
	"net/http"
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

func TestSubmitWagerTransaction(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		tx := mustTransaction(t, betInput(t))
		submitter := &stubWagerSubmitter{result: &app.SubmitWagerTransactionResult{
			Transaction: tx, Balance: mustMoney(t, "20.00"),
		}}
		srv := NewServer(Deps{WagerSubmitter: submitter})

		body := submitWagerTransactionRequest{
			ProviderID: tx.ProviderID(), ExternalTransactionID: tx.ExternalTransactionID(),
			PlayerID: tx.PlayerID(), WalletID: tx.WalletID(), RoundID: tx.RoundID(), GameID: tx.GameID(),
			Kind: tx.Kind(), Amount: tx.Amount(),
		}
		rec := doRequest(t, srv, http.MethodPost, "/wagering/transactions", body)

		require.Equal(t, http.StatusOK, rec.Code)
		var got submitWagerTransactionResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, tx.ID(), got.Transaction.ID)
		assert.Equal(t, tx.ProviderID(), submitter.gotInput.ProviderID)
	})

	t.Run("Idempotency-Key mismatch maps to 400", func(t *testing.T) {
		submitter := &stubWagerSubmitter{}
		srv := NewServer(Deps{WagerSubmitter: submitter})

		b, err := json.Marshal(submitWagerTransactionRequest{
			ProviderID: "provider-a", ExternalTransactionID: "ext-1",
			PlayerID: uuid.New(), WalletID: uuid.New(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustMoney(t, "80.00"),
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
		srv := NewServer(Deps{WagerSubmitter: submitter})

		b, err := json.Marshal(submitWagerTransactionRequest{
			ProviderID: tx.ProviderID(), ExternalTransactionID: tx.ExternalTransactionID(),
			PlayerID: tx.PlayerID(), WalletID: tx.WalletID(), RoundID: tx.RoundID(), GameID: tx.GameID(),
			Kind: tx.Kind(), Amount: tx.Amount(),
		})
		require.NoError(t, err)
		r := newJSONRequest(t, http.MethodPost, "/wagering/transactions", b)
		r.Header.Set("Idempotency-Key", tx.ProviderID()+":"+tx.ExternalTransactionID())
		rec := recordRequest(srv, r)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("idempotency conflict maps to 409", func(t *testing.T) {
		submitter := &stubWagerSubmitter{err: app.ErrIdempotencyConflict}
		srv := NewServer(Deps{WagerSubmitter: submitter})
		rec := doRequest(t, srv, http.MethodPost, "/wagering/transactions", submitWagerTransactionRequest{
			ProviderID: "provider-a", ExternalTransactionID: "ext-1",
			PlayerID: uuid.New(), WalletID: uuid.New(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Amount: mustMoney(t, "80.00"),
		})
		assert.Equal(t, http.StatusConflict, rec.Code)
	})

	t.Run("invalid kind maps to 400", func(t *testing.T) {
		submitter := &stubWagerSubmitter{err: wager.ErrInvalidKind}
		srv := NewServer(Deps{WagerSubmitter: submitter})
		rec := doRequest(t, srv, http.MethodPost, "/wagering/transactions", submitWagerTransactionRequest{
			ProviderID: "provider-a", ExternalTransactionID: "ext-1",
			PlayerID: uuid.New(), WalletID: uuid.New(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindOpening, Amount: mustMoney(t, "80.00"),
		})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestGetWagerTransaction(t *testing.T) {
	tx := mustTransaction(t, betInput(t))

	t.Run("by internal id", func(t *testing.T) {
		srv := NewServer(Deps{WagerTransactionGetter: stubWagerTransactionGetter{tx: tx}})
		rec := doRequest(t, srv, http.MethodGet, "/wagering/transactions/"+tx.ID().String(), nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var got wagerTransactionResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, tx.ID(), got.ID)
	})

	t.Run("not found maps to 404", func(t *testing.T) {
		srv := NewServer(Deps{WagerTransactionGetter: stubWagerTransactionGetter{err: app.ErrWagerTransactionNotFound}})
		rec := doRequest(t, srv, http.MethodGet, "/wagering/transactions/"+uuid.New().String(), nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("by provider and external id", func(t *testing.T) {
		srv := NewServer(Deps{WagerTransactionGetter: stubWagerTransactionGetter{tx: tx}})
		rec := doRequest(t, srv, http.MethodGet, "/providers/"+tx.ProviderID()+"/wagering/transactions/"+tx.ExternalTransactionID(), nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var got wagerTransactionResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, tx.ID(), got.ID)
	})
}
