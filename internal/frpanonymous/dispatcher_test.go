package frpanonymous

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
)

const (
	testRunID    = "anr_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	testProxy    = "anon_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	testRunToken = "nac_cccccccccccccccccccccccccc.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

type proof struct {
	runID     string
	token     string
	proxyName string
}

type recordingStore struct {
	run        anonymous.Run
	err        error
	loginCalls []proof
	proxyCalls []proof
}

func (s *recordingStore) AuthorizeLogin(_ context.Context, runID, token string) (anonymous.Run, error) {
	s.loginCalls = append(s.loginCalls, proof{runID: runID, token: token})
	return s.run, s.err
}

func (s *recordingStore) Authorize(_ context.Context, runID, token, proxyName string) (anonymous.Run, error) {
	s.proxyCalls = append(s.proxyCalls, proof{runID: runID, token: token, proxyName: proxyName})
	return s.run, s.err
}

func TestLoginNormalizesAnonymousSessionIdentity(t *testing.T) {
	store := &recordingStore{run: testRun(anonymous.ProtocolHTTP)}
	dispatcher := mustDispatcher(t, store)
	input := frpplugin.LoginContent{
		Version: "0.70.0", Hostname: "developer-machine", OS: "windows", Arch: "amd64",
		RunID: "client-selected-run", ClientID: "client-selected-id", Metas: testMetas(), PoolCount: 2,
	}

	response, err := dispatcher.Dispatch(context.Background(), request(t, frpplugin.OpLogin, input))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if response.Reject || response.Unchange {
		t.Fatalf("response = %#v, want allowed changed Login", response)
	}
	got, ok := response.Content.(frpplugin.LoginContent)
	if !ok {
		t.Fatalf("response content type = %T", response.Content)
	}
	if got.RunID != testRunID || got.ClientID != testRunID {
		t.Fatalf("normalized identity = %q/%q, want %q", got.RunID, got.ClientID, testRunID)
	}
	if !reflect.DeepEqual(got.Metas, testMetas()) {
		t.Fatalf("Login metadata = %#v; frps must retain it for later callback reauthorization", got.Metas)
	}
	if got.Version != input.Version || got.Hostname != input.Hostname || got.PoolCount != input.PoolCount {
		t.Fatalf("unrelated Login fields changed: %#v", got)
	}
	if len(store.loginCalls) != 1 || store.loginCalls[0] != (proof{runID: testRunID, token: testRunToken}) {
		t.Fatalf("AuthorizeLogin calls = %#v", store.loginCalls)
	}
}

func TestEveryAnonymousCallbackReauthorizesFreshStoreState(t *testing.T) {
	tests := []struct {
		name           string
		op             frpplugin.Operation
		content        any
		wantLoginCalls int
		wantProxyCalls int
	}{
		{name: "Login", op: frpplugin.OpLogin, content: frpplugin.LoginContent{Metas: testMetas()}, wantLoginCalls: 1},
		{name: "Ping", op: frpplugin.OpPing, content: frpplugin.PingContent{User: testUser()}, wantLoginCalls: 1},
		{name: "NewWorkConn", op: frpplugin.OpNewWorkConn, content: frpplugin.NewWorkConnContent{User: testUser(), RunID: testRunID}, wantLoginCalls: 1},
		{name: "NewProxy", op: frpplugin.OpNewProxy, content: validHTTPProxy(), wantProxyCalls: 1},
		{name: "NewUserConn", op: frpplugin.OpNewUserConn, content: frpplugin.NewUserConnContent{User: testUser(), ProxyName: testProxy, ProxyType: "http", RemoteAddr: "192.0.2.1:4567"}, wantProxyCalls: 1},
		{name: "CloseProxy", op: frpplugin.OpCloseProxy, content: frpplugin.CloseProxyContent{User: testUser(), ProxyName: testProxy}, wantProxyCalls: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingStore{run: testRun(anonymous.ProtocolHTTP)}
			dispatcher := mustDispatcher(t, store)
			for call := 0; call < 2; call++ {
				response, err := dispatcher.Dispatch(context.Background(), request(t, test.op, test.content))
				if err != nil || response.Reject {
					t.Fatalf("Dispatch %d = (%#v, %v), want allowed", call+1, response, err)
				}
			}
			if len(store.loginCalls) != 2*test.wantLoginCalls || len(store.proxyCalls) != 2*test.wantProxyCalls {
				t.Fatalf("store calls = login:%#v proxy:%#v", store.loginCalls, store.proxyCalls)
			}
		})
	}
}

func TestCredentialMetadataIsExactAndBounded(t *testing.T) {
	tests := []struct {
		name  string
		metas map[string]string
	}{
		{name: "missing run id", metas: map[string]string{frpplugin.MetadataRunToken: testRunToken}},
		{name: "missing token", metas: map[string]string{frpplugin.MetadataRunID: testRunID}},
		{name: "extra legacy token", metas: map[string]string{frpplugin.MetadataRunID: testRunID, frpplugin.MetadataRunToken: testRunToken, "tunnel_token": "secret"}},
		{name: "account access token", metas: map[string]string{frpplugin.MetadataRunID: testRunID, frpplugin.MetadataRunToken: testRunToken, "authorization": "Bearer secret"}},
		{name: "wrong run namespace", metas: map[string]string{frpplugin.MetadataRunID: "run_aaaaaaaaaaaaaaaaaaaaaaaaaa", frpplugin.MetadataRunToken: testRunToken}},
		{name: "wrong token namespace", metas: map[string]string{frpplugin.MetadataRunID: testRunID, frpplugin.MetadataRunToken: "nrc_cccccccccccccccccccccccccc.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}},
		{name: "noncanonical token secret", metas: map[string]string{frpplugin.MetadataRunID: testRunID, frpplugin.MetadataRunToken: "nac_cccccccccccccccccccccccccc.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}},
		{name: "whitespace", metas: map[string]string{frpplugin.MetadataRunID: " " + testRunID, frpplugin.MetadataRunToken: testRunToken}},
		{name: "oversized token", metas: map[string]string{frpplugin.MetadataRunID: testRunID, frpplugin.MetadataRunToken: "nac_" + strings.Repeat("a", 5000)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingStore{run: testRun(anonymous.ProtocolHTTP)}
			dispatcher := mustDispatcher(t, store)
			response, err := dispatcher.Dispatch(context.Background(), request(t, frpplugin.OpLogin, frpplugin.LoginContent{Metas: test.metas}))
			if err != nil || !response.Reject || response.RejectReason != InvalidCredentialReason || !response.Unchange {
				t.Fatalf("Dispatch = (%#v, %v), want invalid credential rejection", response, err)
			}
			if len(store.loginCalls) != 0 || len(store.proxyCalls) != 0 {
				t.Fatalf("invalid metadata reached store: login=%#v proxy=%#v", store.loginCalls, store.proxyCalls)
			}
		})
	}
}

func TestNewProxyUsesOnlyAuthorizedAllocationAndServerBandwidth(t *testing.T) {
	tests := []struct {
		name       string
		protocol   anonymous.Protocol
		input      frpplugin.NewProxyContent
		wantDomain string
		wantPort   int
	}{
		{name: "HTTP", protocol: anonymous.ProtocolHTTP, input: validHTTPProxy(), wantDomain: "anon-dddddddddddddddddddddddddd"},
		{name: "TCP", protocol: anonymous.ProtocolTCP, input: validPortProxy("tcp", 30001), wantPort: 30001},
		{name: "UDP", protocol: anonymous.ProtocolUDP, input: validPortProxy("udp", 30002), wantPort: 30002},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingStore{run: testRun(test.protocol)}
			dispatcher := mustDispatcher(t, store)
			response, err := dispatcher.Dispatch(context.Background(), request(t, frpplugin.OpNewProxy, test.input))
			if err != nil || response.Reject || response.Unchange {
				t.Fatalf("Dispatch = (%#v, %v), want allowed changed", response, err)
			}
			got, ok := response.Content.(frpplugin.NewProxyContent)
			if !ok {
				t.Fatalf("content type = %T", response.Content)
			}
			if got.ProxyName != testProxy || got.ProxyType != string(test.protocol) || got.Subdomain != test.wantDomain || got.RemotePort != test.wantPort {
				t.Fatalf("allocation fields = %#v", got)
			}
			if got.BandwidthLimit != "2MB" || got.BandwidthLimitMode != "server" {
				t.Fatalf("bandwidth = %q/%q", got.BandwidthLimit, got.BandwidthLimitMode)
			}
			if got.User.Metas != nil {
				t.Fatalf("changed NewProxy content leaked credentials: %#v", got.User.Metas)
			}
			if !got.UseEncryption || !got.UseCompression {
				t.Fatal("transport encryption/compression flags changed")
			}
			encoded, marshalErr := json.Marshal(got)
			if marshalErr != nil || strings.Contains(string(encoded), testRunToken) {
				t.Fatalf("changed content leaked token: %s (%v)", encoded, marshalErr)
			}
		})
	}
}

func TestNewProxyRejectsExposureMutations(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*frpplugin.NewProxyContent)
	}{
		{name: "user namespace", mutate: func(c *frpplugin.NewProxyContent) { c.User.User = "account" }},
		{name: "wrong session", mutate: func(c *frpplugin.NewProxyContent) { c.User.RunID = "anr_zzzzzzzzzzzzzzzzzzzzzzzzzz" }},
		{name: "cross proxy", mutate: func(c *frpplugin.NewProxyContent) { c.ProxyName = "anon_eeeeeeeeeeeeeeeeeeeeeeeeee" }},
		{name: "different protocol", mutate: func(c *frpplugin.NewProxyContent) { c.ProxyType = "tcp" }},
		{name: "different subdomain", mutate: func(c *frpplugin.NewProxyContent) { c.Subdomain = "anon-eeeeeeeeeeeeeeeeeeeeeeeeee" }},
		{name: "custom domain", mutate: func(c *frpplugin.NewProxyContent) { c.CustomDomains = []string{"other.test"} }},
		{name: "remote port", mutate: func(c *frpplugin.NewProxyContent) { c.RemotePort = 443 }},
		{name: "group", mutate: func(c *frpplugin.NewProxyContent) { c.Group = "shared" }},
		{name: "group key", mutate: func(c *frpplugin.NewProxyContent) { c.GroupKey = "secret" }},
		{name: "locations", mutate: func(c *frpplugin.NewProxyContent) { c.Locations = []string{"/admin"} }},
		{name: "HTTP username", mutate: func(c *frpplugin.NewProxyContent) { c.HTTPUser = "user" }},
		{name: "HTTP password", mutate: func(c *frpplugin.NewProxyContent) { c.HTTPPwd = "password" }},
		{name: "host rewrite", mutate: func(c *frpplugin.NewProxyContent) { c.HostHeaderRewrite = "internal" }},
		{name: "request headers", mutate: func(c *frpplugin.NewProxyContent) { c.Headers = map[string]string{"x-added": "yes"} }},
		{name: "response headers", mutate: func(c *frpplugin.NewProxyContent) { c.ResponseHeaders = map[string]string{"x-added": "yes"} }},
		{name: "route by user", mutate: func(c *frpplugin.NewProxyContent) { c.RouteByHTTPUser = "user" }},
		{name: "visitor secret", mutate: func(c *frpplugin.NewProxyContent) { c.SecretKey = "secret" }},
		{name: "visitor allowlist", mutate: func(c *frpplugin.NewProxyContent) { c.AllowUsers = []string{"user"} }},
		{name: "multiplexer", mutate: func(c *frpplugin.NewProxyContent) { c.Multiplexer = "httpconnect" }},
		{name: "proxy metadata", mutate: func(c *frpplugin.NewProxyContent) { c.Metas = map[string]string{"route": "other"} }},
		{name: "annotation", mutate: func(c *frpplugin.NewProxyContent) { c.Annotations = map[string]string{"route": "other"} }},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			store := &recordingStore{run: testRun(anonymous.ProtocolHTTP)}
			dispatcher := mustDispatcher(t, store)
			content := validHTTPProxy()
			mutation.mutate(&content)
			response, err := dispatcher.Dispatch(context.Background(), request(t, frpplugin.OpNewProxy, content))
			if err != nil || !response.Reject || response.RejectReason != InvalidCredentialReason {
				t.Fatalf("Dispatch = (%#v, %v), want invalid credential", response, err)
			}
			if len(store.proxyCalls) != 1 {
				t.Fatalf("fresh authorization calls = %d, want 1", len(store.proxyCalls))
			}
		})
	}
}

func TestPortProxyRejectsHTTPAndPortMutations(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*frpplugin.NewProxyContent)
	}{
		{name: "wrong port", mutate: func(c *frpplugin.NewProxyContent) { c.RemotePort++ }},
		{name: "subdomain", mutate: func(c *frpplugin.NewProxyContent) { c.Subdomain = "anon-dddddddddddddddddddddddddd" }},
		{name: "custom domain", mutate: func(c *frpplugin.NewProxyContent) { c.CustomDomains = []string{"other.test"} }},
		{name: "HTTP auth", mutate: func(c *frpplugin.NewProxyContent) { c.HTTPUser = "user" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			store := &recordingStore{run: testRun(anonymous.ProtocolTCP)}
			dispatcher := mustDispatcher(t, store)
			content := validPortProxy("tcp", 30001)
			mutation.mutate(&content)
			response, err := dispatcher.Dispatch(context.Background(), request(t, frpplugin.OpNewProxy, content))
			if err != nil || !response.Reject || response.RejectReason != InvalidCredentialReason {
				t.Fatalf("Dispatch = (%#v, %v), want invalid credential", response, err)
			}
		})
	}
}

func TestNewUserConnMatchesFreshAuthorizedAllocation(t *testing.T) {
	tests := []struct {
		name      string
		proxyName string
		proxyType string
		user      frpplugin.UserInfo
		wantErr   bool
	}{
		{name: "exact", proxyName: testProxy, proxyType: "http", user: testUser()},
		{name: "cross proxy", proxyName: "anon_eeeeeeeeeeeeeeeeeeeeeeeeee", proxyType: "http", user: testUser(), wantErr: true},
		{name: "wrong protocol", proxyName: testProxy, proxyType: "tcp", user: testUser(), wantErr: true},
		{name: "wrong session", proxyName: testProxy, proxyType: "http", user: frpplugin.UserInfo{Metas: testMetas(), RunID: "anr_zzzzzzzzzzzzzzzzzzzzzzzzzz"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingStore{run: testRun(anonymous.ProtocolHTTP)}
			dispatcher := mustDispatcher(t, store)
			content := frpplugin.NewUserConnContent{User: test.user, ProxyName: test.proxyName, ProxyType: test.proxyType, RemoteAddr: "192.0.2.1:4567"}
			response, err := dispatcher.Dispatch(context.Background(), request(t, frpplugin.OpNewUserConn, content))
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if response.Reject != test.wantErr {
				t.Fatalf("response = %#v, want rejected=%v", response, test.wantErr)
			}
			if len(store.proxyCalls) != 1 || store.proxyCalls[0].proxyName != test.proxyName {
				t.Fatalf("Authorize calls = %#v", store.proxyCalls)
			}
		})
	}
}

func TestInvalidStoreResultsFailClosedAsUnavailable(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*anonymous.Run)
	}{
		{name: "different run", mutate: func(run *anonymous.Run) { run.RunID = "anr_eeeeeeeeeeeeeeeeeeeeeeeeee" }},
		{name: "malformed proxy", mutate: func(run *anonymous.Run) { run.ProxyName = "anon_invalid" }},
		{name: "wrong state", mutate: func(run *anonymous.Run) { run.State = anonymous.StateReleased }},
		{name: "stopped desire", mutate: func(run *anonymous.Run) { run.DesiredState = anonymous.DesiredStopped }},
		{name: "malformed HTTP endpoint", mutate: func(run *anonymous.Run) { run.PublicEndpoint = "https://evil.test/path" }},
		{name: "malformed port endpoint", mutate: func(run *anonymous.Run) {
			run.Protocol = anonymous.ProtocolTCP
			run.PublicEndpoint = "tunnel.test:030001"
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			run := testRun(anonymous.ProtocolHTTP)
			mutation.mutate(&run)
			store := &recordingStore{run: run}
			dispatcher := mustDispatcher(t, store)
			response, err := dispatcher.Dispatch(context.Background(), request(t, frpplugin.OpLogin, frpplugin.LoginContent{Metas: testMetas()}))
			if !errors.Is(err, ErrAuthorizationUnavailable) || !response.Reject || response.RejectReason != UnavailableReason {
				t.Fatalf("Dispatch = (%#v, %v), want unavailable", response, err)
			}
		})
	}
}

func TestStoreErrorsHaveStableSecretFreeClasses(t *testing.T) {
	tests := []struct {
		name       string
		storeError error
		wantReason string
		wantError  bool
	}{
		{name: "invalid credential", storeError: anonymous.ErrInvalidCredential, wantReason: InvalidCredentialReason},
		{name: "missing run", storeError: anonymous.ErrRunNotFound, wantReason: InvalidCredentialReason},
		{name: "expired", storeError: anonymous.ErrRunExpired, wantReason: RunStoppedReason},
		{name: "stopped", storeError: anonymous.ErrRunStopped, wantReason: RunStoppedReason},
		{name: "corrupt state", storeError: anonymous.ErrInvalidState, wantReason: UnavailableReason, wantError: true},
		{name: "Redis failure", storeError: anonymous.ErrUnavailable, wantReason: UnavailableReason, wantError: true},
		{name: "unexpected secret", storeError: errors.New("dependency exposed " + testRunToken), wantReason: UnavailableReason, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingStore{err: test.storeError}
			dispatcher := mustDispatcher(t, store)
			response, err := dispatcher.Dispatch(context.Background(), request(t, frpplugin.OpPing, frpplugin.PingContent{User: testUser()}))
			if response.RejectReason != test.wantReason || !response.Reject || !response.Unchange {
				t.Fatalf("response = %#v", response)
			}
			if test.wantError {
				if !errors.Is(err, ErrAuthorizationUnavailable) || err.Error() != ErrAuthorizationUnavailable.Error() {
					t.Fatalf("error = %v, want stable unavailable", err)
				}
			} else if err != nil {
				t.Fatalf("error = %v, want normal rejection", err)
			}
			encoded, marshalErr := json.Marshal(response)
			if marshalErr != nil || strings.Contains(string(encoded), testRunToken) || err != nil && strings.Contains(err.Error(), testRunToken) {
				t.Fatalf("secret leaked through response/error: %s %v", encoded, err)
			}
		})
	}
}

func TestMalformedCallbackContentIsRejectedBeforeStore(t *testing.T) {
	store := &recordingStore{run: testRun(anonymous.ProtocolHTTP)}
	dispatcher := mustDispatcher(t, store)
	response, err := dispatcher.Dispatch(context.Background(), frpplugin.Request{Op: frpplugin.OpLogin, Content: json.RawMessage(`{"Metas":{"nodelane_run_id":"secret"}}`)})
	if err != nil || !response.Reject || response.RejectReason != InvalidRequestReason {
		t.Fatalf("Dispatch = (%#v, %v), want invalid request", response, err)
	}
	if len(store.loginCalls) != 0 || len(store.proxyCalls) != 0 {
		t.Fatalf("malformed content reached store: %#v %#v", store.loginCalls, store.proxyCalls)
	}
}

func TestCallbackSpecificSessionFieldsAreValidated(t *testing.T) {
	tests := []struct {
		name    string
		op      frpplugin.Operation
		content any
	}{
		{name: "Login user namespace", op: frpplugin.OpLogin, content: frpplugin.LoginContent{User: "account", Metas: testMetas()}},
		{name: "Ping user namespace", op: frpplugin.OpPing, content: frpplugin.PingContent{User: frpplugin.UserInfo{User: "account", Metas: testMetas(), RunID: testRunID}}},
		{name: "Ping wrong session", op: frpplugin.OpPing, content: frpplugin.PingContent{User: frpplugin.UserInfo{Metas: testMetas(), RunID: "anr_zzzzzzzzzzzzzzzzzzzzzzzzzz"}}},
		{name: "work connection wrong outer session", op: frpplugin.OpNewWorkConn, content: frpplugin.NewWorkConnContent{User: testUser(), RunID: "anr_zzzzzzzzzzzzzzzzzzzzzzzzzz"}},
		{name: "close wrong proxy", op: frpplugin.OpCloseProxy, content: frpplugin.CloseProxyContent{User: testUser(), ProxyName: "anon_eeeeeeeeeeeeeeeeeeeeeeeeee"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingStore{run: testRun(anonymous.ProtocolHTTP)}
			dispatcher := mustDispatcher(t, store)
			response, err := dispatcher.Dispatch(context.Background(), request(t, test.op, test.content))
			if err != nil || !response.Reject || response.RejectReason != InvalidCredentialReason {
				t.Fatalf("Dispatch = (%#v, %v), want invalid credential", response, err)
			}
		})
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	if dispatcher, err := New(nil, "2MB"); dispatcher != nil || !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("New(nil) = (%v, %v)", dispatcher, err)
	}
	var typedNil *recordingStore
	if dispatcher, err := New(typedNil, "2MB"); dispatcher != nil || !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("New(typed nil) = (%v, %v)", dispatcher, err)
	}
	store := &recordingStore{}
	for _, limit := range []string{"", " 2MB", "0", "-1MB", "NaN", "Inf", strings.Repeat("1", 65)} {
		if dispatcher, err := New(store, limit); dispatcher != nil || !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("New(limit=%q) = (%v, %v)", limit, dispatcher, err)
		}
	}
}

func testMetas() map[string]string {
	return map[string]string{frpplugin.MetadataRunID: testRunID, frpplugin.MetadataRunToken: testRunToken}
}

func testUser() frpplugin.UserInfo {
	return frpplugin.UserInfo{Metas: testMetas(), RunID: testRunID}
}

func testRun(protocol anonymous.Protocol) anonymous.Run {
	created := time.Date(2026, 9, 6, 1, 2, 3, 0, time.UTC)
	run := anonymous.Run{
		RunID: testRunID, ProxyName: testProxy, Protocol: protocol,
		State: anonymous.StateReserved, DesiredState: anonymous.DesiredRunning,
		CreatedAt: created, ConnectDeadlineAt: created.Add(2 * time.Minute), HardExpiresAt: created.Add(time.Hour),
	}
	switch protocol {
	case anonymous.ProtocolHTTP:
		run.PublicEndpoint = "anon-dddddddddddddddddddddddddd.tunnel.test"
	case anonymous.ProtocolTCP:
		run.PublicEndpoint = "tunnel.test:30001"
	case anonymous.ProtocolUDP:
		run.PublicEndpoint = "tunnel.test:30002"
	}
	return run
}

func validHTTPProxy() frpplugin.NewProxyContent {
	return frpplugin.NewProxyContent{
		User: testUser(), ProxyName: testProxy, ProxyType: "http",
		Subdomain:     "anon-dddddddddddddddddddddddddd",
		UseEncryption: true, UseCompression: true, BandwidthLimit: "100GB", BandwidthLimitMode: "client",
	}
}

func validPortProxy(protocol string, port int) frpplugin.NewProxyContent {
	return frpplugin.NewProxyContent{
		User: testUser(), ProxyName: testProxy, ProxyType: protocol, RemotePort: port,
		UseEncryption: true, UseCompression: true, BandwidthLimit: "100GB", BandwidthLimitMode: "client",
	}
}

func request(t *testing.T, operation frpplugin.Operation, content any) frpplugin.Request {
	t.Helper()
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal callback content: %v", err)
	}
	return frpplugin.Request{Version: frpplugin.APIVersion, Op: operation, Content: encoded}
}

func mustDispatcher(t *testing.T, store Store) *Dispatcher {
	t.Helper()
	dispatcher, err := New(store, "2MB")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return dispatcher
}
