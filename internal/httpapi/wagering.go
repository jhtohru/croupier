package httpapi

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wager"
)

type wagerTransactionResponse struct {
	ID                             uuid.UUID         `json:"id"`
	Status                         wager.TxStatus    `json:"status"`
	Kind                           wager.Kind        `json:"kind"`
	ProviderID                     string            `json:"providerId,omitempty"`
	ExternalTransactionID          string            `json:"externalTransactionId,omitempty"`
	PlayerID                       uuid.UUID         `json:"playerId"`
	WalletID                       uuid.UUID         `json:"walletId"`
	RoundID                        string            `json:"roundId,omitempty"`
	GameID                         string            `json:"gameId,omitempty"`
	Amount                         money.Money       `json:"amount"`
	ReferenceExternalTransactionID *string           `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         *uuid.UUID        `json:"referenceTransactionId,omitempty"`
	FailureCode                    wager.FailureCode `json:"failureCode,omitempty"`
	CreatedAt                      string            `json:"createdAt"`
	UpdatedAt                      string            `json:"updatedAt"`
}

func newWagerTransactionResponse(tx *wager.Transaction) wagerTransactionResponse {
	return wagerTransactionResponse{
		ID:                             tx.ID(),
		Status:                         tx.Status(),
		Kind:                           tx.Kind(),
		ProviderID:                     tx.ProviderID(),
		ExternalTransactionID:          tx.ExternalTransactionID(),
		PlayerID:                       tx.PlayerID(),
		WalletID:                       tx.WalletID(),
		RoundID:                        tx.RoundID(),
		GameID:                         tx.GameID(),
		Amount:                         tx.Amount(),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		ReferenceTransactionID:         tx.ReferenceTransactionID(),
		FailureCode:                    tx.FailureCode(),
		CreatedAt:                      tx.CreatedAt().Format(timeFormat),
		UpdatedAt:                      tx.UpdatedAt().Format(timeFormat),
	}
}

type submitWagerTransactionRequest struct {
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	PlayerID                       uuid.UUID   `json:"playerId"`
	WalletID                       uuid.UUID   `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           wager.Kind  `json:"kind"`
	Amount                         money.Money `json:"amount"`
	ReferenceExternalTransactionID *string     `json:"referenceExternalTransactionId,omitempty"`
}

type submitWagerTransactionResponse struct {
	Transaction      wagerTransactionResponse `json:"transaction"`
	Balance          money.Money              `json:"balance"`
	IdempotentReplay bool                     `json:"idempotentReplay"`
}

// submitWagerTransaction is the shared HTTP entry point for provider-
// submitted wagering events (Fase 8's SQS consumer feeds the same
// app.WagerSubmitter.Submit with the same input struct, so both paths carry
// identical idempotency/business-rule guarantees per ARCHITECTURE.md).
//
// The Idempotency-Key header, when present, is cross-checked against
// providerId:externalTransactionId from the body — this is a client-facing
// consistency check, not the idempotency mechanism itself (Submit already
// derives its own key from those two body fields regardless of any header;
// see the Fase 5 note in TODO.md). The header is optional: its absence
// doesn't weaken idempotency, only loses this extra check.
func (h *handler) submitWagerTransaction(w http.ResponseWriter, r *http.Request) {
	var req submitWagerTransactionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "malformed request body"})
		return
	}

	if key := r.Header.Get("Idempotency-Key"); key != "" {
		if want := req.ProviderID + ":" + req.ExternalTransactionID; key != want {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "Idempotency-Key does not match providerId:externalTransactionId"})
			return
		}
	}

	result, err := h.deps.WagerSubmitter.Submit(r.Context(), app.SubmitWagerTransactionInput{
		ProviderID:                     req.ProviderID,
		ExternalTransactionID:          req.ExternalTransactionID,
		PlayerID:                       req.PlayerID,
		WalletID:                       req.WalletID,
		RoundID:                        req.RoundID,
		GameID:                         req.GameID,
		Kind:                           req.Kind,
		Amount:                         req.Amount,
		ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, submitWagerTransactionResponse{
		Transaction:      newWagerTransactionResponse(result.Transaction),
		Balance:          result.Balance,
		IdempotentReplay: result.IdempotentReplay,
	})
}

// getWagerTransaction looks a transaction up by its internal id — not
// provider-scoped, matching app.WagerTransactionGetter.Get's own contract
// (providers never see internal ids). Intended for internal/operator use,
// not exposed to providers.
func (h *handler) getWagerTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("transactionId"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid transactionId"})
		return
	}
	tx, err := h.deps.WagerTransactionGetter.Get(r.Context(), id)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, newWagerTransactionResponse(tx))
}

// getWagerTransactionByProvider is the provider-facing lookup route.
// providerId comes straight from the URL, not from an authenticated identity
// — see the limitation noted on Server.NewServer and in ARCHITECTURE.md.
// Fase 9's auth middleware must set providerId here from the caller's own
// verified identity before this route can be trusted to isolate providers
// from each other.
func (h *handler) getWagerTransactionByProvider(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("providerId")
	externalTransactionID := r.PathValue("externalTransactionId")
	if strings.TrimSpace(providerID) == "" || strings.TrimSpace(externalTransactionID) == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "providerId and externalTransactionId are required"})
		return
	}
	tx, err := h.deps.WagerTransactionGetter.GetByProvider(r.Context(), providerID, externalTransactionID)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, newWagerTransactionResponse(tx))
}
