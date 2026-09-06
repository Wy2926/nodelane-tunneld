package controlserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/bff"
	jose "github.com/go-jose/go-jose/v4"
)

func TestComposedOIDCCallbackValidatesIssuerBeforeExchange(t *testing.T) {
	f := isolatedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "callback-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	var responseMu sync.Mutex
	var idToken, codeChallenge string
	var discoveries, exchanges, keyRequests atomic.Int32
	var provider *httptest.Server
	provider = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		issuer := provider.URL + "/oidc"
		switch r.URL.Path {
		case "/oidc/.well-known/openid-configuration":
			discoveries.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/auth", "token_endpoint": issuer + "/token",
				"jwks_uri": issuer + "/keys", "revocation_endpoint": issuer + "/revoke", "end_session_endpoint": issuer + "/end",
				"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
				"code_challenge_methods_supported": []string{"S256"}, "id_token_signing_alg_values_supported": []string{"RS256"},
				"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
				"token_endpoint_auth_methods_supported":          []string{"client_secret_basic"},
				"authorization_response_iss_parameter_supported": true,
			})
		case "/oidc/keys":
			keyRequests.Add(1)
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
				{Key: &key.PublicKey, KeyID: "callback-fixture", Algorithm: "RS256", Use: "sig"},
			}})
		case "/oidc/token":
			exchanges.Add(1)
			user, password, ok := r.BasicAuth()
			if !ok || user != f.cfg.OIDCWebClientID || password != f.cfg.OIDCWebClientSecret {
				t.Error("callback exchange did not use the configured confidential client")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Method != http.MethodPost || r.ParseForm() != nil || len(r.Form) != 4 ||
				r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "callback-fixture-code" ||
				r.Form.Get("redirect_uri") != f.cfg.PublicOrigin+"/auth/callback" {
				t.Error("callback exchange did not send the expected authorization-code request")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			responseMu.Lock()
			expectedChallenge, signedToken := codeChallenge, idToken
			responseMu.Unlock()
			digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if expectedChallenge == "" || base64.RawURLEncoding.EncodeToString(digest[:]) != expectedChallenge {
				t.Error("callback exchange did not bind its PKCE verifier to the login challenge")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "callback-fixture-access", "refresh_token": "callback-fixture-refresh",
				"token_type": "Bearer", "expires_in": 3600, "id_token": signedToken,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	f.cfg.OIDCIssuer = provider.URL + "/oidc"
	runtime, err := openWithHTTPClient(ctx, f.cfg, provider.Client())
	if err != nil {
		t.Fatalf("open persistent service: %v", err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	if discoveries.Load() != 0 {
		t.Fatal("startup eagerly contacted the OIDC provider")
	}
	request := func(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, f.cfg.PublicOrigin+path, nil).WithContext(ctx)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		runtime.Handler().ServeHTTP(response, r)
		return response
	}
	returnTo := "/console/tunnels?locale=zh-cn"
	login := request("/auth/login?"+url.Values{"return_to": {returnTo}, "locale": {"zh-cn"}}.Encode(), nil)
	if login.Code != http.StatusFound || discoveries.Load() != 1 {
		t.Fatalf("login did not discover and redirect to the OIDC provider: status=%d", login.Code)
	}
	authorization, err := url.Parse(login.Header().Get("Location"))
	if err != nil || authorization.Scheme+"://"+authorization.Host+authorization.Path != f.cfg.OIDCIssuer+"/auth" {
		t.Fatal("login did not redirect to the discovered authorization endpoint")
	}
	parameters := authorization.Query()
	state, nonce := parameters.Get("state"), parameters.Get("nonce")
	if len(state) != 43 || len(nonce) != 43 || len(parameters.Get("code_challenge")) != 43 ||
		parameters.Get("code_challenge_method") != "S256" || parameters.Get("client_id") != f.cfg.OIDCWebClientID ||
		parameters.Get("redirect_uri") != f.cfg.PublicOrigin+"/auth/callback" || parameters.Get("response_type") != "code" ||
		parameters.Get("scope") != "openid profile email offline_access" || parameters.Get("ui_locales") != "zh-CN" ||
		parameters.Get("prompt") != "consent" || len(parameters["prompt"]) != 1 || parameters.Has("max_age") ||
		parameters.Has("code_verifier") || parameters.Has("client_secret") || parameters.Has("resource") {
		t.Fatal("login redirect omitted or exposed authorization parameters")
	}
	loginCookies := login.Result().Cookies()
	if len(loginCookies) != 1 {
		t.Fatal("login did not create exactly one binding cookie")
	}
	loginCookie := loginCookies[0]
	if !strings.HasPrefix(loginCookie.Name, "__Host-nodelane-tunnel-login-") || len(loginCookie.Value) != 43 ||
		loginCookie.Path != "/" || loginCookie.Domain != "" || !loginCookie.Secure || !loginCookie.HttpOnly ||
		loginCookie.SameSite != http.SameSiteLaxMode || loginCookie.MaxAge <= 0 {
		t.Fatal("login binding cookie did not enforce the browser security contract")
	}

	// The signature and stores are real; this synthetic provider is not live-identity acceptance.
	now := time.Now().UTC()
	payload, err := json.Marshal(map[string]any{
		"iss": f.cfg.OIDCIssuer, "sub": "callback-fixture-subject", "aud": f.cfg.OIDCWebClientID, "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "name": "Callback Fixture", "email": "callback@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	compact, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	responseMu.Lock()
	idToken, codeChallenge = compact, parameters.Get("code_challenge")
	responseMu.Unlock()
	callback := func(issuers []string) *httptest.ResponseRecorder {
		query := url.Values{"code": {"callback-fixture-code"}, "state": {state}}
		if issuers != nil {
			query["iss"] = issuers
		}
		return request("/auth/callback?"+query.Encode(), loginCookie)
	}
	for _, test := range []struct {
		name    string
		issuers []string
	}{
		{"wrong issuer", []string{"https://foreign.example/oidc"}},
		{"missing issuer", nil},
		{"empty issuer", []string{""}},
		{"duplicate issuer", []string{f.cfg.OIDCIssuer, f.cfg.OIDCIssuer}},
		{"conflicting issuer", []string{f.cfg.OIDCIssuer, "https://foreign.example/oidc"}},
		{"non-exact issuer", []string{f.cfg.OIDCIssuer + "/"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := callback(test.issuers)
			if response.Code != http.StatusUnauthorized || exchanges.Load() != 0 || keyRequests.Load() != 0 {
				t.Fatalf("invalid issuer reached token verification or was not rejected: status=%d", response.Code)
			}
			if len(response.Result().Cookies()) != 0 {
				t.Fatal("invalid issuer changed the browser's pending login or session cookie")
			}
		})
	}
	var accounts int
	if err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM tunnel_accounts`).Scan(&accounts); err != nil || accounts != 0 {
		t.Fatalf("invalid issuer created a local account: count=%d err=%v", accounts, err)
	}

	accepted := callback([]string{f.cfg.OIDCIssuer})
	if accepted.Code != http.StatusSeeOther || accepted.Header().Get("Location") != returnTo || exchanges.Load() != 1 || keyRequests.Load() != 1 {
		t.Fatalf("original login did not survive rejected issuers and complete exactly one exchange: status=%d", accepted.Code)
	}
	var sessionCookie *http.Cookie
	var clearedLogin bool
	for _, cookie := range accepted.Result().Cookies() {
		if cookie.Name == bff.SessionCookieName {
			sessionCookie = cookie
		}
		if cookie.Name == loginCookie.Name && cookie.Value == "" && cookie.MaxAge < 0 {
			clearedLogin = true
		}
	}
	if !clearedLogin || sessionCookie == nil || len(sessionCookie.Value) != 43 || sessionCookie.Path != "/" ||
		sessionCookie.Domain != "" || !sessionCookie.Secure || !sessionCookie.HttpOnly ||
		sessionCookie.SameSite != http.SameSiteLaxMode || sessionCookie.MaxAge <= 0 {
		t.Fatal("successful callback did not replace the binding with a secure session cookie")
	}
	record, err := runtime.sessions.ReadSession(ctx, sessionCookie.Value)
	if err != nil || record.AccountID == "" || record.CSRFToken == "" || record.Version != 1 ||
		record.Tokens.Identity.Issuer != f.cfg.OIDCIssuer || record.Tokens.Identity.Subject != "callback-fixture-subject" ||
		record.Tokens.Identity.ClientID != f.cfg.OIDCWebClientID || record.Tokens.RefreshToken != "callback-fixture-refresh" {
		t.Fatalf("verified callback identity and tokens were not persisted in Redis: %v", err)
	}
	var persistedIssuer, persistedSubject string
	if err := f.db.QueryRowContext(ctx, `SELECT identity_issuer,identity_subject FROM tunnel_accounts WHERE id=$1`, record.AccountID).
		Scan(&persistedIssuer, &persistedSubject); err != nil || persistedIssuer != f.cfg.OIDCIssuer || persistedSubject != "callback-fixture-subject" {
		t.Fatalf("session was not linked to its verified PostgreSQL identity: %v", err)
	}
	sessionResponse := request("/api/v1/session", sessionCookie)
	var browserSession struct {
		Authenticated bool   `json:"authenticated"`
		AccountID     string `json:"account_id"`
		Name          string `json:"name"`
		Email         string `json:"email"`
		CSRFToken     string `json:"csrf_token"`
	}
	if sessionResponse.Code != http.StatusOK || json.Unmarshal(sessionResponse.Body.Bytes(), &browserSession) != nil ||
		!browserSession.Authenticated || browserSession.AccountID != record.AccountID || browserSession.Name != "Callback Fixture" ||
		browserSession.Email != "callback@example.test" || browserSession.CSRFToken != record.CSRFToken {
		t.Fatal("new browser session could not read its authenticated account")
	}
	for _, credential := range []string{record.Tokens.AccessToken, record.Tokens.RefreshToken, compact, f.cfg.OIDCWebClientSecret} {
		if strings.Contains(sessionResponse.Body.String(), credential) {
			t.Fatal("browser session response exposed a server-only credential")
		}
	}
	replayed := callback([]string{f.cfg.OIDCIssuer})
	if replayed.Code != http.StatusUnauthorized || exchanges.Load() != 1 || discoveries.Load() != 1 {
		t.Fatalf("callback replay was accepted or repeated the authorization-code exchange: status=%d", replayed.Code)
	}
	if _, err := runtime.sessions.ReadSession(ctx, sessionCookie.Value); err != nil {
		t.Fatalf("callback replay invalidated the established session: %v", err)
	}
}
