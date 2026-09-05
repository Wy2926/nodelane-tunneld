package identity

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestOIDCTokensExcludeCredentialsFromJSON(t *testing.T) {
	tokens := OIDCTokens{
		AccessToken: "private-access-token", RefreshToken: "private-refresh-token", IDToken: "private-id-token",
		AccessTokenExpiresAt: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC),
		Identity:             OIDCIdentity{Issuer: "https://issuer.test/oidc", Subject: "subject-a", ClientID: "web-client"},
	}
	encoded, err := json.Marshal(tokens)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-access-token", "private-refresh-token", "private-id-token"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("OIDC credentials leaked through default JSON serialization")
		}
	}
}
