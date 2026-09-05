package runtimestats_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/runtimestats"
)

const routeProxyName = "rte_abcdefghijklmnopqrstuvwxyz"

func TestSnapshotMapsOnlyCurrentNativeFields(t *testing.T) {
	observedAt := time.Date(2026, 9, 6, 8, 30, 0, 123, time.FixedZone("test", 8*60*60))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "stats-reader" || password != "private-password" {
			t.Fatal("request did not use the configured Basic Auth credentials")
		}
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/v2/proxies/"+routeProxyName || r.URL.RawQuery != "" {
			t.Fatalf("unexpected native request: %s %s", r.Method, r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"code":200,
			"msg":"success",
			"data":{
				"name":"` + routeProxyName + `",
				"user":"must-not-leak",
				"clientID":"must-not-leak",
				"spec":{"type":"http","http":{"subdomain":"must-not-leak"}},
				"status":{"phase":"online","todayTrafficIn":101,"todayTrafficOut":202,"curConns":3,"lastStartAt":99}
			}
		}`))
	}))
	defer server.Close()

	client, err := runtimestats.NewClient(runtimestats.Options{
		Endpoint: server.URL, Username: "stats-reader", Password: "private-password",
		HTTPClient: server.Client(), Now: func() time.Time { return observedAt },
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := client.Snapshot(context.Background(), routeProxyName)
	if snapshot.Availability != runtimestats.Available || snapshot.ObservedAt != observedAt.UTC() {
		t.Fatalf("unexpected availability/time: %#v", snapshot)
	}
	if snapshot.CurrentConnections == nil || *snapshot.CurrentConnections != 3 ||
		snapshot.UploadBytesToday == nil || *snapshot.UploadBytesToday != 202 ||
		snapshot.DownloadBytesToday == nil || *snapshot.DownloadBytesToday != 101 ||
		snapshot.ProxyState == nil || *snapshot.ProxyState != "online" {
		t.Fatalf("native fields mapped incorrectly: %#v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"must-not-leak", "clientID", "spec", "lastStartAt", "private-password"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("snapshot leaked an upstream or secret field %q: %s", forbidden, encoded)
		}
	}
}

func TestSnapshotDistinguishesNotObservedFromUnavailable(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		availability runtimestats.Availability
	}{
		{name: "not observed", status: http.StatusNotFound, body: `{"code":404,"msg":"no proxy info found","data":null}`, availability: runtimestats.NotObserved},
		{name: "server failure", status: http.StatusInternalServerError, body: `{"code":500,"msg":"private upstream detail","data":null}`, availability: runtimestats.Unavailable},
		{name: "invalid envelope", status: http.StatusOK, body: `{"code":200,"data":{"name":"wrong","status":{"phase":"online","todayTrafficIn":1,"todayTrafficOut":2,"curConns":3}}}`, availability: runtimestats.Unavailable},
		{name: "negative counter", status: http.StatusOK, body: `{"code":200,"data":{"name":"` + routeProxyName + `","status":{"phase":"online","todayTrafficIn":-1,"todayTrafficOut":2,"curConns":3}}}`, availability: runtimestats.Unavailable},
		{name: "unknown phase", status: http.StatusOK, body: `{"code":200,"data":{"name":"` + routeProxyName + `","status":{"phase":"private-state","todayTrafficIn":1,"todayTrafficOut":2,"curConns":3}}}`, availability: runtimestats.Unavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := runtimestats.NewClient(runtimestats.Options{Endpoint: server.URL, Username: "u", Password: "p", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			snapshot := client.Snapshot(context.Background(), routeProxyName)
			if snapshot.Availability != test.availability {
				t.Fatalf("availability = %q, want %q", snapshot.Availability, test.availability)
			}
			if snapshot.CurrentConnections != nil || snapshot.UploadBytesToday != nil || snapshot.DownloadBytesToday != nil || snapshot.ProxyState != nil {
				t.Fatalf("unavailable snapshot invented data: %#v", snapshot)
			}
		})
	}
}

func TestSnapshotNeverFollowsRedirectOrAcceptsArbitraryProxyPaths(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, target.URL, http.StatusFound)
	}))
	defer server.Close()
	client, err := runtimestats.NewClient(runtimestats.Options{Endpoint: server.URL, Username: "u", Password: "p", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if got := client.Snapshot(context.Background(), routeProxyName); got.Availability != runtimestats.Unavailable {
		t.Fatalf("redirect returned availability %q", got.Availability)
	}
	if redirected.Load() != 0 {
		t.Fatal("native API credentials followed a redirect")
	}
	for _, invalid := range []string{"", "anon_example", "rte_short", "rte_abcdefghijklmnopqrstuvwxyz/traffic", "rte_00000000000000000000000000", "rte_ABCDEFGHIJKLMNOPQRSTUVWXYZ"} {
		if got := client.Snapshot(context.Background(), invalid); got.Availability != runtimestats.Unavailable {
			t.Fatalf("invalid proxy name %q returned %q", invalid, got.Availability)
		}
	}
}

func TestNewClientRejectsUnsafeOrAmbiguousEndpoints(t *testing.T) {
	tests := []runtimestats.Options{
		{},
		{Endpoint: "http://example.test", Username: "u", Password: "p"},
		{Endpoint: "http://localhost:7500", Username: "u", Password: "p"},
		{Endpoint: "https://user:secret@example.test", Username: "u", Password: "p"},
		{Endpoint: "https://example.test/private", Username: "u", Password: "p"},
		{Endpoint: "https://example.test?path=/api/v2/proxies", Username: "u", Password: "p"},
		{Endpoint: "https://example.test#fragment", Username: "u", Password: "p"},
		{Endpoint: "https://example.test:https", Username: "u", Password: "p"},
		{Endpoint: "https://example.test:0", Username: "u", Password: "p"},
		{Endpoint: "https://example.test:65536", Username: "u", Password: "p"},
		{Endpoint: "https://example.test", Username: "", Password: "p"},
		{Endpoint: "https://example.test", Username: "u:admin", Password: "p"},
		{Endpoint: "https://example.test", Username: "u", Password: ""},
	}
	for _, options := range tests {
		if _, err := runtimestats.NewClient(options); err == nil {
			t.Fatalf("unsafe options accepted: %#v", options)
		}
	}
	for _, endpoint := range []string{"http://127.0.0.1:7500", "http://[::1]:7500", "https://frps.internal.example:7500", "https://frps.internal.example:7500/"} {
		if _, err := runtimestats.NewClient(runtimestats.Options{Endpoint: endpoint, Username: "u", Password: "p"}); err != nil {
			t.Fatalf("safe fixed endpoint %q rejected: %v", endpoint, err)
		}
	}
}

func TestSnapshotBoundsResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat(" ", 70<<10)))
	}))
	defer server.Close()
	client, err := runtimestats.NewClient(runtimestats.Options{Endpoint: server.URL, Username: "u", Password: "p", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if got := client.Snapshot(context.Background(), routeProxyName); got.Availability != runtimestats.Unavailable {
		t.Fatalf("oversized response returned availability %q", got.Availability)
	}
}
