package bff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/session"
)

var browserTestNow = time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)

const browserTestSessionID = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type fakeProvider struct {
	authorizationErr error
	exchangeErr      error
	revokeErr        error
	endSessionErr    error
	state            string
	nonce            string
	verifier         string
	locale           string
	code             string
	revoked          string
	exchangeCalls    int
	revokeCalls      int
	tokens           identity.OIDCTokens
}

func (p *fakeProvider) AuthorizationURL(state, nonce, verifier, locale string) (string, error) {
	p.state, p.nonce, p.verifier, p.locale = state, nonce, verifier, locale
	if p.authorizationErr != nil {
		return "", p.authorizationErr
	}
	return "https://auth.example.test/authorize?state=" + url.QueryEscape(state), nil
}

func (p *fakeProvider) Exchange(_ context.Context, code, verifier, nonce string) (identity.OIDCTokens, error) {
	p.exchangeCalls++
	p.code, p.verifier, p.nonce = code, verifier, nonce
	return p.tokens, p.exchangeErr
}

func (p *fakeProvider) Revoke(_ context.Context, refreshToken string) error {
	p.revokeCalls++
	p.revoked = refreshToken
	return p.revokeErr
}

func (p *fakeProvider) EndSessionURL(locale string) (string, error) {
	p.locale = locale
	if p.endSessionErr != nil {
		return "", p.endSessionErr
	}
	return "https://auth.example.test/end-session?locale=" + url.QueryEscape(locale), nil
}

type fakeSessions struct {
	login        session.LoginTransaction
	record       session.Record
	putErr       error
	consumeErr   error
	createErr    error
	readErr      error
	deleteErr    error
	putCalls     int
	consumeCalls int
	createCalls  int
	readCalls    int
	deleteCalls  int
	deletedID    string
}

func (s *fakeSessions) PutLogin(_ context.Context, login session.LoginTransaction) error {
	s.putCalls++
	if s.putErr == nil {
		s.login = login
	}
	return s.putErr
}

func (s *fakeSessions) ConsumeLogin(_ context.Context, state, binding string) (session.LoginTransaction, error) {
	s.consumeCalls++
	if s.consumeErr != nil {
		return session.LoginTransaction{}, s.consumeErr
	}
	if s.login.State == "" || state != s.login.State || binding != s.login.Binding {
		return session.LoginTransaction{}, session.ErrNotFound
	}
	login := s.login
	s.login = session.LoginTransaction{}
	return login, nil
}

func (s *fakeSessions) CreateSession(_ context.Context, record session.Record) error {
	s.createCalls++
	if s.createErr == nil {
		s.record = record
	}
	return s.createErr
}

func (s *fakeSessions) ReadSession(_ context.Context, id string) (session.Record, error) {
	s.readCalls++
	if s.readErr != nil {
		return session.Record{}, s.readErr
	}
	if s.record.ID == "" || id != s.record.ID {
		return session.Record{}, session.ErrNotFound
	}
	return s.record, nil
}

func (s *fakeSessions) DeleteSession(_ context.Context, id string) error {
	s.deleteCalls++
	s.deletedID = id
	if s.deleteErr == nil && id == s.record.ID {
		s.record = session.Record{}
	}
	return s.deleteErr
}

type fakeAccounts struct {
	account domain.Account
	err     error
	issuer  string
	subject string
	calls   int
}

func (a *fakeAccounts) ResolveAccount(_ context.Context, issuer, subject string) (domain.Account, error) {
	a.calls++
	a.issuer, a.subject = issuer, subject
	return a.account, a.err
}

type browserFixture struct {
	provider *fakeProvider
	sessions *fakeSessions
	accounts *fakeAccounts
	server   *Server
}

func deterministicRandom() io.Reader {
	data := make([]byte, 4096)
	for i := range data {
		data[i] = byte(i%251 + 1)
	}
	return bytes.NewReader(data)
}

func newBrowserFixture(t *testing.T) *browserFixture {
	t.Helper()
	provider := &fakeProvider{tokens: identity.OIDCTokens{
		AccessToken: "private-access-token", RefreshToken: "private-refresh-token", IDToken: "private-id-token",
		AccessTokenExpiresAt: browserTestNow.Add(time.Hour),
		Identity:             identity.OIDCIdentity{Issuer: "https://auth.example.test/oidc", Subject: "private-subject", ClientID: "web-client", ExpiresAt: browserTestNow.Add(time.Hour), Name: "Tunnel User", Email: "user@example.test"},
	}}
	sessions := &fakeSessions{}
	accounts := &fakeAccounts{account: domain.Account{ID: "account-123", IdentityIssuer: provider.tokens.Identity.Issuer, IdentitySubject: provider.tokens.Identity.Subject}}
	server, err := New(Options{
		PublicOrigin: "https://tunnel.example.test", Provider: provider, Sessions: sessions, Accounts: accounts,
		Now: func() time.Time { return browserTestNow }, Random: deterministicRandom(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &browserFixture{provider: provider, sessions: sessions, accounts: accounts, server: server}
}

func (f *browserFixture) request(method, target, body string, change func(*http.Request)) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://tunnel.example.test"+target, strings.NewReader(body))
	if change != nil {
		change(request)
	}
	response := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(response, request)
	return response
}

func (f *browserFixture) loginAndCallback(code string) *httptest.ResponseRecorder {
	login := f.request(http.MethodGet, "/auth/login", "", nil)
	cookie := login.Result().Cookies()[0]
	state := f.sessions.login.State
	return f.request(http.MethodGet, "/auth/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state), "", func(r *http.Request) {
		r.AddCookie(cookie)
	})
}

func requireBFFError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid error response %q: %v", response.Body.String(), err)
	}
	if response.Code != status || envelope.Error.Code != code || envelope.Error.Message == "" || envelope.Error.RequestID == "" {
		t.Fatalf("got %d %#v, want %d %q", response.Code, envelope, status, code)
	}
	if response.Header().Get("X-Request-ID") != envelope.Error.RequestID || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing response protections: %#v", response.Header())
	}
}

func TestLoginCreatesBoundFiveMinuteFlow(t *testing.T) {
	f := newBrowserFixture(t)
	response := f.request(http.MethodGet, "/auth/login?return_to=%2Fconsole%2Ftunnels%3Fview%3Ddeleted&locale=zh-cn", "", nil)
	if response.Code != http.StatusFound {
		t.Fatalf("login status = %d, body = %s", response.Code, response.Body)
	}
	if response.Header().Get("Location") != "https://auth.example.test/authorize?state="+url.QueryEscape(f.provider.state) {
		t.Fatalf("unexpected authorization redirect: %s", response.Header().Get("Location"))
	}
	if f.sessions.putCalls != 1 || f.sessions.login.State == "" || f.sessions.login.Nonce == "" || f.sessions.login.Verifier == "" || f.sessions.login.Binding == "" {
		t.Fatalf("login transaction not persisted: %#v", f.sessions.login)
	}
	if f.sessions.login.ExpiresAt != browserTestNow.Add(5*time.Minute) || f.sessions.login.ReturnTo != "/console/tunnels?view=deleted" || f.sessions.login.Locale != "zh-CN" {
		t.Fatalf("login transaction metadata incorrect: %#v", f.sessions.login)
	}
	if len(f.sessions.login.Verifier) != 43 || f.provider.nonce != f.sessions.login.Nonce || f.provider.verifier != f.sessions.login.Verifier || f.provider.locale != "zh-CN" {
		t.Fatalf("OIDC authorization inputs incorrect: %#v", f.provider)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %d, want one", len(cookies))
	}
	cookie := cookies[0]
	if !strings.HasPrefix(cookie.Name, "__Host-nodelane-tunnel-login-") || cookie.Value != f.sessions.login.Binding || cookie.Path != "/" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 300 || cookie.Domain != "" {
		t.Fatalf("unsafe login binding cookie: %#v", cookie)
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("login response is cacheable or referrer-leaking: %#v", response.Header())
	}
	for _, secret := range []string{f.sessions.login.Nonce, f.sessions.login.Verifier, f.sessions.login.Binding} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatal("login response body leaked a server-side flow secret")
		}
	}
}

func TestLoginRejectsOpenRedirectsAndUnsupportedLocalesBeforeDependencies(t *testing.T) {
	for _, target := range []string{
		"/auth/login?return_to=https%3A%2F%2Fevil.example",
		"/auth/login?return_to=%2F%2Fevil.example",
		"/auth/login?return_to=%2Fapi%2Fv1%2Froutes",
		"/auth/login?return_to=%2Fconsole%2F..%2Fauth%2Fcallback",
		"/auth/login?locale=unknown",
		"/auth/login?locale=en&locale=fr",
		"/auth/login?return_to=%2Fconsole&return_to=%2Fconsole%2Ftunnels",
	} {
		t.Run(target, func(t *testing.T) {
			f := newBrowserFixture(t)
			response := f.request(http.MethodGet, target, "", nil)
			requireBFFError(t, response, http.StatusBadRequest, "invalid_request")
			if f.sessions.putCalls != 0 || f.provider.state != "" {
				t.Fatal("invalid login request reached OIDC or Redis")
			}
		})
	}
}

func TestCallbackConsumesBindingCreatesSessionAndRedirectsLocally(t *testing.T) {
	f := newBrowserFixture(t)
	login := f.request(http.MethodGet, "/auth/login?return_to=%2Fconsole%2Ftunnels%2Fnew&locale=pt-br", "", nil)
	loginCookie := login.Result().Cookies()[0]
	state := f.sessions.login.State
	response := f.request(http.MethodGet, "/auth/callback?code=authorization-code&state="+url.QueryEscape(state), "", func(r *http.Request) {
		r.AddCookie(loginCookie)
	})
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/console/tunnels/new" {
		t.Fatalf("callback result = %d location %q body %s", response.Code, response.Header().Get("Location"), response.Body)
	}
	if f.sessions.consumeCalls != 1 || f.provider.exchangeCalls != 1 || f.provider.code != "authorization-code" || f.accounts.calls != 1 || f.sessions.createCalls != 1 {
		t.Fatalf("callback dependency calls incorrect: sessions=%#v provider=%#v accounts=%#v", f.sessions, f.provider, f.accounts)
	}
	if f.accounts.issuer != f.provider.tokens.Identity.Issuer || f.accounts.subject != f.provider.tokens.Identity.Subject {
		t.Fatal("callback did not resolve the verified issuer/subject")
	}
	record := f.sessions.record
	if record.ID == "" || record.CSRFToken == "" || record.AccountID != "account-123" || record.CreatedAt != browserTestNow || record.ExpiresAt != browserTestNow.Add(24*time.Hour) || record.Tokens.RefreshToken != "private-refresh-token" {
		t.Fatalf("created session incorrect: %#v", record)
	}
	var sessionCookie, clearedLogin *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		switch {
		case cookie.Name == SessionCookieName:
			sessionCookie = cookie
		case cookie.Name == loginCookie.Name:
			clearedLogin = cookie
		}
	}
	if sessionCookie == nil || sessionCookie.Value != record.ID || sessionCookie.Path != "/" || !sessionCookie.Secure || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode || sessionCookie.MaxAge != 86400 || sessionCookie.Domain != "" {
		t.Fatalf("unsafe session cookie: %#v", sessionCookie)
	}
	if clearedLogin == nil || clearedLogin.MaxAge >= 0 || clearedLogin.Value != "" {
		t.Fatalf("callback did not clear binding cookie: %#v", clearedLogin)
	}
	for _, secret := range []string{record.CSRFToken, record.Tokens.AccessToken, record.Tokens.RefreshToken, record.Tokens.IDToken, f.provider.tokens.Identity.Subject} {
		if strings.Contains(response.Body.String(), secret) || strings.Contains(response.Header().Get("Location"), secret) {
			t.Fatal("callback leaked session or token material")
		}
	}
}

func TestCallbackRevokesTokensAndFailsClosedOnInvalidIdentityOrAccountBinding(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*browserFixture)
		revokes   int
	}{
		{name: "missing refresh token", configure: func(f *browserFixture) { f.provider.tokens.RefreshToken = "" }, revokes: 0},
		{name: "missing verified issuer", configure: func(f *browserFixture) { f.provider.tokens.Identity.Issuer = "" }, revokes: 1},
		{name: "missing verified subject", configure: func(f *browserFixture) { f.provider.tokens.Identity.Subject = "" }, revokes: 1},
		{name: "account issuer mismatch", configure: func(f *browserFixture) { f.accounts.account.IdentityIssuer = "https://other.example.test/oidc" }, revokes: 1},
		{name: "account subject mismatch", configure: func(f *browserFixture) { f.accounts.account.IdentitySubject = "other-subject" }, revokes: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newBrowserFixture(t)
			test.configure(f)
			response := f.loginAndCallback("authorization-code")
			requireBFFError(t, response, http.StatusServiceUnavailable, "dependency_unavailable")
			if f.provider.revokeCalls != test.revokes || f.sessions.createCalls != 0 {
				t.Fatalf("invalid identity cleanup incorrect: revokes=%d sessions=%d", f.provider.revokeCalls, f.sessions.createCalls)
			}
			if test.revokes != 0 && f.provider.revoked != "private-refresh-token" {
				t.Fatalf("wrong refresh token revoked: %q", f.provider.revoked)
			}
		})
	}
}

func TestCallbackMapsOnlyExplicitCredentialFailuresToUnauthorized(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*browserFixture)
		status    int
		code      string
	}{
		{name: "login missing", configure: func(f *browserFixture) { f.sessions.consumeErr = session.ErrNotFound }, status: http.StatusUnauthorized, code: "unauthorized"},
		{name: "login expired", configure: func(f *browserFixture) { f.sessions.consumeErr = session.ErrExpired }, status: http.StatusUnauthorized, code: "unauthorized"},
		{name: "login store unavailable", configure: func(f *browserFixture) { f.sessions.consumeErr = session.ErrUnavailable }, status: http.StatusServiceUnavailable, code: "dependency_unavailable"},
		{name: "login store unknown", configure: func(f *browserFixture) { f.sessions.consumeErr = errors.New("private redis detail") }, status: http.StatusServiceUnavailable, code: "dependency_unavailable"},
		{name: "exchange rejected", configure: func(f *browserFixture) { f.provider.exchangeErr = identity.ErrOIDCUnauthorized }, status: http.StatusUnauthorized, code: "unauthorized"},
		{name: "exchange unavailable", configure: func(f *browserFixture) { f.provider.exchangeErr = identity.ErrOIDCUnavailable }, status: http.StatusServiceUnavailable, code: "dependency_unavailable"},
		{name: "exchange misconfigured", configure: func(f *browserFixture) { f.provider.exchangeErr = identity.ErrOIDCConfiguration }, status: http.StatusServiceUnavailable, code: "dependency_unavailable"},
		{name: "exchange unknown", configure: func(f *browserFixture) { f.provider.exchangeErr = errors.New("private provider detail") }, status: http.StatusServiceUnavailable, code: "dependency_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newBrowserFixture(t)
			test.configure(f)
			response := f.loginAndCallback("authorization-code")
			requireBFFError(t, response, test.status, test.code)
			if strings.Contains(response.Body.String(), "private") || f.sessions.createCalls != 0 {
				t.Fatalf("authentication error leaked or created session: %s calls=%d", response.Body, f.sessions.createCalls)
			}
		})
	}
}

func TestBrowserEntryPointsBoundRawQueryBeforeDependencies(t *testing.T) {
	f := newBrowserFixture(t)
	response := f.request(http.MethodGet, "/auth/login?return_to=%2Fconsole&padding="+strings.Repeat("a", 9<<10), "", nil)
	requireBFFError(t, response, http.StatusBadRequest, "invalid_request")
	if f.sessions.putCalls != 0 || f.provider.state != "" {
		t.Fatal("oversized login query reached dependencies")
	}

	f = newBrowserFixture(t)
	response = f.request(http.MethodGet, "/auth/callback?code="+strings.Repeat("a", 17<<10)+"&state="+browserTestSessionID, "", nil)
	requireBFFError(t, response, http.StatusUnauthorized, "unauthorized")
	if f.sessions.consumeCalls != 0 || f.provider.exchangeCalls != 0 {
		t.Fatal("oversized callback query reached dependencies")
	}
}

func TestBoundedQueryRejectsSizeBeforeParsingMalformedData(t *testing.T) {
	_, err := parseBoundedQuery(strings.Repeat("%", maxLoginQuery+1), maxLoginQuery)
	if !errors.Is(err, errQueryTooLarge) {
		t.Fatalf("oversized malformed query error = %v, want query-too-large", err)
	}
}

func TestCallbackRejectsMissingDuplicateOrWrongFlowBinding(t *testing.T) {
	tests := []struct {
		name   string
		target func(string) string
		cookie func(*http.Cookie) *http.Cookie
	}{
		{name: "missing code", target: func(state string) string { return "/auth/callback?state=" + state }},
		{name: "missing state", target: func(string) string { return "/auth/callback?code=code" }},
		{name: "duplicate code", target: func(state string) string { return "/auth/callback?code=a&code=b&state=" + state }},
		{name: "duplicate state", target: func(state string) string { return "/auth/callback?code=a&state=" + state + "&state=other" }},
		{name: "malformed query", target: func(string) string { return "/auth/callback?code=%ZZ&state=ignored" }},
		{name: "oversized code", target: func(state string) string {
			return "/auth/callback?code=" + strings.Repeat("a", 4097) + "&state=" + state
		}},
		{name: "code line break", target: func(state string) string { return "/auth/callback?code=a%0Ab&state=" + state }},
		{name: "malformed state", target: func(string) string { return "/auth/callback?code=a&state=short" }},
		{name: "missing cookie", target: func(state string) string { return "/auth/callback?code=a&state=" + state }, cookie: func(*http.Cookie) *http.Cookie { return nil }},
		{name: "wrong binding", target: func(state string) string { return "/auth/callback?code=a&state=" + state }, cookie: func(cookie *http.Cookie) *http.Cookie {
			copy := *cookie
			copy.Value = browserTestSessionID
			return &copy
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newBrowserFixture(t)
			login := f.request(http.MethodGet, "/auth/login", "", nil)
			cookie := login.Result().Cookies()[0]
			if test.cookie != nil {
				cookie = test.cookie(cookie)
			}
			response := f.request(http.MethodGet, test.target(url.QueryEscape(f.sessions.login.State)), "", func(r *http.Request) {
				if cookie != nil {
					r.AddCookie(cookie)
				}
			})
			requireBFFError(t, response, http.StatusUnauthorized, "unauthorized")
			if f.provider.exchangeCalls != 0 || f.accounts.calls != 0 || f.sessions.createCalls != 0 {
				t.Fatal("invalid callback created an authenticated session")
			}
		})
	}
}

func TestSessionEndpointReturnsOnlyBrowserSafeProjection(t *testing.T) {
	f := newBrowserFixture(t)
	f.sessions.record = session.Record{
		ID: browserTestSessionID, AccountID: "account-123", CSRFToken: "private-csrf-token",
		CreatedAt: browserTestNow.Add(-time.Hour), ExpiresAt: browserTestNow.Add(23 * time.Hour), Tokens: f.provider.tokens,
	}
	response := f.request(http.MethodGet, "/api/v1/session", "", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: f.sessions.record.ID})
	})
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("session status = %d headers %#v body %s", response.Code, response.Header(), response.Body)
	}
	var body struct {
		Authenticated bool      `json:"authenticated"`
		AccountID     string    `json:"account_id"`
		Name          string    `json:"name"`
		Email         string    `json:"email"`
		CSRFToken     string    `json:"csrf_token"`
		ExpiresAt     time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Authenticated || body.AccountID != "account-123" || body.Name != "Tunnel User" || body.Email != "user@example.test" || body.CSRFToken != "private-csrf-token" || body.ExpiresAt != f.sessions.record.ExpiresAt {
		t.Fatalf("session projection incorrect: %#v", body)
	}
	for _, forbidden := range []string{browserTestSessionID, "private-access-token", "private-refresh-token", "private-id-token", "private-subject", "https://auth.example.test/oidc", "web-client"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("session response leaked %q", forbidden)
		}
	}
}

func TestSessionEndpointTreatsMissingOrExpiredSessionAsSignedOut(t *testing.T) {
	for _, readErr := range []error{nil, session.ErrNotFound, session.ErrExpired} {
		f := newBrowserFixture(t)
		f.sessions.readErr = readErr
		response := f.request(http.MethodGet, "/api/v1/session", "", func(r *http.Request) {
			if readErr != nil {
				r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: browserTestSessionID})
			}
		})
		if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"authenticated":false}` {
			t.Fatalf("signed-out response = %d %s", response.Code, response.Body)
		}
		if readErr != nil {
			cookies := response.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != SessionCookieName || cookies[0].MaxAge >= 0 {
				t.Fatalf("stale session cookie not cleared: %#v", cookies)
			}
		}
	}
	f := newBrowserFixture(t)
	f.sessions.readErr = session.ErrUnavailable
	response := f.request(http.MethodGet, "/api/v1/session", "", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: browserTestSessionID})
	})
	requireBFFError(t, response, http.StatusServiceUnavailable, "dependency_unavailable")
}

func TestLogoutRequiresSessionCSRFExactOriginAndJSON(t *testing.T) {
	changes := []struct {
		name   string
		change func(*http.Request)
		status int
		code   string
	}{
		{name: "missing csrf", change: func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, status: 403, code: "insufficient_scope"},
		{name: "wrong csrf", change: func(r *http.Request) { r.Header.Set("X-CSRF-Token", "wrong") }, status: 403, code: "insufficient_scope"},
		{name: "missing origin", change: func(r *http.Request) { r.Header.Del("Origin") }, status: 403, code: "insufficient_scope"},
		{name: "suffix origin", change: func(r *http.Request) { r.Header.Set("Origin", "https://tunnel.example.test.evil") }, status: 403, code: "insufficient_scope"},
		{name: "missing content type", change: func(r *http.Request) { r.Header.Del("Content-Type") }, status: 400, code: "invalid_request"},
		{name: "form content type", change: func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, status: 400, code: "invalid_request"},
	}
	for _, test := range changes {
		t.Run(test.name, func(t *testing.T) {
			f := newBrowserFixture(t)
			f.sessions.record = session.Record{ID: browserTestSessionID, AccountID: "account", CSRFToken: "csrf", ExpiresAt: browserTestNow.Add(time.Hour), Tokens: f.provider.tokens}
			response := f.request(http.MethodPost, "/auth/logout", `{}`, func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: f.sessions.record.ID})
				r.Header.Set("Origin", "https://tunnel.example.test")
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-CSRF-Token", f.sessions.record.CSRFToken)
				test.change(r)
			})
			requireBFFError(t, response, test.status, test.code)
			if f.sessions.deleteCalls != 0 || f.provider.revokeCalls != 0 {
				t.Fatal("rejected logout changed authentication state")
			}
		})
	}
}

func TestLogoutClearsLocalSessionRevokesRefreshAndReturnsEndSessionURL(t *testing.T) {
	f := newBrowserFixture(t)
	f.sessions.record = session.Record{ID: browserTestSessionID, AccountID: "account", CSRFToken: "csrf", ExpiresAt: browserTestNow.Add(time.Hour), Tokens: f.provider.tokens}
	response := f.request(http.MethodPost, "/auth/logout", `{}`, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: f.sessions.record.ID})
		r.Header.Set("Origin", "https://tunnel.example.test")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", f.sessions.record.CSRFToken)
	})
	if response.Code != http.StatusOK || f.sessions.deleteCalls != 1 || f.sessions.deletedID != browserTestSessionID || f.provider.revokeCalls != 1 || f.provider.revoked != "private-refresh-token" {
		t.Fatalf("logout did not clear and revoke: status=%d sessions=%#v provider=%#v", response.Code, f.sessions, f.provider)
	}
	var body struct {
		LoggedOut     bool   `json:"logged_out"`
		EndSessionURL string `json:"end_session_url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !body.LoggedOut || body.EndSessionURL != "https://auth.example.test/end-session?locale=" {
		t.Fatalf("logout response incorrect: %#v err=%v", body, err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != SessionCookieName || cookies[0].MaxAge >= 0 || cookies[0].Value != "" {
		t.Fatalf("logout did not clear session cookie: %#v", cookies)
	}
	for _, secret := range []string{"private-refresh-token", "private-access-token", "private-id-token", browserTestSessionID, "csrf"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatal("logout response leaked credentials")
		}
	}
}

func TestLogoutFailureStillRemovesLocalSessionWithoutClaimingSuccess(t *testing.T) {
	f := newBrowserFixture(t)
	f.provider.revokeErr = identity.ErrOIDCUnavailable
	f.sessions.record = session.Record{ID: browserTestSessionID, AccountID: "account", CSRFToken: "csrf", ExpiresAt: browserTestNow.Add(time.Hour), Tokens: f.provider.tokens}
	response := f.request(http.MethodPost, "/auth/logout", `{}`, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: f.sessions.record.ID})
		r.Header.Set("Origin", "https://tunnel.example.test")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", f.sessions.record.CSRFToken)
	})
	requireBFFError(t, response, http.StatusServiceUnavailable, "dependency_unavailable")
	if f.sessions.deleteCalls != 1 || f.provider.revokeCalls != 1 {
		t.Fatal("degraded logout did not perform local cleanup before reporting provider failure")
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != SessionCookieName || cookies[0].MaxAge >= 0 {
		t.Fatalf("degraded logout left the browser session cookie: %#v", cookies)
	}
}

func TestNewRejectsMissingDependenciesAndUnsafeOrigins(t *testing.T) {
	valid := Options{PublicOrigin: "https://tunnel.example.test", Provider: &fakeProvider{}, Sessions: &fakeSessions{}, Accounts: &fakeAccounts{}, Random: deterministicRandom()}
	changes := []func(*Options){
		func(options *Options) { options.PublicOrigin = "http://tunnel.example.test" },
		func(options *Options) { options.PublicOrigin = "https://user:secret@tunnel.example.test" },
		func(options *Options) { options.PublicOrigin = "https://tunnel.example.test/path" },
		func(options *Options) { options.PublicOrigin = "https://tunnel.example.test?query=1" },
		func(options *Options) { options.PublicOrigin = "https://tunnel.example.test?" },
		func(options *Options) { options.Provider = nil },
		func(options *Options) { options.Sessions = nil },
		func(options *Options) { options.Accounts = nil },
		func(options *Options) { options.Random = nil },
	}
	for _, change := range changes {
		options := valid
		change(&options)
		if _, err := New(options); err == nil {
			t.Fatalf("unsafe options accepted: %#v", options)
		}
	}
}

func TestDependencyErrorsAreStableAndDoNotLeakSecrets(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*browserFixture)
		status int
		code   string
	}{
		{name: "authorization unavailable", setup: func(f *browserFixture) { f.provider.authorizationErr = identity.ErrOIDCUnavailable }, status: 503, code: "dependency_unavailable"},
		{name: "login redis unavailable", setup: func(f *browserFixture) { f.sessions.putErr = session.ErrUnavailable }, status: 503, code: "dependency_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newBrowserFixture(t)
			test.setup(f)
			response := f.request(http.MethodGet, "/auth/login", "", nil)
			requireBFFError(t, response, test.status, test.code)
			for _, secret := range []string{"private-access-token", "private-refresh-token", "private-id-token", "private-password"} {
				if strings.Contains(response.Body.String(), secret) {
					t.Fatal("dependency error leaked a secret")
				}
			}
		})
	}
}
