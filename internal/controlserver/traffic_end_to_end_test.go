package controlserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

	"github.com/Wy2926/nodelane-tunneld/internal/bff"
	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/runclient"
	"github.com/Wy2926/nodelane-tunneld/internal/runtimestats"
	"github.com/Wy2926/nodelane-tunneld/internal/session"
	frpconfig "github.com/fatedier/frp/pkg/config"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	frpserver "github.com/fatedier/frp/server"
	frpmodel "github.com/fatedier/frp/server/http/model"
)

func TestRegisteredRouteStatsMatchStockTrafficDirectionAndConnectionLifetime(t *testing.T) {
	if os.Getenv("NODELANE_CONTROL_TRAFFIC_E2E_HELPER") != "1" {
		if os.Getenv("NODELANE_TEST_DATABASE_URL") == "" || os.Getenv("NODELANE_TEST_REDIS_URL") == "" {
			t.Skip("isolated PostgreSQL and Redis fixture URLs are required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRegisteredRouteStatsMatchStockTrafficDirectionAndConnectionLifetime$", "-test.v", "-test.timeout=30s")
		command.Env = append(os.Environ(), "NODELANE_CONTROL_TRAFFIC_E2E_HELPER=1")
		output, err := command.CombinedOutput()
		if err != nil || strings.Contains(string(output), "--- SKIP") {
			t.Fatalf("isolated native traffic process did not pass: %v\n%s", err, output)
		}
		t.Log(strings.TrimSpace(string(output)))
		return
	}

	f := isolatedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	const requestBytes, responseBytes = 64 << 10, 512 << 10
	requestBody := bytes.Repeat([]byte{'q'}, requestBytes)
	responseBody := bytes.Repeat([]byte{'r'}, responseBytes)
	held := make(chan error, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseResponse := func() { releaseOnce.Do(func() { close(release) }) }
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, requestBytes+1))
		if err != nil || r.Method != http.MethodPost || r.URL.Path != "/native-traffic" || !bytes.Equal(body, requestBody) {
			held <- errors.New("backend did not receive the exact known request body")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(responseBytes))
		w.Header().Set("Connection", "close")
		w.Header().Set("Cache-Control", "no-store")
		if _, err := w.Write(responseBody[:32<<10]); err != nil {
			held <- err
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			held <- err
			return
		}
		held <- nil
		select {
		case <-release:
			_, _ = w.Write(responseBody[32<<10:])
		case <-r.Context().Done():
		}
	}))
	defer backend.Close()
	defer releaseResponse()
	backend.Config.SetKeepAlivesEnabled(false)
	backendURL, _ := url.Parse(backend.URL)
	backendPort, _ := strconv.Atoi(backendURL.Port())
	caPath, certificatePath, keyPath := endToEndCertificates(t)
	pluginListener := endToEndListener(t)
	frpPort, publicPort, adminPort := endToEndPorts(t)
	f.cfg.FRPServerAddr, f.cfg.FRPServerPort = "127.0.0.1", frpPort
	f.cfg.FRPTLSServerName, f.cfg.FRPTrustedCAFile = "frps.e2e.test", caPath
	f.cfg.PluginListenAddr = pluginListener.Addr().String()
	f.cfg.FRPSAdminURL = fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	stock := v1.ServerConfig{
		BindAddr: "127.0.0.1", BindPort: frpPort, ProxyBindAddr: "127.0.0.1", VhostHTTPPort: publicPort, SubDomainHost: f.cfg.PublicDomain,
		Auth:        v1.AuthServerConfig{Method: v1.AuthMethodToken, Token: "", AdditionalScopes: []v1.AuthScope{v1.AuthScopeHeartBeats, v1.AuthScopeNewWorkConns}},
		Transport:   v1.ServerTransportConfig{HeartbeatTimeout: 45, TLS: v1.TLSServerConfig{Force: true, TLSConfig: v1.TLSConfig{CertFile: certificatePath, KeyFile: keyPath}}},
		WebServer:   v1.WebServerConfig{Addr: "127.0.0.1", Port: adminPort, User: f.cfg.FRPSAdminUsername, Password: f.cfg.FRPSAdminPassword},
		HTTPPlugins: []v1.HTTPPluginOptions{{Name: "nodelane", Addr: "http://" + f.cfg.PluginListenAddr, Path: "/internal/frp", Ops: []string{"Login", "NewProxy", "CloseProxy", "Ping", "NewWorkConn", "NewUserConn"}}},
	}
	writeStockConfig(t, f.cfg.FRPSConfigFile, stock)
	runtime, err := Open(ctx, f.cfg)
	if err != nil {
		t.Fatalf("open persistent traffic control plane: %v", err)
	}
	defer runtime.Close()
	pluginHTTP := &http.Server{Handler: runtime.PluginHandler(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	pluginDone := make(chan error, 1)
	go func() { pluginDone <- pluginHTTP.Serve(pluginListener) }()
	defer func() {
		_ = pluginHTTP.Close()
		if err := <-pluginDone; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("traffic plugin listener failed: %v", err)
		}
	}()
	publicAPI := httptest.NewServer(runtime.Handler())
	defer publicAPI.Close()
	loadedStock, legacy, err := frpconfig.LoadServerConfig(f.cfg.FRPSConfigFile, true)
	if err != nil || legacy {
		t.Fatalf("load preflight-checked stock traffic config: %v", err)
	}
	stockServer, err := frpserver.NewService(loadedStock)
	if err != nil {
		t.Fatalf("start owned unmodified stock frps: %v", err)
	}
	go stockServer.Run(ctx)
	// The subprocess owns stock's default mux listener, whose Accept is not
	// unblocked by Service.Close. No upstream implementation is replaced.
	defer stockServer.Close()
	account, err := runtime.postgres.ResolveAccount(ctx, f.cfg.OIDCIssuer, "traffic-fixture-subject")
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.postgres.CreateRoute(ctx, domain.CreateRouteCommand{AccountID: account.ID, Protocol: "http", Subdomain: "traffic-fixture", IdempotencyKey: "traffic-create"})
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
		t.Fatalf("traffic bootstrap failed: %v", err)
	}
	run, err := api.Redeem(ctx, launch.Token, "traffic-redemption")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sessionID := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{37}, 32))
	if err := runtime.sessions.CreateSession(ctx, session.Record{ID: sessionID, AccountID: account.ID, CSRFToken: "traffic-fixture-csrf", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Tokens: identity.OIDCTokens{AccessToken: "synthetic-traffic-access", AccessTokenExpiresAt: now.Add(time.Hour), Identity: identity.OIDCIdentity{Issuer: f.cfg.OIDCIssuer, Subject: "traffic-fixture-subject", ClientID: f.cfg.OIDCWebClientID}}}); err != nil {
		t.Fatal(err)
	}
	statsClient := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second}
	defer statsClient.CloseIdleConnections()
	readStats := func() runtimestats.Snapshot {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, publicAPI.URL+"/api/v1/routes/"+created.Route.ID+"/stats", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(&http.Cookie{Name: bff.SessionCookieName, Value: sessionID})
		response, err := statsClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var body struct {
			runtimestats.Snapshot
			RouteID  string `json:"route_id"`
			TimeZone string `json:"time_zone"`
		}
		decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if response.StatusCode != http.StatusOK || decoder.Decode(&body) != nil || body.RouteID != created.Route.ID || body.TimeZone != "UTC" ||
			body.Availability != runtimestats.Available || body.ObservedAt.IsZero() || body.ProxyState == nil || *body.ProxyState != "online" ||
			body.CurrentConnections == nil || body.UploadBytesToday == nil || body.DownloadBytesToday == nil {
			t.Fatal("authenticated route stats API did not return a complete native online snapshot")
		}
		return body.Snapshot
	}
	statuses := make(chan runclient.Status, 32)
	runner, err := runclient.NewRunner(runclient.RunnerOptions{Backend: api, HeartbeatInterval: 100 * time.Millisecond,
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
	go func() {
		runnerDone <- runner.Run(runnerCtx, bootstrap, run, runclient.Target{Protocol: "http", LocalHost: "127.0.0.1", LocalPort: backendPort})
	}()
	runnerFinished := false
	defer func() {
		stopRunner()
		if !runnerFinished {
			select {
			case <-runnerDone:
			case <-time.After(5 * time.Second):
				t.Error("traffic Runner cleanup exceeded its bound")
			}
		}
	}()
	online := false
	for !online {
		select {
		case status := <-statuses:
			online = status.State == runclient.StatusOnline
		case err := <-runnerDone:
			runnerFinished = true
			t.Fatalf("traffic Runner exited before native online: %v", err)
		case <-ctx.Done():
			t.Fatal("traffic Runner did not become online before scenario deadline")
		}
	}
	baseline := readStats()
	if *baseline.CurrentConnections != 0 || *baseline.UploadBytesToday != 0 || *baseline.DownloadBytesToday != 0 {
		t.Fatal("fresh stock traffic fixture did not start from zero proxy counters")
	}
	visitor := &http.Client{Transport: &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true}, Timeout: 15 * time.Second}
	defer visitor.CloseIdleConnections()
	visitorCtx, stopVisitor := context.WithCancel(ctx)
	defer stopVisitor()
	transferDone := make(chan error, 1)
	partialReceived := make(chan struct{})
	go func() {
		request, err := http.NewRequestWithContext(visitorCtx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/native-traffic", publicPort), bytes.NewReader(requestBody))
		if err != nil {
			transferDone <- err
			return
		}
		request.Host = "traffic-fixture." + f.cfg.PublicDomain
		request.Header.Set("Content-Type", "application/octet-stream")
		response, err := visitor.Do(request)
		if err != nil {
			transferDone <- err
			return
		}
		defer response.Body.Close()
		var prefix [32 << 10]byte
		if _, err := io.ReadFull(response.Body, prefix[:]); err != nil {
			transferDone <- err
			return
		}
		if response.StatusCode != http.StatusOK || !bytes.Equal(prefix[:], responseBody[:len(prefix)]) {
			transferDone <- errors.New("visitor did not receive the exact held response prefix")
			return
		}
		close(partialReceived)
		body, err := io.ReadAll(io.LimitReader(response.Body, responseBytes-int64(len(prefix))+1))
		if err == nil && !bytes.Equal(body, responseBody[len(prefix):]) {
			err = errors.New("visitor did not receive the exact known response body")
		}
		transferDone <- err
	}()
	transferFinished := false
	defer func() {
		stopVisitor()
		releaseResponse()
		if !transferFinished {
			select {
			case <-transferDone:
			case <-time.After(3 * time.Second):
				t.Error("traffic visitor did not stop")
			}
		}
	}()
	select {
	case err := <-held:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-transferDone:
		transferFinished = true
		t.Fatalf("traffic transfer finished before held sample: %v", err)
	case <-ctx.Done():
		t.Fatal("backend did not reach held-response boundary")
	}
	select {
	case <-partialReceived:
	case err := <-transferDone:
		transferFinished = true
		t.Fatalf("visitor finished before receiving held response prefix: %v", err)
	case <-ctx.Done():
		t.Fatal("visitor did not receive partial response before held snapshot")
	}
	heldStats := readStats()
	if *heldStats.CurrentConnections != 1 || *heldStats.UploadBytesToday != *baseline.UploadBytesToday || *heldStats.DownloadBytesToday != *baseline.DownloadBytesToday {
		t.Fatalf("held HTTP work connection snapshot=%d/%d/%d, want connections=1 and unflushed byte counters=0/0", *heldStats.CurrentConnections, *heldStats.UploadBytesToday, *heldStats.DownloadBytesToday)
	}
	t.Log("held after 65536 request bytes and a partial response: current_connections=1, upload=0, download=0")
	releaseResponse()
	select {
	case err := <-transferDone:
		transferFinished = true
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("known asymmetric HTTP transfer did not complete")
	}
	var closed runtimestats.Snapshot
	for deadline := time.Now().Add(3 * time.Second); ; {
		closed = readStats()
		if *closed.CurrentConnections == 0 && *closed.UploadBytesToday > 0 && *closed.DownloadBytesToday > 0 {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("native work connection close did not publish its traffic counters")
		}
		time.Sleep(10 * time.Millisecond)
	}
	upload := *closed.UploadBytesToday - *baseline.UploadBytesToday
	download := *closed.DownloadBytesToday - *baseline.DownloadBytesToday
	// AC-STAT-01 uses the local service's viewpoint. Native counts include HTTP
	// headers, so bound their small overhead without pretending to body-only totals.
	if download < requestBytes || download > requestBytes+8192 || upload < responseBytes || upload > responseBytes+8192 {
		t.Fatalf("local-service traffic directions are swapped or invalid: request_body=%d response_body=%d upload=%d download=%d", requestBytes, responseBytes, upload, download)
	}
	native := readStockTrafficSnapshot(t, ctx, statsClient, f.cfg, run)
	if native.CurConns != 0 || native.TodayTrafficIn != download || native.TodayTrafficOut != upload {
		t.Fatalf("public traffic snapshot does not map native In to download and Out to upload: native=%+v public_upload=%d public_download=%d", native, upload, download)
	}
	t.Logf("closed: current_connections=0, download=%d (native todayTrafficIn; request body=%d), upload=%d (native todayTrafficOut; response body=%d)", download, requestBytes, upload, responseBytes)
	if _, err := api.Stop(ctx, run.ID, run.CredentialToken); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-runnerDone:
		runnerFinished = true
		if !errors.Is(err, runclient.ErrRunStopped) {
			t.Fatalf("traffic Runner did not confirm stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("traffic Runner did not stop within five seconds")
	}
}

func readStockTrafficSnapshot(t *testing.T, ctx context.Context, client *http.Client, cfg Config, run runclient.Run) frpmodel.V2ProxyStatusResp {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.FRPSAdminURL+"/api/v2/proxies/"+run.ProxyName, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth(cfg.FRPSAdminUsername, cfg.FRPSAdminPassword)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var envelope struct {
		Code int                  `json:"code"`
		Data frpmodel.V2ProxyResp `json:"data"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&envelope) != nil ||
		envelope.Code != http.StatusOK || envelope.Data.Name != run.ProxyName || envelope.Data.ClientID != run.ID || envelope.Data.Spec.Type != "http" {
		t.Fatal("owned stock proxy traffic response has invalid identity")
	}
	return envelope.Data.Status
}
