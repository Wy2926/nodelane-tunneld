package frptransporttest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/client"
	"github.com/Wy2926/nodelane-tunneld/internal/frpauth"
	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
	"github.com/Wy2926/nodelane-tunneld/internal/frppluginhttp"
	"github.com/Wy2926/nodelane-tunneld/internal/frpregistered"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	frpserver "github.com/fatedier/frp/server"
)

type blockedRegistration struct {
	inner   *frpregistered.Dispatcher
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (d *blockedRegistration) Dispatch(ctx context.Context, request frpplugin.Request) (frpplugin.Response, error) {
	response, err := d.inner.Dispatch(ctx, request)
	if request.Op == frpplugin.OpNewProxy && err == nil && !response.Reject {
		d.once.Do(func() {
			close(d.entered)
			select {
			case <-d.release:
			case <-ctx.Done():
			}
		})
	}
	return response, err
}

func TestStockFRPSessionReconnectWaitsForRegistrationDrain(t *testing.T) {
	protocol := os.Getenv("NODELANE_FRP_SESSION_CASE")
	if protocol == "" {
		for _, wire := range []string{"v1", "v2"} {
			t.Run(wire, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStockFRPSessionReconnectWaitsForRegistrationDrain$", "-test.v", "-test.timeout=35s")
				command.Env = append(os.Environ(), "NODELANE_FRP_SESSION_CASE="+wire)
				output, err := command.CombinedOutput()
				if err != nil || strings.Contains(string(output), "--- SKIP") {
					t.Fatalf("stock session subprocess failed: %v\n%s", err, output)
				}
				t.Log(strings.TrimSpace(string(output)))
			})
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cert, key := certificate(t, "frps.test")
	authorizer, err := frpauth.New(repository{}, "5MB")
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := frpregistered.New(authorizer)
	if err != nil {
		t.Fatal(err)
	}
	blocked := &blockedRegistration{inner: dispatcher, entered: make(chan struct{}), release: make(chan struct{})}
	plugin, err := frppluginhttp.New(frppluginhttp.Options{Dispatcher: blocked})
	if err != nil {
		t.Fatal(err)
	}
	pluginServer := httptest.NewServer(plugin)
	defer pluginServer.Close()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocked.release) }) }
	defer release()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "same-logical-route:"+r.URL.Path)
	}))
	defer backend.Close()
	backendURL, _ := url.Parse(backend.URL)
	backendPort, _ := strconv.Atoi(backendURL.Port())
	bindPort, publicPort, adminPort := availablePort(t), availablePort(t), availablePort(t)
	cfg := &v1.ServerConfig{
		BindAddr: "127.0.0.1", BindPort: bindPort, ProxyBindAddr: "127.0.0.1", VhostHTTPPort: publicPort, SubDomainHost: "tunnel.test",
		Auth:        v1.AuthServerConfig{Method: v1.AuthMethodToken, Token: "", AdditionalScopes: []v1.AuthScope{v1.AuthScopeHeartBeats, v1.AuthScopeNewWorkConns}},
		Transport:   v1.ServerTransportConfig{HeartbeatTimeout: 45, TLS: v1.TLSServerConfig{Force: true, TLSConfig: v1.TLSConfig{CertFile: cert, KeyFile: key}}},
		WebServer:   v1.WebServerConfig{Addr: "127.0.0.1", Port: adminPort, User: "session-test", Password: "synthetic-private-admin"},
		HTTPPlugins: []v1.HTTPPluginOptions{{Name: "nodelane", Addr: pluginServer.URL, Path: "/internal/frp", Ops: []string{"Login", "NewProxy", "CloseProxy", "Ping", "NewWorkConn", "NewUserConn"}}},
	}
	if err := cfg.Complete(); err != nil {
		t.Fatal(err)
	}
	server, err := frpserver.NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	go server.Run(ctx)
	defer server.Close()
	observer, err := frpevidence.NewClient(frpevidence.Options{Endpoint: fmt.Sprintf("http://127.0.0.1:%d", adminPort), Username: cfg.WebServer.User, Password: cfg.WebServer.Password})
	if err != nil {
		t.Fatal(err)
	}
	configText := fmt.Sprintf(`serverAddr = "127.0.0.1"
serverPort = %d
loginFailExit = true
auth.method = "oidc"
auth.token = ""
auth.additionalScopes = ["HeartBeats", "NewWorkConns"]
auth.oidc.tokenSource.type = "file"
auth.oidc.tokenSource.file.path = %q
transport.protocol = "tcp"
transport.wireProtocol = %q
transport.heartbeatInterval = 10
transport.heartbeatTimeout = 45
transport.tls.enable = true
transport.tls.serverName = "frps.test"
transport.tls.trustedCaFile = %q
log.level = "error"
metadatas.nodelane_run_id = %q
metadatas.nodelane_run_token = %q
[[proxies]]
name = %q
type = "http"
localIP = "127.0.0.1"
localPort = %d
subdomain = "forward"
`, bindPort, syntheticProofFile(t, runToken), protocol, cert, runID, runToken, routeID, backendPort)
	startClient := func() (context.CancelFunc, <-chan error) {
		t.Helper()
		frpc, err := client.NewEmbeddedFRPClient(configText, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		clientCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- frpc.Run(clientCtx) }()
		t.Cleanup(stop)
		return stop, done
	}
	stopA, doneA := startClient()
	select {
	case <-blocked.entered:
	case err := <-doneA:
		t.Fatalf("old client failed before NewProxy: %v", err)
	case <-ctx.Done():
		t.Fatal("old NewProxy did not enter plugin")
	}
	first := observer.ObserveClient(ctx, runID)
	if first.Availability != frpevidence.Available || !first.Online || first.ClientID != runID || !frpplugin.ValidSessionID(first.NativeSessionID) {
		t.Fatalf("blocked native client not observed online: %+v", first)
	}
	stopA()
	select {
	case <-doneA:
	case <-time.After(5 * time.Second):
		t.Fatal("old stock client did not close")
	}
	held := observer.ObserveClient(ctx, runID)
	if held.Availability != frpevidence.Available || !held.Online || held.NativeSessionID != first.NativeSessionID {
		t.Fatalf("native client marked offline before synchronous registration returned: %+v", held)
	}
	stopDuplicate, doneDuplicate := startClient()
	select {
	case err := <-doneDuplicate:
		if err == nil {
			t.Fatal("new stock control claimed logical client while old registration was pending")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("duplicate stock login was not rejected promptly")
	}
	stopDuplicate()
	held = observer.ObserveClient(ctx, runID)
	if held.Availability != frpevidence.Available || !held.Online || held.NativeSessionID != first.NativeSessionID {
		t.Fatalf("rejected duplicate altered old native registry: %+v", held)
	}
	release()
	drainDeadline := time.Now().Add(5 * time.Second)
	var offline frpevidence.ClientEvidence
	for time.Now().Before(drainDeadline) {
		offline = observer.ObserveClient(ctx, runID)
		if offline.Availability == frpevidence.Available && !offline.Online && offline.NativeSessionID == "" && offline.DisconnectedAt > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if offline.Availability != frpevidence.Available || offline.Online || offline.NativeSessionID != "" || offline.DisconnectedAt <= 0 {
		t.Fatalf("native control did not become disconnected after registration drained: %+v", offline)
	}
	proxy := observer.Observe(ctx, frpevidence.Expected{RunID: runID, ProxyName: routeID, Protocol: "http"})
	if proxy.Availability != frpevidence.Available || proxy.Phase != "offline" || proxy.CurrentConnections != 0 {
		t.Fatalf("native offline preceded removal of pending proxy: %+v", proxy)
	}
	stopB, doneB := startClient()
	defer func() {
		stopB()
		select {
		case <-doneB:
		case <-time.After(5 * time.Second):
			t.Error("reconnected client did not stop")
		}
	}()
	probe := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: time.Second}
	defer probe.CloseIdleConnections()
	reconnectDeadline := time.Now().Add(7 * time.Second)
	var reconnected frpevidence.ClientEvidence
	forwarded := false
	for time.Now().Before(reconnectDeadline) {
		reconnected = observer.ObserveClient(ctx, runID)
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/reconnected", publicPort), nil)
		request.Host = "forward.tunnel.test"
		response, err := probe.Do(request)
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			forwarded = response.StatusCode == http.StatusOK && string(body) == "same-logical-route:/reconnected"
		}
		if forwarded && reconnected.Availability == frpevidence.Available && reconnected.Online {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !forwarded || reconnected.Availability != frpevidence.Available || !reconnected.Online || reconnected.ClientID != runID || reconnected.NativeSessionID == first.NativeSessionID || !frpplugin.ValidSessionID(reconnected.NativeSessionID) {
		t.Fatalf("same logical route did not reconnect using a new native session: forwarded=%t evidence=%+v", forwarded, reconnected)
	}
	t.Log("stock " + protocol + " held logical identity until synchronous NewProxy drained; native offline and new-session reconnect verified")
}
