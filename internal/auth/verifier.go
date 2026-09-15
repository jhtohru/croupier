// Package auth validates OIDC access tokens issued by the IdP (Keycloak,
// via deploy/keycloak/realm-export.json) and extracts the caller's identity
// — which provider they are, and whether they hold the internal-service
// role. It knows nothing about HTTP; internal/httpapi's middleware is what
// turns a missing/invalid token into a 401 response.
package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
)

// InternalServiceRole gates wallet operations (POST /wallets, GET
// /wallets/*, reconciliation, and the internal wager-transaction lookup by
// id) — provider clients are authenticated but never hold this role, so a
// valid provider token is still refused on those routes. See
// deploy/keycloak/realm-export.json for how it's assigned.
const InternalServiceRole = "internal-service"

var ErrInvalidToken = errors.New("invalid token")

// Claims is what a request handler needs from a verified token. ProviderID
// comes from a hardcoded-claim protocol mapper set up per provider client in
// the realm export — it identifies which provider authenticated, not a
// value the caller can put in a request body or URL and have trusted.
type Claims struct {
	ProviderID string
	Roles      []string
}

func (c Claims) HasRole(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Verifier checks an access token's signature, issuer and expiry against
// the IdP's discovery document and published keys (fetched once at startup
// via NewVerifier, cached and auto-refreshed by go-oidc internally).
type Verifier struct {
	verifier *oidc.IDTokenVerifier
}

// NewVerifier discovers the OIDC provider at issuerURL (e.g.
// "http://keycloak:8080/realms/croupier") and builds a token verifier for
// it. SkipClientIDCheck is required here: this API is a resource server
// that accepts tokens from multiple different clients (provider-a,
// provider-b, internal-service, ...), so there's no single expected
// audience/client id to check against — identity comes from the providerId
// claim and realm roles instead, checked by the caller of Verify.
func NewVerifier(ctx context.Context, issuerURL string) (*Verifier, error) {
	provider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("auth: discovering OIDC provider at %q: %w", issuerURL, err)
	}
	return &Verifier{verifier: provider.Verifier(&oidc.Config{SkipClientIDCheck: true})}, nil
}

// Verify checks rawToken and extracts its claims. Any failure (malformed,
// bad signature, wrong issuer, expired) is reported as ErrInvalidToken —
// callers don't need to distinguish why a token was rejected, only that it
// was.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (Claims, error) {
	idToken, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	var raw struct {
		ProviderID  string `json:"providerId"`
		RealmAccess struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
	}
	if err := idToken.Claims(&raw); err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	return Claims{ProviderID: raw.ProviderID, Roles: raw.RealmAccess.Roles}, nil
}
