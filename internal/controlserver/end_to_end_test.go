package controlserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/runclient"
	frpconfig "github.com/fatedier/frp/pkg/config"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	frpserver "github.com/fatedier/frp/server"
)

func TestPersistentControlAndStockFRPEndToEnd(t *testing.T) {
	if os.Getenv("NODELANE_CONTROL_E2E_HELPER") != "1" {
		if os.Getenv("NODELANE_TEST_DATABASE_URL") == "" || os.Getenv("NODELANE_TEST_REDIS_URL") == "" {
			t.Skip("isolated PostgreSQL and Redis fixture URLs are required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPersistentControlAndStockFRPEndToEnd$", "-test.v", "-test.timeout=80s")
		command.Env = append(os.Environ(), "NODELANE_CONTROL_E2E_HELPER=1")
		output, err := command.CombinedOutput()
		if err != nil || strings.Contains(string(output), "--- SKIP") {
			t.Fatalf("isolated end-to-end process did not pass: %v\n%s", err, output)
		}
		t.Log(strings.TrimSpace(string(output)))
		return
	}

	f := isolatedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	caPath, certificatePath, keyPath := endToEndCertificates(t)
	pluginListener := endToEndListener(t)
	forwarderListener := endToEndListener(t)
	frpPort, publicPort, adminPort := endToEndPorts(t)
	forwarderPort := forwarderListener.Addr().(*net.TCPAddr).Port
	f.cfg.FRPServerAddr, f.cfg.FRPServerPort, f.cfg.FRPSBindPort = "127.0.0.1", forwarderPort, frpPort
	f.cfg.FRPTLSServerName, f.cfg.FRPTrustedCAFile = "frps.e2e.test", caPath
	f.cfg.PluginListenAddr = pluginListener.Addr().String()
	f.cfg.FRPSAdminURL = fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	stock := v1.ServerConfig{
		BindAddr: "127.0.0.1", BindPort: frpPort, ProxyBindAddr: "127.0.0.1", VhostHTTPPort: publicPort, SubDomainHost: f.cfg.PublicDomain,
		Auth:        v1.AuthServerConfig{Method: v1.AuthMethodToken, Token: "", AdditionalScopes: []v1.AuthScope{v1.AuthScopeHeartBeats, v1.AuthScopeNewWorkConns}},
		Transport:   v1.ServerTransportConfig{HeartbeatTimeout: 15, TLS: v1.TLSServerConfig{Force: true, TLSConfig: v1.TLSConfig{CertFile: certificatePath, KeyFile: keyPath}}},
		WebServer:   v1.WebServerConfig{Addr: "127.0.0.1", Port: adminPort, User: f.cfg.FRPSAdminUsername, Password: f.cfg.FRPSAdminPassword},
		HTTPPlugins: []v1.HTTPPluginOptions{{Name: "nodelane", Addr: "http://" + f.cfg.PluginListenAddr, Path: "/internal/frp", Ops: []string{"Login", "NewProxy", "CloseProxy", "Ping", "NewWorkConn", "NewUserConn"}}},
	}
	writeStockConfig(t, f.cfg.FRPSConfigFile, stock)
	runtime, err := Open(ctx, f.cfg)
	if err != nil {
		t.Fatalf("open persistent control plane: %v", err)
	}
	defer runtime.Close()
	pluginHTTP := &http.Server{Handler: runtime.PluginHandler(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	pluginDone := make(chan error, 1)
	go func() { pluginDone <- pluginHTTP.Serve(pluginListener) }()
	defer func() {
		_ = pluginHTTP.Close()
		if err := <-pluginDone; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("private plugin listener failed: %v", err)
		}
	}()
	publicAPI := httptest.NewServer(runtime.Handler())
	defer publicAPI.Close()
	loadedStock, legacy, err := frpconfig.LoadServerConfig(f.cfg.FRPSConfigFile, true)
	if err != nil || legacy {
		t.Fatalf("load the preflight-checked stock config: %v", err)
	}
	stockServer, err := frpserver.NewService(loadedStock)
	if err != nil {
		t.Fatalf("start unmodified stock frps: %v", err)
	}
	go stockServer.Run(ctx)
	// Stock's default mux listener does not unblock Accept on Close. The
	// bounded child process owns that upstream goroutine and all its sockets.
	defer stockServer.Close()
	forwardedConnections := endToEndTCPForwarder(t, forwarderListener, fmt.Sprintf("127.0.0.1:%d", frpPort))

	account, err := runtime.postgres.ResolveAccount(ctx, f.cfg.OIDCIssuer, "end-to-end-fixture")
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.postgres.CreateRoute(ctx, domain.CreateRouteCommand{AccountID: account.ID, Protocol: "http", Subdomain: "e2e-fixture", IdempotencyKey: "e2e-create"})
	if err != nil {
		t.Fatal(err)
	}
	launch, err := runtime.postgres.IssueLaunchCode(ctx, domain.IssueLaunchCodeCommand{AccountID: account.ID, RouteID: created.Route.ID})
	if err != nil {
		t.Fatal(err)
	}
	api, err := runclient.New(runclient.Options{BaseURL: publicAPI.URL + "/api/v1"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := api.Bootstrap(ctx)
	if err != nil || runclient.ValidateBootstrapConfig(bootstrap, "") != nil {
		t.Fatalf("fetch and validate public bootstrap: %v", err)
	}
	if bootstrap.FRP.ServerPort != forwarderPort || bootstrap.FRP.ServerPort == frpPort {
		t.Fatal("public bootstrap did not advertise the TCP forwarder's external port")
	}
	run, err := api.Redeem(ctx, launch.Token, "end-to-end-redemption")
	if err != nil {
		t.Fatalf("redeem launch through the public HTTP client: %v", err)
	}
	if run.RouteID != created.Route.ID || run.Subdomain != "e2e-fixture" {
		t.Fatal("public redemption returned the wrong route")
	}
	t.Log("real public bootstrap and launch redemption passed")

	var backendHits atomic.Int32
	const marker = "nodelane-real-control-frp-end-to-end"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, marker+":"+r.URL.Path)
	}))
	defer backend.Close()
	backendURL, _ := url.Parse(backend.URL)
	backendPort, _ := strconv.Atoi(backendURL.Port())
	target := runclient.Target{Protocol: "http", LocalHost: "127.0.0.1", LocalPort: backendPort}
	if err := runclient.Preflight(ctx, target); err != nil {
		t.Fatalf("local target preflight: %v", err)
	}
	statuses := make(chan runclient.Status, 32)
	runner, err := runclient.NewRunner(runclient.RunnerOptions{Backend: api, HeartbeatInterval: 100 * time.Millisecond,
		StopTimeout: 2 * time.Second, CloseTimeout: 2 * time.Second, Jitter: func(d time.Duration) time.Duration { return d },
		OnStatus: func(status runclient.Status) {
			select {
			case statuses <- status:
			default:
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	runnerCtx, stopRunner := context.WithCancel(ctx)
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- runner.Run(runnerCtx, bootstrap, run, target) }()
	finished := false
	defer func() {
		stopRunner()
		if !finished {
			select {
			case <-runnerDone:
			case <-time.After(5 * time.Second):
				t.Error("real Runner did not stop within cleanup bound")
			}
		}
	}()
	online := false
	for !online {
		select {
		case status := <-statuses:
			online = status.State == runclient.StatusOnline
		case err := <-runnerDone:
			finished = true
			t.Fatalf("real Runner exited before online: %v", err)
		case <-ctx.Done():
			t.Fatal("real Runner did not obtain native online evidence within 30 seconds")
		}
	}
	proof := domain.RunProof{RunID: run.ID, Token: run.CredentialToken}
	authorized, err := runtime.postgres.AuthorizeRun(ctx, proof)
	if err != nil || authorized.Run.Status != domain.RunOnline || authorized.Run.ConnectedAt == nil || authorized.Run.LeaseExpiresAt == nil || !authorized.Run.LeaseExpiresAt.After(time.Now()) || authorized.Run.ConnectedIP != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("real native client/proxy evidence did not persist an online run and IP: status=%s ip=%s err=%v", authorized.Run.Status, authorized.Run.ConnectedIP, err)
	}
	t.Log("real Runner heartbeat persisted stock frps online evidence and client IP")
	probe := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: time.Second}
	defer probe.CloseIdleConnections()
	forwarded := func() bool {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/end-to-end", publicPort), nil)
		request.Host = "e2e-fixture." + f.cfg.PublicDomain
		response, err := probe.Do(request)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
		return err == nil && response.StatusCode == http.StatusOK && string(body) == marker+":/end-to-end"
	}
	if !forwarded() || backendHits.Load() != 1 || forwardedConnections.Load() == 0 {
		t.Fatal("real stock frps/frpc did not forward the local HTTP response")
	}
	t.Log("public HTTP request traversed the distinct-port raw TCP forwarder and stock frps/frpc TLS to the real local backend")
	if _, err := api.Stop(ctx, run.ID, run.CredentialToken); err != nil {
		t.Fatalf("stop real run through its public API: %v", err)
	}
	select {
	case err := <-runnerDone:
		finished = true
		if !errors.Is(err, runclient.ErrRunStopped) {
			t.Fatalf("real Runner stop was not confirmed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("real Runner did not exit within five seconds of remote stop")
	}
	if forwarded() || backendHits.Load() != 1 {
		t.Fatal("normal-client stop left public HTTP forwarding active")
	}
	view, err := runtime.postgres.GetRouteView(ctx, account.ID, created.Route.ID)
	if err != nil || view.CurrentRun == nil || view.CurrentRun.DesiredState != domain.DesiredStopped {
		t.Fatalf("stop did not persist desired state: %v", err)
	}
	t.Log("normal Runner stopped within its bound and public HTTP forwarding no longer reaches the backend")
	recovered := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		for _, reconcile := range runtime.reconcileRuns {
			if err := reconcile(ctx); err != nil {
				t.Fatalf("reconcile stopped run: %v", err)
			}
		}
		view, err = runtime.postgres.GetRouteView(ctx, account.ID, created.Route.ID)
		if err != nil {
			t.Fatal(err)
		}
		if view.CurrentRun == nil {
			recovered = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("stopped run slot was not released after native registration drain and entry removal")
	}
	restarted, err := runtime.postgres.StartAccountRun(ctx, domain.AccountStartCommand{AccountID: account.ID, RouteID: created.Route.ID, IdempotencyKey: "e2e-restart", RequestIP: netip.MustParseAddr("127.0.0.1")})
	if err != nil || restarted.Run.ID == run.ID {
		t.Fatalf("same route could not start a new run after recovery: %v", err)
	}
	t.Log("confirmed native entry release permits a new run on the same route")
}

func endToEndListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func endToEndTCPForwarder(t *testing.T, listener net.Listener, upstreamAddress string) *atomic.Int32 {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var connections atomic.Int32
	var active sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer active.Wait()
		for {
			downstream, err := listener.Accept()
			if err != nil {
				if ctx.Err() == nil {
					t.Error("raw TCP forwarder listener failed")
				}
				return
			}
			active.Add(1)
			go func() {
				defer active.Done()
				defer downstream.Close()
				dialer := net.Dialer{Timeout: 2 * time.Second}
				upstream, err := dialer.DialContext(ctx, "tcp", upstreamAddress)
				if err != nil {
					if ctx.Err() == nil {
						t.Error("raw TCP forwarder could not reach stock frps")
					}
					return
				}
				defer upstream.Close()
				connections.Add(1)
				stopClose := context.AfterFunc(ctx, func() {
					_ = downstream.Close()
					_ = upstream.Close()
				})
				defer stopClose()
				uploadDone := make(chan struct{})
				go func() {
					defer close(uploadDone)
					_, _ = io.Copy(upstream, downstream)
					_ = upstream.(*net.TCPConn).CloseWrite()
				}()
				_, _ = io.Copy(downstream, upstream)
				_ = downstream.(*net.TCPConn).CloseWrite()
				<-uploadDone
			}()
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("raw TCP forwarder did not stop within its cleanup bound")
		}
	})
	return &connections
}

func endToEndPorts(t *testing.T) (int, int, int) {
	t.Helper()
	listeners := []net.Listener{endToEndListener(t), endToEndListener(t), endToEndListener(t)}
	ports := [3]int{}
	for index, listener := range listeners {
		ports[index] = listener.Addr().(*net.TCPAddr).Port
	}
	for _, listener := range listeners {
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return ports[0], ports[1], ports[2]
}

func endToEndCertificates(t *testing.T) (string, string, string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "NodeLane isolated e2e CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "frps.e2e.test"}, DNSNames: []string{"frps.e2e.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	caPath, certPath, keyPath := filepath.Join(directory, "public-ca.pem"), filepath.Join(directory, "server-cert.pem"), filepath.Join(directory, "server-private-key.pem")
	for path, data := range map[string][]byte{caPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), certPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), keyPath: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return caPath, certPath, keyPath
}
