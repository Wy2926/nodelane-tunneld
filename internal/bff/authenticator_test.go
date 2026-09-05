package bff

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/controlapi"
	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/session"
)

type fakeNativeVerifier struct {
	identity identity.OIDCIdentity
	err      error
	token    string
	calls    int
}

func (v *fakeNativeVerifier) VerifyNative(_ context.Context, token string) (identity.OIDCIdentity, error) {
	v.calls++
	v.token = token
	return v.identity, v.err
}

func newAPIAuthenticator(t *testing.T) (*APIAuthenticator, *fakeSessions, *fakeAccounts, *fakeNativeVerifier) {
	t.Helper()
	sessions := &fakeSessions{record: session.Record{ID: browserTestSessionID, AccountID: "web-account", CSRFToken: "csrf-secret"}}
	accounts := &fakeAccounts{account: domainAccount("native-account")}
	verifier := &fakeNativeVerifier{identity: identity.OIDCIdentity{
		Issuer: "https://auth.example.test/oidc", Subject: "native-user", ClientID: "native-client",
		Scopes: []string{"routes:read", "runs:start"},
	}}
	authenticator, err := NewAPIAuthenticator(sessions, accounts, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return authenticator, sessions, accounts, verifier
}

func domainAccount(id string) domain.Account {
	return domain.Account{ID: id, IdentityIssuer: "https://auth.example.test/oidc", IdentitySubject: "native-user"}
}

func TestAPIAuthenticatorUsesBrowserSessionWithoutExposingTokens(t *testing.T) {
	authenticator, sessions, accounts, verifier := newAPIAuthenticator(t)
	request := httptest.NewRequest(http.MethodGet, "https://tunnel.example.test/api/v1/routes", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessions.record.ID})
	principal, err := authenticator.Authenticate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := controlapi.Principal{AccountID: "web-account", Kind: controlapi.PrincipalKindWeb, CSRFToken: "csrf-secret"}
	if !reflect.DeepEqual(principal, want) {
		t.Fatalf("principal = %#v, want %#v", principal, want)
	}
	if sessions.readCalls != 1 || accounts.calls != 0 || verifier.calls != 0 {
		t.Fatalf("browser authentication crossed media: sessions=%d accounts=%d verifier=%d", sessions.readCalls, accounts.calls, verifier.calls)
	}
}

func TestAPIAuthenticatorVerifiesNativeBearerAndResolvesAccount(t *testing.T) {
	authenticator, sessions, accounts, verifier := newAPIAuthenticator(t)
	request := httptest.NewRequest(http.MethodGet, "https://tunnel.example.test/api/v1/routes", nil)
	request.Header.Set("Authorization", "Bearer signed-native-access-token")
	principal, err := authenticator.Authenticate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := controlapi.Principal{AccountID: "native-account", Kind: controlapi.PrincipalKindNative, Scopes: []string{"routes:read", "runs:start"}}
	if !reflect.DeepEqual(principal, want) {
		t.Fatalf("principal = %#v, want %#v", principal, want)
	}
	if verifier.calls != 1 || verifier.token != "signed-native-access-token" || accounts.calls != 1 || accounts.issuer != verifier.identity.Issuer || accounts.subject != verifier.identity.Subject || sessions.readCalls != 0 {
		t.Fatalf("native authentication dependencies incorrect: verifier=%#v accounts=%#v sessions=%#v", verifier, accounts, sessions)
	}
	principal.Scopes[0] = "mutated"
	if verifier.identity.Scopes[0] != "routes:read" {
		t.Fatal("principal shared a mutable scope slice with the verifier")
	}
}

func TestAPIAuthenticatorRejectsAmbiguousOrMalformedMediaBeforeVerification(t *testing.T) {
	for _, configure := range []func(*http.Request){
		func(request *http.Request) {},
		func(request *http.Request) { request.Header.Set("Authorization", "Basic abc") },
		func(request *http.Request) { request.Header.Set("Authorization", "Bearer ") },
		func(request *http.Request) { request.Header.Set("Authorization", "Bearer token with space") },
		func(request *http.Request) {
			request.Header.Add("Authorization", "Bearer one")
			request.Header.Add("Authorization", "Bearer two")
		},
		func(request *http.Request) {
			request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "one"})
			request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "two"})
		},
		func(request *http.Request) {
			request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: browserTestSessionID})
			request.Header.Set("Authorization", "Bearer token")
		},
	} {
		authenticator, sessions, accounts, verifier := newAPIAuthenticator(t)
		request := httptest.NewRequest(http.MethodGet, "https://tunnel.example.test/api/v1/routes", nil)
		configure(request)
		principal, err := authenticator.Authenticate(context.Background(), request)
		if !errors.Is(err, controlapi.ErrUnauthorized) || !reflect.DeepEqual(principal, controlapi.Principal{}) {
			t.Fatalf("malformed medium returned %#v, %v", principal, err)
		}
		if sessions.readCalls != 0 || accounts.calls != 0 || verifier.calls != 0 {
			t.Fatalf("malformed medium reached dependencies: sessions=%d accounts=%d verifier=%d", sessions.readCalls, accounts.calls, verifier.calls)
		}
	}
}

func TestAPIAuthenticatorMapsDependencyAndCredentialFailures(t *testing.T) {
	tests := []struct {
		name      string
		browser   bool
		configure func(*fakeSessions, *fakeAccounts, *fakeNativeVerifier)
		want      error
	}{
		{name: "missing browser session", browser: true, configure: func(s *fakeSessions, _ *fakeAccounts, _ *fakeNativeVerifier) { s.readErr = session.ErrNotFound }, want: controlapi.ErrUnauthorized},
		{name: "expired browser session", browser: true, configure: func(s *fakeSessions, _ *fakeAccounts, _ *fakeNativeVerifier) { s.readErr = session.ErrExpired }, want: controlapi.ErrUnauthorized},
		{name: "redis unavailable", browser: true, configure: func(s *fakeSessions, _ *fakeAccounts, _ *fakeNativeVerifier) { s.readErr = session.ErrUnavailable }, want: controlapi.ErrUnavailable},
		{name: "native rejected", configure: func(_ *fakeSessions, _ *fakeAccounts, v *fakeNativeVerifier) { v.err = identity.ErrOIDCUnauthorized }, want: controlapi.ErrUnauthorized},
		{name: "oidc unavailable", configure: func(_ *fakeSessions, _ *fakeAccounts, v *fakeNativeVerifier) { v.err = identity.ErrOIDCUnavailable }, want: controlapi.ErrUnavailable},
		{name: "account unavailable", configure: func(_ *fakeSessions, a *fakeAccounts, _ *fakeNativeVerifier) {
			a.err = errors.New("private database error")
		}, want: controlapi.ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticator, sessions, accounts, verifier := newAPIAuthenticator(t)
			test.configure(sessions, accounts, verifier)
			request := httptest.NewRequest(http.MethodGet, "https://tunnel.example.test/api/v1/routes", nil)
			if test.browser {
				request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessions.record.ID})
			} else {
				request.Header.Set("Authorization", "Bearer native-token")
			}
			principal, err := authenticator.Authenticate(context.Background(), request)
			if !errors.Is(err, test.want) || !reflect.DeepEqual(principal, controlapi.Principal{}) {
				t.Fatalf("got %#v, %v; want %v", principal, err, test.want)
			}
		})
	}
}

func TestAPIAuthenticatorFailsClosedOnResolvedIdentityMismatch(t *testing.T) {
	for _, mutate := range []func(*domain.Account){
		func(account *domain.Account) { account.IdentityIssuer = "https://other.example.test/oidc" },
		func(account *domain.Account) { account.IdentitySubject = "other-subject" },
	} {
		authenticator, sessions, accounts, verifier := newAPIAuthenticator(t)
		mutate(&accounts.account)
		request := httptest.NewRequest(http.MethodGet, "https://tunnel.example.test/api/v1/routes", nil)
		request.Header.Set("Authorization", "Bearer native-token")
		principal, err := authenticator.Authenticate(context.Background(), request)
		if !errors.Is(err, controlapi.ErrUnavailable) || !reflect.DeepEqual(principal, controlapi.Principal{}) {
			t.Fatalf("identity mismatch returned %#v, %v", principal, err)
		}
		if verifier.calls != 1 || accounts.calls != 1 || sessions.readCalls != 0 {
			t.Fatalf("identity mismatch dependency calls incorrect: verifier=%d accounts=%d sessions=%d", verifier.calls, accounts.calls, sessions.readCalls)
		}
	}
}

func TestNewAPIAuthenticatorRejectsMissingDependencies(t *testing.T) {
	sessions := &fakeSessions{}
	accounts := &fakeAccounts{}
	verifier := &fakeNativeVerifier{}
	for _, dependencies := range []struct {
		sessions SessionReader
		accounts AccountStore
		verifier NativeVerifier
	}{{nil, accounts, verifier}, {sessions, nil, verifier}, {sessions, accounts, nil}} {
		if _, err := NewAPIAuthenticator(dependencies.sessions, dependencies.accounts, dependencies.verifier); err == nil {
			t.Fatal("missing authentication dependency accepted")
		}
	}
}
