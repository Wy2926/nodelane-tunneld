package controlserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestNativeClientObservationRequiresExactOnlineRunAndLiteralIP(t *testing.T) {
	const id = "run_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	valid := `{"code":200,"msg":"success","data":{"total":1,"page":1,"pageSize":2,"items":[{"key":"run_aaaaaaaaaaaaaaaaaaaaaaaaaa","user":"","clientID":"run_aaaaaaaaaaaaaaaaaaaaaaaaaa","runID":"run_aaaaaaaaaaaaaaaaaaaaaaaaaa","hostname":"host","firstConnectedAt":1,"lastConnectedAt":1,"online":true,"clientIP":"198.51.100.8"}]}}`
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"matching native client", valid, true},
		{"business code mismatch", strings.ReplaceAll(valid, `"code":200`, `"code":0`), false},
		{"offline", strings.ReplaceAll(valid, `"online":true`, `"online":false`), false},
		{"different run", strings.ReplaceAll(valid, `"runID":"`+id+`"`, `"runID":"run_bbbbbbbbbbbbbbbbbbbbbbbbbb"`), false},
		{"nonliteral IP", strings.ReplaceAll(valid, "198.51.100.8", "attacker.test"), false},
		{"case alias", strings.ReplaceAll(valid, "clientIP", "ClientIP"), false},
		{"duplicate identity", strings.ReplaceAll(valid, `"online":true`, `"online":true,"online":true`), false},
		{"ambiguous inventory", strings.ReplaceAll(valid, `"total":1`, `"total":2`), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				if !ok || user != "admin" || password != "private-password" || r.URL.Path != "/api/v2/clients" || r.URL.Query().Get("runID") != id || r.URL.Query().Get("clientID") != id || r.URL.Query().Get("status") != "online" || r.URL.Query().Get("pageSize") != "2" {
					t.Error("unsafe native client request")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer upstream.Close()
			observer, err := newClientObserver(upstream.URL, "admin", "private-password", nil)
			if err != nil {
				t.Fatal(err)
			}
			ip, err := observer.connectedIP(context.Background(), id)
			if test.valid && (err != nil || ip != netip.MustParseAddr("198.51.100.8")) || !test.valid && err == nil {
				t.Fatalf("native observation accepted incorrectly: %s %v", ip, err)
			}
		})
	}
}
