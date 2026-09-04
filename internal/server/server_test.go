package server

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/lease"
	"github.com/Wy2926/nodelane-tunneld/internal/store"
)

func TestReleaseFilesAreServedWhenConfigured(t *testing.T) {
	server, _ := testServer()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "stable.txt"), []byte("0.1.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.cfg.ReleaseDir = directory
	request := httptest.NewRequest(http.MethodGet, "/releases/stable.txt", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "0.1.1\n" {
		t.Fatalf("release response status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestFrontendServesLocalizedPagesAndSEOFiles(t *testing.T) {
	server, _ := testServer()
	handler := server.Handler()

	tests := []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/", contentType: "text/html", contains: `<html lang="en" dir="ltr">`},
		{path: "/zh-cn/", contentType: "text/html", contains: `<html lang="zh-CN" dir="ltr">`},
		{path: "/robots.txt", contentType: "text/plain", contains: "Sitemap: https://tunnel.nodelane.net/sitemap.xml"},
		{path: "/sitemap.xml", contentType: "text/xml", contains: `hreflang="en"`},
		{path: "/nodelane-mark.png", contentType: "image/png", contains: ""},
		{path: "/nodelane-mark-96.png", contentType: "image/png", contains: ""},
		{path: "/nodelane-mark-192.png", contentType: "image/png", contains: ""},
		{path: "/nodelane-tunnel-og.png", contentType: "image/png", contains: ""},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("response status=%d", response.Code)
			}
			if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, test.contentType) {
				t.Fatalf("Content-Type=%q, want prefix %q", contentType, test.contentType)
			}
			if test.contains != "" && !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("response did not contain %q", test.contains)
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/missing-page", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing frontend route status=%d, want 404", response.Code)
	}

	assets, err := fs.Glob(publicAssets, "assets/web/assets/*.css")
	if err != nil || len(assets) == 0 {
		t.Fatalf("find embedded frontend assets: matches=%v error=%v", assets, err)
	}
	request = httptest.NewRequest(http.MethodGet, strings.TrimPrefix(assets[0], "assets/web"), nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("frontend asset response status=%d", response.Code)
	}
	if cacheControl := response.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "immutable") {
		t.Fatalf("frontend asset Cache-Control=%q, want immutable", cacheControl)
	}

	for _, scriptPath := range []string{"/run.sh", "/run.ps1"} {
		request = httptest.NewRequest(http.MethodGet, scriptPath, nil)
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if robots := response.Header().Get("X-Robots-Tag"); robots != "noindex, nofollow" {
			t.Fatalf("%s X-Robots-Tag=%q, want noindex, nofollow", scriptPath, robots)
		}
		body := response.Body.String()
		if !strings.Contains(body, "nt_") || strings.Contains(body, "ft_") {
			t.Fatalf("%s does not exclusively reference nt release assets", scriptPath)
		}
	}
}

func TestInternalEndpointsRejectForwardedPublicRequests(t *testing.T) {
	_, handler := testServer()
	request := httptest.NewRequest(http.MethodPost, "/internal/frp?op=Login", bytes.NewReader([]byte(`{"content":{}}`)))
	request.RemoteAddr = "127.0.0.1:41000"
	request.Header.Set("X-Real-IP", "203.0.113.10")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("forwarded internal request status=%d, want 404", response.Code)
	}
}

func TestRequestIPTrustBoundary(t *testing.T) {
	server, _ := testServer()
	server.cfg.TrustedProxyRanges = []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
	}

	tests := []struct {
		name       string
		remoteAddr string
		realIP     string
		forwarded  string
		want       string
		wantError  bool
	}{
		{
			name: "untrusted peer cannot spoof headers", remoteAddr: "203.0.113.9:4000",
			realIP: "192.0.2.1", forwarded: "192.0.2.2", want: "203.0.113.9",
		},
		{
			name: "trusted proxy supplies real ip", remoteAddr: "127.0.0.1:4000",
			realIP: "198.51.100.7", forwarded: "192.0.2.2", want: "198.51.100.7",
		},
		{
			name: "forwarded chain skips trusted hops", remoteAddr: "127.0.0.1:4000",
			forwarded: "198.51.100.8, 10.0.0.4", want: "198.51.100.8",
		},
		{
			name: "malformed proxy header is rejected", remoteAddr: "127.0.0.1:4000",
			realIP: "not-an-ip", wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.RemoteAddr = test.remoteAddr
			request.Header.Set("X-Real-IP", test.realIP)
			request.Header.Set("X-Forwarded-For", test.forwarded)
			got, err := server.requestIP(request)
			if test.wantError {
				if err == nil {
					t.Fatalf("requestIP() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("requestIP() error = %v", err)
			}
			if got.String() != test.want {
				t.Fatalf("requestIP() = %s, want %s", got, test.want)
			}
		})
	}
}

func testServer() (*Server, http.Handler) {
	cfg := Config{
		ListenAddr: ":0", DevMode: true, PublicScheme: "http", PublicDomain: "tunnel.nodelane.net",
		NodeID:        "test",
		FRPServerAddr: "tunnel.nodelane.net", FRPServerPort: 7000,
		FRPAuthToken: "frp-token", FRPTLSServerName: "tunnel.nodelane.net", FRPBandwidth: "5MB",
		TokenPepper: "test-pepper", TunnelJWTSecret: []byte("01234567890123456789012345678901"), AdminToken: "admin-secret",
		TunnelTTL: time.Hour, MaxPerClient: 1, MaxPerIP: 2,
		TCPPortStart: 20000, TCPPortEnd: 20010, UDPPortStart: 30000, UDPPortEnd: 30010,
	}
	server := New(cfg, store.NewMemory(), lease.NewMemory(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	return server, server.Handler()
}

func TestClientTunnelAndFRPPluginFlow(t *testing.T) {
	server, handler := testServer()
	credentials := registerTestClient(t, handler, "203.0.113.10:41000")
	tunnel := createTestTunnel(t, handler, credentials, "http", 3000, http.StatusCreated)
	if tunnel.PublicURL == "" || tunnel.Subdomain == "" || tunnel.TunnelToken == "" {
		t.Fatalf("incomplete tunnel response: %#v", tunnel)
	}
	if want := "http://" + tunnel.Subdomain + ".tunnel.nodelane.net"; tunnel.PublicURL != want {
		t.Fatalf("public URL = %q, want %q", tunnel.PublicURL, want)
	}

	login := frpRequest("Login", map[string]any{
		"client_id": credentials.ClientID, "run_id": "", "client_address": "203.0.113.10:51000",
		"metas": map[string]any{"tunnel_token": tunnel.TunnelToken},
	})
	pluginResponse := callJSON(t, handler, http.MethodPost, "/internal/frp?version=0.1.0&op=Login", login, "", "127.0.0.1:5000")
	if rejected(pluginResponse) {
		t.Fatalf("login rejected: %#v", pluginResponse)
	}

	newProxy := frpRequest("NewProxy", map[string]any{
		"user": map[string]any{
			"user": "", "run_id": "run-1",
			"metas": map[string]any{"tunnel_token": tunnel.TunnelToken},
		},
		"proxy_name": tunnel.ProxyName, "proxy_type": "http", "subdomain": tunnel.Subdomain,
		"custom_domains": []any{}, "bandwidth_limit": "999GB", "bandwidth_limit_mode": "client",
		"metas": map[string]any{"session_id": tunnel.ID},
	})
	pluginResponse = callJSON(t, handler, http.MethodPost, "/internal/frp?version=0.1.0&op=NewProxy", newProxy, "", "127.0.0.1:5000")
	if rejected(pluginResponse) || pluginResponse["unchange"] != false {
		t.Fatalf("new proxy was not accepted and normalized: %#v", pluginResponse)
	}
	content := pluginResponse["content"].(map[string]any)
	if content["bandwidth_limit"] != "5MB" || content["bandwidth_limit_mode"] != "server" {
		t.Fatalf("server bandwidth policy was not enforced: %#v", content)
	}

	statusResponse := callJSON(t, handler, http.MethodGet, "/api/v1/tunnels/"+tunnel.ID, nil, credentials.ClientToken, "203.0.113.10:41000")
	if statusResponse["status"] != "online" {
		t.Fatalf("tunnel status = %v", statusResponse["status"])
	}

	createTestTunnel(t, handler, credentials, "tcp", 22, http.StatusTooManyRequests)

	badProxy := newProxy
	badContent := badProxy["content"].(map[string]any)
	badContent["subdomain"] = "stolen-name"
	pluginResponse = callJSON(t, handler, http.MethodPost, "/internal/frp?version=0.1.0&op=NewProxy", badProxy, "", "127.0.0.1:5000")
	if !rejected(pluginResponse) {
		t.Fatalf("mismatched subdomain was accepted: %#v", pluginResponse)
	}

	request := httptest.NewRequest(http.MethodDelete, "/api/v1/tunnels/"+tunnel.ID, nil)
	request.RemoteAddr = "203.0.113.10:41000"
	request.Header.Set("Authorization", "Bearer "+credentials.ClientToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body=%s", response.Code, response.Body.String())
	}
	closeProxy := frpRequest("CloseProxy", map[string]any{
		"user": map[string]any{
			"user": "", "run_id": "run-1",
			"metas": map[string]any{"tunnel_token": tunnel.TunnelToken},
		},
		"proxy_name": tunnel.ProxyName,
	})
	pluginResponse = callJSON(t, handler, http.MethodPost, "/internal/frp?version=0.1.0&op=CloseProxy", closeProxy, "", "127.0.0.1:5000")
	if rejected(pluginResponse) {
		t.Fatalf("idempotent CloseProxy callback was rejected: %#v", pluginResponse)
	}
	createTestTunnel(t, handler, credentials, "tcp", 22, http.StatusCreated)

	_ = server
}

func TestHTTPSTunnelURLCanBeEnabledByConfiguration(t *testing.T) {
	server, handler := testServer()
	server.cfg.PublicScheme = "https"
	credentials := registerTestClient(t, handler, "203.0.113.10:41000")
	tunnel := createTestTunnel(t, handler, credentials, "http", 3000, http.StatusCreated)
	if want := "https://" + tunnel.Subdomain + ".tunnel.nodelane.net"; tunnel.PublicURL != want {
		t.Fatalf("public URL = %q, want %q", tunnel.PublicURL, want)
	}
}

func TestConfigRejectsUnsupportedPublicScheme(t *testing.T) {
	server, _ := testServer()
	server.cfg.PublicScheme = "ftp"
	if err := server.cfg.Validate(); err == nil {
		t.Fatal("Config.Validate() accepted an unsupported public scheme")
	}
}

func TestIPBanBlocksRegistrationAndPluginHeartbeat(t *testing.T) {
	_, handler := testServer()
	credentials := registerTestClient(t, handler, "198.51.100.23:41000")
	tunnel := createTestTunnel(t, handler, credentials, "http", 3000, http.StatusCreated)
	login := frpRequest("Login", map[string]any{
		"client_id": credentials.ClientID, "run_id": "run-ban", "client_address": "198.51.100.23:51000",
		"metas": map[string]any{"tunnel_token": tunnel.TunnelToken},
	})
	pluginResponse := callJSON(t, handler, http.MethodPost, "/internal/frp?version=0.1.0&op=Login", login, "", "127.0.0.1:5000")
	if rejected(pluginResponse) {
		t.Fatalf("login before ban was rejected: %#v", pluginResponse)
	}

	ban := map[string]any{"network": "198.51.100.0/24", "scope": "tunnel_client", "reason": "abuse"}
	callJSON(t, handler, http.MethodPost, "/internal/admin/ip-bans", ban, "admin-secret", "127.0.0.1:5000")

	request := httptest.NewRequest(http.MethodPost, "/api/v1/clients", bytes.NewReader([]byte("{}")))
	request.RemoteAddr = "198.51.100.99:41000"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("banned registration status = %d, body=%s", response.Code, response.Body.String())
	}

	ping := frpRequest("Ping", map[string]any{
		"user": map[string]any{
			"user": "", "run_id": "run-ban",
			"metas": map[string]any{"tunnel_token": tunnel.TunnelToken},
		},
	})
	pluginResponse = callJSON(t, handler, http.MethodPost, "/internal/frp?version=0.1.0&op=Ping", ping, "", "127.0.0.1:5000")
	if !rejected(pluginResponse) {
		t.Fatalf("heartbeat from newly banned IP was accepted: %#v", pluginResponse)
	}
}

func registerTestClient(t *testing.T, handler http.Handler, remote string) Credentials {
	t.Helper()
	response := callJSON(t, handler, http.MethodPost, "/api/v1/clients", map[string]any{}, "", remote)
	return Credentials{ClientID: response["client_id"].(string), ClientToken: response["client_token"].(string)}
}

type Credentials struct {
	ClientID    string
	ClientToken string
}

type testTunnel struct {
	ID          string
	ProxyName   string
	Subdomain   string
	PublicURL   string
	TunnelToken string
}

func createTestTunnel(t *testing.T, handler http.Handler, credentials Credentials, protocol string, port, wantStatus int) testTunnel {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"protocol": protocol, "local_port": port, "client_version": "test"})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tunnels", bytes.NewReader(body))
	request.RemoteAddr = "203.0.113.10:41000"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+credentials.ClientToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("create tunnel status = %d, want %d, body=%s", response.Code, wantStatus, response.Body.String())
	}
	if wantStatus != http.StatusCreated {
		return testTunnel{}
	}
	var result struct {
		ID          string `json:"id"`
		ProxyName   string `json:"proxy_name"`
		Subdomain   string `json:"subdomain"`
		PublicURL   string `json:"public_url"`
		TunnelToken string `json:"tunnel_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return testTunnel(result)
}

func callJSON(t *testing.T, handler http.Handler, method, path string, body any, token, remote string) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	request := httptest.NewRequest(method, path, reader)
	request.RemoteAddr = remote
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code < 200 || response.Code >= 300 {
		t.Fatalf("%s %s status=%d body=%s", method, path, response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func rejected(response map[string]any) bool {
	value, _ := response["reject"].(bool)
	return value
}

func TestProxyNameMustMatchExactly(t *testing.T) {
	claims := identity.TunnelClaims{Subject: "cli_test", ProxyName: "tun_test"}
	if !proxyNameMatches(claims, "tun_test") {
		t.Fatal("exact proxy name was rejected")
	}
	if proxyNameMatches(claims, "cli_test.tun_test") {
		t.Fatal("prefixed proxy name was accepted")
	}
}

func TestLoginRejectsUserField(t *testing.T) {
	_, handler := testServer()
	credentials := registerTestClient(t, handler, "203.0.113.10:41000")
	tunnel := createTestTunnel(t, handler, credentials, "http", 3000, http.StatusCreated)
	login := frpRequest("Login", map[string]any{
		"client_id": credentials.ClientID,
		"user":      credentials.ClientID,
		"metas":     map[string]any{"tunnel_token": tunnel.TunnelToken},
	})
	response := callJSON(t, handler, http.MethodPost, "/internal/frp?version=0.1.0&op=Login", login, "", "127.0.0.1:5000")
	if !rejected(response) {
		t.Fatalf("login with user field was accepted: %#v", response)
	}
}

func frpRequest(op string, content map[string]any) map[string]any {
	return map[string]any{"version": "0.1.0", "op": op, "content": content}
}
