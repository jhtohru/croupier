package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/jhtohru/croupier/internal/auth"
)

// tokenVerifier is the one method this package needs from *auth.Verifier —
// consumer-defined, same pattern as every other dependency in server.go, so
// middleware tests use a stub instead of a real Keycloak.
type tokenVerifier interface {
	Verify(ctx context.Context, rawToken string) (auth.Claims, error)
}

type claimsContextKey struct{}

func claimsFromContext(ctx context.Context) auth.Claims {
	claims, _ := ctx.Value(claimsContextKey{}).(auth.Claims)
	return claims
}

// requireAuth rejects a request with no valid bearer token before next ever
// runs, and makes the token's claims available to next via the request
// context. This is authentication only — which caller is this — not
// authorization; see requireInternalRole for the one route class
// (wallet operations) that also needs a specific role.
func requireAuth(verifier tokenVerifier, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "missing bearer token"})
			return
		}
		claims, err := verifier.Verify(r.Context(), token)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "invalid or expired token"})
			return
		}
		ctx := context.WithValue(r.Context(), claimsContextKey{}, claims)
		next(w, r.WithContext(ctx))
	}
}

// requireInternalRole additionally requires the internal-service role —
// wallet operations (POST /wallets, GET /wallets/*, reconciliation, the
// internal wager-transaction lookup by id) are not exposed to providers at
// all, regardless of how valid their own token is. A provider's perfectly
// valid token is authenticated but not authorized here: 403, not 401.
func requireInternalRole(verifier tokenVerifier, next http.HandlerFunc) http.HandlerFunc {
	return requireAuth(verifier, func(w http.ResponseWriter, r *http.Request) {
		if !claimsFromContext(r.Context()).HasRole(auth.InternalServiceRole) {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "internal-service role required"})
			return
		}
		next(w, r)
	})
}
