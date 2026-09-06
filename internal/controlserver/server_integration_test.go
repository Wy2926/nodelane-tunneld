package controlserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/bff"
	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/session"
	frpconfig "github.com/fatedier/frp/pkg/config"
)

func TestRealPersistentServiceMountsNewAPIRefreshesSessionAndObservesNativeConnection(t *testing.T) {
	f := isolatedFixture(t)
	var refreshed atomic.Int32
	var oidcServer *httptest.Server
	oidcServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oidc/.well-known/openid-configuration" {
			issuer := oidcServer.URL + "/oidc"
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/auth", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "revocation_endpoint": issuer + "/revoke", "end_session_endpoint": issuer + "/end", "code_challenge_methods_supported": []string{"S256"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"client_secret_basic"}})
			return
		}
		if r.URL.Path == "/oidc/token" {
			user, password, ok := r.BasicAuth()
			if !ok || user != f.cfg.OIDCWebClientID || password != f.cfg.OIDCWebClientSecret {
				t.Error("refresh did not use configured confidential identity")
			}
			if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "fixture-refresh" {
				t.Error("unexpected refresh request")
			}
			refreshed.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed-access", "refresh_token": "rotated-refresh", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer oidcServer.Close()
	f.cfg.OIDCIssuer = oidcServer.URL + "/oidc"
	var observedRun, observedProxy string
	var online atomic.Bool
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		user, password, ok := r.BasicAuth()
		if !ok || user != f.cfg.FRPSAdminUsername || password != f.cfg.FRPSAdminPassword {
			t.Error("missing native admin credentials")
		}
		if !online.Load() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/api/v2/clients" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "page": 1, "pageSize": 2, "items": []any{map[string]any{"key": observedRun, "user": "", "clientID": observedRun, "runID": observedRun, "online": true, "clientIP": "198.51.100.77"}}}})
			return
		}
		if r.URL.Path == "/api/v2/proxies/"+observedProxy {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": observedProxy, "clientID": observedRun, "spec": map[string]any{"type": "http"}, "status": map[string]any{"phase": "online", "curConns": 0}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer admin.Close()
	f.cfg.FRPSAdminURL = admin.URL
	stock, _, err := frpconfig.LoadServerConfig(f.cfg.FRPSConfigFile, true)
	if err != nil {
		t.Fatal(err)
	}
	adminURL, _ := url.Parse(admin.URL)
	stock.WebServer.Port, _ = strconv.Atoi(adminURL.Port())
	writeStockConfig(t, f.cfg.FRPSConfigFile, *stock)
	runtime, err := openWithHTTPClient(context.Background(), f.cfg, oidcServer.Client())
	if err != nil {
		t.Fatalf("open persistent service: %v", err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	account, err := runtime.postgres.ResolveAccount(context.Background(), f.cfg.OIDCIssuer, "fixture-subject")
	if err != nil {
		t.Fatal(err)
	}
	identifier := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
	record := session.Record{ID: identifier, AccountID: account.ID, CSRFToken: "fixture-csrf", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC(), Version: 1,
		Tokens: identity.OIDCTokens{AccessToken: "expired-access", RefreshToken: "fixture-refresh", IDToken: "fixture-id", AccessTokenExpiresAt: time.Now().Add(-time.Minute), Identity: identity.OIDCIdentity{Issuer: f.cfg.OIDCIssuer, Subject: "fixture-subject", ClientID: f.cfg.OIDCWebClientID}}}
	if err := runtime.sessions.CreateSession(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, token string, browser bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "192.0.2.9:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "fixture:"+path)
		if browser {
			r.AddCookie(&http.Cookie{Name: bff.SessionCookieName, Value: identifier})
			r.Header.Set("Origin", f.cfg.PublicOrigin)
			r.Header.Set("X-CSRF-Token", "fixture-csrf")
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		runtime.Handler().ServeHTTP(w, r)
		return w
	}
	created := request(http.MethodPost, "/api/v1/routes", `{"protocol":"http","subdomain":"service-fixture"}`, "", true)
	if created.Code != http.StatusCreated {
		t.Fatalf("route creation failed: %d %s", created.Code, created.Body.String())
	}
	var routeResponse struct {
		Route domain.Route `json:"route"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &routeResponse); err != nil {
		t.Fatal(err)
	}
	if refreshed.Load() != 1 {
		t.Fatalf("expired browser access token was not refreshed once: %d", refreshed.Load())
	}
	updated, err := runtime.sessions.ReadSession(context.Background(), identifier)
	if err != nil || updated.Version != 2 || updated.Tokens.RefreshToken != "rotated-refresh" {
		t.Fatalf("session refresh was not committed: %v", err)
	}
	launch := request(http.MethodPost, "/api/v1/routes/"+routeResponse.Route.ID+"/launch-codes", `{}`, "", true)
	var launchResponse struct {
		Code string `json:"launch_code"`
	}
	if launch.Code != http.StatusCreated || json.Unmarshal(launch.Body.Bytes(), &launchResponse) != nil {
		t.Fatalf("launch issuance failed: %s", launch.Body.String())
	}
	redemption := request(http.MethodPost, "/api/v1/launch/redeem", `{"launch_code":"`+launchResponse.Code+`","nonce":"fixture-redemption"}`, "", false)
	var start struct {
		Run   domain.Run `json:"run"`
		Token string     `json:"credential_token"`
	}
	if redemption.Code != http.StatusCreated || json.Unmarshal(redemption.Body.Bytes(), &start) != nil {
		t.Fatalf("redemption failed: %s", redemption.Body.String())
	}
	observedRun, observedProxy = start.Run.ID, routeResponse.Route.ID
	proof := domain.RunProof{RunID: start.Run.ID, Token: start.Token}
	heartbeat := request(http.MethodPost, "/api/v1/runs/"+start.Run.ID+"/heartbeat", `{}`, start.Token, false)
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("starting heartbeat failed: %s", heartbeat.Body.String())
	}
	authorized, err := runtime.postgres.AuthorizeRun(context.Background(), proof)
	if err != nil || authorized.Run.ConnectedAt != nil {
		t.Fatalf("404 observation created online state: %v", err)
	}
	online.Store(true)
	heartbeat = request(http.MethodPost, "/api/v1/runs/"+start.Run.ID+"/heartbeat", `{}`, start.Token, false)
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("online heartbeat failed: %s", heartbeat.Body.String())
	}
	authorized, err = runtime.postgres.AuthorizeRun(context.Background(), proof)
	if err != nil || authorized.Run.Status != domain.RunOnline || authorized.Run.ConnectedIP != netip.MustParseAddr("198.51.100.77") || authorized.Run.LeaseExpiresAt == nil {
		t.Fatalf("native connection was not confirmed accurately: %+v %v", authorized.Run, err)
	}
	allocation := request(http.MethodPost, "/api/v1/anonymous/runs", `{"installation_id":"fixture-installation","protocol":"http","local_host":"127.0.0.1","local_port":8080}`, "", false)
	if allocation.Code != http.StatusServiceUnavailable {
		t.Fatalf("fresh anonymous namespace was made ready automatically: %d %s", allocation.Code, allocation.Body.String())
	}
	pluginRequest := httptest.NewRequest(http.MethodPost, "/internal/frp?version=0.1.0&op=Login", strings.NewReader(`{"version":"0.1.0","op":"Login","content":{"privilege_key":"`+start.Token+`","metas":{"nodelane_run_id":"`+start.Run.ID+`","nodelane_run_token":"`+start.Token+`"}}}`))
	pluginRequest.RemoteAddr = "127.0.0.1:7654"
	pluginRequest.Header.Set("Content-Type", "application/json")
	pluginResponse := httptest.NewRecorder()
	runtime.PluginHandler().ServeHTTP(pluginResponse, pluginRequest)
	var decision frpplugin.Response
	if pluginResponse.Code != 200 || json.Unmarshal(pluginResponse.Body.Bytes(), &decision) != nil || decision.Reject {
		t.Fatalf("private plugin did not authorize real persisted run: %s", pluginResponse.Body.String())
	}
	pluginRequest.RemoteAddr = "198.51.100.8:7654"
	pluginResponse = httptest.NewRecorder()
	runtime.PluginHandler().ServeHTTP(pluginResponse, pluginRequest)
	if pluginResponse.Code != 404 {
		t.Fatal("plugin accepted a non-loopback peer")
	}
}

func TestOpenRejectsBadCAWithoutDatabaseOrRedisWrites(t *testing.T) {
	f := isolatedFixture(t)
	f.cfg.FRPTrustedCAFile = "missing-public-ca-file.pem"
	if runtime, err := Open(context.Background(), f.cfg); err == nil || runtime != nil {
		t.Fatal("service accepted unavailable public CA")
	}
	var tables int
	if err := f.db.QueryRow(`SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname=current_schema()`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("invalid public CA caused schema mutation: count=%d err=%v", tables, err)
	}
	keys, err := f.redis.Keys(context.Background(), f.cfg.RedisPrefix+":*").Result()
	if err != nil || len(keys) != 0 {
		t.Fatalf("invalid public CA caused Redis mutation: count=%d err=%v", len(keys), err)
	}
}

func TestComposedLogoutDeletesExpiredAccessSessionBeforeOIDCOutage(t *testing.T) {
	f := isolatedFixture(t)
	ctx := context.Background()
	identifier := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{53}, 32))
	var runtime *Server
	var loggingOut atomic.Bool
	var discoveries, tokenRequests atomic.Int32
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oidc/token" {
			tokenRequests.Add(1)
		}
		discoveries.Add(1)
		if loggingOut.Load() {
			if _, err := runtime.sessions.ReadSession(r.Context(), identifier); !errors.Is(err, session.ErrNotFound) {
				t.Error("logout attempted remote identity work before deleting its local session")
			}
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer issuer.Close()
	f.cfg.OIDCIssuer = issuer.URL + "/oidc"
	var err error
	runtime, err = openWithHTTPClient(ctx, f.cfg, issuer.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	account, err := runtime.postgres.ResolveAccount(ctx, f.cfg.OIDCIssuer, "logout-fixture")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := session.Record{ID: identifier, AccountID: account.ID, CSRFToken: "logout-csrf", CreatedAt: now, ExpiresAt: now.Add(time.Hour), Version: 1,
		Tokens: identity.OIDCTokens{AccessToken: "expired-access", RefreshToken: "logout-refresh", IDToken: "logout-id", AccessTokenExpiresAt: now.Add(-time.Minute),
			Identity: identity.OIDCIdentity{Issuer: f.cfg.OIDCIssuer, Subject: "logout-fixture", ClientID: f.cfg.OIDCWebClientID}}}
	if err := runtime.sessions.CreateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	read := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	read.AddCookie(&http.Cookie{Name: bff.SessionCookieName, Value: identifier})
	readResponse := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusServiceUnavailable || discoveries.Load() != 1 {
		t.Fatal("ordinary session reads bypassed the refresh requirement")
	}
	if _, err := runtime.sessions.ReadSession(ctx, identifier); err != nil {
		t.Fatalf("failed session refresh unexpectedly removed the local session: %v", err)
	}
	loggingOut.Store(true)
	request := httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader(`{}`))
	request.AddCookie(&http.Cookie{Name: bff.SessionCookieName, Value: identifier})
	request.Header.Set("Origin", f.cfg.PublicOrigin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", record.CSRFToken)
	response := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(response, request)
	if _, err := runtime.sessions.ReadSession(ctx, identifier); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("OIDC outage prevented local logout: %v", err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != bff.SessionCookieName || cookies[0].MaxAge >= 0 || cookies[0].Value != "" {
		t.Fatal("OIDC outage prevented clearing the browser session cookie")
	}
	var body struct {
		LoggedOut     bool   `json:"logged_out"`
		EndSessionURL string `json:"end_session_url"`
		Error         struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusServiceUnavailable || body.Error.Code != "dependency_unavailable" || body.LoggedOut || body.EndSessionURL != "" {
		t.Fatalf("degraded logout claimed unconfirmed remote revocation: status=%d body=%s", response.Code, response.Body.String())
	}
	if discoveries.Load() != 2 || tokenRequests.Load() != 0 {
		t.Fatal("logout refreshed an expired access token before cleanup")
	}
}

func TestOIDCOutageDoesNotPreventStartupOrIndependentAnonymousAllocation(t *testing.T) {
	f := isolatedFixture(t)
	var anonRun, anonProxy string
	var nativeOnline atomic.Bool
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !nativeOnline.Load() {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":404,"msg":"not found","data":null}`))
			return
		}
		if r.URL.Path != "/api/v2/proxies/"+anonProxy {
			t.Error("anonymous observer queried an unrelated proxy")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "success", "data": map[string]any{"name": anonProxy, "clientID": anonRun, "spec": map[string]any{"type": "http"}, "status": map[string]any{"phase": "online", "curConns": 0}}})
	}))
	defer admin.Close()
	f.cfg.FRPSAdminURL = admin.URL
	stock, _, err := frpconfig.LoadServerConfig(f.cfg.FRPSConfigFile, true)
	if err != nil {
		t.Fatal(err)
	}
	adminURL, _ := url.Parse(admin.URL)
	stock.WebServer.Port, _ = strconv.Atoi(adminURL.Port())
	writeStockConfig(t, f.cfg.FRPSConfigFile, *stock)
	var discoveries atomic.Int32
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		discoveries.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer issuer.Close()
	f.cfg.OIDCIssuer = issuer.URL + "/oidc"
	runtime, err := openWithHTTPClient(context.Background(), f.cfg, issuer.Client())
	if err != nil {
		t.Fatalf("OIDC outage prevented persistent service startup: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if discoveries.Load() != 0 {
		t.Fatal("startup eagerly contacted OIDC")
	}
	// Only this isolated fixture authorizes its own empty anonymous namespace.
	anonymousStore, err := anonymous.NewStore(anonymous.Config{Client: f.redis, Prefix: f.cfg.RedisPrefix + ":anonymous", CredentialPepper: f.cfg.AnonymousPepper,
		ReplayKey: f.cfg.AnonymousReplayKey, FenceOwnerToken: f.cfg.AnonymousFenceToken, Random: rand.Reader, PublicDomain: f.cfg.PublicDomain,
		TCPPorts: portRange(f.cfg.TCPPortStart, f.cfg.TCPPortEnd), UDPPorts: portRange(f.cfg.UDPPortStart, f.cfg.UDPPortEnd)})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := anonymousStore.ObserveResourceFence(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anonymousStore.MarkResourcesVerified(context.Background(), fence); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/anonymous/runs", strings.NewReader(`{"installation_id":"outage-fixture","protocol":"http","local_host":"127.0.0.1","local_port":8080}`))
	request.RemoteAddr = "192.0.2.8:5432"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "outage-independent")
	response := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated || discoveries.Load() != 0 {
		t.Fatalf("anonymous allocation depended on OIDC: %d %s", response.Code, response.Body.String())
	}
	var allocation struct {
		Run struct {
			ID        string `json:"id"`
			ProxyName string `json:"proxy_name"`
		} `json:"run"`
		Token string `json:"credential_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &allocation); err != nil {
		t.Fatal(err)
	}
	anonRun, anonProxy = allocation.Run.ID, allocation.Run.ProxyName
	for _, expected := range []anonymous.State{anonymous.StateReserved, anonymous.StateOnline} {
		if expected == anonymous.StateOnline {
			nativeOnline.Store(true)
		}
		heartbeat := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+anonRun+"/heartbeat", strings.NewReader(`{}`))
		heartbeat.RemoteAddr = "192.0.2.8:5432"
		heartbeat.Header.Set("Content-Type", "application/json")
		heartbeat.Header.Set("Authorization", "Bearer "+allocation.Token)
		result := httptest.NewRecorder()
		runtime.Handler().ServeHTTP(result, heartbeat)
		if result.Code != http.StatusOK {
			t.Fatalf("anonymous heartbeat failed: %s", result.Body.String())
		}
		run, err := anonymousStore.AuthorizeLogin(context.Background(), anonRun, allocation.Token)
		if err != nil || run.State != expected {
			t.Fatalf("anonymous heartbeat online evidence not enforced: state=%s err=%v", run.State, err)
		}
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/routes", nil)
	request.Header.Set("Authorization", "Bearer native-token")
	response = httptest.NewRecorder()
	runtime.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || discoveries.Load() != 1 {
		t.Fatalf("native API did not fail closed on OIDC outage: %d %s", response.Code, response.Body.String())
	}
}
