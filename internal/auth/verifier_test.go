//go:build integration

package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/auth"
)

func issuerURL(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("KEYCLOAK_ISSUER_URL"); v != "" {
		return v
	}
	return "http://localhost:8080/realms/croupier"
}

// fetchToken performs a real client_credentials grant against the real
// Keycloak realm provisioned by deploy/keycloak/realm-export.json — no
// mocking of the IdP anywhere in this file.
func fetchToken(t *testing.T, clientID, clientSecret string) string {
	t.Helper()
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	resp, err := http.PostForm(issuerURL(t)+"/protocol/openid-connect/token", form)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEmpty(t, body.AccessToken)
	return body.AccessToken
}

func TestVerifierAgainstRealKeycloak(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	verifier, err := auth.NewVerifier(ctx, issuerURL(t))
	require.NoError(t, err)

	t.Run("provider token carries its own providerId, not another provider's", func(t *testing.T) {
		token := fetchToken(t, "provider-a", "provider-a-secret")
		claims, err := verifier.Verify(context.Background(), token)
		require.NoError(t, err)
		assert.Equal(t, "provider-a", claims.ProviderID)
		assert.False(t, claims.HasRole(auth.InternalServiceRole))

		tokenB := fetchToken(t, "provider-b", "provider-b-secret")
		claimsB, err := verifier.Verify(context.Background(), tokenB)
		require.NoError(t, err)
		assert.Equal(t, "provider-b", claimsB.ProviderID)
	})

	t.Run("internal-service token carries the internal-service role, no providerId", func(t *testing.T) {
		token := fetchToken(t, "internal-service", "internal-service-secret")
		claims, err := verifier.Verify(context.Background(), token)
		require.NoError(t, err)
		assert.True(t, claims.HasRole(auth.InternalServiceRole))
		assert.Empty(t, claims.ProviderID)
	})

	t.Run("wrong client secret is rejected", func(t *testing.T) {
		form := url.Values{
			"grant_type":    {"client_credentials"},
			"client_id":     {"provider-a"},
			"client_secret": {"not-the-real-secret"},
		}
		resp, err := http.PostForm(issuerURL(t)+"/protocol/openid-connect/token", form)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.NotEqual(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("expired token is rejected", func(t *testing.T) {
		// Challenge spec §13.27: "rejeição de credenciais... expiradas."
		// provider-short-lived (deploy/keycloak/realm-export.json) exists
		// only for this test — access.token.lifespan=2s, a per-client
		// override that leaves every other client's normal 300s realm
		// default untouched, so this doesn't slow down or change behavior
		// for anything else in this suite.
		token := fetchToken(t, "provider-short-lived", "provider-short-lived-secret")
		time.Sleep(3 * time.Second)

		_, err := verifier.Verify(context.Background(), token)
		assert.ErrorIs(t, err, auth.ErrInvalidToken)
	})

	t.Run("malformed token is rejected", func(t *testing.T) {
		_, err := verifier.Verify(context.Background(), "not-a-jwt")
		assert.ErrorIs(t, err, auth.ErrInvalidToken)
	})

	t.Run("tampered payload is rejected", func(t *testing.T) {
		token := fetchToken(t, "provider-a", "provider-a-secret")
		parts := strings.Split(token, ".")
		require.Len(t, parts, 3)
		// Flip the payload's first character rather than the signature's
		// last one: base64url's final quantum can have unused padding bits,
		// and a single-character change landing there can decode to the
		// exact same bytes — observed directly, flipping the signature's
		// last character passed verification intermittently because of
		// this. A change anywhere but the last one or two characters of a
		// base64url segment always changes the decoded bytes, and
		// corrupting the payload invalidates the signature deterministically
		// regardless of where in the payload it lands.
		payload := []byte(parts[1])
		payload[0] = flipChar(payload[0])
		tampered := parts[0] + "." + string(payload) + "." + parts[2]

		_, err := verifier.Verify(context.Background(), tampered)
		assert.ErrorIs(t, err, auth.ErrInvalidToken)
	})
}

func flipChar(b byte) byte {
	if b == 'A' {
		return 'B'
	}
	return 'A'
}
