package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicHandlerPreservesSecurityHeaders(t *testing.T) {
	handler := PublicHandler("")
	for _, path := range []string{"/", "/run.sh", "/run.ps1", "/run.cmd", "/install.cmd", "/missing-page"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			for header, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"Referrer-Policy":        "strict-origin-when-cross-origin",
				"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
			} {
				if got := response.Header().Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
			if path == "/" && response.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("public HTML has no content security policy")
			}
			if path == "/run.sh" && response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("bootstrap script must not be cached")
			}
		})
	}
}

func TestPublicHandlerDoesNotExposeBusinessOrConsoleRoutes(t *testing.T) {
	handler := PublicHandler("")
	for _, path := range []string{
		"/api/v1/clients", "/api/v1/me", "/api/v1/tunnels", "/api/v1/tunnels/tun_test",
		"/internal/frp", "/internal/admin/ip-bans", "/internal/admin/clients/cli_test/ban",
		"/console", "/console/", "/console/en/", "/console/_shells/en/", "/console/_shells/en/index.html",
		"/public/%2e%2e/console/_shells/en/index.html", "/public%2f..%2fconsole/_shells/en/index.html",
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("public-only route status = %d, want 404", response.Code)
			}
		})
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/clients", bytes.NewBufferString("{}")))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("removed client registration status = %d, want 405", response.Code)
	}
}
