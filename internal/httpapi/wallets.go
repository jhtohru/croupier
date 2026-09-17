package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wallet"
)

type walletResponse struct {
	ID        uuid.UUID   `json:"id"`
	PlayerID  uuid.UUID   `json:"playerId"`
	Balance   money.Money `json:"balance"`
	Version   int64       `json:"version"`
	CreatedAt string      `json:"createdAt"`
	UpdatedAt string      `json:"updatedAt"`
}

func newWalletResponse(w *wallet.Wallet) walletResponse {
	return walletResponse{
		ID:        w.ID(),
		PlayerID:  w.PlayerID(),
		Balance:   w.Balance(),
		Version:   w.Version(),
		CreatedAt: w.CreatedAt().Format(timeFormat),
		UpdatedAt: w.UpdatedAt().Format(timeFormat),
	}
}

type createWalletRequest struct {
	PlayerID       uuid.UUID   `json:"playerId"`
	InitialBalance money.Money `json:"initialBalance"`
}

func (h *handler) createWallet(w http.ResponseWriter, r *http.Request) {
	var req createWalletRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "malformed request body"})
		return
	}
	if req.PlayerID == uuid.Nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "playerId is required"})
		return
	}

	created, err := h.deps.WalletCreator.Create(r.Context(), app.CreateWalletInput{
		PlayerID:       req.PlayerID,
		InitialBalance: req.InitialBalance,
		CorrelationID:  correlationIDFromContext(r.Context()),
	})
	if err != nil {
		writeError(r.Context(), w, err, "playerId", req.PlayerID)
		return
	}
	writeJSON(w, http.StatusCreated, newWalletResponse(created))
}

func (h *handler) getWallet(w http.ResponseWriter, r *http.Request) {
	walletID, err := uuid.Parse(r.PathValue("walletId"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid walletId"})
		return
	}

	got, err := h.deps.WalletGetter.Get(r.Context(), walletID)
	if err != nil {
		writeError(r.Context(), w, err, "walletId", walletID)
		return
	}
	writeJSON(w, http.StatusOK, newWalletResponse(got))
}

// checkedEntries matches the challenge spec's §9 example response field name
// literally (not "entriesChecked").
type reconciliationResponse struct {
	WalletID          uuid.UUID   `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int         `json:"checkedEntries"`
}

func (h *handler) reconcileWallet(w http.ResponseWriter, r *http.Request) {
	walletID, err := uuid.Parse(r.PathValue("walletId"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid walletId"})
		return
	}

	result, err := h.deps.WalletReconciler.Reconcile(r.Context(), walletID)
	if err != nil {
		writeError(r.Context(), w, err, "walletId", walletID)
		return
	}
	if h.deps.Metrics != nil {
		h.deps.Metrics.ObserveReconciliation(result.Consistent, result.Difference.Amount())
	}
	if !result.Consistent {
		// Challenge spec §9.38: "Reporte divergências... nos logs" — the
		// difference amount is exactly what this report is about (unlike
		// Fase 11's "sem payloads financeiros completos" convention
		// elsewhere), so it's deliberately included here, not omitted.
		slog.WarnContext(r.Context(), "httpapi: reconciliation found a divergence",
			"walletId", walletID, "correlationId", correlationIDFromContext(r.Context()),
			"storedBalance", result.StoredBalance, "calculatedBalance", result.CalculatedBalance,
			"difference", result.Difference, "checkedEntries", result.EntriesChecked)
	}
	writeJSON(w, http.StatusOK, reconciliationResponse{
		WalletID:          result.WalletID,
		StoredBalance:     result.StoredBalance,
		CalculatedBalance: result.CalculatedBalance,
		Difference:        result.Difference,
		Consistent:        result.Consistent,
		CheckedEntries:    result.EntriesChecked,
	})
}

type ledgerEntryResponse struct {
	ID            uuid.UUID        `json:"id"`
	WalletID      uuid.UUID        `json:"walletId"`
	TransactionID uuid.UUID        `json:"transactionId"`
	Direction     wallet.Direction `json:"direction"`
	Amount        money.Money      `json:"amount"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	CreatedAt     string           `json:"createdAt"`
}

func newLedgerEntryResponse(e *wallet.LedgerEntry) ledgerEntryResponse {
	return ledgerEntryResponse{
		ID:            e.ID(),
		WalletID:      e.WalletID(),
		TransactionID: e.TransactionID(),
		Direction:     e.Direction(),
		Amount:        e.Amount(),
		BalanceBefore: e.BalanceBefore(),
		BalanceAfter:  e.BalanceAfter(),
		CreatedAt:     e.CreatedAt().Format(timeFormat),
	}
}

type listLedgerResponse struct {
	Entries []ledgerEntryResponse `json:"entries"`
}

func (h *handler) listWalletLedger(w http.ResponseWriter, r *http.Request) {
	walletID, err := uuid.Parse(r.PathValue("walletId"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid walletId"})
		return
	}

	var cursor *uuid.UUID
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid cursor"})
			return
		}
		cursor = &parsed
	}

	limit := 0 // 0 lets WalletLedgerLister apply its own default/max
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid limit"})
			return
		}
		limit = parsed
	}

	entries, err := h.deps.WalletLedgerLister.List(r.Context(), app.ListWalletLedgerInput{
		WalletID: walletID,
		Cursor:   cursor,
		Limit:    limit,
	})
	if err != nil {
		writeError(r.Context(), w, err, "walletId", walletID)
		return
	}
	resp := listLedgerResponse{Entries: make([]ledgerEntryResponse, len(entries))}
	for i, e := range entries {
		resp.Entries[i] = newLedgerEntryResponse(e)
	}
	writeJSON(w, http.StatusOK, resp)
}
