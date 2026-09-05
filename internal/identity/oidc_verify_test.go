package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"reflect"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestOIDCNativeVerifiesResourceUserAndScopes(t *testing.T) {
	f := newOIDCTestProvider(t)
	c := f.client()
	raw := f.sign(f.nativeClaims(), "at+jwt", "first", f.key)
	for range 3 {
		identity, err := c.VerifyNative(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		if identity.Issuer != f.issuer() || identity.Subject != "user-123" || identity.ClientID != "native-app" || !identity.ExpiresAt.Equal(oidcTestNow.Add(time.Hour)) || !reflect.DeepEqual(identity.Scopes, []string{"routes:read", "runs:start"}) {
			t.Fatalf("unexpected native identity: %+v", identity)
		}
	}
	if len(f.requestLog("/keys")) != 1 || len(f.requestLog("/oidc/.well-known/openid-configuration")) != 1 {
		t.Fatal("native verification did not reuse discovery/JWKS cache")
	}
	if _, err := c.Exchange(context.Background(), "code", oidcTestVerifier, "nonce-123"); err != nil {
		t.Fatal(err)
	}
	if len(f.requestLog("/keys")) != 1 {
		t.Fatal("web and native verification did not share JWKS cache")
	}
}

func TestOIDCNativeRejectsInvalidClaims(t *testing.T) {
	f := newOIDCTestProvider(t)
	c := f.client()
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{"issuer", func(m map[string]any) { m["iss"] = "https://other.example/oidc" }},
		{"audience", func(m map[string]any) { m["aud"] = "https://other.example/api" }},
		{"web audience", func(m map[string]any) { m["aud"] = "web-app" }},
		{"client", func(m map[string]any) { m["client_id"] = "other-native-app" }},
		{"web client", func(m map[string]any) { m["client_id"] = "web-app" }},
		{"missing client", func(m map[string]any) { delete(m, "client_id") }},
		{"empty subject", func(m map[string]any) { m["sub"] = "" }},
		{"client credentials subject", func(m map[string]any) { m["sub"] = "native-app" }},
		{"scope array", func(m map[string]any) { m["scope"] = []string{"routes:read"} }},
		{"scope number", func(m map[string]any) { m["scope"] = 1 }},
		{"scope null", func(m map[string]any) { m["scope"] = nil }},
		{"scope missing", func(m map[string]any) { delete(m, "scope") }},
		{"scope control", func(m map[string]any) { m["scope"] = "routes:read\nruns:start" }},
		{"expiry equality", func(m map[string]any) { m["exp"] = oidcTestNow.Unix() }},
		{"expired", func(m map[string]any) { m["exp"] = oidcTestNow.Add(-time.Second).Unix() }},
		{"missing expiry", func(m map[string]any) { delete(m, "exp") }},
		{"future iat", func(m map[string]any) { m["iat"] = oidcTestNow.Add(31 * time.Second).Unix() }},
		{"future nbf", func(m map[string]any) { m["nbf"] = oidcTestNow.Add(31 * time.Second).Unix() }},
		{"missing iat", func(m map[string]any) { delete(m, "iat") }},
		{"iat shape", func(m map[string]any) { m["iat"] = "tomorrow" }},
		{"nbf shape", func(m map[string]any) { m["nbf"] = "tomorrow" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := f.nativeClaims()
			tc.edit(claims)
			if _, err := c.VerifyNative(context.Background(), f.sign(claims, "at+jwt", "first", f.key)); !errors.Is(err, ErrOIDCUnauthorized) {
				t.Fatalf("got %v, want unauthorized", err)
			}
		})
	}
	claims := f.nativeClaims()
	claims["iat"], claims["nbf"] = oidcTestNow.Add(30*time.Second).Unix(), oidcTestNow.Add(30*time.Second).Unix()
	claims["scope"] = ""
	identity, err := c.VerifyNative(context.Background(), f.sign(claims, "at+jwt", "first", f.key))
	if err != nil || len(identity.Scopes) != 0 {
		t.Fatalf("empty granted scopes and 30 second skew must verify without granting operation permissions: %v", err)
	}
}

func TestOIDCNativeRejectsTokenTypeOpaqueAndInvalidSignature(t *testing.T) {
	f := newOIDCTestProvider(t)
	c := f.client()
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"id token":     f.sign(f.idClaims(), "JWT", "first", f.key),
		"wrong type":   f.sign(f.nativeClaims(), "JWT", "first", f.key),
		"missing type": f.sign(f.nativeClaims(), "", "first", f.key),
		"opaque":       "opaque-access-token-test-only", "empty": "", "malformed": "a.b.c",
		"wrong signature": f.sign(f.nativeClaims(), "at+jwt", "first", otherKey),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := c.VerifyNative(context.Background(), raw); !errors.Is(err, ErrOIDCUnauthorized) {
				t.Fatalf("got %v, want unauthorized", err)
			}
		})
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("test-only-hmac-key-with-at-least-thirty-two-bytes")}, (&jose.SignerOptions{}).WithType("at+jwt"))
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign([]byte(`{"iss":"unused"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.VerifyNative(context.Background(), raw); !errors.Is(err, ErrOIDCUnauthorized) {
		t.Fatalf("HS256 must not be accepted: %v", err)
	}
}

func TestOIDCJWKSRotatesWithoutRediscovery(t *testing.T) {
	f := newOIDCTestProvider(t)
	c := f.client()
	if _, err := c.VerifyNative(context.Background(), f.sign(f.nativeClaims(), "at+jwt", "first", f.key)); err != nil {
		t.Fatal(err)
	}
	next, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.keys = []jose.JSONWebKey{{Key: &next.PublicKey, KeyID: "second", Algorithm: "RS256", Use: "sig"}}
	f.mu.Unlock()
	for range 2 {
		if _, err := c.VerifyNative(context.Background(), f.sign(f.nativeClaims(), "at+jwt", "second", next)); err != nil {
			t.Fatalf("rotated key failed: %v", err)
		}
	}
	if len(f.requestLog("/keys")) != 2 || len(f.requestLog("/oidc/.well-known/openid-configuration")) != 1 {
		t.Fatal("rotation must refetch keys exactly once without repeating discovery")
	}
}
