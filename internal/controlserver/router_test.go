package controlserver

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/server"
)

func TestPublicRouterSeparatesRunNamespacesAndNeverMountsLegacyOrPluginRoutes(t *testing.T) {
	marked := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Owner", name)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	router := newPublicRouter(ClientConfig{}, marked("auth"), marked("control"), marked("anonymous"), server.PublicHandler(""))
	for _, test := range []struct {
		path, owner string
		status      int
	}{
		{"/api/v1/session", "auth", 204}, {"/auth/login", "auth", 204}, {"/api/v1/routes", "control", 204},
		{"/api/v1/anonymous/runs", "anonymous", 204}, {"/api/v1/runs/anr_aaaaaaaaaaaaaaaaaaaaaaaaaa/heartbeat", "anonymous", 204},
		{"/api/v1/runs/run_aaaaaaaaaaaaaaaaaaaaaaaaaa/heartbeat", "control", 204},
		{"/clients", "", 404}, {"/tunnels", "", 404}, {"/admin/logs", "", 404}, {"/traffic", "", 404},
		{"/api/v1/clients", "", 404}, {"/api/v1/tunnels", "", 404}, {"/api/v1/me", "", 404},
		{"/internal/frp", "", 404}, {"/internal/admin/ip-bans", "", 404}, {"/healthz", "", 200},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.status || response.Header().Get("X-Owner") != test.owner {
			t.Errorf("%s: status=%d owner=%q", test.path, response.Code, response.Header().Get("X-Owner"))
		}
	}
}

func TestPublicAPIRequestsHaveBoundedExecutionContexts(t *testing.T) {
	bounded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, ok := r.Context().Deadline()
		if !ok || deadline.After(time.Now().Add(15*time.Second)) {
			t.Error("public API request has no bounded execution context")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	router := newPublicRouter(ClientConfig{}, bounded, bounded, bounded, http.NotFoundHandler())
	for _, path := range []string{"/api/v1/routes", "/api/v1/anonymous/runs", "/auth/login"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
	}
}

func TestPublicClientConfigIsFixedAndUncacheable(t *testing.T) {
	cfg := ClientConfig{FRP: FRPClientConfig{ServerAddr: "tunnel.test", ServerPort: 7000, TLSServerName: "frps.test", TrustedCAPEM: "public-certificate"},
		OIDC: OIDCClientConfig{Issuer: "https://auth.test/oidc", ClientID: "configured-native", Resource: APIResource}}
	router := newPublicRouter(cfg, http.NotFoundHandler(), http.NotFoundHandler(), http.NotFoundHandler(), http.NotFoundHandler())
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/client-config", nil)
	request.Header.Set("X-Request-ID", "untrusted-caller-request-id")
	router.ServeHTTP(response, request)
	var body map[string]map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || len(body) != 2 || len(body["frp"]) != 4 || len(body["oidc"]) != 3 ||
		body["frp"]["trusted_ca_pem"] != "public-certificate" || body["oidc"]["client_id"] != "configured-native" {
		t.Fatalf("unsafe bootstrap: %d %s", response.Code, response.Body.String())
	}
	if id := response.Header().Get("X-Request-ID"); id == "" || id == "untrusted-caller-request-id" {
		t.Fatal("bootstrap did not generate a response request ID")
	}
	for _, method := range []string{http.MethodPost, http.MethodHead} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/client-config", nil))
		if response.Code != http.StatusMethodNotAllowed || strings.Contains(response.Body.String(), "public-certificate") {
			t.Fatal("bootstrap accepted unsupported method")
		}
	}
}

func TestRouterAPIRejectionsHaveConsistentJSONAndServerRequestIDs(t *testing.T) {
	router := newPublicRouter(ClientConfig{}, http.NotFoundHandler(), http.NotFoundHandler(), http.NotFoundHandler(), http.NotFoundHandler())
	seen := map[string]bool{}
	for _, test := range []struct {
		name, method, path string
		status             int
		code               string
	}{
		{"bootstrap query", http.MethodGet, "/api/v1/client-config?token=private-query", 400, "invalid_request"},
		{"bootstrap empty query", http.MethodGet, "/api/v1/client-config?", 400, "invalid_request"},
		{"bootstrap method", http.MethodPost, "/api/v1/client-config", 405, "invalid_request"},
		{"unknown API", http.MethodGet, "/api/v1/not-a-route", 404, "route_not_found"},
		{"retired API", http.MethodPost, "/api/v1/clients", 404, "route_not_found"},
		{"encoded API prefix", http.MethodGet, "/%61pi/v1/client-config", 404, "route_not_found"},
		{"encoded API separator", http.MethodGet, "/api%2fv1/client-config", 404, "route_not_found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("X-Request-ID", "untrusted-caller-request-id")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var envelope struct {
				Error struct {
					Code      string `json:"code"`
					Message   string `json:"message"`
					RequestID string `json:"request_id"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("API rejection was not JSON: status=%d body=%q", response.Code, response.Body.String())
			}
			id := response.Header().Get("X-Request-ID")
			decoded, err := hex.DecodeString(id)
			if response.Code != test.status || envelope.Error.Code != test.code || envelope.Error.Message == "" || envelope.Error.RequestID != id || err != nil || len(decoded) != 16 || seen[id] {
				t.Fatalf("invalid router error contract: status=%d envelope=%+v id=%q", response.Code, envelope, id)
			}
			seen[id] = true
			if response.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") || response.Header().Get("X-Content-Type-Options") != "nosniff" || strings.Contains(response.Body.String(), "private-query") {
				t.Fatal("router error is cacheable, untyped, or exposes query data")
			}
			if test.status == http.StatusMethodNotAllowed && response.Header().Get("Allow") != http.MethodGet {
				t.Fatal("method rejection omitted Allow header")
			}
		})
	}
}
