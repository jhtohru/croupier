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

	t.Run("malformed token is rejected", func(t *testing.T) {
		_, err := verifier.Verify(context.Background(), "not-a-jwt")
		assert.ErrorIs(t, err, auth.ErrInvalidToken)
	})

	t.Run("tampered signature is rejected", func(t *testing.T) {
		token := fetchToken(t, "provider-a", "provider-a-secret")
		parts := strings.Split(token, ".")
		require.Len(t, parts, 3)
		// Flip the last character of the signature — same header/payload,
		// broken signature.
		sig := []byte(parts[2])
		sig[len(sig)-1] = flipChar(sig[len(sig)-1])
		tampered := parts[0] + "." + parts[1] + "." + string(sig)

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
