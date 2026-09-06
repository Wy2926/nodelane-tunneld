package controlserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/bff"
	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/session"
)

// This opt-in, loopback-only test hosts the real API on an isolated database for
// browser QA. Its synthetic session adapter is never compiled into tunneld.
func TestConsoleBrowserFixture(t *testing.T) {
	if os.Getenv("NODELANE_CONSOLE_PREVIEW") != "1" {
		t.Skip("explicit browser fixture is required")
	}
	f := isolatedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	runtime, err := Open(ctx, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	account, err := runtime.postgres.ResolveAccount(ctx, f.cfg.OIDCIssuer, "browser-fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"webhook", "preview", "docs"} {
		if _, err := runtime.postgres.CreateRoute(ctx, domain.CreateRouteCommand{AccountID: account.ID, Protocol: "http", Subdomain: name, IdempotencyKey: "browser-" + name}); err != nil {
			t.Fatal(err)
		}
	}
	id := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{39}, 32))
	now := time.Now().UTC()
	record := session.Record{ID: id, AccountID: account.ID, CSRFToken: "browser-fixture-csrf", CreatedAt: now, ExpiresAt: now.Add(time.Hour), Version: 1,
		Tokens: identity.OIDCTokens{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", IDToken: "fixture-id", AccessTokenExpiresAt: now.Add(time.Hour),
			Identity: identity.OIDCIdentity{Issuer: f.cfg.OIDCIssuer, Subject: "browser-fixture", ClientID: f.cfg.OIDCWebClientID, Name: "Local Test"}}}
	if err := runtime.sessions.CreateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__fixture/close" && r.Method == http.MethodPost {
			w.WriteHeader(204)
			cancel()
			return
		}
		r.AddCookie(&http.Cookie{Name: bff.SessionCookieName, Value: id})
		if r.Header.Get("Origin") == origin {
			r.Header.Set("Origin", f.cfg.PublicOrigin)
		}
		runtime.Handler().ServeHTTP(w, r)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Fprintf(os.Stdout, "CONSOLE_FIXTURE_URL=%s/console/tunnels?lang=zh-CN\n", origin)
	<-ctx.Done()
	_ = server.Close()
	if err := <-done; err != nil && !strings.Contains(err.Error(), "Server closed") {
		t.Error(err)
	}
}
