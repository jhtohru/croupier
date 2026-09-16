package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

type errorResponse struct {
	Error string `json:"error"`
}

// validationErrors are the domain sentinels reachable from malformed but
// well-formed-JSON client input at these specific routes (a negative initial
// balance, a BET amount of zero, a REFUND with no reference, an unparseable
// currency/amount inside an embedded Money field, ...). Deliberately not
// exhaustive over every sentinel in money/wallet/wager: only ones the
// handlers below can actually trigger by construction — e.g.
// wallet.ErrInsufficientBalance never reaches here, since WagerSubmitter
// handles it internally as a REJECTED transaction, not a returned error.
var validationErrors = []error{
	wallet.ErrNegativeInitialBalance,
	wager.ErrInvalidInput,
	wager.ErrInvalidKind,
	wager.ErrNonPositiveAmount,
	wager.ErrLossAmountMustBeZero,
	wager.ErrMissingReference,
	wager.ErrUnexpectedReference,
	money.ErrInvalidAmount,
	money.ErrInvalidCurrency,
	money.ErrOverflow,
	money.ErrCurrencyMismatch,
}

// writeError maps an error from the app layer to an HTTP response. Only
// errors that can legitimately originate from client-supplied input get a
// specific 4xx — anything else is logged server-side and returned as an
// opaque 500, so internal error detail (which could include implementation
// detail or, in a differently-shaped bug, financial data) never reaches the
// client.
//
// extra is an optional list of alternating key/value pairs (walletId,
// transactionId, ...) a specific handler can attach to the 500 log line —
// correlationId and providerId are always included on their own since every
// caller has both available via ctx. Never pass amount/balance/currency
// here — see Fase 11's "sem payloads financeiros completos" in
// ARCHITECTURE.md → "Observabilidade".
func writeError(ctx context.Context, w http.ResponseWriter, err error, extra ...any) {
	switch {
	case errors.Is(err, app.ErrWalletNotFound),
		errors.Is(err, app.ErrWagerTransactionNotFound),
		errors.Is(err, app.ErrLedgerEntryNotFound):
		writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
	case errors.Is(err, app.ErrWalletAlreadyExists),
		errors.Is(err, app.ErrIdempotencyConflict):
		writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
	case isValidationError(err):
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, app.ErrUnavailable):
		// A dependency (Postgres) couldn't even be reached — transient
		// infrastructure unavailability, not a bug or bad input. Challenge
		// spec §9 requires this be distinguishable by contract from a
		// generic 500, so it gets its own status instead of falling into
		// default below. Still logged: an operator watching 503 rates cares
		// just as much as they would about a 500.
		slog.WarnContext(ctx, "httpapi: dependency unavailable",
			"error", err, "correlationId", correlationIDFromContext(ctx))
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "service temporarily unavailable"})
	default:
		args := []any{
			"error", err,
			"correlationId", correlationIDFromContext(ctx),
			"providerId", claimsFromContext(ctx).ProviderID,
		}
		args = append(args, extra...)
		slog.ErrorContext(ctx, "unhandled httpapi error", args...)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
	}
}

func isValidationError(err error) bool {
	for _, sentinel := range validationErrors {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}
