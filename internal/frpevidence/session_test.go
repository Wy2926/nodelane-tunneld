package frpevidence_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
	frpmodel "github.com/fatedier/frp/server/http/model"
)

const nativeSession = "fcs_cccccccccccccccccccccccccc"

func clientPage(t *testing.T, logicalID string, online bool) string {
	t.Helper()
	item := frpmodel.ClientInfoResp{Key: logicalID, User: "", ClientID: logicalID, RunID: nativeSession,
		ClientIP: "198.51.100.8", Online: online, FirstConnectedAt: 1, LastConnectedAt: 2}
	if !online {
		item.RunID = ""
		item.DisconnectedAt = 3
	}
	body, err := json.Marshal(map[string]any{"code": 200, "msg": "success", "data": map[string]any{
		"total": 1, "page": 1, "pageSize": 2, "items": []frpmodel.ClientInfoResp{item},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestObserveClientSeparatesLogicalIdentityAndNativeSession(t *testing.T) {
	for _, logicalID := range []string{registeredRun, anonymousRunA} {
		for _, online := range []bool{true, false} {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertBasicAuth(t, r)
				query := r.URL.Query()
				if r.Method != http.MethodGet || r.URL.Path != "/api/v2/clients" || len(query) != 4 || !query.Has("user") || query.Get("user") != "" || query.Get("clientID") != logicalID || query.Get("page") != "1" || query.Get("pageSize") != "2" {
					t.Fatalf("client lookup must include offline rows by exact logical identity: %s", r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(clientPage(t, logicalID, online)))
			}))
			got := newTestClient(t, server).ObserveClient(context.Background(), logicalID)
			server.Close()
			if got.Availability != frpevidence.Available || got.ClientID != logicalID || got.Online != online || got.ClientIP != netip.MustParseAddr("198.51.100.8") {
				t.Fatalf("client evidence mismatch: %+v", got)
			}
			if online && (got.NativeSessionID != nativeSession || got.DisconnectedAt != 0) || !online && (got.NativeSessionID != "" || got.DisconnectedAt != 3) {
				t.Fatalf("online/offline session evidence mismatch: %+v", got)
			}
		}
	}
}

func TestObserveClientKeepsMissingDistinctFromUntrustworthyEvidence(t *testing.T) {
	valid := clientPage(t, registeredRun, true)
	offline := clientPage(t, registeredRun, false)
	for _, test := range []struct {
		name, body string
		status     int
		want       frpevidence.Availability
	}{
		{"empty exact inventory", `{"code":200,"data":{"total":0,"page":1,"pageSize":2,"items":[]}}`, 200, frpevidence.NotObserved},
		{"absent resource", `{"code":404,"data":null}`, 404, frpevidence.NotObserved},
		{"missing online", strings.ReplaceAll(valid, `,"online":true`, ``), 200, frpevidence.Unavailable},
		{"null online", strings.ReplaceAll(valid, `"online":true`, `"online":null`), 200, frpevidence.Unavailable},
		{"mistyped online", strings.ReplaceAll(valid, `"online":true`, `"online":"true"`), 200, frpevidence.Unavailable},
		{"missing user", strings.ReplaceAll(valid, `"user":"",`, ``), 200, frpevidence.Unavailable},
		{"different user", strings.ReplaceAll(valid, `"user":""`, `"user":"account"`), 200, frpevidence.Unavailable},
		{"missing client id", strings.ReplaceAll(valid, `"clientID":"`+registeredRun+`",`, ``), 200, frpevidence.Unavailable},
		{"different client id", strings.ReplaceAll(valid, `"clientID":"`+registeredRun+`"`, `"clientID":"`+anonymousRunA+`"`), 200, frpevidence.Unavailable},
		{"missing native id", strings.ReplaceAll(valid, `"runID":"`+nativeSession+`",`, ``), 200, frpevidence.Unavailable},
		{"legacy native id", strings.ReplaceAll(valid, nativeSession, registeredRun), 200, frpevidence.Unavailable},
		{"malformed native id", strings.ReplaceAll(valid, nativeSession, "fcs_invalid"), 200, frpevidence.Unavailable},
		{"blank native id online", strings.ReplaceAll(valid, nativeSession, ""), 200, frpevidence.Unavailable},
		{"negative connected timestamp", strings.ReplaceAll(valid, `"lastConnectedAt":2`, `"lastConnectedAt":-2`), 200, frpevidence.Unavailable},
		{"negative disconnected timestamp", strings.ReplaceAll(offline, `"disconnectedAt":3`, `"disconnectedAt":-3`), 200, frpevidence.Unavailable},
		{"zero disconnected timestamp", strings.ReplaceAll(offline, `"disconnectedAt":3`, `"disconnectedAt":0`), 200, frpevidence.Unavailable},
		{"missing disconnected timestamp", strings.ReplaceAll(offline, `"disconnectedAt":3,`, ``), 200, frpevidence.Unavailable},
		{"offline retains native id", strings.ReplaceAll(offline, `"runID":""`, `"runID":"`+nativeSession+`"`), 200, frpevidence.Unavailable},
		{"invalid IP", strings.ReplaceAll(valid, "198.51.100.8", "host.test"), 200, frpevidence.Unavailable},
		{"unspecified IP", strings.ReplaceAll(valid, "198.51.100.8", "0.0.0.0"), 200, frpevidence.Unavailable},
		{"mapped unspecified IP", strings.ReplaceAll(valid, "198.51.100.8", "::ffff:0.0.0.0"), 200, frpevidence.Unavailable},
		{"mapped multicast IP", strings.ReplaceAll(valid, "198.51.100.8", "::ffff:224.0.0.1"), 200, frpevidence.Unavailable},
		{"scoped IP", strings.ReplaceAll(valid, "198.51.100.8", "fe80::1%eth0"), 200, frpevidence.Unavailable},
		{"duplicate field", strings.ReplaceAll(valid, `"online":true`, `"online":true,"online":false`), 200, frpevidence.Unavailable},
		{"case alias", strings.ReplaceAll(valid, `"online":true`, `"Online":true`), 200, frpevidence.Unavailable},
		{"ambiguous count", strings.ReplaceAll(valid, `"total":1`, `"total":2`), 200, frpevidence.Unavailable},
		{"wrong page", strings.ReplaceAll(valid, `"page":1`, `"page":2`), 200, frpevidence.Unavailable},
		{"wrong page size", strings.ReplaceAll(valid, `"pageSize":2`, `"pageSize":200`), 200, frpevidence.Unavailable},
		{"null items", `{"code":200,"data":{"total":0,"page":1,"pageSize":2,"items":null}}`, 200, frpevidence.Unavailable},
		{"null data", `{"code":200,"data":null}`, 200, frpevidence.Unavailable},
		{"business code mismatch", strings.ReplaceAll(valid, `"code":200`, `"code":0`), 200, frpevidence.Unavailable},
		{"trailing JSON", valid + `{}`, 200, frpevidence.Unavailable},
		{"oversize", valid + strings.Repeat(" ", 64<<10), 200, frpevidence.Unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			got := newTestClient(t, server).ObserveClient(context.Background(), registeredRun)
			if got.Availability != test.want || got.ClientID != "" || got.Online || got.NativeSessionID != "" || got.DisconnectedAt != 0 || got.ClientIP.IsValid() {
				t.Fatalf("unconfirmed client exposed evidence: %+v want %s", got, test.want)
			}
		})
	}
}

func TestObserveClientRejectsInvalidLogicalIdentityWithoutUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid logical client ID reached native API") }))
	defer server.Close()
	client := newTestClient(t, server)
	for _, id := range []string{"", nativeSession, "run_invalid", registeredRun + "?status=online", " " + registeredRun} {
		if got := client.ObserveClient(context.Background(), id); got.Availability != frpevidence.Unavailable {
			t.Fatalf("invalid logical identity accepted: %+v", got)
		}
	}
}
