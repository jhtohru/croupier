package httpapi

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/auth"
	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wager"
)

// wagerOutcome classifies a submission result for logs/metrics — mirrors
// internal/sqs's identical helper (kept separate rather than shared, same
// reasoning as this package's own copy of the request/response DTOs: no
// reason for either package to depend on the other for three lines of
// logic).
func wagerOutcome(result *app.SubmitWagerTransactionResult) string {
	if result.IdempotentReplay {
		return "replay"
	}
	if result.Transaction == nil {
		return "unknown"
	}
	return strings.ToLower(string(result.Transaction.Status()))
}

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
	Money                          money.Money       `json:"money"`
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
		Money:                          tx.Amount(),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		ReferenceTransactionID:         tx.ReferenceTransactionID(),
		FailureCode:                    tx.FailureCode(),
		CreatedAt:                      tx.CreatedAt().Format(timeFormat),
		UpdatedAt:                      tx.UpdatedAt().Format(timeFormat),
	}
}

// submitWagerTransactionRequest has no providerId field — it comes from the
// caller's own verified token (see requireAuth/claimsFromContext), never
// from something the client writes into its own request body. A provider
// asserting someone else's providerId in a body field is exactly the kind
// of cross-provider leak Fase 9 exists to close. The amount field is named
// "money" (not "amount") to match the challenge spec's contract literally
// (§9's example body embeds `"money": {"amount": "...", "currency": "..."}`).
type submitWagerTransactionRequest struct {
	ExternalTransactionID          string      `json:"externalTransactionId"`
	PlayerID                       uuid.UUID   `json:"playerId"`
	WalletID                       uuid.UUID   `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           wager.Kind  `json:"kind"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID *string     `json:"referenceExternalTransactionId,omitempty"`
}

// submitWagerTransactionResponse matches the challenge spec's §9 example
// response literally: a flat {transactionId, status, balance,
// idempotentReplay}, not a nested transaction object. GET endpoints below
// still return the richer wagerTransactionResponse — the spec only dictates
// this exact flat shape for the submission response.
type submitWagerTransactionResponse struct {
	TransactionID    uuid.UUID      `json:"transactionId"`
	Status           wager.TxStatus `json:"status"`
	Balance          money.Money    `json:"balance"`
	IdempotentReplay bool           `json:"idempotentReplay"`
}

// submitWagerTransaction is the shared HTTP entry point for provider-
// submitted wagering events (Fase 8's SQS consumer feeds the same
// app.WagerSubmitter.Submit with the same input struct, so both paths carry
// identical idempotency/business-rule guarantees per ARCHITECTURE.md).
// providerId is the authenticated caller's own, from requireAuth — see the
// note on submitWagerTransactionRequest.
//
// The Idempotency-Key header is mandatory (challenge spec §9: "O header
// Idempotency-Key é obrigatório") and is cross-checked against
// providerId:externalTransactionId — a client-facing consistency check, not
// the idempotency mechanism itself (Submit already derives its own key from
// the body regardless of the header's value; see the Fase 5 note in
// TODO.md, and the spec's own "uma operação financeira identificada por
// (providerId, externalTransactionId) não pode ser reaplicada usando outra
// chave" — the pair is the real identity, the header is a consistency
// check on top of it, not a second source of truth).
func (h *handler) submitWagerTransaction(w http.ResponseWriter, r *http.Request) {
	var req submitWagerTransactionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "malformed request body"})
		return
	}
	providerID := claimsFromContext(r.Context()).ProviderID

	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "Idempotency-Key header is required"})
		return
	}
	if want := providerID + ":" + req.ExternalTransactionID; key != want {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "Idempotency-Key does not match providerId:externalTransactionId"})
		return
	}

	result, err := h.deps.WagerSubmitter.Submit(r.Context(), app.SubmitWagerTransactionInput{
		ProviderID:                     providerID,
		ExternalTransactionID:          req.ExternalTransactionID,
		PlayerID:                       req.PlayerID,
		WalletID:                       req.WalletID,
		RoundID:                        req.RoundID,
		GameID:                         req.GameID,
		Kind:                           req.Kind,
		Amount:                         req.Money,
		ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
		CorrelationID:                  correlationIDFromContext(r.Context()),
	})
	if err != nil {
		writeError(r.Context(), w, err, "walletId", req.WalletID, "externalTransactionId", req.ExternalTransactionID)
		return
	}
	if h.deps.Metrics != nil {
		h.deps.Metrics.ObserveWagerSubmission(string(req.Kind), wagerOutcome(result))
	}
	writeJSON(w, http.StatusOK, submitWagerTransactionResponse{
		TransactionID:    result.Transaction.ID(),
		Status:           result.Transaction.Status(),
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
		writeError(r.Context(), w, err, "transactionId", id)
		return
	}
	writeJSON(w, http.StatusOK, newWagerTransactionResponse(tx))
}

// getWagerTransactionByProvider is the provider-facing lookup route. The
// actual cross-provider isolation happens here: a provider may only look up
// its own transactions (path providerId must match the caller's own,
// verified providerId claim), never another provider's by guessing/trying
// their id in the URL. internal-service callers are exempt — that's a
// deliberate operational escape hatch, not a hole, since internal-service
// tokens are never issued to providers (see deploy/keycloak/realm-export.json).
func (h *handler) getWagerTransactionByProvider(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("providerId")
	externalTransactionID := r.PathValue("externalTransactionId")
	if strings.TrimSpace(providerID) == "" || strings.TrimSpace(externalTransactionID) == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "providerId and externalTransactionId are required"})
		return
	}
	claims := claimsFromContext(r.Context())
	if !claims.HasRole(auth.InternalServiceRole) && claims.ProviderID != providerID {
		writeJSON(w, http.StatusForbidden, errorResponse{Error: "cannot access another provider's transactions"})
		return
	}
	tx, err := h.deps.WagerTransactionGetter.GetByProvider(r.Context(), providerID, externalTransactionID)
	if err != nil {
		writeError(r.Context(), w, err, "externalTransactionId", externalTransactionID)
		return
	}
	writeJSON(w, http.StatusOK, newWagerTransactionResponse(tx))
}
