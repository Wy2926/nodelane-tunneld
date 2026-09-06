package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
)

func newOIDCES384Provider(t *testing.T) (*oidcTestProvider, *ecdsa.PrivateKey) {
	t.Helper()
	f := newOIDCTestProvider(t)
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.keys = []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "ec-current", Algorithm: "ES384", Use: "sig"}}
	f.metadata["id_token_signing_alg_values_supported"] = []string{"RS256", "ES384"}
	f.token = f.tokenResponse(signOIDCAlgorithm(t, f.idClaims(), "JWT", "ec-current", jose.ES384, key))
	return f, key
}

func signOIDCAlgorithm(t *testing.T, claims map[string]any, typ, kid string, algorithm jose.SignatureAlgorithm, key any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: algorithm, Key: key},
		(&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader("kid", kid))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOIDCES384WebExchangeAndRefresh(t *testing.T) {
	f, key := newOIDCES384Provider(t)
	c := f.client()
	previous, err := c.Exchange(context.Background(), "code", oidcTestVerifier, "nonce-123")
	if err != nil || previous.Identity.Subject != "user-123" || previous.Identity.ClientID != "web-app" {
		t.Fatalf("Logto ES384 ID token exchange failed: %v", err)
	}
	claims := f.idClaims()
	delete(claims, "nonce")
	claims["name"] = "Refreshed EC User"
	f.mu.Lock()
	f.token = f.tokenResponse(signOIDCAlgorithm(t, claims, "JWT", "ec-current", jose.ES384, key))
	f.token["refresh_token"] = "rotated-ec-refresh"
	f.mu.Unlock()
	refreshed, err := c.Refresh(context.Background(), previous)
	if err != nil || refreshed.Identity.Name != "Refreshed EC User" || refreshed.RefreshToken != "rotated-ec-refresh" {
		t.Fatalf("Logto ES384 refreshed ID token failed: %v", err)
	}
}

func TestOIDCES384NativeTokenAndKeyRotation(t *testing.T) {
	f := newOIDCTestProvider(t)
	c := f.client()
	if _, err := c.VerifyNative(context.Background(), f.sign(f.nativeClaims(), "at+jwt", "first", f.key)); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.keys = []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "ec-rotated", Algorithm: "ES384", Use: "sig"}}
	f.mu.Unlock()
	raw := signOIDCAlgorithm(t, f.nativeClaims(), "at+jwt", "ec-rotated", jose.ES384, key)
	for range 2 {
		verified, err := c.VerifyNative(context.Background(), raw)
		if err != nil || verified.Subject != "user-123" || verified.ClientID != "native-app" || !reflect.DeepEqual(verified.Scopes, []string{"routes:read", "runs:start"}) {
			t.Fatalf("Logto ES384 resource access token failed: %v", err)
		}
	}
	if len(f.requestLog("/keys")) != 2 || len(f.requestLog("/oidc/.well-known/openid-configuration")) != 1 {
		t.Fatal("RSA to ES384 rotation must reload JWKS once without repeating discovery")
	}
}

func TestOIDCES384RejectsSignatureAndAlgorithmTampering(t *testing.T) {
	f, key := newOIDCES384Provider(t)
	c := f.client()
	other, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	unsupported, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.keys = append(f.keys, jose.JSONWebKey{Key: &unsupported.PublicKey, KeyID: "unsupported", Algorithm: "ES256", Use: "sig"})
	f.mu.Unlock()
	for _, kind := range []string{"web", "native"} {
		t.Run(kind, func(t *testing.T) {
			claims, typ := f.idClaims(), "JWT"
			verify := func(raw string) error {
				_, err := c.verifyWeb(context.Background(), raw, "nonce-123")
				return err
			}
			if kind == "native" {
				claims, typ = f.nativeClaims(), "at+jwt"
				verify = func(raw string) error {
					_, err := c.VerifyNative(context.Background(), raw)
					return err
				}
			}
			valid := signOIDCAlgorithm(t, claims, typ, "ec-current", jose.ES384, key)
			if err := verify(valid); err != nil {
				t.Fatal(err)
			}
			cases := map[string]string{
				"wrong signature": signOIDCAlgorithm(t, claims, typ, "ec-current", jose.ES384, other),
				"HMAC":            signOIDCAlgorithm(t, claims, typ, "ec-current", jose.HS256, []byte("public-test-material-must-not-be-an-oidc-hmac-key")),
				"ES256":           signOIDCAlgorithm(t, claims, typ, "unsupported", jose.ES256, unsupported),
			}
			for _, algorithm := range []string{"RS256", "HS256", "none"} {
				parts := strings.Split(valid, ".")
				header, err := json.Marshal(map[string]string{"alg": algorithm, "typ": typ, "kid": "ec-current"})
				if err != nil {
					t.Fatal(err)
				}
				parts[0] = base64.RawURLEncoding.EncodeToString(header)
				if algorithm == "none" {
					parts[2] = ""
				}
				cases["tampered "+algorithm] = strings.Join(parts, ".")
			}
			for name, raw := range cases {
				t.Run(name, func(t *testing.T) {
					if err := verify(raw); !errors.Is(err, ErrOIDCUnauthorized) {
						t.Fatalf("invalid token returned %v, want unauthorized", err)
					}
				})
			}
		})
	}
}
