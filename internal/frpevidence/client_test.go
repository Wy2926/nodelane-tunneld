package frpevidence_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
	frphttp "github.com/fatedier/frp/pkg/util/http"
	frpmodel "github.com/fatedier/frp/server/http/model"
)

const (
	registeredProxy = "rte_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	registeredRun   = "run_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	anonymousProxyA = "anon_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	anonymousProxyM = "anon_mmmmmmmmmmmmmmmmmmmmmmmmmm"
	anonymousProxyZ = "anon_zzzzzzzzzzzzzzzzzzzzzzzzzz"
	anonymousRunA   = "anr_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	anonymousRunM   = "anr_mmmmmmmmmmmmmmmmmmmmmmmmmm"
	anonymousRunZ   = "anr_zzzzzzzzzzzzzzzzzzzzzzzzzz"
)

func TestObserveMapsOnlyExactNativeEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertNativeRequest(t, r, "/api/v2/proxies/"+registeredProxy, "")
		writeJSON(t, w, http.StatusOK, map[string]any{
			"code": 200,
			"msg":  "success",
			"data": map[string]any{
				"name":     registeredProxy,
				"user":     "must-not-leak",
				"clientID": registeredRun,
				"spec": map[string]any{
					"type": "http",
					"http": map[string]any{"subdomain": "must-not-leak"},
				},
				"status": map[string]any{
					"phase":           "online",
					"curConns":        7,
					"todayTrafficIn":  123,
					"todayTrafficOut": 456,
					"lastStartAt":     789,
				},
			},
		})
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got := client.Observe(context.Background(), frpevidence.Expected{
		ProxyName: registeredProxy,
		RunID:     registeredRun,
		Protocol:  "http",
	})
	want := frpevidence.Evidence{
		Availability:       frpevidence.Available,
		ProxyName:          registeredProxy,
		RunID:              registeredRun,
		Protocol:           "http",
		Phase:              "online",
		CurrentConnections: 7,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence = %#v, want %#v", got, want)
	}
	encoded := fmt.Sprintf("%#v", got)
	for _, forbidden := range []string{"must-not-leak", "todayTraffic", "lastStartAt"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("evidence leaked upstream field %q: %s", forbidden, encoded)
		}
	}
}

func TestObserveDecodesStockV070ResponseTypesAndEnvelope(t *testing.T) {
	server := httptest.NewTLSServer(frphttp.MakeHTTPHandlerFuncV2(func(ctx *frphttp.Context) (any, error) {
		assertNativeRequest(t, ctx.Req, "/api/v2/proxies/"+anonymousProxyA, "")
		return frpmodel.V2ProxyResp{
			Name: anonymousProxyA, User: "ignored", ClientID: anonymousRunA,
			Spec: frpmodel.V2ProxySpec{Type: "tcp", TCP: &frpmodel.V2TCPProxySpec{}},
			Status: frpmodel.V2ProxyStatusResp{
				State: "offline", CurConns: 2, TodayTrafficIn: 123, TodayTrafficOut: 456, LastCloseAt: 789,
			},
		}, nil
	}))
	defer server.Close()
	got := newTestClient(t, server).Observe(context.Background(), frpevidence.Expected{ProxyName: anonymousProxyA, RunID: anonymousRunA, Protocol: "tcp"})
	want := frpevidence.Evidence{Availability: frpevidence.Available, ProxyName: anonymousProxyA, RunID: anonymousRunA, Protocol: "tcp", Phase: "offline", CurrentConnections: 2}
	if got != want {
		t.Fatalf("stock v0.70.0 response = %#v, want %#v", got, want)
	}
}

func TestObserveDistinguishesNotObservedFromUnavailable(t *testing.T) {
	validBody := func(proxy, runID, protocol, phase string, conns int64) map[string]any {
		return map[string]any{
			"code": 200,
			"msg":  "success",
			"data": nativeProxy(proxy, runID, protocol, phase, conns),
		}
	}
	tests := []struct {
		name string
		code int
		body any
		want frpevidence.Availability
	}{
		{name: "not observed", code: http.StatusNotFound, body: map[string]any{"code": 404, "msg": "no proxy info found", "data": nil}, want: frpevidence.NotObserved},
		{name: "upstream failure", code: http.StatusInternalServerError, body: map[string]any{"code": 500, "msg": "private detail", "data": nil}, want: frpevidence.Unavailable},
		{name: "wrong envelope code", code: http.StatusOK, body: map[string]any{"code": 201, "data": nativeProxy(registeredProxy, registeredRun, "http", "online", 0)}, want: frpevidence.Unavailable},
		{name: "missing data", code: http.StatusOK, body: map[string]any{"code": 200, "data": nil}, want: frpevidence.Unavailable},
		{name: "wrong proxy", code: http.StatusOK, body: validBody("rte_cccccccccccccccccccccccccc", registeredRun, "http", "online", 0), want: frpevidence.Unavailable},
		{name: "wrong run", code: http.StatusOK, body: validBody(registeredProxy, "run_cccccccccccccccccccccccccc", "http", "online", 0), want: frpevidence.Unavailable},
		{name: "wrong protocol", code: http.StatusOK, body: validBody(registeredProxy, registeredRun, "tcp", "online", 0), want: frpevidence.Unavailable},
		{name: "unknown phase", code: http.StatusOK, body: validBody(registeredProxy, registeredRun, "http", "starting", 0), want: frpevidence.Unavailable},
		{name: "negative connections", code: http.StatusOK, body: validBody(registeredProxy, registeredRun, "http", "online", -1), want: frpevidence.Unavailable},
		{name: "missing connections", code: http.StatusOK, body: map[string]any{"code": 200, "data": map[string]any{"name": registeredProxy, "clientID": registeredRun, "spec": map[string]any{"type": "http"}, "status": map[string]any{"phase": "online"}}}, want: frpevidence.Unavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, test.code, test.body)
			}))
			defer server.Close()
			got := newTestClient(t, server).Observe(context.Background(), frpevidence.Expected{ProxyName: registeredProxy, RunID: registeredRun, Protocol: "http"})
			if got.Availability != test.want {
				t.Fatalf("availability = %q, want %q (%#v)", got.Availability, test.want, got)
			}
			if got.Availability != frpevidence.Available && got != (frpevidence.Evidence{Availability: test.want}) {
				t.Fatalf("failed observation exposed or invented evidence: %#v", got)
			}
		})
	}
}

func TestObserveRejectsInvalidExpectedWithoutCallingUpstream(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := newTestClient(t, server)
	tests := []frpevidence.Expected{
		{},
		{ProxyName: registeredProxy, RunID: registeredRun, Protocol: "https"},
		{ProxyName: registeredProxy, RunID: anonymousRunA, Protocol: "http"},
		{ProxyName: anonymousProxyA, RunID: registeredRun, Protocol: "http"},
		{ProxyName: "rte_short", RunID: registeredRun, Protocol: "http"},
		{ProxyName: registeredProxy + "/traffic", RunID: registeredRun, Protocol: "http"},
		{ProxyName: "rte_00000000000000000000000000", RunID: registeredRun, Protocol: "http"},
		{ProxyName: registeredProxy, RunID: "run_ABCDEFGHIJKLMNOPQRSTUVWXYZ", Protocol: "http"},
	}
	for _, expected := range tests {
		if got := client.Observe(context.Background(), expected); got != (frpevidence.Evidence{Availability: frpevidence.Unavailable}) {
			t.Fatalf("invalid expected %#v returned %#v", expected, got)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid expectations made %d upstream calls", calls.Load())
	}
}

func TestClientCopiesConfigurationDisablesCookiesAndRedirects(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Cookies()) != 0 {
			t.Fatalf("management request sent caller cookies: %#v", r.Cookies())
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(server.URL)
	jar.SetCookies(parsed, []*http.Cookie{{Name: "session", Value: "private"}})
	originalRedirect := func(*http.Request, []*http.Request) error { return nil }
	original := &http.Client{Transport: server.Client().Transport, Timeout: 10 * time.Second, Jar: jar, CheckRedirect: originalRedirect}
	client, err := frpevidence.NewClient(frpevidence.Options{Endpoint: server.URL, Username: "reader", Password: "password", HTTPClient: original})
	if err != nil {
		t.Fatal(err)
	}
	if original.Timeout != 10*time.Second || original.Jar != jar || reflect.ValueOf(original.CheckRedirect).Pointer() != reflect.ValueOf(originalRedirect).Pointer() {
		t.Fatal("NewClient mutated the caller's http.Client")
	}
	if got := client.Observe(context.Background(), frpevidence.Expected{ProxyName: registeredProxy, RunID: registeredRun, Protocol: "http"}); got.Availability != frpevidence.Unavailable {
		t.Fatalf("redirect returned %q", got.Availability)
	}
	if targetCalls.Load() != 0 {
		t.Fatal("management credentials followed a redirect")
	}
}

func TestNewClientRejectsUnsafeOrAmbiguousConfiguration(t *testing.T) {
	invalid := []frpevidence.Options{
		{},
		{Endpoint: " http://127.0.0.1:7500", Username: "u", Password: "p"},
		{Endpoint: "http://localhost:7500", Username: "u", Password: "p"},
		{Endpoint: "http://192.0.2.1:7500", Username: "u", Password: "p"},
		{Endpoint: "ftp://127.0.0.1:7500", Username: "u", Password: "p"},
		{Endpoint: "https://user:secret@frps.internal", Username: "u", Password: "p"},
		{Endpoint: "https://frps.internal/private", Username: "u", Password: "p"},
		{Endpoint: "https://frps.internal?path=/api/v2/proxies", Username: "u", Password: "p"},
		{Endpoint: "https://frps.internal#fragment", Username: "u", Password: "p"},
		{Endpoint: "https://frps.internal#", Username: "u", Password: "p"},
		{Endpoint: "https://frps.internal?", Username: "u", Password: "p"},
		{Endpoint: "https://frps.internal:", Username: "u", Password: "p"},
		{Endpoint: "https://:7500", Username: "u", Password: "p"},
		{Endpoint: "https://::1:7500", Username: "u", Password: "p"},
		{Endpoint: "https://frps.internal:0", Username: "u", Password: "p"},
		{Endpoint: "https://frps.internal:65536", Username: "u", Password: "p"},
		{Endpoint: "https://frps.internal:https", Username: "u", Password: "p"},
		{Endpoint: "https://frps.internal", Username: "", Password: "p"},
		{Endpoint: "https://frps.internal", Username: "u:admin", Password: "p"},
		{Endpoint: "https://frps.internal", Username: "u\r\nadmin", Password: "p"},
		{Endpoint: "https://frps.internal", Username: "u", Password: ""},
	}
	for _, options := range invalid {
		if _, err := frpevidence.NewClient(options); err == nil {
			t.Errorf("unsafe configuration accepted: %#v", options)
		}
	}
	for _, endpoint := range []string{"http://127.0.0.1:7500", "http://127.1.2.3:7500", "http://[::1]:7500", "https://frps.internal:7500", "https://frps.internal:7500/"} {
		if _, err := frpevidence.NewClient(frpevidence.Options{Endpoint: endpoint, Username: "u", Password: "p"}); err != nil {
			t.Fatalf("fixed endpoint %q rejected: %v", endpoint, err)
		}
	}
}

func TestObserveBoundsDetailResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat(" ", (64<<10)+1)))
	}))
	defer server.Close()
	got := newTestClient(t, server).Observe(context.Background(), frpevidence.Expected{ProxyName: registeredProxy, RunID: registeredRun, Protocol: "http"})
	if got.Availability != frpevidence.Unavailable {
		t.Fatalf("oversized detail returned %q", got.Availability)
	}
}

func TestObserveAcceptsOfflineSamplesForBothNamespacesAndAllProtocols(t *testing.T) {
	for _, protocol := range []string{"http", "tcp", "udp"} {
		for _, expected := range []frpevidence.Expected{
			{ProxyName: registeredProxy, RunID: registeredRun, Protocol: protocol},
			{ProxyName: anonymousProxyA, RunID: anonymousRunA, Protocol: protocol},
		} {
			t.Run(protocol+"/"+expected.ProxyName, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assertNativeRequest(t, r, "/api/v2/proxies/"+expected.ProxyName, "")
					writeJSON(t, w, http.StatusOK, map[string]any{"code": 200, "data": nativeProxy(expected.ProxyName, expected.RunID, protocol, "offline", 0)})
				}))
				defer server.Close()
				want := frpevidence.Evidence{Availability: frpevidence.Available, ProxyName: expected.ProxyName, RunID: expected.RunID, Protocol: protocol, Phase: "offline"}
				if got := newTestClient(t, server).Observe(context.Background(), expected); got != want {
					t.Fatalf("offline sample = %#v, want %#v", got, want)
				}
			})
		}
	}
}

func TestObserveRejectsMalformedOrAmbiguousJSON(t *testing.T) {
	valid := fmt.Sprintf(`{"code":200,"data":{"name":%q,"clientID":%q,"spec":{"type":"http"},"status":{"phase":"online","curConns":1}}}`, registeredProxy, registeredRun)
	for _, test := range []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{name: "invalid syntax", status: 200, contentType: "application/json", body: `{"code":200,`},
		{name: "trailing document", status: 200, contentType: "application/json", body: valid + `{}`},
		{name: "wrong content type", status: 200, contentType: "text/html", body: valid},
		{name: "null connections", status: 200, contentType: "application/json", body: strings.Replace(valid, `"curConns":1`, `"curConns":null`, 1)},
		{name: "fractional connections", status: 200, contentType: "application/json", body: strings.Replace(valid, `"curConns":1`, `"curConns":0.5`, 1)},
		{name: "overflow connections", status: 200, contentType: "application/json", body: strings.Replace(valid, `"curConns":1`, `"curConns":9223372036854775808`, 1)},
		{name: "deep unknown field", status: 200, contentType: "application/json", body: strings.TrimSuffix(valid, "}") + `,"deep":` + strings.Repeat("[", 33) + `null` + strings.Repeat("]", 33) + "}"},
		{name: "duplicate phase", status: 200, contentType: "application/json", body: strings.Replace(valid, `"phase":"online"`, `"phase":"online","phase":"offline"`, 1)},
		{name: "duplicate envelope code", status: 200, contentType: "application/json", body: strings.Replace(valid, `"code":200`, `"code":500,"code":200`, 1)},
		{name: "case alias", status: 200, contentType: "application/json", body: strings.Replace(valid, `"phase":"online"`, `"phase":"online","Phase":"offline"`, 1)},
		{name: "unicode phase alias", status: 200, contentType: "application/json", body: strings.Replace(valid, `"phase":"online"`, `"phase":"online","pha\u017fe":"offline"`, 1)},
		{name: "unicode connections alias", status: 200, contentType: "application/json", body: strings.Replace(valid, `"curConns":1`, `"curConns":1,"curConn\u017f":0`, 1)},
		{name: "html 404", status: 404, contentType: "text/html", body: "not found"},
		{name: "wrong 404 envelope", status: 404, contentType: "application/json", body: `{"code":200,"data":null}`},
		{name: "404 has data", status: 404, contentType: "application/json", body: `{"code":404,"data":{}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			got := newTestClient(t, server).Observe(context.Background(), frpevidence.Expected{ProxyName: registeredProxy, RunID: registeredRun, Protocol: "http"})
			if got != (frpevidence.Evidence{Availability: frpevidence.Unavailable}) {
				t.Fatalf("ambiguous response produced evidence: %#v", got)
			}
		})
	}
}

func TestListAnonymousRejectsUnicodeFieldAliases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "http" {
			writeList(t, w, 0, 1, 200, []any{})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":200,"data":{"total":1,"page":1,"pageSize":200,"items":[{"name":%q,"clientID":%q,"spec":{"type":"http"},"status":{"phase":"online","pha\u017fe":"offline","curConns":7,"curConn\u017f":0}}]}}`, anonymousProxyA, anonymousRunA)
	}))
	defer server.Close()
	got := newTestClient(t, server).ListAnonymous(context.Background())
	if got.Availability != frpevidence.Unavailable || got.Proxies != nil {
		t.Fatalf("Unicode aliases fabricated an offline inventory: %#v", got)
	}
}

func TestObserveAllowsCaseSensitiveIgnoredNativeMaps(t *testing.T) {
	server := httptest.NewServer(frphttp.MakeHTTPHandlerFuncV2(func(*frphttp.Context) (any, error) {
		return proxyWithCaseSensitiveMaps(registeredProxy, registeredRun), nil
	}))
	defer server.Close()
	got := newTestClient(t, server).Observe(context.Background(), frpevidence.Expected{ProxyName: registeredProxy, RunID: registeredRun, Protocol: "http"})
	want := frpevidence.Evidence{Availability: frpevidence.Available, ProxyName: registeredProxy, RunID: registeredRun, Protocol: "http", Phase: "online", CurrentConnections: 3}
	if got != want {
		t.Fatalf("valid native case-sensitive maps rejected: %#v", got)
	}
}

func TestListAnonymousAllowsCaseSensitiveIgnoredNativeMaps(t *testing.T) {
	server := httptest.NewServer(frphttp.MakeHTTPHandlerFuncV2(func(ctx *frphttp.Context) (any, error) {
		items := []frpmodel.V2ProxyResp{}
		if ctx.Req.URL.Query().Get("type") == "http" {
			items = []frpmodel.V2ProxyResp{
				proxyWithCaseSensitiveMaps(anonymousProxyA, anonymousRunA),
				proxyWithCaseSensitiveMaps("other-proxy", "other-client"),
			}
		}
		return frpmodel.V2PageResp[frpmodel.V2ProxyResp]{Total: len(items), Page: 1, PageSize: 200, Items: items}, nil
	}))
	defer server.Close()
	got := newTestClient(t, server).ListAnonymous(context.Background())
	want := []frpevidence.Evidence{{Availability: frpevidence.Available, ProxyName: anonymousProxyA, RunID: anonymousRunA, Protocol: "http", Phase: "online", CurrentConnections: 3}}
	if got.Availability != frpevidence.Available || !reflect.DeepEqual(got.Proxies, want) {
		t.Fatalf("valid native case-sensitive maps rejected in inventory: %#v", got)
	}
}

func proxyWithCaseSensitiveMaps(name, runID string) frpmodel.V2ProxyResp {
	return frpmodel.V2ProxyResp{
		Name: name, ClientID: runID,
		Spec: frpmodel.V2ProxySpec{Type: "http", HTTP: &frpmodel.V2HTTPProxySpec{
			V2ProxyBaseSpec: frpmodel.V2ProxyBaseSpec{
				Annotations: map[string]string{"FOO": "upper", "foo": "lower"},
				Metadatas:   map[string]string{"phase": "online", "Phase": "offline", "pha\u017fe": "custom"},
			},
		}},
		Status: frpmodel.V2ProxyStatusResp{State: "online", CurConns: 3},
	}
}

func TestClientEnforcesOperationDeadlines(t *testing.T) {
	t.Run("detail default deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer server.Close()
		started := time.Now()
		got := newTestClient(t, server).Observe(context.Background(), frpevidence.Expected{ProxyName: registeredProxy, RunID: registeredRun, Protocol: "http"})
		if got.Availability != frpevidence.Unavailable || time.Since(started) > 3500*time.Millisecond {
			t.Fatalf("detail exceeded its deadline: %#v, elapsed=%s", got, time.Since(started))
		}
	})
	t.Run("shorter caller deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		started := time.Now()
		got := newTestClient(t, server).Observe(ctx, frpevidence.Expected{ProxyName: registeredProxy, RunID: registeredRun, Protocol: "http"})
		if got.Availability != frpevidence.Unavailable || time.Since(started) > 500*time.Millisecond {
			t.Fatalf("caller deadline was not respected: %#v, elapsed=%s", got, time.Since(started))
		}
	})
	t.Run("shorter configured deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer server.Close()
		caller := server.Client()
		caller.Timeout = 25 * time.Millisecond
		client, err := frpevidence.NewClient(frpevidence.Options{Endpoint: server.URL, Username: "reader", Password: "password", HTTPClient: caller})
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		got := client.Observe(context.Background(), frpevidence.Expected{ProxyName: registeredProxy, RunID: registeredRun, Protocol: "http"})
		if got.Availability != frpevidence.Unavailable || time.Since(started) > 500*time.Millisecond {
			t.Fatalf("configured deadline was not respected: %#v, elapsed=%s", got, time.Since(started))
		}
	})
	t.Run("all inventory requests share one deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			timer := time.NewTimer(1100 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
				writeList(t, w, 0, 1, 200, []any{})
			case <-r.Context().Done():
			}
		}))
		defer server.Close()
		started := time.Now()
		got := newTestClient(t, server).ListAnonymous(context.Background())
		if got.Availability != frpevidence.Unavailable || got.Proxies != nil || time.Since(started) > 3500*time.Millisecond {
			t.Fatalf("inventory exceeded its operation deadline: %#v, elapsed=%s", got, time.Since(started))
		}
	})
}

func TestListAnonymousUsesFixedTypedPaginationAndSortsEvidence(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertBasicAuth(t, r)
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/v2/proxies" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
		calls = append(calls, r.URL.RawQuery)
		query := r.URL.Query()
		if len(query) != 3 || query.Get("pageSize") != "200" {
			t.Fatalf("unexpected query: %q", r.URL.RawQuery)
		}
		page, err := strconv.Atoi(query.Get("page"))
		if err != nil {
			t.Fatalf("bad page query: %q", r.URL.RawQuery)
		}
		switch query.Get("type") {
		case "http":
			if page == 1 {
				items := make([]any, 0, 200)
				for index := 0; index < 199; index++ {
					items = append(items, nativeProxy(fmt.Sprintf("other-%03d", index), "other-client", "http", "online", 0))
				}
				items = append(items, nativeProxy(anonymousProxyZ, anonymousRunZ, "http", "offline", 0))
				writeList(t, w, 201, 1, 200, items)
				return
			}
			if page == 2 {
				writeList(t, w, 201, 2, 200, []any{nativeProxy(anonymousProxyA, anonymousRunA, "http", "online", 3)})
				return
			}
		case "tcp":
			if page == 1 {
				writeList(t, w, 1, 1, 200, []any{nativeProxy(anonymousProxyM, anonymousRunM, "tcp", "online", 1)})
				return
			}
		case "udp":
			if page == 1 {
				writeList(t, w, 0, 1, 200, []any{})
				return
			}
		}
		t.Fatalf("unexpected pagination request: %q", r.URL.RawQuery)
	}))
	defer server.Close()

	got := newTestClient(t, server).ListAnonymous(context.Background())
	if got.Availability != frpevidence.Available {
		t.Fatalf("inventory availability = %q", got.Availability)
	}
	want := []frpevidence.Evidence{
		{Availability: frpevidence.Available, ProxyName: anonymousProxyA, RunID: anonymousRunA, Protocol: "http", Phase: "online", CurrentConnections: 3},
		{Availability: frpevidence.Available, ProxyName: anonymousProxyM, RunID: anonymousRunM, Protocol: "tcp", Phase: "online", CurrentConnections: 1},
		{Availability: frpevidence.Available, ProxyName: anonymousProxyZ, RunID: anonymousRunZ, Protocol: "http", Phase: "offline", CurrentConnections: 0},
	}
	if !reflect.DeepEqual(got.Proxies, want) {
		t.Fatalf("inventory = %#v, want %#v", got.Proxies, want)
	}
	wantCalls := []string{
		"page=1&pageSize=200&type=http",
		"page=2&pageSize=200&type=http",
		"page=1&pageSize=200&type=tcp",
		"page=1&pageSize=200&type=udp",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("requests = %#v, want %#v", calls, wantCalls)
	}
}

func TestListAnonymousRejectsRepeatedRowsThatCanHidePaginationGaps(t *testing.T) {
	server := pagedServer(t, func(proxyType string, page int) (int, []any) {
		if proxyType == "http" {
			if page == 1 {
				items := make([]any, 199)
				for index := range items {
					items[index] = nativeProxy(fmt.Sprintf("other-%03d", index), "other-client", "http", "online", 0)
				}
				items = append(items, nativeProxy(anonymousProxyA, anonymousRunA, "http", "online", 2))
				return 201, items
			}
			return 201, []any{nativeProxy(anonymousProxyA, anonymousRunA, "http", "online", 2)}
		}
		return 0, []any{}
	})
	defer server.Close()
	got := newTestClient(t, server).ListAnonymous(context.Background())
	if got.Availability != frpevidence.Unavailable || got.Proxies != nil {
		t.Fatalf("repeated row concealed a pagination gap: %#v", got)
	}
}

func TestListAnonymousRejectsRepeatedNonAnonymousRows(t *testing.T) {
	server := pagedServer(t, func(protocol string, page int) (int, []any) {
		if protocol != "http" {
			return 0, []any{}
		}
		if page == 1 {
			items := make([]any, 200)
			for index := range items {
				items[index] = nativeProxy(fmt.Sprintf("other-%03d", index), "other-client", "http", "online", 0)
			}
			return 201, items
		}
		return 201, []any{nativeProxy("other-199", "other-client", "http", "online", 0)}
	})
	defer server.Close()
	if got := newTestClient(t, server).ListAnonymous(context.Background()); got.Availability != frpevidence.Unavailable || got.Proxies != nil {
		t.Fatalf("repeated non-anonymous row concealed a pagination gap: %#v", got)
	}
}

func TestListAnonymousRejectsRepeatedNamesAcrossProtocols(t *testing.T) {
	server := pagedServer(t, func(protocol string, _ int) (int, []any) {
		if protocol == "udp" {
			return 0, []any{}
		}
		return 1, []any{nativeProxy("other", "other-client", protocol, "online", 0)}
	})
	defer server.Close()
	if got := newTestClient(t, server).ListAnonymous(context.Background()); got.Availability != frpevidence.Unavailable || got.Proxies != nil {
		t.Fatalf("repeated name across protocols was accepted: %#v", got)
	}
}

func TestListAnonymousRejectsAnomaliesWithoutPartialEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(proxyType string, page int, total *int, responsePage *int, pageSize *int, items *[]any)
	}{
		{name: "echoed page mismatch", mutate: func(proxyType string, page int, _ *int, responsePage *int, _ *int, _ *[]any) {
			if proxyType == "http" {
				*responsePage = page + 1
			}
		}},
		{name: "echoed page size mismatch", mutate: func(proxyType string, _ int, _ *int, _ *int, pageSize *int, _ *[]any) {
			if proxyType == "http" {
				*pageSize = 199
			}
		}},
		{name: "negative total", mutate: func(proxyType string, _ int, total *int, _ *int, _ *int, _ *[]any) {
			if proxyType == "http" {
				*total = -1
			}
		}},
		{name: "total exceeds bound", mutate: func(proxyType string, _ int, total *int, _ *int, _ *int, _ *[]any) {
			if proxyType == "http" {
				*total = 1 << 30
			}
		}},
		{name: "too many items", mutate: func(proxyType string, _ int, total *int, _ *int, _ *int, items *[]any) {
			if proxyType == "http" {
				*total = 201
				*items = append(*items, make([]any, 200)...)
			}
		}},
		{name: "wrong list protocol", mutate: func(proxyType string, _ int, _ *int, _ *int, _ *int, items *[]any) {
			if proxyType == "http" {
				*items = []any{nativeProxy(anonymousProxyA, anonymousRunA, "tcp", "online", 0)}
			}
		}},
		{name: "malformed anonymous name", mutate: func(proxyType string, _ int, _ *int, _ *int, _ *int, items *[]any) {
			if proxyType == "http" {
				*items = []any{nativeProxy("anon_short", anonymousRunA, "http", "online", 0)}
			}
		}},
		{name: "malformed anonymous run", mutate: func(proxyType string, _ int, _ *int, _ *int, _ *int, items *[]any) {
			if proxyType == "http" {
				*items = []any{nativeProxy(anonymousProxyA, "anr_short", "http", "online", 0)}
			}
		}},
		{name: "unknown phase", mutate: func(proxyType string, _ int, _ *int, _ *int, _ *int, items *[]any) {
			if proxyType == "http" {
				*items = []any{nativeProxy(anonymousProxyA, anonymousRunA, "http", "starting", 0)}
			}
		}},
		{name: "negative connections", mutate: func(proxyType string, _ int, _ *int, _ *int, _ *int, items *[]any) {
			if proxyType == "http" {
				*items = []any{nativeProxy(anonymousProxyA, anonymousRunA, "http", "online", -1)}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				proxyType := r.URL.Query().Get("type")
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				total, responsePage, pageSize := 1, page, 200
				items := []any{nativeProxy(anonymousProxyA, anonymousRunA, proxyType, "online", 0)}
				if proxyType != "http" {
					total, items = 0, []any{}
				}
				test.mutate(proxyType, page, &total, &responsePage, &pageSize, &items)
				writeList(t, w, total, responsePage, pageSize, items)
			}))
			defer server.Close()
			got := newTestClient(t, server).ListAnonymous(context.Background())
			if got.Availability != frpevidence.Unavailable || got.Proxies != nil {
				t.Fatalf("anomaly returned partial evidence: %#v", got)
			}
		})
	}
}

func TestListAnonymousRejectsChangedTotalsAndConflictingDuplicates(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		name := "changed total"
		if conflict {
			name = "conflicting duplicate"
		}
		t.Run(name, func(t *testing.T) {
			server := pagedServer(t, func(proxyType string, page int) (int, []any) {
				if proxyType != "http" {
					return 0, []any{}
				}
				if page == 1 {
					items := make([]any, 199)
					for index := range items {
						items[index] = nativeProxy(fmt.Sprintf("other-%03d", index), "other-client", "http", "online", 0)
					}
					items = append(items, nativeProxy(anonymousProxyA, anonymousRunA, "http", "online", 2))
					return 201, items
				}
				if conflict {
					return 201, []any{nativeProxy(anonymousProxyA, anonymousRunZ, "http", "offline", 0)}
				}
				return 202, []any{nativeProxy(anonymousProxyZ, anonymousRunZ, "http", "offline", 0), nativeProxy(anonymousProxyM, anonymousRunM, "http", "offline", 0)}
			})
			defer server.Close()
			got := newTestClient(t, server).ListAnonymous(context.Background())
			if got.Availability != frpevidence.Unavailable || got.Proxies != nil {
				t.Fatalf("inconsistent pagination returned evidence: %#v", got)
			}
		})
	}
}

func TestListAnonymousRejectsMissingRowsAndMalformedNonAnonymousRows(t *testing.T) {
	for _, test := range []struct {
		name  string
		total int
		items []any
	}{
		{name: "missing last item", total: 2, items: []any{nativeProxy(anonymousProxyA, anonymousRunA, "http", "online", 0)}},
		{name: "missing full page", total: 201, items: []any{nativeProxy(anonymousProxyA, anonymousRunA, "http", "online", 0)}},
		{name: "null items", total: 0, items: nil},
		{name: "null row", total: 1, items: []any{nil}},
		{name: "unrelated malformed row", total: 1, items: []any{nativeProxy("other", "other-client", "http", "starting", 0)}},
		{name: "anonymous client outside namespace", total: 1, items: []any{nativeProxy("other", anonymousRunA, "http", "online", 0)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := pagedServer(t, func(protocol string, _ int) (int, []any) {
				if protocol == "http" {
					return test.total, test.items
				}
				return 0, []any{}
			})
			defer server.Close()
			if got := newTestClient(t, server).ListAnonymous(context.Background()); got.Availability != frpevidence.Unavailable || got.Proxies != nil {
				t.Fatalf("incomplete or malformed inventory returned evidence: %#v", got)
			}
		})
	}
}

func TestListAnonymousRejectsRedirectFailureAndOversizedPage(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		var targetCalls atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
		defer server.Close()
		got := newTestClient(t, server).ListAnonymous(context.Background())
		if got.Availability != frpevidence.Unavailable || targetCalls.Load() != 0 {
			t.Fatalf("redirect was accepted or followed: %#v, calls=%d", got, targetCalls.Load())
		}
	})
	t.Run("failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "private", http.StatusInternalServerError) }))
		defer server.Close()
		if got := newTestClient(t, server).ListAnonymous(context.Background()); got.Availability != frpevidence.Unavailable || got.Proxies != nil {
			t.Fatalf("failure returned evidence: %#v", got)
		}
	})
	t.Run("oversized page", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(strings.Repeat(" ", (2<<20)+1)))
		}))
		defer server.Close()
		if got := newTestClient(t, server).ListAnonymous(context.Background()); got.Availability != frpevidence.Unavailable || got.Proxies != nil {
			t.Fatalf("oversized list returned evidence: %#v", got)
		}
	})
}

func newTestClient(t *testing.T, server *httptest.Server) *frpevidence.Client {
	t.Helper()
	client, err := frpevidence.NewClient(frpevidence.Options{
		Endpoint:   server.URL,
		Username:   "reader",
		Password:   "password",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertNativeRequest(t *testing.T, r *http.Request, path, rawQuery string) {
	t.Helper()
	assertBasicAuth(t, r)
	if r.Method != http.MethodGet || r.URL.EscapedPath() != path || r.URL.RawQuery != rawQuery {
		t.Fatalf("unexpected native request: %s %s", r.Method, r.URL.RequestURI())
	}
	if r.Header.Get("Accept") != "application/json" {
		t.Fatalf("Accept = %q", r.Header.Get("Accept"))
	}
}

func assertBasicAuth(t *testing.T, r *http.Request) {
	t.Helper()
	username, password, ok := r.BasicAuth()
	if !ok || username != "reader" || password != "password" {
		t.Fatalf("unexpected Basic Auth: %q/%q/%v", username, password, ok)
	}
}

func nativeProxy(name, runID, protocol, phase string, conns int64) map[string]any {
	return map[string]any{
		"name":     name,
		"clientID": runID,
		"spec":     map[string]any{"type": protocol},
		"status":   map[string]any{"phase": phase, "curConns": conns},
	}
}

func writeList(t *testing.T, w http.ResponseWriter, total, page, pageSize int, items []any) {
	t.Helper()
	writeJSON(t, w, http.StatusOK, map[string]any{
		"code": 200,
		"msg":  "success",
		"data": map[string]any{"total": total, "page": page, "pageSize": pageSize, "items": items},
	})
}

func writeJSON(t *testing.T, w http.ResponseWriter, code int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func pagedServer(t *testing.T, page func(proxyType string, page int) (int, []any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyType := r.URL.Query().Get("type")
		pageNumber, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil {
			t.Fatalf("invalid page: %q", r.URL.RawQuery)
		}
		total, items := page(proxyType, pageNumber)
		writeList(t, w, total, pageNumber, 200, items)
	}))
}

// Keep the test's desired inventory order explicit even if constants change.
func init() {
	values := []string{anonymousProxyA, anonymousProxyM, anonymousProxyZ}
	if !sort.StringsAreSorted(values) {
		panic("anonymous proxy test constants must be sorted")
	}
}
