package controlapi

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

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/runtimestats"
)

const testRouteID = "rte_abcdefghijklmnopqrstuvwxyz"
const testStatsProxyName = "rte_bbbbbbbbbbbbbbbbbbbbbbbbbb"
const testRunID = "run_abcdefghijklmnopqrstuvwxyz"
const testRunToken = "nrc_abcdefghijklmnopqrstuvwxyz.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
const testLaunchToken = "nlc_abcdefghijklmnopqrstuvwxyz.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

var testTime = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

type recordingAuthenticator struct {
	principal Principal
	err       error
	calls     int
}

func (a *recordingAuthenticator) Authenticate(context.Context, *http.Request) (Principal, error) {
	a.calls++
	return a.principal, a.err
}

type recordingRepository struct {
	calls              []string
	err                error
	getErr             error
	authorizeErr       error
	create             domain.CreateRouteCommand
	start              domain.AccountStartCommand
	redeem             domain.LaunchRedeemCommand
	proof              domain.RunProof
	accountID, routeID string
	query              domain.RouteQuery
	view               domain.RouteView
	result             domain.StartResult
}

func (f *recordingRepository) ListRouteViews(_ context.Context, accountID string, q domain.RouteQuery) ([]domain.RouteView, error) {
	f.calls = append(f.calls, "list")
	f.accountID = accountID
	f.query = q
	return []domain.RouteView{f.view}, f.err
}
func (f *recordingRepository) GetRouteView(_ context.Context, accountID, routeID string) (domain.RouteView, error) {
	f.calls = append(f.calls, "get")
	f.accountID = accountID
	f.routeID = routeID
	if f.getErr != nil {
		return domain.RouteView{}, f.getErr
	}
	return f.view, f.err
}
func (f *recordingRepository) CreateRoute(_ context.Context, cmd domain.CreateRouteCommand) (domain.CreateRouteResult, error) {
	f.calls = append(f.calls, "create")
	f.create = cmd
	return domain.CreateRouteResult{Route: f.view.Route, Replayed: true}, f.err
}
func (f *recordingRepository) DeleteRoute(_ context.Context, accountID, routeID string) (domain.Route, error) {
	f.calls = append(f.calls, "delete")
	f.accountID = accountID
	f.routeID = routeID
	return f.view.Route, f.err
}
func (f *recordingRepository) RestoreRoute(_ context.Context, accountID, routeID string) (domain.Route, error) {
	f.calls = append(f.calls, "restore")
	f.accountID = accountID
	f.routeID = routeID
	return f.view.Route, f.err
}
func (f *recordingRepository) IssueLaunchCode(_ context.Context, cmd domain.IssueLaunchCodeCommand) (domain.IssuedLaunchCode, error) {
	f.calls = append(f.calls, "issue")
	f.accountID = cmd.AccountID
	f.routeID = cmd.RouteID
	return domain.IssuedLaunchCode{Code: domain.LaunchCode{ID: "secret-code-id", SecretHash: "secret-hash", ExpiresAt: testTime.Add(10 * time.Minute)}, Token: testLaunchToken}, f.err
}
func (f *recordingRepository) StartAccountRun(_ context.Context, cmd domain.AccountStartCommand) (domain.StartResult, error) {
	f.calls = append(f.calls, "start")
	f.start = cmd
	return f.result, f.err
}
func (f *recordingRepository) RedeemLaunchCode(_ context.Context, cmd domain.LaunchRedeemCommand) (domain.StartResult, error) {
	f.calls = append(f.calls, "redeem")
	f.redeem = cmd
	return f.result, f.err
}
func (f *recordingRepository) AuthorizeRun(_ context.Context, proof domain.RunProof) (domain.RunAuthorization, error) {
	f.calls = append(f.calls, "authorize")
	f.proof = proof
	if f.authorizeErr != nil {
		return domain.RunAuthorization{}, f.authorizeErr
	}
	return domain.RunAuthorization{Route: f.view.Route, Run: f.result.Run, CredentialID: f.result.CredentialID}, f.err
}
func (f *recordingRepository) Heartbeat(_ context.Context, proof domain.RunProof) (domain.HeartbeatResult, error) {
	f.calls = append(f.calls, "heartbeat")
	f.proof = proof
	return domain.HeartbeatResult{Run: f.result.Run}, f.err
}
func (f *recordingRepository) RequestOwnedStop(_ context.Context, accountID, routeID string) (domain.Run, error) {
	f.calls = append(f.calls, "owned-stop")
	f.accountID = accountID
	f.routeID = routeID
	return f.result.Run, f.err
}
func (f *recordingRepository) RequestCredentialStop(_ context.Context, proof domain.RunProof) (domain.Run, error) {
	f.calls = append(f.calls, "credential-stop")
	f.proof = proof
	return f.result.Run, f.err
}

type recordingStats struct {
	calls    int
	proxy    string
	snapshot runtimestats.Snapshot
}

func (s *recordingStats) Snapshot(_ context.Context, proxyName string) runtimestats.Snapshot {
	s.calls++
	s.proxy = proxyName
	return s.snapshot
}

type rateCall struct {
	operation, key string
	limit          int
	window         time.Duration
}
type fixture struct {
	options Options
	auth    *recordingAuthenticator
	repo    *recordingRepository
	stats   *recordingStats
	rates   []rateCall
}

func newFixture() *fixture {
	route := domain.Route{ID: testRouteID, AccountID: "private-account-id", Protocol: "http", Subdomain: "example", ProxyName: testRouteID, Status: domain.RouteActive, CreatedAt: testTime, UpdatedAt: testTime}
	run := domain.Run{ID: testRunID, RouteID: testRouteID, StartedVia: domain.StartedViaDeviceLogin, Status: domain.RunStarting, DesiredState: domain.DesiredRunning, RequestIP: netip.MustParseAddr("192.0.2.9"), ConnectedIP: netip.MustParseAddr("192.0.2.10"), CreatedAt: testTime, ConnectDeadlineAt: testTime.Add(2 * time.Minute)}
	connections, upload, download, state := int64(3), int64(202), int64(101), "online"
	f := &fixture{
		auth:  &recordingAuthenticator{principal: Principal{AccountID: "verified-account", Kind: PrincipalKindWeb, CSRFToken: "private-csrf-token"}},
		repo:  &recordingRepository{view: domain.RouteView{Route: route, CurrentRun: &run}, result: domain.StartResult{Run: run, CredentialID: "private-credential-id", CredentialToken: testRunToken, Replayed: true}},
		stats: &recordingStats{snapshot: runtimestats.Snapshot{CurrentConnections: &connections, UploadBytesToday: &upload, DownloadBytesToday: &download, ProxyState: &state, ObservedAt: testTime, Availability: runtimestats.Available}},
	}
	f.options = Options{PublicOrigin: "https://tunnel.example.test", PublicDomain: "tunnel.example.test", Authenticator: f.auth, Routes: f.repo, Runs: f.repo, Stats: f.stats, SourceIP: func(*http.Request) (netip.Addr, error) { return netip.MustParseAddr("::ffff:192.0.2.9"), nil }, Banned: func(context.Context, netip.Addr) (bool, error) { return false, nil }, RateLimit: func(_ context.Context, operation, key string, limit int, window time.Duration) (time.Duration, error) {
		f.rates = append(f.rates, rateCall{operation, key, limit, window})
		return 0, nil
	}, Now: func() time.Time { return testTime }}
	return f
}
func (f *fixture) request(t *testing.T, method, path, body string, change func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	s, err := New(f.options)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, "https://tunnel.example.test"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", f.options.PublicOrigin)
	r.Header.Set("X-CSRF-Token", f.auth.principal.CSRFToken)
	r.Header.Set("Idempotency-Key", "  exact retry key  ")
	if change != nil {
		change(r)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func assertError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var envelope struct {
		Error struct {
			Code, Message string
			RequestID     string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid error JSON: %s, %v", w.Body, err)
	}
	if w.Code != status || envelope.Error.Code != code {
		t.Fatalf("got %d %s, want %d %s", w.Code, w.Body, status, code)
	}
	if envelope.Error.RequestID == "" || envelope.Error.RequestID != w.Header().Get("X-Request-ID") {
		t.Fatalf("missing/mismatched request ID: %s", w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cacheable error: %v", w.Header())
	}
}

func TestAccountEndpointMediumAndScopeMatrix(t *testing.T) {
	endpoints := []struct {
		method, path, body           string
		web, nativeRead, nativeStart bool
	}{
		{"GET", "/api/v1/routes", "", true, true, false},
		{"GET", "/api/v1/routes/" + testRouteID, "", true, true, false},
		{"GET", "/api/v1/routes/" + testRouteID + "/stats", "", true, true, false},
		{"POST", "/api/v1/routes", `{"protocol":"http","subdomain":"example"}`, true, false, false},
		{"DELETE", "/api/v1/routes/" + testRouteID, "{}", true, false, false},
		{"POST", "/api/v1/routes/" + testRouteID + "/restore", "{}", true, false, false},
		{"POST", "/api/v1/routes/" + testRouteID + "/launch-codes", "{}", true, false, false},
		{"POST", "/api/v1/routes/" + testRouteID + "/runs", "{}", false, false, true},
		{"POST", "/api/v1/routes/" + testRouteID + "/runs/current/stop", "{}", true, false, false},
	}
	for _, endpoint := range endpoints {
		for _, medium := range []string{"web", "native-read", "native-start", "native-none", "unknown", "missing-account"} {
			t.Run(endpoint.method+endpoint.path+"/"+medium, func(t *testing.T) {
				f := newFixture()
				allowed := false
				switch medium {
				case "web":
					allowed = endpoint.web
				case "native-read":
					f.auth.principal.Kind = PrincipalKindNative
					f.auth.principal.Scopes = []string{"routes:read"}
					allowed = endpoint.nativeRead
				case "native-start":
					f.auth.principal.Kind = PrincipalKindNative
					f.auth.principal.Scopes = []string{"runs:start"}
					allowed = endpoint.nativeStart
				case "native-none":
					f.auth.principal.Kind = PrincipalKindNative
				case "unknown":
					f.auth.principal.Kind = "run"
				case "missing-account":
					f.auth.principal.AccountID = ""
				}
				w := f.request(t, endpoint.method, endpoint.path, endpoint.body, nil)
				if allowed {
					if w.Code < 200 || w.Code >= 300 {
						t.Fatalf("allowed request rejected: %d %s", w.Code, w.Body)
					}
					if len(f.repo.calls) == 0 {
						t.Fatal("allowed request did not reach use case")
					}
				} else {
					status := 403
					code := "insufficient_scope"
					if medium == "unknown" || medium == "missing-account" {
						status = 401
						code = "unauthorized"
					}
					assertError(t, w, status, code)
					if len(f.repo.calls) != 0 || len(f.rates) != 0 || f.stats.calls != 0 {
						t.Fatalf("denied request touched dependencies: repo=%v rates=%v stats=%d", f.repo.calls, f.rates, f.stats.calls)
					}
				}
			})
		}
	}
}

func TestBrowserWritesRejectCSRFOriginContentTypeAndAmbiguousJSON(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		change     func(*http.Request)
		status     int
		code       string
	}{
		{"csrf-missing", `{"protocol":"http","subdomain":"example"}`, func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, 403, "insufficient_scope"},
		{"csrf-wrong", `{"protocol":"http","subdomain":"example"}`, func(r *http.Request) { r.Header.Set("X-CSRF-Token", "wrong") }, 403, "insufficient_scope"},
		{"origin-missing", `{}`, func(r *http.Request) { r.Header.Del("Origin") }, 403, "insufficient_scope"},
		{"origin-suffix", `{}`, func(r *http.Request) { r.Header.Set("Origin", "https://tunnel.example.test.attacker.test") }, 403, "insufficient_scope"},
		{"origin-path", `{}`, func(r *http.Request) { r.Header.Set("Origin", "https://tunnel.example.test/") }, 403, "insufficient_scope"},
		{"origin-multiple", `{}`, func(r *http.Request) { r.Header.Add("Origin", "https://attacker.test") }, 403, "insufficient_scope"},
		{"type-missing", `{}`, func(r *http.Request) { r.Header.Del("Content-Type") }, 400, "invalid_request"},
		{"type-form", `{}`, func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, 400, "invalid_request"},
		{"foreign-account", `{"protocol":"http","subdomain":"example","account_id":"attacker"}`, nil, 400, "invalid_request"},
		{"unknown", `{"protocol":"http","subdomain":"example","extra":1}`, nil, 400, "invalid_request"},
		{"duplicate", `{"protocol":"http","protocol":"tcp","subdomain":"example"}`, nil, 400, "invalid_request"},
		{"trailing", `{"protocol":"http","subdomain":"example"} {}`, nil, 400, "invalid_request"},
		{"null", `null`, nil, 400, "invalid_request"},
		{"array", `[]`, nil, 400, "invalid_request"},
		{"empty", ``, nil, 400, "invalid_request"},
		{"too-large", `{"protocol":"http","subdomain":"example"}` + strings.Repeat(" ", 65536), nil, 400, "invalid_request"},
		{"missing-idempotency", `{"protocol":"http","subdomain":"example"}`, func(r *http.Request) { r.Header.Del("Idempotency-Key") }, 400, "invalid_request"},
		{"duplicate-idempotency", `{"protocol":"http","subdomain":"example"}`, func(r *http.Request) { r.Header.Add("Idempotency-Key", "another") }, 400, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			w := f.request(t, "POST", "/api/v1/routes", tc.body, tc.change)
			assertError(t, w, tc.status, tc.code)
			if len(f.repo.calls) != 0 || len(f.rates) != 0 {
				t.Fatalf("invalid write reached dependencies: %v %v", f.repo.calls, f.rates)
			}
		})
	}
}

func TestParameterlessCommandsAcceptEmptyOrObjectButRejectFields(t *testing.T) {
	for _, path := range []string{"/api/v1/routes/" + testRouteID + "/restore", "/api/v1/routes/" + testRouteID + "/launch-codes", "/api/v1/routes/" + testRouteID + "/runs/current/stop"} {
		for _, body := range []string{"", "{}", " {} \n", `{"account_id":"other"}`, "{} {}", "null"} {
			t.Run(path+body, func(t *testing.T) {
				f := newFixture()
				w := f.request(t, "POST", path, body, nil)
				if body == "" || body == "{}" || body == " {} \n" {
					if w.Code < 200 || w.Code >= 300 {
						t.Fatalf("valid parameterless command: %d %s", w.Code, w.Body)
					}
				} else {
					assertError(t, w, 400, "invalid_request")
					if len(f.repo.calls) != 0 {
						t.Fatal("invalid body called use case")
					}
				}
			})
		}
	}
	f := newFixture()
	w := f.request(t, "POST", "/api/v1/routes/"+testRouteID+"/restore", "", func(r *http.Request) { r.Header.Del("Content-Type") })
	assertError(t, w, 400, "invalid_request")
}

func TestNewRejectsMissingDependenciesAndUnsafeOrigins(t *testing.T) {
	for _, change := range []func(*Options){func(o *Options) { o.Authenticator = nil }, func(o *Options) { o.Routes = nil }, func(o *Options) { o.Runs = nil }, func(o *Options) { o.Stats = nil }, func(o *Options) { o.SourceIP = nil }, func(o *Options) { o.Banned = nil }, func(o *Options) { o.RateLimit = nil }, func(o *Options) { o.PublicOrigin = "http://tunnel.example.test" }, func(o *Options) { o.PublicOrigin = "https://user:secret@tunnel.example.test" }, func(o *Options) { o.PublicOrigin = "https://tunnel.example.test/path" }, func(o *Options) { o.PublicOrigin = "https://tunnel.example.test?query=1" }, func(o *Options) { o.PublicOrigin = "https://tunnel.example.test?" }, func(o *Options) { o.PublicDomain = "https://tunnel.example.test" }, func(o *Options) { o.PublicDomain = "tunnel.example.test/path" }, func(o *Options) { o.PublicDomain = "*.example.test" }, func(o *Options) { o.PublicDomain = "" }} {
		f := newFixture()
		change(&f.options)
		if _, err := New(f.options); err == nil {
			t.Fatal("invalid API options accepted")
		}
	}
}

func TestRouteListRejectsMalformedQueryBeforeAuthentication(t *testing.T) {
	f := newFixture()
	w := f.request(t, http.MethodGet, "/api/v1/routes", "", func(r *http.Request) {
		r.URL.RawQuery = "deleted=true&ignored=%ZZ"
	})
	assertError(t, w, http.StatusBadRequest, "invalid_request")
	if f.auth.calls != 0 || len(f.repo.calls) != 0 {
		t.Fatalf("malformed query reached dependencies: auth=%d repo=%v", f.auth.calls, f.repo.calls)
	}
}

func TestAuthenticationErrorsAndUnknownRoutesUseSafeEnvelope(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{ErrUnauthorized, 401, "unauthorized"}, {ErrUnavailable, 503, "dependency_unavailable"}, {errors.New("database-password-raw-error"), 503, "dependency_unavailable"}} {
		f := newFixture()
		f.auth.err = tc.err
		w := f.request(t, "GET", "/api/v1/routes", "", func(r *http.Request) { r.Header.Set("X-Request-ID", "attacker-controlled") })
		assertError(t, w, tc.status, tc.code)
		if strings.Contains(w.Body.String(), "database-password") || w.Header().Get("X-Request-ID") == "attacker-controlled" {
			t.Fatal("error leaked private input")
		}
		if len(f.repo.calls) != 0 {
			t.Fatal("authentication failure called repository")
		}
	}
	f := newFixture()
	first := f.request(t, "GET", "/api/v1/unknown", "", nil)
	assertError(t, first, 404, "route_not_found")
	second := f.request(t, "PUT", "/api/v1/routes", "{}", nil)
	assertError(t, second, 405, "invalid_request")
	if first.Header().Get("X-Request-ID") == second.Header().Get("X-Request-ID") {
		t.Fatal("request IDs reused")
	}
}

func TestInvalidOpaqueCredentialsNeverUseAccountAuthentication(t *testing.T) {
	for _, suffix := range []string{"heartbeat", "stop"} {
		for _, token := range []string{"account.access.token", testLaunchToken, "", testRunToken + ",other"} {
			t.Run(suffix+token, func(t *testing.T) {
				f := newFixture()
				w := f.request(t, "POST", "/api/v1/runs/"+testRunID+"/"+suffix, "{}", func(r *http.Request) {
					if token != "" {
						r.Header.Set("Authorization", "Bearer "+token)
					}
				})
				assertError(t, w, 401, "unauthorized")
				if f.auth.calls != 0 || len(f.repo.calls) != 0 {
					t.Fatalf("opaque proof substituted account auth: %d %v", f.auth.calls, f.repo.calls)
				}
			})
		}
	}
	for _, body := range []string{`{"launch_code":"account.access.token","nonce":"retry"}`, `{"nonce":"retry"}`} {
		f := newFixture()
		w := f.request(t, "POST", "/api/v1/launch/redeem", body, nil)
		assertError(t, w, 401, "unauthorized")
		if f.auth.calls != 0 || len(f.repo.calls) != 0 {
			t.Fatal("invalid code authenticated as account")
		}
	}
	f := newFixture()
	w := f.request(t, "POST", "/api/v1/launch/redeem", `{"launch_code":"`+testLaunchToken+`","nonce":"retry"}`, func(r *http.Request) { r.Header.Set("Authorization", "Bearer account.access.token") })
	assertError(t, w, 400, "invalid_request")
	if f.auth.calls != 0 || len(f.repo.calls) != 0 || len(f.rates) != 0 {
		t.Fatal("ambiguous credential used")
	}
}

func TestDomainErrorsMapToStableStatusWithoutRawDetails(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrInvalidRequest, 400, "invalid_request"}, {domain.ErrSubdomainInvalid, 400, "subdomain_invalid"}, {domain.ErrProtocolNotAllowed, 400, "invalid_request"}, {domain.ErrSubdomainReserved, 409, "subdomain_reserved"}, {domain.ErrSubdomainConflict, 409, "subdomain_conflict"}, {domain.ErrRouteLimitReached, 409, "route_limit_reached"}, {domain.ErrRouteDeleted, 409, "route_deleted"}, {domain.ErrRouteNotFound, 404, "route_not_found"}, {domain.ErrNotFound, 404, "route_not_found"}, {domain.ErrRunAlreadyActive, 409, "run_already_active"}, {domain.ErrRunStopped, 410, "run_stopped"}, {domain.ErrIdempotencyConflict, 409, "idempotency_conflict"}, {domain.ErrLaunchCodeExpired, 410, "launch_code_expired"}, {domain.ErrLaunchCodeUsed, 410, "launch_code_used"}, {domain.ErrLaunchCodeRevoked, 410, "launch_code_revoked"}, {domain.ErrInvalidRunProof, 401, "unauthorized"}, {identity.ErrInvalidCredential, 401, "unauthorized"}, {errors.New("private-database-query"), 503, "dependency_unavailable"},
	} {
		t.Run(tc.code+tc.err.Error(), func(t *testing.T) {
			f := newFixture()
			f.repo.err = tc.err
			w := f.request(t, "GET", "/api/v1/routes/"+testRouteID, "", nil)
			assertError(t, w, tc.status, tc.code)
			if strings.Contains(w.Body.String(), "private-database-query") {
				t.Fatal("raw database error leaked")
			}
		})
	}
}

func TestSourceAndBanChecksOnlyGuardAllocatingOrRenewingOperations(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		configure   func(*fixture)
		wantGuarded bool
	}{
		{name: "list", method: http.MethodGet, path: "/api/v1/routes"},
		{name: "detail", method: http.MethodGet, path: "/api/v1/routes/" + testRouteID},
		{name: "stats", method: http.MethodGet, path: "/api/v1/routes/" + testRouteID + "/stats"},
		{name: "delete", method: http.MethodDelete, path: "/api/v1/routes/" + testRouteID, body: "{}"},
		{name: "owner stop", method: http.MethodPost, path: "/api/v1/routes/" + testRouteID + "/runs/current/stop", body: "{}"},
		{name: "credential stop", method: http.MethodPost, path: "/api/v1/runs/" + testRunID + "/stop", body: "{}", configure: func(f *fixture) {
			f.auth.principal = Principal{}
		}},
		{name: "create", method: http.MethodPost, path: "/api/v1/routes", body: `{"protocol":"http","subdomain":"example"}`, wantGuarded: true},
		{name: "restore", method: http.MethodPost, path: "/api/v1/routes/" + testRouteID + "/restore", body: "{}", wantGuarded: true},
		{name: "issue", method: http.MethodPost, path: "/api/v1/routes/" + testRouteID + "/launch-codes", body: "{}", wantGuarded: true},
		{name: "account start", method: http.MethodPost, path: "/api/v1/routes/" + testRouteID + "/runs", body: "{}", wantGuarded: true, configure: func(f *fixture) {
			f.auth.principal.Kind = PrincipalKindNative
			f.auth.principal.Scopes = []string{"runs:start"}
		}},
		{name: "redeem", method: http.MethodPost, path: "/api/v1/launch/redeem", body: `{"launch_code":"` + testLaunchToken + `","nonce":"retry"}`, wantGuarded: true},
		{name: "heartbeat", method: http.MethodPost, path: "/api/v1/runs/" + testRunID + "/heartbeat", body: "{}", wantGuarded: true, configure: func(f *fixture) {
			f.auth.principal = Principal{}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture()
			if test.configure != nil {
				test.configure(f)
			}
			sourceCalls := 0
			banCalls := 0
			f.options.SourceIP = func(*http.Request) (netip.Addr, error) {
				sourceCalls++
				return netip.MustParseAddr("192.0.2.9"), nil
			}
			f.options.Banned = func(context.Context, netip.Addr) (bool, error) {
				banCalls++
				return true, nil
			}
			w := f.request(t, test.method, test.path, test.body, func(r *http.Request) {
				if strings.Contains(test.path, "/api/v1/runs/") && !strings.Contains(test.path, "/routes/") {
					r.Header.Set("Authorization", "Bearer "+testRunToken)
				}
			})
			if test.wantGuarded {
				assertError(t, w, http.StatusForbidden, "ip_banned")
				if len(f.repo.calls) != 0 || sourceCalls != 1 || banCalls != 1 || len(f.rates) != 0 {
					t.Fatalf("guarded request order: repo=%v source=%d ban=%d rate=%v", f.repo.calls, sourceCalls, banCalls, f.rates)
				}
				return
			}
			if w.Code < 200 || w.Code >= 300 {
				t.Fatalf("cleanup/read request failed under ban: %d %s", w.Code, w.Body)
			}
			if sourceCalls != 0 || banCalls != 0 {
				t.Fatalf("non-gated request looked up source/ban: source=%d ban=%d", sourceCalls, banCalls)
			}
		})
	}
}

func TestGuardFailuresFailClosedBeforeRepository(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*fixture)
		status     int
		code       string
		retryAfter string
	}{
		{name: "source unavailable", configure: func(f *fixture) {
			f.options.SourceIP = func(*http.Request) (netip.Addr, error) { return netip.Addr{}, errors.New("private proxy failure") }
		}, status: http.StatusServiceUnavailable, code: "dependency_unavailable"},
		{name: "ban lookup unavailable", configure: func(f *fixture) {
			f.options.Banned = func(context.Context, netip.Addr) (bool, error) { return false, errors.New("private database failure") }
		}, status: http.StatusServiceUnavailable, code: "dependency_unavailable"},
		{name: "rate dependency unavailable", configure: func(f *fixture) {
			f.options.RateLimit = func(context.Context, string, string, int, time.Duration) (time.Duration, error) {
				return 0, errors.New("private redis failure")
			}
		}, status: http.StatusServiceUnavailable, code: "dependency_unavailable"},
		{name: "rate exceeded", configure: func(f *fixture) {
			f.options.RateLimit = func(context.Context, string, string, int, time.Duration) (time.Duration, error) {
				return 2500 * time.Millisecond, nil
			}
		}, status: http.StatusTooManyRequests, code: "rate_limited", retryAfter: "3"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture()
			test.configure(f)
			w := f.request(t, http.MethodPost, "/api/v1/routes", `{"protocol":"http","subdomain":"example"}`, nil)
			assertError(t, w, test.status, test.code)
			if got := w.Header().Get("Retry-After"); got != test.retryAfter {
				t.Fatalf("Retry-After = %q, want %q", got, test.retryAfter)
			}
			if len(f.repo.calls) != 0 {
				t.Fatalf("failed guard reached repository: %v", f.repo.calls)
			}
		})
	}
}

func TestCreateRejectsInvalidRouteFieldsBeforeSourceAndRateDependencies(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "protocol", body: `{"protocol":"tcp","subdomain":"example"}`, code: "invalid_request"},
		{name: "subdomain", body: `{"protocol":"http","subdomain":"bad!"}`, code: "subdomain_invalid"},
		{name: "reserved", body: `{"protocol":"http","subdomain":"anon-example"}`, code: "subdomain_reserved"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture()
			sourceCalls := 0
			f.options.SourceIP = func(*http.Request) (netip.Addr, error) {
				sourceCalls++
				return netip.MustParseAddr("192.0.2.9"), nil
			}
			w := f.request(t, http.MethodPost, "/api/v1/routes", test.body, nil)
			assertError(t, w, map[string]int{"invalid_request": 400, "subdomain_invalid": 400, "subdomain_reserved": 409}[test.code], test.code)
			if sourceCalls != 0 || len(f.rates) != 0 || len(f.repo.calls) != 0 {
				t.Fatalf("invalid route touched dependencies: source=%d rate=%v repo=%v", sourceCalls, f.rates, f.repo.calls)
			}
		})
	}
}

func TestSuccessfulResponsesUseExplicitSecretSafeDTOs(t *testing.T) {
	f := newFixture()
	w := f.request(t, http.MethodGet, "/api/v1/routes?deleted=true", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list response: %d %s", w.Code, w.Body)
	}
	if !f.repo.query.Deleted {
		t.Fatal("deleted filter was not forwarded")
	}
	body := w.Body.String()
	for _, forbidden := range []string{"private-account-id", "request_ip", "connected_ip", "credential_id", "secret_hash"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("list leaked internal field %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"public_url":"https://example.tunnel.example.test"`) || !strings.Contains(body, `"current_run"`) {
		t.Fatalf("list omitted public projection: %s", body)
	}

	f = newFixture()
	w = f.request(t, http.MethodPost, "/api/v1/routes", `{"protocol":" HTTP ","subdomain":" Example "}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("create response: %d %s", w.Code, w.Body)
	}
	if f.repo.create.AccountID != "verified-account" || f.repo.create.Protocol != "http" || f.repo.create.Subdomain != "example" || f.repo.create.IdempotencyKey != "exact retry key" {
		t.Fatalf("create command not normalized/bound to principal: %+v", f.repo.create)
	}
	if !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatalf("create replay flag omitted: %s", w.Body)
	}

	f = newFixture()
	w = f.request(t, http.MethodPost, "/api/v1/routes/"+testRouteID+"/launch-codes", "{}", nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("launch response: %d %s", w.Code, w.Body)
	}
	for _, forbidden := range []string{"secret-code-id", "secret-hash", "private-account-id"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("launch response leaked %q: %s", forbidden, w.Body)
		}
	}
	if !strings.Contains(w.Body.String(), testLaunchToken) || !strings.Contains(w.Body.String(), `"expires_at"`) {
		t.Fatalf("launch response omitted one-time material: %s", w.Body)
	}
}

func TestAccountStartAndLaunchRedeemReturnUsableRunProjection(t *testing.T) {
	for _, test := range []struct {
		name      string
		path      string
		body      string
		configure func(*fixture)
	}{
		{name: "account start", path: "/api/v1/routes/" + testRouteID + "/runs", body: "{}", configure: func(f *fixture) {
			f.auth.principal.Kind = PrincipalKindNative
			f.auth.principal.Scopes = []string{"runs:start"}
		}},
		{name: "launch redeem", path: "/api/v1/launch/redeem", body: `{"launch_code":"` + testLaunchToken + `","nonce":" retry nonce "}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture()
			if test.configure != nil {
				test.configure(f)
			}
			w := f.request(t, http.MethodPost, test.path, test.body, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("run response: %d %s", w.Code, w.Body)
			}
			body := w.Body.String()
			for _, required := range []string{testRunToken, testRunID, testRouteID, `"proxy_name":"` + testRouteID + `"`, `"replayed":true`} {
				if !strings.Contains(body, required) {
					t.Fatalf("run response omitted %q: %s", required, body)
				}
			}
			for _, forbidden := range []string{"private-account-id", "private-credential-id", "request_ip", "connected_ip"} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("run response leaked %q: %s", forbidden, body)
				}
			}
			if test.name == "account start" {
				if f.repo.start.IdempotencyKey != "exact retry key" || f.repo.start.RequestIP.String() != "192.0.2.9" {
					t.Fatalf("account start command: %+v", f.repo.start)
				}
			} else if f.repo.redeem.Nonce != "retry nonce" || f.repo.redeem.Token != testLaunchToken || f.repo.redeem.RequestIP.String() != "192.0.2.9" {
				t.Fatalf("redeem command: %+v", f.repo.redeem)
			}
		})
	}
}

func TestRunCredentialLifecycleUsesOnlyOpaqueProof(t *testing.T) {
	for _, test := range []struct {
		path     string
		wantCall string
		stopped  bool
	}{
		{path: "/api/v1/runs/" + testRunID + "/heartbeat", wantCall: "heartbeat"},
		{path: "/api/v1/runs/" + testRunID + "/stop", wantCall: "credential-stop", stopped: true},
	} {
		t.Run(test.wantCall, func(t *testing.T) {
			f := newFixture()
			f.auth.principal = Principal{}
			w := f.request(t, http.MethodPost, test.path, "{}", func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+testRunToken)
			})
			if w.Code != http.StatusOK {
				t.Fatalf("lifecycle response: %d %s", w.Code, w.Body)
			}
			if f.auth.calls != 0 || len(f.repo.calls) != 1 || f.repo.calls[0] != test.wantCall || f.repo.proof.RunID != testRunID || f.repo.proof.Token != testRunToken {
				t.Fatalf("wrong proof path: auth=%d calls=%v proof=%+v", f.auth.calls, f.repo.calls, f.repo.proof)
			}
			if strings.Contains(w.Body.String(), testRunToken) || strings.Contains(w.Body.String(), "private-credential-id") {
				t.Fatalf("lifecycle response leaked credential: %s", w.Body)
			}
			wantStopped := `"stopped":false`
			if test.stopped {
				wantStopped = `"stopped":true`
			}
			if !strings.Contains(w.Body.String(), wantStopped) {
				t.Fatalf("lifecycle stopped flag: %s", w.Body)
			}
		})
	}
}

func TestStatsUsesOwnedDatabaseProxyNameAndWhitelistsCurrentSnapshot(t *testing.T) {
	f := newFixture()
	f.repo.view.Route.ProxyName = testStatsProxyName
	w := f.request(t, http.MethodGet, "/api/v1/routes/"+testRouteID+"/stats", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("stats response: %d %s", w.Code, w.Body)
	}
	if f.repo.accountID != "verified-account" || f.repo.routeID != testRouteID || f.stats.calls != 1 || f.stats.proxy != testStatsProxyName {
		t.Fatalf("stats did not resolve owned proxy: account=%q route=%q calls=%d proxy=%q", f.repo.accountID, f.repo.routeID, f.stats.calls, f.stats.proxy)
	}
	for _, field := range []string{
		`"route_id":"` + testRouteID + `"`, `"current_connections":3`, `"upload_bytes_today":202`,
		`"download_bytes_today":101`, `"proxy_state":"online"`, `"observed_at":"2026-09-05T12:00:00Z"`, `"availability":"available"`, `"time_zone":"UTC"`,
	} {
		if !strings.Contains(w.Body.String(), field) {
			t.Fatalf("stats omitted %s: %s", field, w.Body)
		}
	}
	var publicFields map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &publicFields); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{"route_id", "current_connections", "upload_bytes_today", "download_bytes_today", "proxy_state", "observed_at", "availability", "time_zone"}
	if len(publicFields) != len(wantFields) {
		t.Fatalf("stats DTO field count = %d, want %d: %s", len(publicFields), len(wantFields), w.Body)
	}
	for _, field := range wantFields {
		if _, ok := publicFields[field]; !ok {
			t.Fatalf("stats DTO missing %q: %s", field, w.Body)
		}
	}
	for _, forbidden := range []string{"private-account-id", "user", "clientID", "spec", "request_ip", "connected_ip"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("stats leaked %q: %s", forbidden, w.Body)
		}
	}

	f = newFixture()
	f.stats.snapshot = runtimestats.Snapshot{Availability: runtimestats.Unavailable, ObservedAt: testTime}
	w = f.request(t, http.MethodGet, "/api/v1/routes/"+testRouteID+"/stats", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"current_connections":null`) || !strings.Contains(w.Body.String(), `"availability":"unavailable"`) {
		t.Fatalf("unavailable stats invented data or failed request: %d %s", w.Code, w.Body)
	}

	f = newFixture()
	f.repo.getErr = domain.ErrRouteNotFound
	w = f.request(t, http.MethodGet, "/api/v1/routes/"+testRouteID+"/stats", "", nil)
	assertError(t, w, http.StatusNotFound, "route_not_found")
	if f.stats.calls != 0 {
		t.Fatal("foreign or missing route reached native statistics")
	}
}

func TestStatsRejectsQueriesAndWrongMethodsBeforeDependencies(t *testing.T) {
	f := newFixture()
	w := f.request(t, http.MethodGet, "/api/v1/routes/"+testRouteID+"/stats?history=true", "", nil)
	assertError(t, w, http.StatusBadRequest, "invalid_request")
	if f.auth.calls != 0 || len(f.repo.calls) != 0 || f.stats.calls != 0 {
		t.Fatalf("stats query reached dependencies: auth=%d repo=%v stats=%d", f.auth.calls, f.repo.calls, f.stats.calls)
	}

	f = newFixture()
	w = f.request(t, http.MethodPost, "/api/v1/routes/"+testRouteID+"/stats", "{}", nil)
	assertError(t, w, http.StatusMethodNotAllowed, "invalid_request")
	if w.Header().Get("Allow") != http.MethodGet || f.auth.calls != 0 || len(f.repo.calls) != 0 || f.stats.calls != 0 {
		t.Fatalf("stats method handling incorrect: allow=%q auth=%d repo=%v stats=%d", w.Header().Get("Allow"), f.auth.calls, f.repo.calls, f.stats.calls)
	}
}

func TestStatsFailsClosedForIncompleteOrContradictorySnapshots(t *testing.T) {
	connections, upload, download := int64(3), int64(202), int64(101)
	negative := int64(-1)
	online, unknown := "online", "private-state"
	tests := []struct {
		name         string
		snapshot     runtimestats.Snapshot
		availability runtimestats.Availability
	}{
		{name: "negative connections", snapshot: runtimestats.Snapshot{CurrentConnections: &negative, UploadBytesToday: &upload, DownloadBytesToday: &download, ProxyState: &online, ObservedAt: testTime, Availability: runtimestats.Available}, availability: runtimestats.Unavailable},
		{name: "negative upload", snapshot: runtimestats.Snapshot{CurrentConnections: &connections, UploadBytesToday: &negative, DownloadBytesToday: &download, ProxyState: &online, ObservedAt: testTime, Availability: runtimestats.Available}, availability: runtimestats.Unavailable},
		{name: "negative download", snapshot: runtimestats.Snapshot{CurrentConnections: &connections, UploadBytesToday: &upload, DownloadBytesToday: &negative, ProxyState: &online, ObservedAt: testTime, Availability: runtimestats.Available}, availability: runtimestats.Unavailable},
		{name: "missing connections", snapshot: runtimestats.Snapshot{UploadBytesToday: &upload, DownloadBytesToday: &download, ProxyState: &online, ObservedAt: testTime, Availability: runtimestats.Available}, availability: runtimestats.Unavailable},
		{name: "missing upload", snapshot: runtimestats.Snapshot{CurrentConnections: &connections, DownloadBytesToday: &download, ProxyState: &online, ObservedAt: testTime, Availability: runtimestats.Available}, availability: runtimestats.Unavailable},
		{name: "missing download", snapshot: runtimestats.Snapshot{CurrentConnections: &connections, UploadBytesToday: &upload, ProxyState: &online, ObservedAt: testTime, Availability: runtimestats.Available}, availability: runtimestats.Unavailable},
		{name: "unknown state", snapshot: runtimestats.Snapshot{CurrentConnections: &connections, UploadBytesToday: &upload, DownloadBytesToday: &download, ProxyState: &unknown, ObservedAt: testTime, Availability: runtimestats.Available}, availability: runtimestats.Unavailable},
		{name: "missing observation time", snapshot: runtimestats.Snapshot{CurrentConnections: &connections, UploadBytesToday: &upload, DownloadBytesToday: &download, ProxyState: &online, Availability: runtimestats.Available}, availability: runtimestats.Unavailable},
		{name: "not observed with injected fields", snapshot: runtimestats.Snapshot{CurrentConnections: &connections, UploadBytesToday: &upload, DownloadBytesToday: &download, ProxyState: &online, ObservedAt: testTime, Availability: runtimestats.NotObserved}, availability: runtimestats.NotObserved},
		{name: "unavailable with injected fields", snapshot: runtimestats.Snapshot{CurrentConnections: &connections, UploadBytesToday: &upload, DownloadBytesToday: &download, ProxyState: &online, ObservedAt: testTime, Availability: runtimestats.Unavailable}, availability: runtimestats.Unavailable},
		{name: "unknown availability", snapshot: runtimestats.Snapshot{CurrentConnections: &connections, UploadBytesToday: &upload, DownloadBytesToday: &download, ProxyState: &online, ObservedAt: testTime, Availability: "private"}, availability: runtimestats.Unavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture()
			f.stats.snapshot = test.snapshot
			w := f.request(t, http.MethodGet, "/api/v1/routes/"+testRouteID+"/stats", "", nil)
			if w.Code != http.StatusOK {
				t.Fatalf("stats response: %d %s", w.Code, w.Body)
			}
			var body statsDTO
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Availability != test.availability || body.CurrentConnections != nil || body.UploadBytesToday != nil || body.DownloadBytesToday != nil || body.ProxyState != nil {
				t.Fatalf("invalid snapshot crossed public boundary: %#v", body)
			}
		})
	}
}
