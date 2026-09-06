package controlserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type FRPClientConfig struct {
	ServerAddr    string `json:"server_addr"`
	ServerPort    int    `json:"server_port"`
	TLSServerName string `json:"tls_server_name"`
	TrustedCAPEM  string `json:"trusted_ca_pem"`
}

type OIDCClientConfig struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"client_id"`
	Resource string `json:"resource"`
}

type ClientConfig struct {
	FRP  FRPClientConfig  `json:"frp"`
	OIDC OIDCClientConfig `json:"oidc"`
}

var fallbackPublicRequestID atomic.Uint64

func newPublicRouter(config ClientConfig, auth, control, anonymous, public http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		path := r.URL.Path
		apiRequest := path == "/api" || strings.HasPrefix(path, "/api/")
		if apiRequest {
			w.Header().Set("X-Request-ID", newPublicRequestID())
		}
		if r.URL.RawPath != "" || strings.Contains(r.URL.EscapedPath(), "%") {
			if apiRequest {
				writePublicAPIError(w, http.StatusNotFound, "route_not_found")
			} else {
				http.NotFound(w, r)
			}
			return
		}
		switch {
		case path == "/healthz":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			writePublicJSON(w, map[string]any{"ok": true, "service": "tunneld"})
		case path == "/api/v1/client-config":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				writePublicAPIError(w, http.StatusMethodNotAllowed, "invalid_request")
				return
			}
			if r.URL.RawQuery != "" || r.URL.ForceQuery {
				writePublicAPIError(w, http.StatusBadRequest, "invalid_request")
				return
			}
			writePublicJSON(w, config)
		case path == "/api/v1/session" || strings.HasPrefix(path, "/auth/"):
			auth.ServeHTTP(w, r)
		case path == "/api/v1/anonymous/runs" || strings.HasPrefix(path, "/api/v1/runs/anr_"):
			anonymous.ServeHTTP(w, r)
		case path == "/api/v1/routes" || strings.HasPrefix(path, "/api/v1/routes/") || strings.HasPrefix(path, "/api/v1/runs/") || path == "/api/v1/launch/redeem":
			control.ServeHTTP(w, r)
		case deniedPublicPath(path):
			if apiRequest {
				writePublicAPIError(w, http.StatusNotFound, "route_not_found")
			} else {
				http.NotFound(w, r)
			}
		default:
			public.ServeHTTP(w, r)
		}
	})
}

func newPublicRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("fallback-%d", fallbackPublicRequestID.Add(1))
}

func writePublicAPIError(w http.ResponseWriter, status int, code string) {
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	envelope.Error.Code, envelope.Error.RequestID = code, w.Header().Get("X-Request-ID")
	envelope.Error.Message = "The request is invalid."
	if code == "route_not_found" {
		envelope.Error.Message = "The route was not found."
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope)
}

func deniedPublicPath(path string) bool {
	for _, prefix := range []string{"/api", "/internal", "/admin", "/clients", "/tunnels", "/traffic", "/logs"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func writePublicJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}
