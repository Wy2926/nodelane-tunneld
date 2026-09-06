package runclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const accountFixture = `{"run":{"id":"run_test","route_id":"rte_test","status":"starting","desired_state":"running","created_at":"2026-09-06T00:00:00Z","connect_deadline_at":"2026-09-06T00:02:00Z"},"route":{"id":"rte_test","protocol":"http","subdomain":"example","proxy_name":"rte_test","public_url":"https://example.tunnel.test"},"credential_token":"run-secret","replayed":false}`
const anonymousFixture = `{"run":{"id":"anr_test","proxy_name":"anp_test","protocol":"tcp","public_endpoint":"tunnel.test:20001","created_at":"2026-09-06T00:00:00Z","connect_deadline_at":"2026-09-06T00:02:00Z","hard_expires_at":"2026-09-06T01:00:00Z"},"credential_token":"anonymous-secret","replayed":false}`

func newHTTPClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(Options{BaseURL: server.URL + "/api/v1"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestMethodsSendOnlyTheirCredentialAndNormalizeRunDTOs(t *testing.T) {
	var calls int
	client := newHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected ambient credential or query")
		}
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/v1/client-config":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
				t.Error("bootstrap sent credential")
			}
			fmt.Fprint(w, `{"frp":{"server_addr":"tunnel.test","server_port":7000,"tls_server_name":"tunnel.test","trusted_ca_pem":"public-cert"},"oidc":{"issuer":"https://auth.test","client_id":"nt","resource":"https://tunnel.test"}}`)
		case "/api/v1/routes":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer account-secret" {
				t.Error("routes credential")
			}
			fmt.Fprint(w, `{"routes":[{"id":"rte_test","protocol":"http","proxy_name":"rte_test","subdomain":"example","public_url":"https://example.tunnel.test","status":"active"}]}`)
		case "/api/v1/routes/rte_test/runs":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer account-secret" || r.Header.Get("Idempotency-Key") != "start-key" || string(body) != "{}" {
				t.Errorf("start contract: %s", body)
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, accountFixture)
		case "/api/v1/launch/redeem":
			if r.Header.Get("Authorization") != "" || r.Header.Get("Idempotency-Key") != "" {
				t.Error("redeem sent unrelated credential")
			}
			var input map[string]string
			_ = json.Unmarshal(body, &input)
			if len(input) != 2 || input["launch_code"] != "launch-secret" || input["nonce"] != "redeem-nonce" {
				t.Error("redeem body")
			}
			fmt.Fprint(w, accountFixture)
		case "/api/v1/anonymous/runs":
			if r.Header.Get("Authorization") != "" || r.Header.Get("Idempotency-Key") != "allocate-key" {
				t.Error("anonymous credential")
			}
			var input struct {
				InstallationID string `json:"installation_id"`
				Protocol       string `json:"protocol"`
				LocalHost      string `json:"local_host"`
				LocalPort      int    `json:"local_port"`
			}
			_ = json.Unmarshal(body, &input)
			if input.InstallationID != "installation" || input.Protocol != "tcp" || input.LocalHost != "localhost" || input.LocalPort != 3000 {
				t.Error("allocate body")
			}
			fmt.Fprint(w, anonymousFixture)
		case "/api/v1/runs/run_test/heartbeat":
			if r.Header.Get("Authorization") != "Bearer run-secret" || string(body) != "{}" {
				t.Error("heartbeat credential")
			}
			fmt.Fprint(w, `{"run":{"id":"run_test","desired_state":"running","status":"online","lease_expires_at":"2026-09-06T00:03:00Z"},"stopped":false}`)
		case "/api/v1/runs/anr_test/stop":
			if r.Header.Get("Authorization") != "Bearer anonymous-secret" {
				t.Error("stop credential")
			}
			fmt.Fprint(w, `{"run":{"id":"anr_test","desired_state":"stopped","state":"stopping"},"stopped":true}`)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	})
	ctx := context.Background()
	bootstrap, err := client.Bootstrap(ctx)
	if err != nil || bootstrap.FRP.ServerPort != 7000 || bootstrap.OIDC.ClientID != "nt" {
		t.Fatalf("bootstrap: %#v %v", bootstrap, err)
	}
	routes, err := client.Routes(ctx, "account-secret")
	if err != nil || len(routes) != 1 || routes[0].PublicURL != "https://example.tunnel.test" {
		t.Fatalf("routes: %v %v", routes, err)
	}
	run, err := client.Start(ctx, "rte_test", "account-secret", "start-key")
	if err != nil || run.ProxyName != "rte_test" || run.CredentialToken != "run-secret" || run.Subdomain != "example" || run.PublicEndpoint != "https://example.tunnel.test" {
		t.Fatalf("start: %v %v", run, err)
	}
	encoded, _ := json.Marshal(run)
	if strings.Contains(string(encoded), "run-secret") || strings.Contains(fmt.Sprintf("%+v %#v", run, run), "run-secret") {
		t.Fatal("Run formatting leaked credential")
	}
	if _, err := client.Redeem(ctx, "launch-secret", "redeem-nonce"); err != nil {
		t.Fatal(err)
	}
	anon, err := client.Allocate(ctx, "installation", "tcp", "localhost", 3000, "allocate-key")
	if err != nil || anon.ID != "anr_test" || anon.HardExpiresAt.IsZero() {
		t.Fatalf("allocate: %v %v", anon, err)
	}
	heartbeat, err := client.Heartbeat(ctx, "run_test", "run-secret")
	if err != nil || heartbeat.Stopped || heartbeat.Run.LeaseExpiresAt.IsZero() {
		t.Fatalf("heartbeat: %v %v", heartbeat, err)
	}
	stopped, err := client.Stop(ctx, "anr_test", "anonymous-secret")
	if err != nil || stopped.Status != "stopping" || stopped.DesiredState != "stopped" {
		t.Fatalf("stop: %v %v", stopped, err)
	}
	if calls != 7 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestClientRejectsUnsafeBaseURLsAndPathInjectionBeforeNetwork(t *testing.T) {
	for _, raw := range []string{"http://example.com/api/v1", "http://localhost/api/v1", "https://user:secret@example.com/api/v1", "https://example.com/api/v1?token=x", "https://example.com/api/v1#x", "https://example.com/other", "https://example.com/api%2fv1"} {
		if _, err := New(Options{BaseURL: raw}); err == nil {
			t.Errorf("accepted unsafe URL %s", raw)
		}
	}
	var calls atomic.Int32
	client := newHTTPClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	if _, err := client.Start(context.Background(), "rte_x/../client-config", "secret", "key"); err == nil {
		t.Fatal("accepted path traversal")
	}
	if _, err := client.Heartbeat(context.Background(), "run_test", "secret\r\nCookie:x"); err == nil {
		t.Fatal("accepted header injection")
	}
	if _, err := client.Allocate(context.Background(), "i", "https", "localhost", 3000, "k"); err == nil {
		t.Fatal("accepted unsupported protocol")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached network")
	}
}

func TestClientDisablesCookieJarAndRedirectsEvenOnInjectedHTTPClient(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("sent ambient cookies")
		}
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	parsed, _ := url.Parse(server.URL)
	jar.SetCookies(parsed, []*http.Cookie{{Name: "session", Value: "cookie-secret"}})
	injected := &http.Client{Jar: jar}
	client, err := New(Options{BaseURL: server.URL + "/api/v1", HTTPClient: injected})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Routes(context.Background(), "account-secret")
	if err == nil || redirected.Load() != 0 {
		t.Fatalf("redirect followed: %v %d", err, redirected.Load())
	}
	if injected.Jar != jar {
		t.Fatal("mutated caller HTTP client")
	}
}

func TestAPIErrorWhitelistsCodeAndRequestIDAndRoundsRetryAfter(t *testing.T) {
	client := newHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1.2")
		w.Header().Set("X-Request-ID", "0123456789abcdef0123456789abcdef")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":"anonymous_network_rate_limited","message":"account-secret launch-secret https://secret.invalid","request_id":"ignored-body-secret"}}`)
	})
	_, err := client.Allocate(context.Background(), "i", "http", "localhost", 3000, "k")
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != 429 || apiError.Code != "anonymous_network_rate_limited" || apiError.RetryAfter != 2*time.Second || apiError.RequestID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("error: %#v", err)
	}
	if strings.Contains(fmt.Sprint(err), "secret") {
		t.Fatal("server message leaked")
	}
	client = newHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"code":"secret_in_unknown_code","request_id":"secret_in_id"}}`)
	})
	_, err = client.Routes(context.Background(), "account-secret")
	if !errors.As(err, &apiError) || apiError.Code != "request_failed" || apiError.RequestID != "" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("untrusted error leaked: %v", err)
	}
}

func TestClientRejectsOversizedMalformedOrMismatchedSuccess(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", 2<<20), "{", accountFixture + accountFixture, strings.Replace(accountFixture, `"route_id":"rte_test"`, `"route_id":"rte_other"`, 1)} {
		client := newHTTPClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		if _, err := client.Start(context.Background(), "rte_test", "secret", "k"); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe success accepted: %v", err)
		}
	}
}

func TestAllocateRejectsAnAccountRunResponse(t *testing.T) {
	client := newHTTPClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, accountFixture) })
	if _, err := client.Allocate(context.Background(), "installation", "tcp", "localhost", 3000, "key"); err == nil {
		t.Fatal("anonymous allocation accepted account run")
	}
}

func TestBootstrapRejectsPartialOrInsecureOIDCMetadata(t *testing.T) {
	for _, oidc := range []string{`{"issuer":"https://auth.test","client_id":"nt"}`, `{"issuer":"http://auth.test","client_id":"nt","resource":"api"}`, `{"issuer":"https://user:secret@auth.test","client_id":"nt","resource":"api"}`} {
		client := newHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"frp":{"server_addr":"tunnel.test","server_port":7000,"tls_server_name":"tunnel.test","trusted_ca_pem":"public-cert"},"oidc":%s}`, oidc)
		})
		if _, err := client.Bootstrap(context.Background()); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe OIDC metadata: %v", err)
		}
	}
}

func TestAmbiguousAllocationRetryReusesExactBodyAndKey(t *testing.T) {
	for _, mode := range []string{"start", "redeem", "allocate"} {
		t.Run(mode, func(t *testing.T) {
			var calls int
			var firstBody, firstKey string
			client := newHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if calls == 1 {
					firstBody, firstKey = string(body), r.Header.Get("Idempotency-Key")
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = connection.Close()
					return
				}
				if string(body) != firstBody || r.Header.Get("Idempotency-Key") != firstKey {
					t.Error("retry changed identity or body")
				}
				if mode == "allocate" {
					fmt.Fprint(w, anonymousFixture)
				} else {
					fmt.Fprint(w, accountFixture)
				}
			})
			var err error
			switch mode {
			case "start":
				_, err = client.Start(context.Background(), "rte_test", "account-secret", "fixed-key")
			case "redeem":
				_, err = client.Redeem(context.Background(), "launch-secret", "fixed-nonce")
			case "allocate":
				_, err = client.Allocate(context.Background(), "i", "tcp", "localhost", 3000, "fixed-key")
			}
			if err != nil || calls != 2 {
				t.Fatalf("retry result: %v calls=%d", err, calls)
			}
		})
	}
}

func TestHTTPFailuresAreNotRetriedAndTransportErrorsAreRedacted(t *testing.T) {
	var calls int
	client := newHTTPClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) })
	_, err := client.Start(context.Background(), "rte_test", "secret", "k")
	if err == nil || calls != 1 {
		t.Fatalf("HTTP error retry count=%d err=%v", calls, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := server.URL + "/api/v1"
	server.Close()
	client, _ = New(Options{BaseURL: baseURL})
	_, err = client.Routes(context.Background(), "secret")
	if err == nil || strings.Contains(err.Error(), baseURL) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("transport detail leaked: %v", err)
	}
}
