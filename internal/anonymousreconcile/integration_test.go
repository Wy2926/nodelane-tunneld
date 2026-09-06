package anonymousreconcile_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/anonymousapi"
	"github.com/Wy2926/nodelane-tunneld/internal/anonymousreconcile"
	"github.com/Wy2926/nodelane-tunneld/internal/frpanonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
	"github.com/redis/go-redis/v9"
)

func TestRealRedisAnonymousHTTPLifecycleUsesGuardedNativeEvidence(t *testing.T) {
	f := newLifecycleFixture(t)
	api, err := anonymousapi.New(anonymousapi.Options{
		Store: f.store,
		SourceIP: func(*http.Request) (netip.Addr, error) {
			return netip.MustParseAddr("192.0.2.15"), nil
		},
		Banned: func(context.Context, netip.Addr) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	allocationBody := map[string]any{
		"installation_id": "integration-installation", "protocol": "http", "local_host": "127.0.0.1", "local_port": 3000,
	}
	response := lifecycleRequest(t, api.Handler(), "/api/v1/anonymous/runs", allocationBody, "first-start", "")
	if response.Code != http.StatusCreated {
		t.Fatalf("allocation status = %d", response.Code)
	}
	var allocation struct {
		Run struct {
			ID                string    `json:"id"`
			ProxyName         string    `json:"proxy_name"`
			PublicEndpoint    string    `json:"public_endpoint"`
			ConnectDeadlineAt time.Time `json:"connect_deadline_at"`
			HardExpiresAt     time.Time `json:"hard_expires_at"`
		} `json:"run"`
		CredentialToken string `json:"credential_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &allocation); err != nil {
		t.Fatal(err)
	}
	proof := map[string]string{frpplugin.MetadataRunID: allocation.Run.ID, frpplugin.MetadataRunToken: allocation.CredentialToken}
	dispatcher, err := frpanonymous.New(f.store, "1MB")
	if err != nil {
		t.Fatal(err)
	}
	login := lifecycleDispatch(t, dispatcher, frpplugin.OpLogin, frpplugin.LoginContent{Metas: proof})
	if login.Reject {
		t.Fatal("valid anonymous login was rejected")
	}
	loginContent, ok := login.Content.(frpplugin.LoginContent)
	if !ok || loginContent.RunID != allocation.Run.ID || loginContent.ClientID != allocation.Run.ID {
		t.Fatal("login did not bind the native client identity to the anonymous run")
	}
	user := frpplugin.UserInfo{RunID: allocation.Run.ID, Metas: proof}
	label, _, _ := strings.Cut(allocation.Run.PublicEndpoint, ".")
	newProxy := frpplugin.NewProxyContent{User: user, ProxyName: allocation.Run.ProxyName, ProxyType: "http", Subdomain: label}
	if lifecycleDispatch(t, dispatcher, frpplugin.OpNewProxy, newProxy).Reject {
		t.Fatal("valid anonymous NewProxy was rejected")
	}
	run, err := f.store.Authorize(context.Background(), allocation.Run.ID, allocation.CredentialToken, allocation.Run.ProxyName)
	if err != nil || run.State != anonymous.StateReserved || !run.LeaseExpiresAt.IsZero() {
		t.Fatalf("NewProxy authorization prematurely marked online: state=%q, err=%v", run.State, err)
	}
	heartbeatPath := "/api/v1/runs/" + allocation.Run.ID + "/heartbeat"
	response = lifecycleRequest(t, api.Handler(), heartbeatPath, map[string]any{}, "", allocation.CredentialToken)
	if response.Code != http.StatusOK || bytes.Contains(response.Body.Bytes(), []byte("lease_expires_at")) {
		t.Fatal("reserved heartbeat invented an online lease")
	}

	expected := frpevidence.Expected{RunID: allocation.Run.ID, ProxyName: allocation.Run.ProxyName, Protocol: "http"}
	native := newNativeLifecycleFixture(t, expected)
	coordinator := newCoordinator(t, f.store, native.client, nil)
	if _, err := coordinator.ObserveConnected(context.Background(), expected); !errors.Is(err, anonymousreconcile.ErrObservationUnconfirmed) {
		t.Fatalf("missing native proxy marked connected: %v", err)
	}
	native.phase.Store("online")
	online, err := coordinator.ObserveConnected(context.Background(), expected)
	if err != nil || online.State != anonymous.StateOnline || !online.LeaseExpiresAt.Equal(f.clock.Now().Add(90*time.Second)) {
		t.Fatalf("fresh native online observation failed: state=%q, err=%v", online.State, err)
	}
	f.clock.Advance(10 * time.Second)
	observedAgain, err := coordinator.ObserveConnected(context.Background(), expected)
	if err != nil || !observedAgain.LeaseExpiresAt.Equal(online.LeaseExpiresAt) || !observedAgain.HardExpiresAt.Equal(allocation.Run.HardExpiresAt) {
		t.Fatalf("repeated observation renewed an authorization deadline: %v", err)
	}
	response = lifecycleRequest(t, api.Handler(), heartbeatPath, map[string]any{}, "", allocation.CredentialToken)
	if response.Code != http.StatusOK {
		t.Fatalf("online heartbeat status = %d", response.Code)
	}
	var heartbeat struct {
		Run struct {
			LeaseExpiresAt time.Time `json:"lease_expires_at"`
			HardExpiresAt  time.Time `json:"hard_expires_at"`
		} `json:"run"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &heartbeat); err != nil || !heartbeat.Run.LeaseExpiresAt.Equal(f.clock.Now().Add(90*time.Second)) || !heartbeat.Run.HardExpiresAt.Equal(allocation.Run.HardExpiresAt) {
		t.Fatal("heartbeat did not preserve the original hard deadline")
	}

	stopPath := "/api/v1/runs/" + allocation.Run.ID + "/stop"
	for attempt := 0; attempt < 2; attempt++ {
		response = lifecycleRequest(t, api.Handler(), stopPath, map[string]any{}, "", allocation.CredentialToken)
		if response.Code != http.StatusOK {
			t.Fatalf("idempotent stop status = %d", response.Code)
		}
	}
	if !lifecycleDispatch(t, dispatcher, frpplugin.OpPing, frpplugin.PingContent{User: user}).Reject || !lifecycleDispatch(t, dispatcher, frpplugin.OpNewProxy, newProxy).Reject {
		t.Fatal("stopped run retained frps authorization")
	}
	response = lifecycleRequest(t, api.Handler(), "/api/v1/anonymous/runs", allocationBody, "still-held", "")
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("unconfirmed resource released its installation slot: %d", response.Code)
	}
	native.phase.Store("offline")
	report, err := coordinator.Reconcile(context.Background(), 10)
	if err != nil || report != (anonymousreconcile.Report{Inspected: 1, Held: 1}) {
		t.Fatalf("missing drain proof did not hold resources: %#v, %v", report, err)
	}
	response = lifecycleRequest(t, api.Handler(), "/api/v1/anonymous/runs", allocationBody, "after-held-observation", "")
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("offline sample without drain proof released the slot: %d", response.Code)
	}

	// This is a test-owned drain proof, not proof that stock frps has drained.
	guarded := newCoordinator(t, f.store, native.client, &lifecycleDrainProof{expected: expected})
	f.clock.Advance(16 * time.Second)
	report, err = guarded.Reconcile(context.Background(), 10)
	if err != nil || report != (anonymousreconcile.Report{Inspected: 1, Released: 1}) {
		t.Fatalf("trusted proof and offline sample failed to release: %#v, %v", report, err)
	}
	response = lifecycleRequest(t, api.Handler(), stopPath, map[string]any{}, "", allocation.CredentialToken)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"state":"released"`)) {
		t.Fatalf("released stop was not idempotent: %d", response.Code)
	}
	response = lifecycleRequest(t, api.Handler(), "/api/v1/anonymous/runs", allocationBody, "after-confirmation", "")
	if response.Code != http.StatusCreated {
		t.Fatalf("confirmed release did not free the installation slot: %d", response.Code)
	}
	var replacement struct {
		Run struct {
			ID        string `json:"id"`
			ProxyName string `json:"proxy_name"`
		} `json:"run"`
		CredentialToken string `json:"credential_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &replacement); err != nil || replacement.Run.ID == allocation.Run.ID {
		t.Fatal("replacement allocation did not receive a fresh run identity")
	}
	response = lifecycleRequest(t, api.Handler(), stopPath, map[string]any{}, "", allocation.CredentialToken)
	if response.Code != http.StatusOK {
		t.Fatalf("late old stop status = %d", response.Code)
	}
	if _, err := f.store.Authorize(context.Background(), replacement.Run.ID, replacement.CredentialToken, replacement.Run.ProxyName); err != nil {
		t.Fatalf("late old stop affected the replacement: %v", err)
	}
}

func TestRealRedisOnlineObservationCannotBypassConnectionDeadline(t *testing.T) {
	f := newLifecycleFixture(t)
	allocation, err := f.store.Allocate(context.Background(), anonymous.AllocateRequest{
		InstallationID: "deadline-installation", NetworkKey: "192.0.2.19", IdempotencyKey: "deadline-start",
		Protocol: anonymous.ProtocolHTTP, LocalHost: "127.0.0.1", LocalPort: 3000,
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := frpevidence.Expected{RunID: allocation.RunID, ProxyName: allocation.ProxyName, Protocol: "http"}
	native := newNativeLifecycleFixture(t, expected)
	native.phase.Store("online")
	f.clock.Advance(2 * time.Minute)
	run, err := newCoordinator(t, f.store, native.client, nil).ObserveConnected(context.Background(), expected)
	if !errors.Is(err, anonymous.ErrRunStopped) || run != (anonymous.Run{}) {
		t.Fatalf("late online sample bypassed original deadline: state=%q, err=%v", run.State, err)
	}
}

type lifecycleDrainProof struct {
	expected frpevidence.Expected
}

func (g *lifecycleDrainProof) CanConfirmRelease(_ context.Context, item anonymous.VerificationItem) (bool, error) {
	return item.RunID == g.expected.RunID && item.ProxyName == g.expected.ProxyName && string(item.Protocol) == g.expected.Protocol, nil
}

type nativeLifecycleFixture struct {
	client *frpevidence.Client
	phase  atomic.Value
}

func newNativeLifecycleFixture(t *testing.T, expected frpevidence.Expected) *nativeLifecycleFixture {
	t.Helper()
	f := &nativeLifecycleFixture{}
	f.phase.Store("not_observed")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "reader" || password != "fixture-only" || request.Method != http.MethodGet || request.URL.Path != "/api/v2/proxies/"+expected.ProxyName || request.URL.RawQuery != "" {
			t.Error("unexpected native evidence request")
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		phase := f.phase.Load().(string)
		if phase == "not_observed" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 404, "data": nil})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{
				"name": expected.ProxyName, "clientID": expected.RunID,
				"spec": map[string]any{"type": expected.Protocol}, "status": map[string]any{"phase": phase, "curConns": 0},
			},
		})
	}))
	t.Cleanup(server.Close)
	var err error
	f.client, err = frpevidence.NewClient(frpevidence.Options{Endpoint: server.URL, Username: "reader", Password: "fixture-only", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func lifecycleRequest(t *testing.T, handler http.Handler, path string, body any, key, token string) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func lifecycleDispatch(t *testing.T, dispatcher *frpanonymous.Dispatcher, op frpplugin.Operation, content any) frpplugin.Response {
	t.Helper()
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	response, err := dispatcher.Dispatch(context.Background(), frpplugin.Request{Version: frpplugin.APIVersion, Op: op, Content: encoded})
	if err != nil {
		t.Fatalf("callback %s error: %v", op, err)
	}
	return response
}

type lifecycleClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *lifecycleClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *lifecycleClock) Advance(elapsed time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(elapsed)
	c.mu.Unlock()
}

type lifecycleFixture struct {
	store *anonymous.Store
	clock *lifecycleClock
}

func lifecycleRedisOptions(raw string) (*redis.Options, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" ||
		(parsed.Scheme != "redis" && parsed.Scheme != "rediss") || parsed.Path != "/15" {
		return nil, errors.New("unsafe Redis fixture URL")
	}
	options, err := redis.ParseURL(raw)
	if err != nil || options.Network != "tcp" || options.DB != 15 {
		return nil, errors.New("unsafe Redis fixture URL")
	}
	host, _, err := net.SplitHostPort(options.Addr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return nil, errors.New("unsafe Redis fixture URL")
	}
	options.MaxRetries = -1
	options.DialTimeout = time.Second
	options.ReadTimeout = time.Second
	options.WriteTimeout = time.Second
	return options, nil
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	raw := os.Getenv("NODELANE_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("NODELANE_TEST_REDIS_URL is required for real Redis tests")
	}
	options, err := lifecycleRedisOptions(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	marker, err := client.Get(ctx, "nodelane:test:marker").Result()
	if err != nil || marker != "bff_fixture_v1" {
		t.Fatal("Redis fixture marker is missing or invalid")
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	prefix := "ctl_test:anonymous-reconcile:" + hex.EncodeToString(random)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var cursor uint64
		for {
			keys, next, err := client.Scan(ctx, cursor, prefix+":*", 100).Result()
			if err != nil {
				t.Error("scan owned fixture keys failed")
				return
			}
			for _, key := range keys {
				if !strings.HasPrefix(key, prefix+":") {
					t.Error("refusing to remove an unowned fixture key")
					return
				}
			}
			if len(keys) != 0 && client.Unlink(ctx, keys...).Err() != nil {
				t.Error("remove owned fixture keys failed")
				return
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	})
	clock := &lifecycleClock{now: time.Date(2026, 9, 6, 1, 2, 3, 456000000, time.UTC)}
	store, err := anonymous.NewStore(anonymous.Config{
		Client: client, Prefix: prefix,
		CredentialPepper: []byte("credential-pepper-is-independent-32"),
		ReplayKey:        []byte("replay-key-is-exactly-32-bytes!!"),
		FenceOwnerToken:  []byte("fence-owner-token-is-independent-32"),
		Clock:            clock.Now, Random: rand.Reader, PublicDomain: "tunnel.test",
		TCPPorts: []uint16{21001}, UDPPorts: []uint16{22001},
	})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := store.ObserveResourceFence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkResourcesVerified(ctx, fence); err != nil {
		t.Fatal(err)
	}
	return &lifecycleFixture{store: store, clock: clock}
}

func TestLifecycleRedisFixtureRejectsNonIsolatedTargets(t *testing.T) {
	for _, raw := range []string{
		"redis://127.0.0.1:6379/0", "redis://127.0.0.1:6379/15?db=0", "redis://localhost:6379/15",
		"redis://192.0.2.1:6379/15", "redis://127.0.0.1:6379/15#fragment", "redis://127.0.0.1:6379/%31%35",
	} {
		if _, err := lifecycleRedisOptions(raw); err == nil {
			t.Fatalf("unsafe fixture target accepted: %q", raw)
		}
	}
}
