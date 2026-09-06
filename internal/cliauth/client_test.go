package cliauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testResource = "https://tunnel.nodelane.net/api"

type memoryStore struct {
	txMu                        sync.Mutex
	mu                          sync.Mutex
	credentials                 Credentials
	present                     bool
	saveErr, loadErr, deleteErr error
	saves, deletes              int
}

func (s *memoryStore) Transaction(ctx context.Context, action func(Store) error) error {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return action(s)
}

func (s *memoryStore) Load(context.Context) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return Credentials{}, s.loadErr
	}
	if !s.present {
		return Credentials{}, ErrNoCredentials
	}
	return s.credentials, nil
}

func (s *memoryStore) Save(_ context.Context, c Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.credentials, s.present = c, true
	s.saves++
	return nil
}

func (s *memoryStore) Delete(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes++
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.credentials, s.present = Credentials{}, false
	return nil
}

type authFixture struct {
	server     *httptest.Server
	store      *memoryStore
	client     *Client
	tokenCalls atomic.Int32
	device     func(http.ResponseWriter, *http.Request)
	token      func(http.ResponseWriter, *http.Request)
	revoke     func(http.ResponseWriter, *http.Request)
	discovery  func(map[string]any)
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	f := &authFixture{store: &memoryStore{}}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("native auth must not send HTTP auth or cookies")
		}
		switch r.URL.Path {
		case "/oidc/.well-known/openid-configuration":
			doc := map[string]any{"issuer": f.server.URL + "/oidc", "token_endpoint": f.server.URL + "/oidc/token", "device_authorization_endpoint": f.server.URL + "/oidc/device", "revocation_endpoint": f.server.URL + "/oidc/revoke", "token_endpoint_auth_methods_supported": []string{"none"}, "grant_types_supported": []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"}}
			if f.discovery != nil {
				f.discovery(doc)
			}
			_ = json.NewEncoder(w).Encode(doc)
		case "/oidc/device":
			if f.device != nil {
				f.device(w, r)
				return
			}
			checkNativeForm(t, r, true)
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret", "user_code": "ABCD-EFGH", "verification_uri": f.server.URL + "/activate", "verification_uri_complete": f.server.URL + "/activate?user_code=ABCD-EFGH", "expires_in": 30, "interval": 1})
		case "/oidc/token":
			f.tokenCalls.Add(1)
			checkNativeForm(t, r, r.FormValue("grant_type") != "refresh_token")
			if f.token != nil {
				f.token(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-secret", "refresh_token": "refresh-rotated", "token_type": "Bearer", "expires_in": 300})
		case "/oidc/revoke":
			if f.revoke != nil {
				f.revoke(w, r)
				return
			}
			if r.FormValue("client_id") != "native-client" || r.FormValue("token_type_hint") != "refresh_token" || r.FormValue("token") != "refresh-rotated" {
				t.Error("incorrect native revocation form")
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	var err error
	f.client, err = New(context.Background(), Options{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, HTTPClient: f.server.Client(), Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func checkNativeForm(t *testing.T, r *http.Request, wantScopes bool) {
	t.Helper()
	if r.Method != http.MethodPost || r.FormValue("client_id") != "native-client" || r.FormValue("resource") != testResource {
		t.Error("missing native client/resource form parameters")
	}
	if r.FormValue("client_secret") != "" {
		t.Error("native flow sent client_secret")
	}
	if wantScopes && r.FormValue("scope") != "openid offline_access routes:read runs:start" {
		t.Error("incorrect native scopes")
	}
}

func TestLoginPersistsOnlyBoundRefreshTokenAndRestarts(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	callbackCount := 0
	err := f.client.Login(context.Background(), func(code DeviceCode) error {
		callbackCount++
		if code.UserCode != "ABCD-EFGH" || code.VerificationURI != f.server.URL+"/activate" || !code.Expiry.After(time.Now()) {
			t.Error("incorrect user-facing device code")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if callbackCount != 1 {
		t.Fatalf("callbacks = %d", callbackCount)
	}
	c, err := f.store.Load(context.Background())
	if err != nil || c.RefreshToken != "refresh-rotated" || c.Issuer != f.server.URL+"/oidc" || c.ClientID != "native-client" || c.Resource != testResource {
		t.Fatalf("credentials not durably bound: %v", err)
	}
	data, _ := json.Marshal(c)
	if strings.Contains(string(data), "access-secret") || strings.Contains(string(data), "device-secret") {
		t.Fatal("non-refresh secret persisted")
	}
	token, err := f.client.AccessToken(context.Background())
	if err != nil || token != "access-secret" || f.tokenCalls.Load() != 1 {
		t.Fatalf("login access cache failed: %v", err)
	}
	restarted, err := New(context.Background(), Options{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, HTTPClient: f.server.Client(), Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	f.token = func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("grant_type") != "refresh_token" || r.FormValue("refresh_token") != "refresh-rotated" {
			t.Error("restart did not refresh stored token")
		}
		_, _ = fmt.Fprint(w, `{"access_token":"next-access","refresh_token":"next-refresh","token_type":"Bearer","expires_in":300}`)
	}
	token, err = restarted.AccessToken(context.Background())
	if err != nil || token != "next-access" {
		t.Fatalf("restart failed: %v", err)
	}
	c, _ = f.store.Load(context.Background())
	if c.RefreshToken != "next-refresh" {
		t.Fatal("rotated refresh was not saved")
	}
}

func TestFailedAndCancelledLoginPreservePreviousCredentials(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"callback", "provider", "cancel"} {
		t.Run(test, func(t *testing.T) {
			f := newAuthFixture(t)
			previous := Credentials{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, RefreshToken: "previous-refresh"}
			_ = f.store.Save(context.Background(), previous)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test == "provider" {
				f.token = func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = fmt.Fprint(w, `{"error":"access_denied","error_description":"LEAK-provider-secret"}`)
				}
			}
			err := f.client.Login(ctx, func(DeviceCode) error {
				if test == "callback" {
					return errors.New("LEAK-callback-secret")
				}
				if test == "cancel" {
					cancel()
				}
				return nil
			})
			if err == nil || strings.Contains(err.Error(), "LEAK") {
				t.Fatalf("failure missing or unsanitized: %v", err)
			}
			if test == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			got, _ := f.store.Load(context.Background())
			if got != previous {
				t.Fatal("failed login replaced existing account")
			}
		})
	}
}

func TestRefreshDoesNotReturnTokenBeforeDurableRotation(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	f.store.credentials = Credentials{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, RefreshToken: "previous-refresh"}
	f.store.present = true
	f.store.saveErr = errors.New("LEAK-storage-secret")
	token, err := f.client.AccessToken(context.Background())
	if err == nil || token != "" || strings.Contains(err.Error(), "LEAK") {
		t.Fatalf("rotation failure leaked token/error: %v", err)
	}
	f.store.saveErr = nil
	token, err = f.client.AccessToken(context.Background())
	if err != nil || token != "access-secret" || f.tokenCalls.Load() != 1 {
		t.Fatalf("unsaved rotation must be retried before any refresh: %v", err)
	}
	got, _ := f.store.Load(context.Background())
	if got.RefreshToken != "refresh-rotated" {
		t.Fatal("pending rotated token not persisted")
	}
}

func TestConcurrentAccessRefreshesOnceAndLogoutClearsCache(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	f.store.credentials = Credentials{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, RefreshToken: "previous-refresh"}
	f.store.present = true
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := f.client.AccessToken(context.Background())
			if err != nil || token != "access-secret" {
				t.Errorf("access: %v", err)
			}
		}()
	}
	wg.Wait()
	if f.tokenCalls.Load() != 1 {
		t.Fatalf("refresh calls=%d", f.tokenCalls.Load())
	}
	if err := f.client.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Load(context.Background()); !errors.Is(err, ErrNoCredentials) {
		t.Fatal("logout did not remove credentials")
	}
	if token, err := f.client.AccessToken(context.Background()); !errors.Is(err, ErrNoCredentials) || token != "" {
		t.Fatalf("logout left access cache: %v", err)
	}
}

func TestLogoutClearsLocalOnRevocationOrDiscoveryFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"revocation", "discovery", "cancelled"} {
		t.Run(test, func(t *testing.T) {
			f := newAuthFixture(t)
			f.store.credentials = Credentials{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, RefreshToken: "refresh-rotated"}
			f.store.present = true
			if test == "revocation" {
				f.revoke = func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(500)
					_, _ = fmt.Fprint(w, "LEAK-revoke-secret")
				}
			}
			if test == "discovery" {
				f.discovery = func(doc map[string]any) { doc["issuer"] = "https://attacker.invalid/oidc" }
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test == "cancelled" {
				cancel()
			}
			err := f.client.Logout(ctx)
			if !errors.Is(err, ErrRevocationUnconfirmed) || strings.Contains(err.Error(), "LEAK") {
				t.Fatalf("remote failure not reported safely: %v", err)
			}
			if _, err = f.store.Load(context.Background()); !errors.Is(err, ErrNoCredentials) {
				t.Fatal("remote failure kept credentials")
			}
		})
	}
}

func TestDiscoveryRejectsForeignIssuerEndpointsAndUnsafeVerification(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"issuer", "token_endpoint", "device_authorization_endpoint", "revocation_endpoint", "verification", "verification_complete", "interval", "expiry"} {
		t.Run(test, func(t *testing.T) {
			f := newAuthFixture(t)
			if strings.HasPrefix(test, "verification") || test == "interval" || test == "expiry" {
				f.device = func(w http.ResponseWriter, r *http.Request) {
					doc := map[string]any{"device_code": "private-device-code", "user_code": "ABCD-EFGH", "verification_uri": f.server.URL + "/activate", "expires_in": 30, "interval": 1}
					switch test {
					case "verification":
						doc["verification_uri"] = "https://attacker.invalid/activate"
					case "verification_complete":
						doc["verification_uri_complete"] = "javascript:LEAK-secret"
					case "interval":
						doc["interval"] = -1
					case "expiry":
						doc["expires_in"] = 0
					}
					_ = json.NewEncoder(w).Encode(doc)
				}
			} else {
				f.discovery = func(doc map[string]any) { doc[test] = "https://attacker.invalid/oidc" }
			}
			called := false
			err := f.client.Login(context.Background(), func(DeviceCode) error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("unsafe response reached user: %v", err)
			}
			if f.tokenCalls.Load() != 0 {
				t.Fatal("unsafe discovery sent token request")
			}
		})
	}
}

func TestCredentialBindingMismatchNeverSendsToken(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	f.store.credentials = Credentials{Issuer: "https://other.invalid/oidc", ClientID: "native-client", Resource: testResource, RefreshToken: "LEAK-stored-secret"}
	f.store.present = true
	if token, err := f.client.AccessToken(context.Background()); err == nil || token != "" || strings.Contains(err.Error(), "LEAK") {
		t.Fatalf("binding mismatch: %v", err)
	}
	if f.tokenCalls.Load() != 0 {
		t.Fatal("foreign credentials sent to provider")
	}
}

func TestPollingHonorsSlowDownAndContextDeadline(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	f.token = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = fmt.Fprint(w, `{"error":"slow_down"}`)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	err := f.client.Login(ctx, func(DeviceCode) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("poll deadline: %v", err)
	}
	if f.tokenCalls.Load() != 1 {
		t.Fatalf("slow_down ignored: %d polls", f.tokenCalls.Load())
	}
}

func TestProviderRedirectsAndLargeBodiesFailClosed(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"redirect", "large"} {
		t.Run(test, func(t *testing.T) {
			f := newAuthFixture(t)
			f.device = func(w http.ResponseWriter, r *http.Request) {
				if test == "redirect" {
					http.Redirect(w, r, f.server.URL+"/oidc/token", http.StatusTemporaryRedirect)
					return
				}
				_, _ = fmt.Fprint(w, strings.Repeat("x", 70<<10))
			}
			err := f.client.Login(context.Background(), func(DeviceCode) error { t.Error("invalid body reached callback"); return nil })
			if err == nil {
				t.Fatal("unsafe provider response accepted")
			}
			if f.tokenCalls.Load() != 0 {
				t.Fatal("redirect forwarded native request")
			}
		})
	}
}

func TestNewRejectsUnsafeConfigurationAndOnlyExplicitLoopbackHTTP(t *testing.T) {
	t.Parallel()
	for _, issuer := range []string{"http://auth.nodelane.net/oidc", "https://user:secret@auth.nodelane.net/oidc", "https://auth.nodelane.net/oidc?token=secret", "https://auth.nodelane.net/oidc#secret", "https://auth.nodelane.net/oidc\n"} {
		_, err := New(context.Background(), Options{Issuer: issuer, ClientID: "native-client", Resource: testResource, Store: &memoryStore{}})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("accepted/leaked issuer %q: %v", issuer, err)
		}
	}
	for _, test := range []struct {
		issuer      string
		allow, want bool
	}{{"http://127.0.0.1:12345/oidc", false, false}, {"http://127.0.0.1:12345/oidc", true, true}, {"http://localhost:12345/oidc", true, false}, {"http://192.0.2.1/oidc", true, false}} {
		_, err := New(context.Background(), Options{Issuer: test.issuer, ClientID: "native-client", Resource: testResource, Store: &memoryStore{}, AllowInsecureLoopback: test.allow})
		if (err == nil) != test.want {
			t.Errorf("loopback configuration: %v", err)
		}
	}
}

func TestAccountCacheAndPendingRotationCannotUndoAnotherLogout(t *testing.T) {
	t.Parallel()
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			f := newAuthFixture(t)
			f.store.credentials = Credentials{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, RefreshToken: "previous-refresh"}
			f.store.present = true
			if pending {
				f.store.saveErr = errors.New("save failed")
			}
			_, _ = f.client.AccessToken(context.Background())
			f.store.saveErr = nil
			if err := f.store.Transaction(context.Background(), func(store Store) error { return store.Delete(context.Background()) }); err != nil {
				t.Fatal(err)
			}
			token, err := f.client.AccessToken(context.Background())
			if token != "" || !errors.Is(err, ErrNoCredentials) {
				t.Fatalf("another logout was undone: %v", err)
			}
			if _, err := f.store.Load(context.Background()); !errors.Is(err, ErrNoCredentials) {
				t.Fatal("pending token recreated deleted login")
			}
		})
	}
}

func TestMultipleClientsSerializeRefreshRotation(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	f.store.credentials = Credentials{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, RefreshToken: "refresh-0"}
	f.store.present = true
	var providerMu sync.Mutex
	current := 0
	f.token = func(w http.ResponseWriter, r *http.Request) {
		providerMu.Lock()
		defer providerMu.Unlock()
		if r.FormValue("refresh_token") != fmt.Sprintf("refresh-%d", current) {
			w.WriteHeader(400)
			_, _ = fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		current++
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("access-%d", current), "refresh_token": fmt.Sprintf("refresh-%d", current), "token_type": "Bearer", "expires_in": 300})
	}
	var wg sync.WaitGroup
	for range 8 {
		client, err := New(context.Background(), Options{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, HTTPClient: f.server.Client(), Store: f.store})
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if token, err := client.AccessToken(context.Background()); err != nil || token == "" {
				t.Errorf("concurrent rotation: %v", err)
			}
		}()
	}
	wg.Wait()
	stored, _ := f.store.Load(context.Background())
	if stored.RefreshToken != "refresh-8" {
		t.Fatal("latest refresh rotation was lost")
	}
}
