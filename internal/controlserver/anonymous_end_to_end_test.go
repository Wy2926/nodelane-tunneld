package controlserver

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
	"github.com/Wy2926/nodelane-tunneld/internal/runclient"
	frpconfig "github.com/fatedier/frp/pkg/config"
	configtypes "github.com/fatedier/frp/pkg/config/types"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	frpserver "github.com/fatedier/frp/server"
	"github.com/redis/go-redis/v9"
)

func TestAnonymousPersistentControlAndStockFRPEndToEnd(t *testing.T) {
	protocol := os.Getenv("NODELANE_CONTROL_ANONYMOUS_E2E_HELPER")
	if protocol == "" {
		if os.Getenv("NODELANE_TEST_DATABASE_URL") == "" || os.Getenv("NODELANE_TEST_REDIS_URL") == "" {
			t.Skip("isolated PostgreSQL and Redis fixture URLs are required")
		}
		for _, protocol := range []string{"http", "tcp", "udp"} {
			t.Run(protocol, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAnonymousPersistentControlAndStockFRPEndToEnd$", "-test.v", "-test.timeout=30s")
				command.Env = append(os.Environ(), "NODELANE_CONTROL_ANONYMOUS_E2E_HELPER="+protocol)
				output, err := command.CombinedOutput()
				if err != nil || strings.Contains(string(output), "--- SKIP") {
					t.Fatalf("isolated anonymous %s process did not pass: %v\n%s", protocol, err, output)
				}
				t.Log(strings.TrimSpace(string(output)))
			})
		}
		return
	}
	if protocol != "http" && protocol != "tcp" && protocol != "udp" {
		t.Fatal("invalid isolated anonymous protocol")
	}

	f := isolatedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	caPath, certificatePath, keyPath := endToEndCertificates(t)
	target, hits := anonymousEchoTarget(t, protocol)
	pluginListener := endToEndListener(t)
	tcpReservation := endToEndListener(t)
	udpReservation, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udpReservation.Close()
	tcpPort, udpPort := tcpReservation.Addr().(*net.TCPAddr).Port, udpReservation.LocalAddr().(*net.UDPAddr).Port
	frpPort, httpPort, adminPort := endToEndPorts(t)
	f.cfg.FRPServerAddr, f.cfg.FRPServerPort, f.cfg.FRPSBindPort = "127.0.0.1", frpPort, frpPort
	f.cfg.FRPTLSServerName, f.cfg.FRPTrustedCAFile = "frps.e2e.test", caPath
	f.cfg.PluginListenAddr = pluginListener.Addr().String()
	f.cfg.FRPSAdminURL = fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	f.cfg.TCPPortStart, f.cfg.TCPPortEnd = tcpPort, tcpPort
	f.cfg.UDPPortStart, f.cfg.UDPPortEnd = udpPort, udpPort
	stock := v1.ServerConfig{
		BindAddr: "127.0.0.1", BindPort: frpPort, ProxyBindAddr: "127.0.0.1", VhostHTTPPort: httpPort, SubDomainHost: f.cfg.PublicDomain,
		AllowPorts:  []configtypes.PortsRange{{Single: tcpPort}, {Single: udpPort}},
		Auth:        v1.AuthServerConfig{Method: v1.AuthMethodToken, Token: "", AdditionalScopes: []v1.AuthScope{v1.AuthScopeHeartBeats, v1.AuthScopeNewWorkConns}},
		Transport:   v1.ServerTransportConfig{HeartbeatTimeout: 45, TLS: v1.TLSServerConfig{Force: true, TLSConfig: v1.TLSConfig{CertFile: certificatePath, KeyFile: keyPath}}},
		WebServer:   v1.WebServerConfig{Addr: "127.0.0.1", Port: adminPort, User: f.cfg.FRPSAdminUsername, Password: f.cfg.FRPSAdminPassword},
		HTTPPlugins: []v1.HTTPPluginOptions{{Name: "nodelane", Addr: "http://" + f.cfg.PluginListenAddr, Path: "/internal/frp", Ops: []string{"Login", "NewProxy", "CloseProxy", "Ping", "NewWorkConn", "NewUserConn"}}},
	}
	writeStockConfig(t, f.cfg.FRPSConfigFile, stock)
	runtime, err := Open(ctx, f.cfg)
	if err != nil {
		t.Fatalf("open isolated anonymous control plane: %v", err)
	}
	defer runtime.Close()
	assertAnonymousPostgresEmpty(t, ctx, f)
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
		t.Fatalf("load preflight-checked anonymous stock config: %v", err)
	}
	if err := tcpReservation.Close(); err != nil {
		t.Fatal(err)
	}
	if err := udpReservation.Close(); err != nil {
		t.Fatal(err)
	}
	stockServer, err := frpserver.NewService(loadedStock)
	if err != nil {
		t.Fatalf("start isolated unmodified stock frps: %v", err)
	}
	go stockServer.Run(ctx)
	// Each protocol owns a subprocess to bound stock's default mux listener,
	// whose Accept is not unblocked by Service.Close.
	defer stockServer.Close()
	api, err := runclient.New(runclient.Options{BaseURL: publicAPI.URL + "/api/v1"})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := api.Bootstrap(ctx)
	if err != nil || runclient.ValidateBootstrapConfig(bootstrap, "") != nil {
		t.Fatalf("fetch and validate anonymous bootstrap: %v", err)
	}
	if err := runclient.Preflight(ctx, target); err != nil {
		t.Fatalf("preflight anonymous local target: %v", err)
	}
	const installation = "isolated-anonymous-e2e-installation"
	if _, err := api.Allocate(ctx, installation, protocol, target.LocalHost, target.LocalPort, "anonymous-before-init"); err == nil {
		t.Fatal("fresh anonymous namespace allocated before explicit initialization")
	} else {
		var apiError *runclient.APIError
		if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("fresh initialization gate failed for an unrelated reason: %v", err)
		}
	}
	if err := InitializeAnonymousResources(ctx, f.cfg, false); !errors.Is(err, errMaintenanceConfirmation) {
		t.Fatalf("initialization accepted missing operator confirmation: %v", err)
	}
	if err := InitializeAnonymousResources(ctx, f.cfg, true); err != nil {
		t.Fatalf("initialize explicitly confirmed fresh namespace against empty stock inventory: %v", err)
	}
	t.Log("explicit fresh initialization accepted the real empty stock inventory")
	store, err := anonymous.NewStore(anonymous.Config{Client: f.redis, Prefix: f.cfg.RedisPrefix + ":anonymous", CredentialPepper: f.cfg.AnonymousPepper,
		ReplayKey: f.cfg.AnonymousReplayKey, FenceOwnerToken: f.cfg.AnonymousFenceToken, Clock: time.Now, Random: rand.Reader, PublicDomain: f.cfg.PublicDomain,
		TCPPorts: []uint16{uint16(tcpPort)}, UDPPorts: []uint16{uint16(udpPort)}})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := frpevidence.NewClient(frpevidence.Options{Endpoint: f.cfg.FRPSAdminURL, Username: f.cfg.FRPSAdminUsername, Password: f.cfg.FRPSAdminPassword})
	if err != nil {
		t.Fatal(err)
	}
	var first runclient.Run
	var firstNativeSession string
	for index, key := range []string{"anonymous-first-run", "anonymous-reallocate"} {
		run, err := api.Allocate(ctx, installation, protocol, target.LocalHost, target.LocalPort, key)
		if err != nil || run.Replayed || run.Protocol != protocol || !strings.HasPrefix(run.ID, "anr_") || run.RouteID != "" {
			t.Fatalf("anonymous allocation %d did not create an independent run: %v", index+1, err)
		}
		if protocol == "tcp" && run.PublicEndpoint != net.JoinHostPort(f.cfg.PublicDomain, strconv.Itoa(tcpPort)) ||
			protocol == "udp" && run.PublicEndpoint != net.JoinHostPort(f.cfg.PublicDomain, strconv.Itoa(udpPort)) {
			t.Fatal("anonymous allocation escaped its owned ephemeral public port")
		}
		if index == 0 {
			first = run
		} else if run.ID == first.ID || run.ProxyName == first.ProxyName || protocol != "http" && run.PublicEndpoint != first.PublicEndpoint {
			t.Fatal("reallocation did not create a new identity on the same configured public port")
		}
		running := startAnonymousRunner(t, ctx, api, bootstrap, run, target)
		defer running.cleanup(t)
		running.waitOnline(t, ctx)
		authorized, err := store.AuthorizeLogin(ctx, run.ID, run.CredentialToken)
		if err != nil || authorized.State != anonymous.StateOnline || !authorized.ProxyRegistrationGranted || !authorized.LeaseExpiresAt.After(time.Now()) || !authorized.HardExpiresAt.Equal(run.HardExpiresAt) {
			t.Fatalf("real anonymous Runner did not persist native online grant/lease: state=%s grant=%t err=%v", authorized.State, authorized.ProxyRegistrationGranted, err)
		}
		client := observer.ObserveClient(ctx, run.ID)
		if client.Availability != frpevidence.Available || !client.Online || client.ClientID != run.ID || !frpplugin.ValidSessionID(client.NativeSessionID) {
			t.Fatalf("real anonymous native client identity is unconfirmed: %+v", client)
		}
		if index == 0 {
			firstNativeSession = client.NativeSessionID
		} else if client.NativeSessionID == firstNativeSession {
			t.Fatal("new anonymous run reused the old native session")
		}
		ownership := anonymousOwnership(t, ctx, f, run.ID)
		assertAnonymousOwnership(t, ctx, f, run.ID, ownership, true)
		if index == 1 {
			if _, err := api.Stop(ctx, first.ID, first.CredentialToken); err != nil {
				t.Fatalf("late stop of released anonymous run was not idempotent: %v", err)
			}
			for _, reconcile := range runtime.reconcileRuns {
				if err := reconcile(ctx); err != nil {
					t.Fatalf("reconcile after late old stop: %v", err)
				}
			}
			assertAnonymousOwnership(t, ctx, f, run.ID, ownership, true)
			if _, err := store.AuthorizeLogin(ctx, run.ID, run.CredentialToken); err != nil {
				t.Fatalf("late old stop revoked the new anonymous run: %v", err)
			}
		}
		before := hits.Load()
		if err := probeAnonymousEcho(ctx, run, httpPort); err != nil || hits.Load() != before+1 {
			t.Fatalf("real anonymous %s forwarding %d failed: %v", protocol, index+1, err)
		}
		t.Logf("anonymous %s run %d has a real native grant and forwards through stock frps/frpc", protocol, index+1)
		if _, err := api.Stop(ctx, run.ID, run.CredentialToken); err != nil {
			t.Fatalf("stop anonymous run through public API: %v", err)
		}
		running.waitStopped(t)
		for deadline := time.Now().Add(5 * time.Second); ; {
			client = observer.ObserveClient(ctx, run.ID)
			if client.Availability == frpevidence.Available && !client.Online && client.NativeSessionID == "" && client.DisconnectedAt > 0 {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("stopped anonymous native client did not disconnect: %+v", client)
			}
			time.Sleep(20 * time.Millisecond)
		}
		for deadline := time.Now().Add(5 * time.Second); ; {
			for _, reconcile := range runtime.reconcileRuns {
				if err := reconcile(ctx); err != nil {
					t.Fatalf("reconcile anonymous native release: %v", err)
				}
			}
			state, err := f.redis.HGet(ctx, ownership["run_key"], "state").Result()
			if err != nil {
				t.Fatalf("read owned anonymous release state: %v", err)
			}
			if state == "released" {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("anonymous native entry did not release Redis resources: state=%s", state)
			}
			time.Sleep(20 * time.Millisecond)
		}
		assertAnonymousOwnership(t, ctx, f, run.ID, ownership, false)
		before = hits.Load()
		if err := probeAnonymousEcho(ctx, run, httpPort); err == nil || hits.Load() != before {
			t.Fatal("released anonymous entry still forwarded new traffic")
		}
		t.Logf("anonymous %s run %d stopped, native registration ended, and its entry/installation resources were released", protocol, index+1)
	}
	assertAnonymousPostgresEmpty(t, ctx, f)
	t.Log("same installation reallocated successfully; late old stop preserved the new run; PostgreSQL account/route/run tables stayed empty")
}

type anonymousRunnerProcess struct {
	stop     context.CancelFunc
	done     <-chan error
	statuses <-chan runclient.Status
	finished bool
}

func startAnonymousRunner(t *testing.T, ctx context.Context, api *runclient.Client, bootstrap runclient.BootstrapConfig, run runclient.Run, target runclient.Target) *anonymousRunnerProcess {
	t.Helper()
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
	runnerCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runner.Run(runnerCtx, bootstrap, run, target) }()
	return &anonymousRunnerProcess{stop: stop, done: done, statuses: statuses}
}

func (p *anonymousRunnerProcess) waitOnline(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case status := <-p.statuses:
			if status.State == runclient.StatusOnline {
				return
			}
		case err := <-p.done:
			p.finished = true
			t.Fatalf("real anonymous Runner exited before native online: %v", err)
		case <-deadline.C:
			t.Fatal("real anonymous Runner did not become online within five seconds")
		case <-ctx.Done():
			t.Fatal("anonymous scenario deadline expired before online")
		}
	}
}

func (p *anonymousRunnerProcess) waitStopped(t *testing.T) {
	t.Helper()
	select {
	case err := <-p.done:
		p.finished = true
		if !errors.Is(err, runclient.ErrRunStopped) {
			t.Fatalf("anonymous Runner did not confirm remote stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("anonymous Runner did not exit within five seconds of stop")
	}
}

func (p *anonymousRunnerProcess) cleanup(t *testing.T) {
	t.Helper()
	p.stop()
	if !p.finished {
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			t.Error("anonymous Runner cleanup exceeded bound")
		}
	}
}

func anonymousEchoTarget(t *testing.T, protocol string) (runclient.Target, *atomic.Int32) {
	t.Helper()
	hits := &atomic.Int32{}
	target := runclient.Target{Protocol: protocol, LocalHost: "127.0.0.1"}
	if protocol == "http" {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "anonymous-http-echo:"+r.URL.Path)
		}))
		t.Cleanup(server.Close)
		parsed, _ := url.Parse(server.URL)
		target.LocalPort, _ = strconv.Atoi(parsed.Port())
		return target, hits
	}
	if protocol == "tcp" {
		listener := endToEndListener(t)
		target.LocalPort = listener.Addr().(*net.TCPAddr).Port
		var handlers sync.WaitGroup
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				connection, err := listener.Accept()
				if err != nil {
					return
				}
				handlers.Add(1)
				go func() {
					defer handlers.Done()
					defer connection.Close()
					_ = connection.SetDeadline(time.Now().Add(time.Second))
					var buffer [len("anonymous-owned-loopback-echo")]byte
					length, err := io.ReadFull(connection, buffer[:])
					if err == nil {
						hits.Add(1)
						_, _ = connection.Write(buffer[:length])
					}
				}()
			}
		}()
		t.Cleanup(func() { _ = listener.Close(); <-done; handlers.Wait() })
		return target, hits
	}
	connection, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	target.LocalPort = connection.LocalAddr().(*net.UDPAddr).Port
	done := make(chan struct{})
	go func() {
		defer close(done)
		var buffer [64]byte
		for {
			length, peer, err := connection.ReadFrom(buffer[:])
			if err != nil {
				return
			}
			hits.Add(1)
			_, _ = connection.WriteTo(buffer[:length], peer)
		}
	}()
	t.Cleanup(func() { _ = connection.Close(); <-done })
	return target, hits
}

func probeAnonymousEcho(ctx context.Context, run runclient.Run, httpPort int) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if run.Protocol == "http" {
		client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: time.Second}
		defer client.CloseIdleConnections()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/anonymous-e2e", httpPort), nil)
		if err != nil {
			return err
		}
		request.Host = run.PublicEndpoint
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
		if err != nil || response.StatusCode != http.StatusOK || string(body) != "anonymous-http-echo:/anonymous-e2e" {
			return errors.New("anonymous HTTP echo mismatch")
		}
		return nil
	}
	_, port, err := net.SplitHostPort(run.PublicEndpoint)
	if err != nil {
		return err
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, run.Protocol, net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	const payload = "anonymous-owned-loopback-echo"
	if _, err := io.WriteString(connection, payload); err != nil {
		return err
	}
	var reply [len(payload)]byte
	if _, err := io.ReadFull(connection, reply[:]); err != nil {
		return err
	}
	if string(reply[:]) != payload {
		return errors.New("anonymous port echo mismatch")
	}
	return nil
}

func anonymousOwnership(t *testing.T, ctx context.Context, f serviceFixture, runID string) map[string]string {
	t.Helper()
	prefix := f.cfg.RedisPrefix + ":anonymous:"
	iterator := f.redis.Scan(ctx, 0, prefix+"run:*", 100).Iterator()
	for iterator.Next(ctx) {
		key := iterator.Val()
		if !strings.HasPrefix(key, prefix+"run:") {
			t.Fatal("anonymous fixture scan returned unowned key")
		}
		fields, err := f.redis.HGetAll(ctx, key).Result()
		if err != nil {
			t.Fatalf("read owned anonymous run: %v", err)
		}
		if fields["run_id"] != runID {
			continue
		}
		for _, field := range []string{"resource_key", "proxy_key", "installation_active_key", "network_active_key", "replay_key"} {
			if !strings.HasPrefix(fields[field], prefix) {
				t.Fatal("anonymous run references an unowned fixture key")
			}
		}
		fields["run_key"] = key
		return fields
	}
	if err := iterator.Err(); err != nil {
		t.Fatalf("scan owned anonymous runs: %v", err)
	}
	t.Fatal("allocated anonymous run is missing from its isolated Redis namespace")
	return nil
}

func assertAnonymousOwnership(t *testing.T, ctx context.Context, f serviceFixture, runID string, fields map[string]string, held bool) {
	t.Helper()
	for _, field := range []string{"resource_key", "proxy_key"} {
		owner, err := f.redis.Get(ctx, fields[field]).Result()
		if held && (err != nil || owner != runID) || !held && !errors.Is(err, redis.Nil) {
			t.Fatalf("anonymous %s ownership held=%t owner=%q err=%v", field, held, owner, err)
		}
	}
	for _, field := range []string{"installation_active_key", "network_active_key"} {
		_, err := f.redis.ZScore(ctx, fields[field], runID).Result()
		if held && err != nil || !held && !errors.Is(err, redis.Nil) {
			t.Fatalf("anonymous active allocation held=%t: %v", held, err)
		}
	}
	if !held {
		_, err := f.redis.ZScore(ctx, f.cfg.RedisPrefix+":anonymous:verification", fields["run_key"]).Result()
		if !errors.Is(err, redis.Nil) {
			t.Fatalf("released anonymous run remained pending verification: %v", err)
		}
	}
}

func assertAnonymousPostgresEmpty(t *testing.T, ctx context.Context, f serviceFixture) {
	t.Helper()
	var accounts, routes, runs, launchCodes, credentials, replays int64
	err := f.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM tunnel_accounts),(SELECT count(*) FROM tunnel_routes),
 (SELECT count(*) FROM tunnel_runs),(SELECT count(*) FROM route_launch_codes),(SELECT count(*) FROM run_credentials),(SELECT count(*) FROM operation_replays)`).Scan(&accounts, &routes, &runs, &launchCodes, &credentials, &replays)
	if err != nil || accounts != 0 || routes != 0 || runs != 0 || launchCodes != 0 || credentials != 0 || replays != 0 {
		t.Fatalf("anonymous lifecycle mutated PostgreSQL account/route/run state: counts=%d/%d/%d/%d/%d/%d err=%v", accounts, routes, runs, launchCodes, credentials, replays, err)
	}
}
