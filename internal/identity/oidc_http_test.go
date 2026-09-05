package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

type oidcTestTransport func(*http.Request) (*http.Response, error)

func (f oidcTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOIDCHTTPBoundsCustomTimeoutWithoutMutatingClient(t *testing.T) {
	for _, suppliedTimeout := range []time.Duration{0, time.Minute, time.Second} {
		t.Run(suppliedTimeout.String(), func(t *testing.T) {
			f := newOIDCTestProvider(t)
			opts := f.options()
			base := opts.HTTPClient.Transport
			opts.HTTPClient.Timeout = suppliedTimeout
			var sawDeadline bool
			opts.HTTPClient.Transport = oidcTestTransport(func(r *http.Request) (*http.Response, error) {
				deadline, ok := r.Context().Deadline()
				remaining := time.Until(deadline)
				if !ok || remaining <= 0 || remaining > 10*time.Second || (suppliedTimeout == time.Second && remaining > time.Second) {
					t.Error("OIDC HTTP request is missing the bounded timeout")
				}
				sawDeadline = true
				return base.RoundTrip(r)
			})
			if _, err := NewOIDCClient(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			if !sawDeadline || opts.HTTPClient.Timeout != suppliedTimeout || opts.HTTPClient.CheckRedirect != nil {
				t.Fatal("constructor mutated the supplied HTTP client or did not send discovery")
			}
		})
	}
}

func TestOIDCHTTPTimeoutAndContextCancellationAreUnavailable(t *testing.T) {
	f := newOIDCTestProvider(t)
	opts := f.options()
	opts.HTTPClient.Timeout = 40 * time.Millisecond
	c, err := NewOIDCClient(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/exchange" && r.URL.Path != "/keys" {
			return false
		}
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
		return true
	}
	f.mu.Unlock()
	started := time.Now()
	if _, err := c.Exchange(context.Background(), "code", oidcTestVerifier, "nonce-123"); !errors.Is(err, ErrOIDCUnavailable) {
		t.Fatalf("token endpoint timeout: %v", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("token endpoint ignored configured timeout")
	}
	started = time.Now()
	if _, err := c.VerifyNative(context.Background(), f.sign(f.nativeClaims(), "at+jwt", "first", f.key)); !errors.Is(err, ErrOIDCUnavailable) {
		t.Fatalf("JWKS timeout: %v", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("background JWKS request ignored configured timeout")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Exchange(ctx, "code", oidcTestVerifier, "nonce-123"); !errors.Is(err, ErrOIDCUnavailable) {
		t.Fatalf("cancelled exchange: %v", err)
	}
}

func TestOIDCHTTPRejectsOversizedBodies(t *testing.T) {
	for _, path := range []string{"/oidc/.well-known/openid-configuration", "/exchange", "/keys", "/revoke-custom"} {
		t.Run(path, func(t *testing.T) {
			f := newOIDCTestProvider(t)
			var c *OIDCClient
			if path != "/oidc/.well-known/openid-configuration" {
				c = f.client()
			}
			f.mu.Lock()
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != path {
					return false
				}
				var body any = f.token
				if path == "/keys" {
					body = jose.JSONWebKeySet{Keys: f.keys}
				} else if path == "/oidc/.well-known/openid-configuration" {
					body = f.metadata
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(body)
				w.(http.Flusher).Flush()
				_, _ = w.Write([]byte(strings.Repeat(" ", 1<<20)))
				return true
			}
			f.mu.Unlock()
			var err error
			switch path {
			case "/oidc/.well-known/openid-configuration":
				_, err = NewOIDCClient(context.Background(), f.options())
			case "/exchange":
				_, err = c.Exchange(context.Background(), "code", oidcTestVerifier, "nonce-123")
			case "/keys":
				_, err = c.VerifyNative(context.Background(), f.sign(f.nativeClaims(), "at+jwt", "first", f.key))
			case "/revoke-custom":
				err = c.Revoke(context.Background(), "refresh-test-only")
			}
			if !errors.Is(err, ErrOIDCUnavailable) {
				t.Fatalf("oversized response accepted: %v", err)
			}
		})
	}
}

func TestOIDCHTTPDoesNotFollowRedirects(t *testing.T) {
	for _, path := range []string{"/oidc/.well-known/openid-configuration", "/exchange", "/keys", "/revoke-custom"} {
		t.Run(path, func(t *testing.T) {
			f := newOIDCTestProvider(t)
			var c *OIDCClient
			if path != "/oidc/.well-known/openid-configuration" {
				c = f.client()
			}
			f.mu.Lock()
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != path {
					return false
				}
				http.Redirect(w, r, f.server.URL+"/trap", http.StatusTemporaryRedirect)
				return true
			}
			f.mu.Unlock()
			var err error
			switch path {
			case "/oidc/.well-known/openid-configuration":
				_, err = NewOIDCClient(context.Background(), f.options())
			case "/exchange":
				_, err = c.Exchange(context.Background(), "code", oidcTestVerifier, "nonce-123")
			case "/keys":
				_, err = c.VerifyNative(context.Background(), f.sign(f.nativeClaims(), "at+jwt", "first", f.key))
			case "/revoke-custom":
				err = c.Revoke(context.Background(), "refresh-test-only")
			}
			if !errors.Is(err, ErrOIDCUnavailable) || len(f.requestLog("/trap")) != 0 || len(f.requestLog(path)) != 1 {
				t.Fatalf("redirect followed/retried or returned unsafe error: %v", err)
			}
		})
	}
}

func TestOIDCProviderErrorsAreRedactedAndNeverRetried(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
		want   error
	}{
		{"invalid grant", 400, "invalid_grant", ErrOIDCUnauthorized},
		{"invalid client", 401, "invalid_client", ErrOIDCConfiguration},
		{"server failure", 500, "server_error", ErrOIDCUnavailable},
		{"rate limited", 429, "slow_down", ErrOIDCUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOIDCTestProvider(t)
			c := f.client()
			f.mu.Lock()
			f.tokenCode, f.revoke = tc.status, tc.status
			f.token = map[string]any{"error": tc.code, "error_description": "web-secret-test-only refresh-test-only code-test-only raw-provider-sensitive-detail"}
			f.mu.Unlock()
			_, err := c.Exchange(context.Background(), "code-test-only", oidcTestVerifier, "nonce-123")
			assertOIDCRedactedError(t, err, tc.want)
			if len(f.requestLog("/exchange")) != 1 {
				t.Fatal("authorization code exchange was blindly retried")
			}
			err = c.Revoke(context.Background(), "refresh-test-only")
			assertOIDCRedactedError(t, err, tc.want)
			if len(f.requestLog("/revoke-custom")) != 1 {
				t.Fatal("revocation was blindly retried")
			}
		})
	}
}

func TestOIDCDiscoveryAndJWKSErrorsAreRedacted(t *testing.T) {
	for _, path := range []string{"/oidc/.well-known/openid-configuration", "/keys"} {
		for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
			t.Run(path+http.StatusText(status), func(t *testing.T) {
				f := newOIDCTestProvider(t)
				var c *OIDCClient
				if path == "/keys" {
					c = f.client()
				}
				f.mu.Lock()
				f.override = func(w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path != path {
						return false
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_, _ = w.Write([]byte("raw-provider-sensitive-detail web-secret-test-only refresh-test-only code-test-only"))
					return true
				}
				f.mu.Unlock()
				var err error
				if path == "/keys" {
					_, err = c.VerifyNative(context.Background(), f.sign(f.nativeClaims(), "at+jwt", "first", f.key))
				} else {
					_, err = NewOIDCClient(context.Background(), f.options())
				}
				assertOIDCRedactedError(t, err, ErrOIDCUnavailable)
			})
		}
	}
}

func assertOIDCRedactedError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
	for _, secret := range []string{"web-secret-test-only", "refresh-test-only", "code-test-only", "raw-provider-sensitive-detail"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("OIDC error exposed provider data or a secret")
		}
	}
}
