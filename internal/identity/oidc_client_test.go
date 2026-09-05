package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

const oidcTestVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

var oidcTestNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

type oidcTestRequest struct {
	Method string
	Path   string
	Form   url.Values
	User   string
	Secret string
}

type oidcTestProvider struct {
	t         *testing.T
	server    *httptest.Server
	key       *rsa.PrivateKey
	mu        sync.Mutex
	keys      []jose.JSONWebKey
	metadata  map[string]any
	requests  []oidcTestRequest
	token     map[string]any
	tokenCode int
	revoke    int
	override  func(http.ResponseWriter, *http.Request) bool
}

func newOIDCTestProvider(t *testing.T) *oidcTestProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &oidcTestProvider{t: t, key: key, tokenCode: http.StatusOK, revoke: http.StatusOK}
	f.keys = []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "first", Algorithm: "RS256", Use: "sig"}}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	f.metadata = map[string]any{
		"issuer": f.issuer(), "authorization_endpoint": f.server.URL + "/login",
		"token_endpoint": f.server.URL + "/exchange", "jwks_uri": f.server.URL + "/keys",
		"revocation_endpoint": f.server.URL + "/revoke-custom", "end_session_endpoint": f.server.URL + "/logout-custom",
		"userinfo_endpoint": f.server.URL + "/userinfo", "device_authorization_endpoint": f.server.URL + "/device",
		"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "none"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:device_code"},
		"scopes_supported":                      []string{"openid", "profile", "email", "offline_access"},
	}
	f.token = f.tokenResponse(f.sign(f.idClaims(), "JWT", "first", key))
	return f
}

func (f *oidcTestProvider) issuer() string { return f.server.URL + "/oidc" }

func (f *oidcTestProvider) options() OIDCOptions {
	return OIDCOptions{
		Issuer: f.issuer(), PublicOrigin: "https://tunnel.example", APIResource: "https://tunnel.example/api",
		WebClientID: "web-app", WebClientSecret: "web-secret-test-only", NativeClientID: "native-app",
		HTTPClient: f.server.Client(), Now: func() time.Time { return oidcTestNow },
	}
}

func (f *oidcTestProvider) client() *OIDCClient {
	f.t.Helper()
	c, err := NewOIDCClient(context.Background(), f.options())
	if err != nil {
		f.t.Fatalf("NewOIDCClient: %v", err)
	}
	return c
}

func (f *oidcTestProvider) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	user, secret, _ := r.BasicAuth()
	f.requests = append(f.requests, oidcTestRequest{r.Method, r.URL.Path, r.Form, user, secret})
	if f.override != nil && f.override(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/oidc/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(f.metadata)
	case "/keys":
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: f.keys})
	case "/exchange":
		w.WriteHeader(f.tokenCode)
		_ = json.NewEncoder(w).Encode(f.token)
	case "/revoke-custom":
		w.WriteHeader(f.revoke)
		_ = json.NewEncoder(w).Encode(f.token)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *oidcTestProvider) requestLog(path string) []oidcTestRequest {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []oidcTestRequest
	for _, r := range f.requests {
		if r.Path == path {
			result = append(result, r)
		}
	}
	return result
}

func (f *oidcTestProvider) idClaims() map[string]any {
	return map[string]any{
		"iss": f.issuer(), "sub": "user-123", "aud": "web-app", "nonce": "nonce-123",
		"iat": oidcTestNow.Unix(), "exp": oidcTestNow.Add(time.Hour).Unix(),
		"name": "Example User", "email": "example@example.test",
	}
}

func (f *oidcTestProvider) nativeClaims() map[string]any {
	return map[string]any{
		"iss": f.issuer(), "sub": "user-123", "aud": "https://tunnel.example/api", "client_id": "native-app",
		"iat": oidcTestNow.Unix(), "exp": oidcTestNow.Add(time.Hour).Unix(), "scope": "routes:read runs:start",
	}
}

func (f *oidcTestProvider) sign(claims map[string]any, typ, kid string, key *rsa.PrivateKey) string {
	f.t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader("kid", kid))
	if err != nil {
		f.t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		f.t.Fatal(err)
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	raw, err := jws.CompactSerialize()
	if err != nil {
		f.t.Fatal(err)
	}
	return raw
}

func (f *oidcTestProvider) tokenResponse(idToken string) map[string]any {
	return map[string]any{"access_token": "opaque-web-access-test", "refresh_token": "refresh-test-original",
		"token_type": "Bearer", "expires_in": 3600, "id_token": idToken}
}

func TestOIDCClientAuthorizationAndLogoutURLs(t *testing.T) {
	f := newOIDCTestProvider(t)
	c := f.client()
	raw, err := c.AuthorizationURL("state-123", "nonce-123", oidcTestVerifier, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := url.Values{
		"client_id": {"web-app"}, "redirect_uri": {"https://tunnel.example/auth/callback"}, "response_type": {"code"},
		"scope": {"openid profile email offline_access"}, "state": {"state-123"}, "nonce": {"nonce-123"},
		"code_challenge_method": {"S256"}, "code_challenge": {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}, "ui_locales": {"zh-CN"},
	}
	if u.Scheme+"://"+u.Host+u.Path != f.server.URL+"/login" || !reflect.DeepEqual(u.Query(), want) {
		t.Fatalf("unexpected authorization parameters: %v", u.Query())
	}
	raw, err = c.EndSessionURL("en")
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(raw)
	want = url.Values{"client_id": {"web-app"}, "post_logout_redirect_uri": {"https://tunnel.example/"}, "ui_locales": {"en"}}
	if u.Scheme+"://"+u.Host+u.Path != f.server.URL+"/logout-custom" || !reflect.DeepEqual(u.Query(), want) {
		t.Fatalf("unexpected logout URL: %s", raw)
	}
	raw, err = c.EndSessionURL("")
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(raw)
	if u.Query().Has("ui_locales") || u.Query().Has("id_token_hint") {
		t.Fatal("optional locale or token hint appeared in logout URL")
	}
}

func TestOIDCClientRejectsConfigurationBeforeDiscovery(t *testing.T) {
	f := newOIDCTestProvider(t)
	cases := []struct {
		name string
		edit func(*OIDCOptions)
	}{
		{"http issuer", func(o *OIDCOptions) { o.Issuer = "http://auth.example/oidc" }},
		{"issuer credentials", func(o *OIDCOptions) { o.Issuer = "https://user:secret@auth.example/oidc" }},
		{"issuer query", func(o *OIDCOptions) { o.Issuer += "?" }},
		{"issuer fragment", func(o *OIDCOptions) { o.Issuer += "#" }},
		{"http origin", func(o *OIDCOptions) { o.PublicOrigin = "http://tunnel.example" }},
		{"origin path", func(o *OIDCOptions) { o.PublicOrigin += "/console" }},
		{"origin credentials", func(o *OIDCOptions) { o.PublicOrigin = "https://user:secret@tunnel.example" }},
		{"origin query", func(o *OIDCOptions) { o.PublicOrigin += "?x=1" }},
		{"http resource", func(o *OIDCOptions) { o.APIResource = "http://tunnel.example/api" }},
		{"resource fragment", func(o *OIDCOptions) { o.APIResource += "#" }},
		{"empty issuer", func(o *OIDCOptions) { o.Issuer = "" }},
		{"empty resource", func(o *OIDCOptions) { o.APIResource = "" }},
		{"empty web id", func(o *OIDCOptions) { o.WebClientID = " " }},
		{"empty native id", func(o *OIDCOptions) { o.NativeClientID = "" }},
		{"same client ids", func(o *OIDCOptions) { o.NativeClientID = o.WebClientID }},
		{"empty secret", func(o *OIDCOptions) { o.WebClientSecret = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := f.options()
			tc.edit(&o)
			_, err := NewOIDCClient(context.Background(), o)
			if !errors.Is(err, ErrOIDCConfiguration) {
				t.Fatalf("got %v, want configuration error", err)
			}
		})
	}
	if len(f.requestLog("/oidc/.well-known/openid-configuration")) != 0 {
		t.Fatal("invalid configuration made a discovery request")
	}
}

func TestOIDCClientRejectsUnsafeDiscovery(t *testing.T) {
	f := newOIDCTestProvider(t)
	for _, field := range []string{"authorization_endpoint", "token_endpoint", "jwks_uri", "revocation_endpoint", "end_session_endpoint", "userinfo_endpoint", "device_authorization_endpoint"} {
		for _, value := range []string{"http://auth.example/path", "https://foreign.example/path", f.server.URL + "/path?", f.server.URL + "/path#", strings.Replace(f.server.URL, "https://", "https://user:secret@", 1) + "/path"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				f.mu.Lock()
				original := f.metadata[field]
				f.metadata[field] = value
				f.mu.Unlock()
				_, err := NewOIDCClient(context.Background(), f.options())
				f.mu.Lock()
				f.metadata[field] = original
				f.mu.Unlock()
				if !errors.Is(err, ErrOIDCConfiguration) {
					t.Fatalf("got %v, want configuration error", err)
				}
			})
		}
	}
	for _, field := range []string{"authorization_endpoint", "token_endpoint", "jwks_uri", "revocation_endpoint", "end_session_endpoint"} {
		t.Run("missing/"+field, func(t *testing.T) {
			f.mu.Lock()
			original := f.metadata[field]
			delete(f.metadata, field)
			f.mu.Unlock()
			_, err := NewOIDCClient(context.Background(), f.options())
			f.mu.Lock()
			f.metadata[field] = original
			f.mu.Unlock()
			if !errors.Is(err, ErrOIDCConfiguration) {
				t.Fatalf("got %v, want configuration error", err)
			}
		})
	}
	f.mu.Lock()
	f.metadata["issuer"] = f.issuer() + "/"
	f.mu.Unlock()
	if _, err := NewOIDCClient(context.Background(), f.options()); !errors.Is(err, ErrOIDCConfiguration) {
		t.Fatalf("non-exact discovery issuer: %v", err)
	}
}

func TestOIDCExchangeUsesBasicPKCEAndVerifiedIdentity(t *testing.T) {
	f := newOIDCTestProvider(t)
	c := f.client()
	tokens, err := c.Exchange(context.Background(), "code-test-only", oidcTestVerifier, "nonce-123")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.Identity.Issuer != f.issuer() || tokens.Identity.Subject != "user-123" || tokens.Identity.ClientID != "web-app" || tokens.Identity.Name != "Example User" || tokens.Identity.Email != "example@example.test" {
		t.Fatalf("unexpected verified identity: %+v", tokens.Identity)
	}
	if tokens.AccessToken != "opaque-web-access-test" || tokens.RefreshToken != "refresh-test-original" || tokens.IDToken == "" || !tokens.AccessTokenExpiresAt.Equal(oidcTestNow.Add(time.Hour)) {
		t.Fatal("exchange did not retain the expected tokens and deterministic expiry")
	}
	requests := f.requestLog("/exchange")
	want := url.Values{"grant_type": {"authorization_code"}, "code": {"code-test-only"}, "code_verifier": {oidcTestVerifier}, "redirect_uri": {"https://tunnel.example/auth/callback"}}
	if len(requests) != 1 || requests[0].Method != "POST" || requests[0].User != "web-app" || requests[0].Secret != "web-secret-test-only" || !reflect.DeepEqual(requests[0].Form, want) {
		t.Fatal("exchange did not send one Basic-authenticated PKCE request without a resource")
	}
}

func TestOIDCWebTokenRejectsInvalidClaims(t *testing.T) {
	f := newOIDCTestProvider(t)
	c := f.client()
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{"issuer", func(m map[string]any) { m["iss"] = "https://other.example/oidc" }},
		{"audience", func(m map[string]any) { m["aud"] = "native-app" }},
		{"nonce", func(m map[string]any) { m["nonce"] = "other-nonce" }},
		{"missing nonce", func(m map[string]any) { delete(m, "nonce") }},
		{"azp", func(m map[string]any) { m["azp"] = "other-app" }},
		{"null azp", func(m map[string]any) { m["azp"] = nil }},
		{"multiple audiences missing azp", func(m map[string]any) { m["aud"] = []string{"web-app", "other-app"} }},
		{"multiple audiences wrong azp", func(m map[string]any) { m["aud"] = []string{"web-app", "other-app"}; m["azp"] = "other-app" }},
		{"subject", func(m map[string]any) { m["sub"] = "" }},
		{"missing expiry", func(m map[string]any) { delete(m, "exp") }},
		{"expiry equality", func(m map[string]any) { m["exp"] = oidcTestNow.Unix() }},
		{"expired", func(m map[string]any) { m["exp"] = oidcTestNow.Add(-time.Second).Unix() }},
		{"future iat", func(m map[string]any) { m["iat"] = oidcTestNow.Add(31 * time.Second).Unix() }},
		{"future nbf", func(m map[string]any) { m["nbf"] = oidcTestNow.Add(31 * time.Second).Unix() }},
		{"missing iat", func(m map[string]any) { delete(m, "iat") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := f.idClaims()
			tc.edit(claims)
			raw := f.sign(claims, "JWT", "first", f.key)
			f.mu.Lock()
			f.token = f.tokenResponse(raw)
			f.mu.Unlock()
			if _, err := c.Exchange(context.Background(), "code", oidcTestVerifier, "nonce-123"); !errors.Is(err, ErrOIDCUnauthorized) {
				t.Fatalf("got %v, want unauthorized", err)
			}
		})
	}
	claims := f.idClaims()
	claims["aud"], claims["azp"] = []string{"web-app", "other-app"}, "web-app"
	claims["iat"], claims["nbf"] = oidcTestNow.Add(30*time.Second).Unix(), oidcTestNow.Add(30*time.Second).Unix()
	f.mu.Lock()
	f.token = f.tokenResponse(f.sign(claims, "JWT", "first", f.key))
	f.mu.Unlock()
	if _, err := c.Exchange(context.Background(), "code", oidcTestVerifier, "nonce-123"); err != nil {
		t.Fatalf("valid azp and 30 second skew: %v", err)
	}
}

func TestOIDCRefreshPreservesOrRotatesRefreshTokenAndIdentity(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve", true: "rotate"}[rotate], func(t *testing.T) {
			f := newOIDCTestProvider(t)
			c := f.client()
			previous, err := c.Exchange(context.Background(), "code", oidcTestVerifier, "nonce-123")
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.token = map[string]any{"access_token": "refreshed-access", "token_type": "Bearer", "expires_in": 1200}
			if rotate {
				f.token["refresh_token"] = "rotated-refresh"
			}
			f.mu.Unlock()
			tokens, err := c.Refresh(context.Background(), previous)
			if err != nil {
				t.Fatal(err)
			}
			wantRefresh := "refresh-test-original"
			if rotate {
				wantRefresh = "rotated-refresh"
			}
			if tokens.RefreshToken != wantRefresh || tokens.AccessToken != "refreshed-access" || tokens.IDToken != previous.IDToken || !reflect.DeepEqual(tokens.Identity, previous.Identity) || !tokens.AccessTokenExpiresAt.Equal(oidcTestNow.Add(20*time.Minute)) {
				t.Fatal("refresh did not preserve verified identity/ID token or the expected refresh token")
			}
			requests := f.requestLog("/exchange")
			want := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"refresh-test-original"}}
			if len(requests) != 2 || !reflect.DeepEqual(requests[1].Form, want) || requests[1].User != "web-app" || requests[1].Secret != "web-secret-test-only" {
				t.Fatal("refresh request must be Basic authenticated and contain no resource")
			}
		})
	}
}

func TestOIDCRefreshValidatesReturnedIdentity(t *testing.T) {
	f := newOIDCTestProvider(t)
	c := f.client()
	previous, err := c.Exchange(context.Background(), "code", oidcTestVerifier, "nonce-123")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"sub", "iss", "aud", "azp", "exp", "iat", "nbf"} {
		t.Run(field, func(t *testing.T) {
			claims := f.idClaims()
			switch field {
			case "exp":
				claims[field] = oidcTestNow.Unix()
			case "iat", "nbf":
				claims[field] = oidcTestNow.Add(time.Minute).Unix()
			default:
				claims[field] = "different"
			}
			f.mu.Lock()
			f.token = f.tokenResponse(f.sign(claims, "JWT", "first", f.key))
			f.mu.Unlock()
			if _, err := c.Refresh(context.Background(), previous); !errors.Is(err, ErrOIDCUnauthorized) {
				t.Fatalf("got %v, want unauthorized", err)
			}
		})
	}
	claims := f.idClaims()
	delete(claims, "nonce")
	claims["name"] = "Updated User"
	f.mu.Lock()
	f.token = f.tokenResponse(f.sign(claims, "JWT", "first", f.key))
	f.mu.Unlock()
	tokens, err := c.Refresh(context.Background(), previous)
	if err != nil || tokens.Identity.Name != "Updated User" || tokens.IDToken == previous.IDToken {
		t.Fatalf("valid refreshed ID token: %v", err)
	}
	for _, edit := range []func(*OIDCTokens){
		func(p *OIDCTokens) { p.RefreshToken = "" },
		func(p *OIDCTokens) { p.Identity.Subject = "" },
		func(p *OIDCTokens) { p.Identity.Issuer = "https://foreign.example" },
		func(p *OIDCTokens) { p.Identity.ClientID = "native-app" },
	} {
		invalid := previous
		edit(&invalid)
		before := len(f.requestLog("/exchange"))
		if _, err := c.Refresh(context.Background(), invalid); !errors.Is(err, ErrOIDCUnauthorized) {
			t.Fatalf("invalid prior identity: %v", err)
		}
		if len(f.requestLog("/exchange")) != before {
			t.Fatal("invalid prior identity reached token endpoint")
		}
	}
}

func TestOIDCRevokeUsesDiscoveryAndBasicAuth(t *testing.T) {
	f := newOIDCTestProvider(t)
	c := f.client()
	if err := c.Revoke(context.Background(), "refresh-to-revoke"); err != nil {
		t.Fatal(err)
	}
	requests := f.requestLog("/revoke-custom")
	want := url.Values{"token": {"refresh-to-revoke"}, "token_type_hint": {"refresh_token"}}
	if len(requests) != 1 || requests[0].Method != "POST" || requests[0].User != "web-app" || requests[0].Secret != "web-secret-test-only" || !reflect.DeepEqual(requests[0].Form, want) {
		t.Fatal("revocation did not use discovered endpoint and explicit Basic auth")
	}
	if err := c.Revoke(context.Background(), ""); !errors.Is(err, ErrOIDCUnauthorized) {
		t.Fatalf("empty refresh token: %v", err)
	}
}

func TestOIDCRejectsInvalidLoginInputsWithoutRequests(t *testing.T) {
	f := newOIDCTestProvider(t)
	c := f.client()
	for _, v := range []string{"", "short", strings.Repeat("a", 129), strings.Repeat("a", 42) + "+"} {
		if _, err := c.AuthorizationURL("state", "nonce", v, ""); !errors.Is(err, ErrOIDCUnauthorized) {
			t.Fatalf("invalid verifier accepted: %v", err)
		}
		if _, err := c.Exchange(context.Background(), "code", v, "nonce"); !errors.Is(err, ErrOIDCUnauthorized) {
			t.Fatalf("invalid exchange verifier accepted: %v", err)
		}
	}
	if _, err := c.AuthorizationURL("", "nonce", oidcTestVerifier, ""); !errors.Is(err, ErrOIDCUnauthorized) {
		t.Fatalf("empty state: %v", err)
	}
	if _, err := c.AuthorizationURL("state", "", oidcTestVerifier, ""); !errors.Is(err, ErrOIDCUnauthorized) {
		t.Fatalf("empty nonce: %v", err)
	}
	if _, err := c.Exchange(context.Background(), "", oidcTestVerifier, "nonce"); !errors.Is(err, ErrOIDCUnauthorized) {
		t.Fatalf("empty code: %v", err)
	}
	if _, err := c.Exchange(context.Background(), "code", oidcTestVerifier, ""); !errors.Is(err, ErrOIDCUnauthorized) {
		t.Fatalf("empty exchange nonce: %v", err)
	}
	if len(f.requestLog("/exchange")) != 0 {
		t.Fatal("invalid login inputs reached token endpoint")
	}
}
