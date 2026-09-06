package anonymousapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
)

const (
	testRunID     = "anr_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	testProxyName = "anon_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	testToken     = "nac_aaaaaaaaaaaaaaaaaaaaaaaaaa.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

type fakeStore struct {
	allocate  func(context.Context, anonymous.AllocateRequest) (anonymous.Allocation, error)
	heartbeat func(context.Context, string, string) (anonymous.HeartbeatResult, error)
	stop      func(context.Context, string, string) (anonymous.Run, error)
}

func (s *fakeStore) Allocate(ctx context.Context, request anonymous.AllocateRequest) (anonymous.Allocation, error) {
	return s.allocate(ctx, request)
}

func (s *fakeStore) Heartbeat(ctx context.Context, runID, token string) (anonymous.HeartbeatResult, error) {
	return s.heartbeat(ctx, runID, token)
}

func (s *fakeStore) RequestStop(ctx context.Context, runID, token string) (anonymous.Run, error) {
	return s.stop(ctx, runID, token)
}

func TestAllocateDerivesNetworkIdentityAndReturnsExplicitCredential(t *testing.T) {
	now := time.Date(2026, 9, 6, 1, 2, 3, 456000000, time.UTC)
	var captured anonymous.AllocateRequest
	store := &fakeStore{
		allocate: func(_ context.Context, request anonymous.AllocateRequest) (anonymous.Allocation, error) {
			captured = request
			return anonymous.Allocation{
				RunID: testRunID, ProxyName: testProxyName, PublicEndpoint: "anon-aaaaaaaaaaaaaaaaaaaaaaaaaa.tunnel.test",
				CredentialToken: testToken, Protocol: anonymous.ProtocolHTTP, CreatedAt: now,
				ConnectDeadlineAt: now.Add(2 * time.Minute), HardExpiresAt: now.Add(time.Hour),
			}, nil
		},
		heartbeat: unexpectedHeartbeat(t), stop: unexpectedStop(t),
	}
	server := newTestServer(t, store, netip.MustParseAddr("2001:db8:1234:5678:abcd::1"), false, nil)
	recorder := request(t, server.Handler(), http.MethodPost, "/api/v1/anonymous/runs", `{"installation_id":"install-1","protocol":"http","local_host":"localhost","local_port":3000}`, func(r *http.Request) {
		r.Header.Set("Idempotency-Key", " allocation-1 ")
	})

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if captured.InstallationID != "install-1" || captured.NetworkKey != "2001:db8:1234:5678::/64" || captured.IdempotencyKey != "allocation-1" || captured.Protocol != anonymous.ProtocolHTTP || captured.LocalHost != "localhost" || captured.LocalPort != 3000 {
		t.Fatalf("allocate request=%#v", captured)
	}
	if recorder.Header().Get("X-Request-ID") == "" || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers=%v", recorder.Header())
	}
	var body struct {
		Run struct {
			ID             string `json:"id"`
			State          string `json:"state"`
			DesiredState   string `json:"desired_state"`
			ProxyName      string `json:"proxy_name"`
			PublicEndpoint string `json:"public_endpoint"`
		} `json:"run"`
		CredentialToken string `json:"credential_token"`
		Replayed        bool   `json:"replayed"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Run.ID != testRunID || body.Run.State != "reserved" || body.Run.DesiredState != "running" || body.Run.ProxyName != testProxyName || body.CredentialToken != testToken || body.Replayed {
		t.Fatalf("response=%#v", body)
	}
}

func TestAllocateRechecksBanBeforeStoreAndReplayIsOK(t *testing.T) {
	called := false
	store := &fakeStore{
		allocate: func(context.Context, anonymous.AllocateRequest) (anonymous.Allocation, error) {
			called = true
			now := time.Date(2026, 9, 6, 1, 2, 3, 0, time.UTC)
			return anonymous.Allocation{
				RunID: testRunID, ProxyName: testProxyName, PublicEndpoint: "tunnel.test:21001",
				CredentialToken: testToken, Protocol: anonymous.ProtocolTCP, CreatedAt: now,
				ConnectDeadlineAt: now.Add(2 * time.Minute), HardExpiresAt: now.Add(time.Hour), Replayed: true,
			}, nil
		},
		heartbeat: unexpectedHeartbeat(t), stop: unexpectedStop(t),
	}
	server := newTestServer(t, store, netip.MustParseAddr("192.0.2.4"), true, nil)
	recorder := request(t, server.Handler(), http.MethodPost, "/api/v1/anonymous/runs", `{"installation_id":"install-1","protocol":"tcp","local_host":"127.0.0.1","local_port":80}`, func(r *http.Request) {
		r.Header.Set("Idempotency-Key", "same-retry")
	})
	if recorder.Code != http.StatusForbidden || errorCode(t, recorder) != "ip_banned" || called {
		t.Fatalf("status=%d code=%s store_called=%v", recorder.Code, errorCode(t, recorder), called)
	}

	server = newTestServer(t, store, netip.MustParseAddr("192.0.2.4"), false, nil)
	recorder = request(t, server.Handler(), http.MethodPost, "/api/v1/anonymous/runs", `{"installation_id":"install-1","protocol":"tcp","local_host":"127.0.0.1","local_port":80}`, func(r *http.Request) {
		r.Header.Set("Idempotency-Key", "same-retry")
	})
	if recorder.Code != http.StatusOK || !called {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAllocateRejectsAmbiguousOrUnexpectedTransportInputs(t *testing.T) {
	store := &fakeStore{
		allocate: func(context.Context, anonymous.AllocateRequest) (anonymous.Allocation, error) {
			t.Fatal("store called")
			return anonymous.Allocation{}, nil
		},
		heartbeat: unexpectedHeartbeat(t), stop: unexpectedStop(t),
	}
	server := newTestServer(t, store, netip.MustParseAddr("192.0.2.4"), false, nil)
	tests := []struct {
		name   string
		path   string
		body   string
		mutate func(*http.Request)
	}{
		{name: "query", path: "/api/v1/anonymous/runs?x=1", body: `{}`, mutate: func(r *http.Request) { r.Header.Set("Idempotency-Key", "x") }},
		{name: "authorization", path: "/api/v1/anonymous/runs", body: `{}`, mutate: func(r *http.Request) {
			r.Header.Set("Idempotency-Key", "x")
			r.Header.Set("Authorization", "Bearer "+testToken)
		}},
		{name: "missing idempotency", path: "/api/v1/anonymous/runs", body: `{}`, mutate: func(*http.Request) {}},
		{name: "duplicate idempotency", path: "/api/v1/anonymous/runs", body: `{}`, mutate: func(r *http.Request) { r.Header.Add("Idempotency-Key", "x"); r.Header.Add("Idempotency-Key", "y") }},
		{name: "unknown field", path: "/api/v1/anonymous/runs", body: `{"installation_id":"x","protocol":"http","local_host":"localhost","local_port":1,"extra":true}`, mutate: func(r *http.Request) { r.Header.Set("Idempotency-Key", "x") }},
		{name: "duplicate field", path: "/api/v1/anonymous/runs", body: `{"installation_id":"x","installation_id":"y","protocol":"http","local_host":"localhost","local_port":1}`, mutate: func(r *http.Request) { r.Header.Set("Idempotency-Key", "x") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := request(t, server.Handler(), http.MethodPost, test.path, test.body, test.mutate)
			if recorder.Code != http.StatusBadRequest || errorCode(t, recorder) != "invalid_request" {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestHeartbeatAndStopRequireMatchingAnonymousBearer(t *testing.T) {
	now := time.Date(2026, 9, 6, 1, 2, 3, 0, time.UTC)
	heartbeats, stops := 0, 0
	store := &fakeStore{
		allocate: func(context.Context, anonymous.AllocateRequest) (anonymous.Allocation, error) {
			t.Fatal("allocate called")
			return anonymous.Allocation{}, nil
		},
		heartbeat: func(_ context.Context, runID, token string) (anonymous.HeartbeatResult, error) {
			heartbeats++
			if runID != testRunID || token != testToken {
				t.Fatalf("heartbeat proof=%q %q", runID, token)
			}
			return anonymous.HeartbeatResult{RunID: runID, DesiredState: anonymous.DesiredRunning, LeaseExpiresAt: now.Add(90 * time.Second), HardExpiresAt: now.Add(time.Hour)}, nil
		},
		stop: func(_ context.Context, runID, token string) (anonymous.Run, error) {
			stops++
			if runID != testRunID || token != testToken {
				t.Fatalf("stop proof=%q %q", runID, token)
			}
			return anonymous.Run{RunID: runID, ProxyName: testProxyName, PublicEndpoint: "anon-aaaaaaaaaaaaaaaaaaaaaaaaaa.tunnel.test", Protocol: anonymous.ProtocolHTTP, State: anonymous.StateStopping, DesiredState: anonymous.DesiredStopped, CreatedAt: now, HardExpiresAt: now.Add(time.Hour)}, nil
		},
	}
	server := newTestServer(t, store, netip.MustParseAddr("192.0.2.4"), false, nil)
	for _, operation := range []string{"heartbeat", "stop"} {
		recorder := request(t, server.Handler(), http.MethodPost, "/api/v1/runs/"+testRunID+"/"+operation, `{}`, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+testToken)
		})
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"stopped":`+map[bool]string{true: "true", false: "false"}[operation == "stop"]) {
			t.Fatalf("%s status=%d body=%s", operation, recorder.Code, recorder.Body.String())
		}
	}
	if heartbeats != 1 || stops != 1 {
		t.Fatalf("heartbeat=%d stop=%d", heartbeats, stops)
	}

	for _, authorization := range []string{"", "Bearer nrc_aaaaaaaaaaaaaaaaaaaaaaaaaa.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "Bearer " + testToken + " extra"} {
		recorder := request(t, server.Handler(), http.MethodPost, "/api/v1/runs/"+testRunID+"/heartbeat", `{}`, func(r *http.Request) {
			if authorization != "" {
				r.Header.Set("Authorization", authorization)
			}
		})
		if recorder.Code != http.StatusUnauthorized || errorCode(t, recorder) != "unauthorized" {
			t.Fatalf("authorization=%q status=%d body=%s", authorization, recorder.Code, recorder.Body.String())
		}
	}
}

func TestStableAnonymousErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
		retry  string
	}{
		{name: "invalid", err: anonymous.ErrInvalidRequest, status: 400, code: "invalid_request"},
		{name: "idempotency", err: anonymous.ErrIdempotencyConflict, status: 409, code: "idempotency_conflict"},
		{name: "installation", err: anonymous.ErrInstallationLimit, status: 409, code: "anonymous_run_limit_reached"},
		{name: "network", err: anonymous.ErrNetworkLimit, status: 409, code: "anonymous_run_limit_reached"},
		{name: "rate", err: &anonymous.RateLimitError{Scope: anonymous.LimitInstallation, RetryAfter: 1500 * time.Millisecond}, status: 429, code: "rate_limited", retry: "2"},
		{name: "resources", err: anonymous.ErrResourcesUnverified, status: 503, code: "dependency_unavailable"},
		{name: "unavailable", err: anonymous.ErrUnavailable, status: 503, code: "dependency_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{
				allocate: func(context.Context, anonymous.AllocateRequest) (anonymous.Allocation, error) {
					return anonymous.Allocation{}, test.err
				},
				heartbeat: unexpectedHeartbeat(t), stop: unexpectedStop(t),
			}
			server := newTestServer(t, store, netip.MustParseAddr("192.0.2.4"), false, nil)
			recorder := request(t, server.Handler(), http.MethodPost, "/api/v1/anonymous/runs", `{"installation_id":"install-1","protocol":"http","local_host":"localhost","local_port":3000}`, func(r *http.Request) { r.Header.Set("Idempotency-Key", "x") })
			if recorder.Code != test.status || errorCode(t, recorder) != test.code || recorder.Header().Get("Retry-After") != test.retry {
				t.Fatalf("status=%d code=%s retry=%q body=%s", recorder.Code, errorCode(t, recorder), recorder.Header().Get("Retry-After"), recorder.Body.String())
			}
		})
	}
}

func TestDependencyAndCredentialFailuresFailClosed(t *testing.T) {
	store := &fakeStore{
		allocate: func(context.Context, anonymous.AllocateRequest) (anonymous.Allocation, error) {
			return anonymous.Allocation{}, nil
		},
		heartbeat: func(context.Context, string, string) (anonymous.HeartbeatResult, error) {
			return anonymous.HeartbeatResult{}, anonymous.ErrInvalidCredential
		},
		stop: func(context.Context, string, string) (anonymous.Run, error) {
			return anonymous.Run{}, anonymous.ErrRunStopped
		},
	}
	server := newTestServer(t, store, netip.MustParseAddr("192.0.2.4"), false, nil)
	heartbeat := request(t, server.Handler(), http.MethodPost, "/api/v1/runs/"+testRunID+"/heartbeat", `{}`, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testToken) })
	if heartbeat.Code != http.StatusUnauthorized || errorCode(t, heartbeat) != "unauthorized" {
		t.Fatalf("heartbeat=%d %s", heartbeat.Code, heartbeat.Body.String())
	}
	stop := request(t, server.Handler(), http.MethodPost, "/api/v1/runs/"+testRunID+"/stop", `{}`, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testToken) })
	if stop.Code != http.StatusGone || errorCode(t, stop) != "run_stopped" {
		t.Fatalf("stop=%d %s", stop.Code, stop.Body.String())
	}

	server = newTestServer(t, store, netip.Addr{}, false, errors.New("proxy chain invalid"))
	allocation := request(t, server.Handler(), http.MethodPost, "/api/v1/anonymous/runs", `{"installation_id":"install-1","protocol":"http","local_host":"localhost","local_port":3000}`, func(r *http.Request) { r.Header.Set("Idempotency-Key", "x") })
	if allocation.Code != http.StatusServiceUnavailable || errorCode(t, allocation) != "dependency_unavailable" {
		t.Fatalf("allocation=%d %s", allocation.Code, allocation.Body.String())
	}
}

func newTestServer(t *testing.T, store Store, ip netip.Addr, banned bool, sourceErr error) *Server {
	t.Helper()
	server, err := New(Options{
		Store:    store,
		SourceIP: func(*http.Request) (netip.Addr, error) { return ip, sourceErr },
		Banned:   func(context.Context, netip.Addr) (bool, error) { return banned, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func request(t *testing.T, handler http.Handler, method, target, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.RequestID == "" || body.Error.RequestID != recorder.Header().Get("X-Request-ID") {
		t.Fatalf("request id mismatch: %#v headers=%v", body, recorder.Header())
	}
	return body.Error.Code
}

func unexpectedHeartbeat(t *testing.T) func(context.Context, string, string) (anonymous.HeartbeatResult, error) {
	t.Helper()
	return func(context.Context, string, string) (anonymous.HeartbeatResult, error) {
		t.Fatal("heartbeat called")
		return anonymous.HeartbeatResult{}, nil
	}
}

func unexpectedStop(t *testing.T) func(context.Context, string, string) (anonymous.Run, error) {
	t.Helper()
	return func(context.Context, string, string) (anonymous.Run, error) {
		t.Fatal("stop called")
		return anonymous.Run{}, nil
	}
}
