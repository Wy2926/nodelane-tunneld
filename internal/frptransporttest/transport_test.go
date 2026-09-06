package frptransporttest

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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/client"
	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/frpauth"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
	"github.com/Wy2926/nodelane-tunneld/internal/frppluginhttp"
	"github.com/Wy2926/nodelane-tunneld/internal/frpregistered"
	frpclient "github.com/fatedier/frp/client"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/msg"
	"github.com/fatedier/frp/pkg/proto/wire"
	frputil "github.com/fatedier/frp/pkg/util/util"
	frpserver "github.com/fatedier/frp/server"
)

const (
	runID    = "run_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	routeID  = "rte_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	runToken = "nrc_cccccccccccccccccccccccccc.ZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGQ"
)

type repository struct{ unavailable bool }

func (r repository) AuthorizeRun(_ context.Context, proof domain.RunProof) (domain.RunAuthorization, error) {
	if r.unavailable {
		return domain.RunAuthorization{}, errors.New("dependency unavailable")
	}
	if proof.RunID != runID || proof.Token != runToken {
		return domain.RunAuthorization{}, domain.ErrInvalidRunProof
	}
	return domain.RunAuthorization{
		Route: domain.Route{ID: routeID, ProxyName: routeID, Protocol: "http", Subdomain: "forward"},
		Run:   domain.Run{ID: runID, RouteID: routeID}, CredentialID: "credential",
	}, nil
}

type recordingDispatcher struct {
	inner        *frpregistered.Dispatcher
	mu           sync.Mutex
	ops          map[frpplugin.Operation]int
	rejectedWork int
}

func (d *recordingDispatcher) Dispatch(ctx context.Context, request frpplugin.Request) (frpplugin.Response, error) {
	d.mu.Lock()
	d.ops[request.Op]++
	d.mu.Unlock()
	response, err := d.inner.Dispatch(ctx, request)
	if request.Op == frpplugin.OpNewWorkConn && response.Reject {
		d.mu.Lock()
		d.rejectedWork++
		d.mu.Unlock()
	}
	return response, err
}

func (d *recordingDispatcher) rejectedWorkCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rejectedWork
}

func (d *recordingDispatcher) count(op frpplugin.Operation) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ops[op]
}

func TestStockFRPEmptyTokenUsesTLSAndNodeLaneRunAuthorization(t *testing.T) {
	for _, test := range []struct {
		name                                      string
		wire                                      string
		credential                                string
		sourceProof                               string
		tls                                       bool
		wrongCA, wrongSNI, unavailable, plugin503 bool
		wantSuccess                               bool
	}{
		{name: "valid run forwards HTTP over v1", wire: "v1", credential: runToken, tls: true, wantSuccess: true},
		{name: "valid run forwards HTTP over v2", wire: "v2", credential: runToken, tls: true, wantSuccess: true},
		{name: "missing run credential", wire: "v1", tls: true},
		{name: "forged run credential", wire: "v1", credential: "forged", tls: true},
		{name: "different direct run proof", wire: "v1", credential: runToken, sourceProof: "different-synthetic-run-proof", tls: true},
		{name: "authorization dependency error", wire: "v1", credential: runToken, tls: true, unavailable: true},
		{name: "plugin HTTP 503", wire: "v1", credential: runToken, tls: true, plugin503: true},
		{name: "TLS disabled", wire: "v1", credential: runToken},
		{name: "untrusted CA", wire: "v1", credential: runToken, tls: true, wrongCA: true},
		{name: "incorrect SNI", wire: "v1", credential: runToken, tls: true, wrongSNI: true},
	} {
		if selected := os.Getenv("NODELANE_FRP_TRANSPORT_CASE"); selected != "" && selected != test.name {
			continue
		}
		t.Run(test.name, func(t *testing.T) {
			if os.Getenv("NODELANE_FRP_TRANSPORT_CASE") == "" {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStockFRPEmptyTokenUsesTLSAndNodeLaneRunAuthorization$", "-test.v", "-test.timeout=25s")
				command.Env = append(os.Environ(), "NODELANE_FRP_TRANSPORT_CASE="+test.name)
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("stock FRP child process failed: %v\n%s", err, output)
				}
				return
			}
			cert, key := certificate(t, "frps.test")
			trustedCA := cert
			if test.wrongCA {
				trustedCA, _ = certificate(t, "untrusted.test")
			}
			sni := "frps.test"
			if test.wrongSNI {
				sni = "other.test"
			}
			authorizer, err := frpauth.New(repository{unavailable: test.unavailable}, "5MB")
			if err != nil {
				t.Fatal(err)
			}
			dispatcher, err := frpregistered.New(authorizer)
			if err != nil {
				t.Fatal(err)
			}
			recorder := &recordingDispatcher{inner: dispatcher, ops: make(map[frpplugin.Operation]int)}
			plugin, err := frppluginhttp.New(frppluginhttp.Options{Dispatcher: recorder})
			if err != nil {
				t.Fatal(err)
			}
			pluginServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.plugin503 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				plugin.ServeHTTP(w, r)
			}))
			defer pluginServer.Close()
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = io.WriteString(w, "nodelane-stock-frp-forwarded:"+r.URL.Path)
			}))
			defer backend.Close()
			backendURL, _ := url.Parse(backend.URL)
			backendPort, _ := strconv.Atoi(backendURL.Port())
			bindPort, httpPort := availablePort(t), availablePort(t)
			cfg := &v1.ServerConfig{
				BindAddr: "127.0.0.1", BindPort: bindPort, ProxyBindAddr: "127.0.0.1",
				VhostHTTPPort: httpPort, SubDomainHost: "tunnel.test",
				Auth:        v1.AuthServerConfig{Method: v1.AuthMethodToken, Token: "", AdditionalScopes: []v1.AuthScope{v1.AuthScopeHeartBeats, v1.AuthScopeNewWorkConns}},
				Transport:   v1.ServerTransportConfig{HeartbeatTimeout: 5, TLS: v1.TLSServerConfig{Force: true, TLSConfig: v1.TLSConfig{CertFile: cert, KeyFile: key}}},
				HTTPPlugins: []v1.HTTPPluginOptions{{Name: "nodelane", Addr: pluginServer.URL, Path: "/internal/frp", Ops: []string{"Login", "NewProxy", "CloseProxy", "Ping", "NewWorkConn", "NewUserConn"}}},
			}
			if err := cfg.Complete(); err != nil {
				t.Fatal(err)
			}
			server, err := frpserver.NewService(cfg)
			if err != nil {
				t.Fatal(err)
			}
			serverCtx, stopServer := context.WithCancel(context.Background())
			go server.Run(serverCtx)
			defer func() {
				stopServer()
				_ = server.Close()
				// The stock mux default listener does not unblock Accept on Close.
				// Each case owns a subprocess, which bounds that upstream goroutine.
			}()
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
transport.heartbeatInterval = 1
transport.heartbeatTimeout = 4
transport.tls.enable = %t
transport.tls.serverName = %q
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
`, bindPort, syntheticProofFile(t, test.sourceProof), test.wire, test.tls, sni, trustedCA, runID, test.credential, routeID, backendPort)
			frpc, err := client.NewEmbeddedFRPClient(configText, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			clientCtx, stopClient := context.WithCancel(context.Background())
			clientDone := make(chan error, 1)
			go func() { clientDone <- frpc.Run(clientCtx) }()
			finished := false
			defer func() {
				stopClient()
				if !finished {
					select {
					case <-clientDone:
					case <-time.After(5 * time.Second):
						t.Error("stock frpc did not stop")
					}
				}
			}()
			if !test.wantSuccess {
				select {
				case err := <-clientDone:
					finished = true
					if err == nil {
						t.Fatal("unauthorized or insecure client accepted")
					}
				case <-time.After(12 * time.Second):
					t.Fatal("invalid client was not rejected")
				}
				if recorder.count(frpplugin.OpNewProxy) != 0 {
					t.Fatal("invalid client reached proxy registration")
				}
				return
			}
			probe := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
			defer probe.CloseIdleConnections()
			deadline := time.Now().Add(7 * time.Second)
			forwarded := false
			for time.Now().Before(deadline) {
				request, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/transport-proof", httpPort), nil)
				request.Host = "forward.tunnel.test"
				response, err := probe.Do(request)
				if err == nil {
					body, _ := io.ReadAll(response.Body)
					response.Body.Close()
					forwarded = response.StatusCode == http.StatusOK && string(body) == "nodelane-stock-frp-forwarded:/transport-proof"
				}
				if forwarded && recorder.count(frpplugin.OpPing) > 0 {
					break
				}
				select {
				case err := <-clientDone:
					finished = true
					t.Fatalf("valid client stopped: %v", err)
				case <-time.After(25 * time.Millisecond):
				}
			}
			if !forwarded {
				t.Fatal("stock frps/frpc did not forward authorized HTTP traffic")
			}
			for _, op := range []frpplugin.Operation{frpplugin.OpLogin, frpplugin.OpNewProxy, frpplugin.OpPing, frpplugin.OpNewWorkConn} {
				if recorder.count(op) == 0 {
					t.Errorf("stock traffic did not invoke %s authorization", op)
				}
			}
			rejectUnprovedWorkConnections(t, bindPort, trustedCA, test.wire, recorder)
		})
	}
}

func rejectUnprovedWorkConnections(t *testing.T, port int, ca, protocol string, recorder *recordingDispatcher) {
	t.Helper()
	for _, proof := range []string{"", frputil.GetAuthKey("", 0), "nrc_eeeeeeeeeeeeeeeeeeeeeeeeee.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			enabled := true
			common := &v1.ClientCommonConfig{ServerAddr: "127.0.0.1", ServerPort: port,
				Transport: v1.ClientTransportConfig{Protocol: "tcp", WireProtocol: protocol, TCPMux: &enabled, DialServerTimeout: 2,
					TLS: v1.TLSClientConfig{Enable: &enabled, TLSConfig: v1.TLSConfig{TrustedCaFile: ca, ServerName: "frps.test"}}}}
			if err := common.Complete(); err != nil {
				t.Fatal(err)
			}
			common.Transport.ProxyURL = ""
			connector := frpclient.NewConnector(ctx, common)
			defer connector.Close()
			if err := connector.Open(); err != nil {
				t.Fatalf("open isolated TLS work connector: %v", err)
			}
			connection, err := connector.Connect()
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := wire.WriteMagicIfV2(connection, protocol); err != nil {
				t.Fatal(err)
			}
			before := recorder.rejectedWorkCount()
			messages := msg.NewReadWriter(connection, protocol)
			if err := messages.WriteMsg(&msg.NewWorkConn{RunID: runID, PrivilegeKey: proof}); err != nil {
				t.Fatal(err)
			}
			var response msg.StartWorkConn
			err = messages.ReadMsgInto(&response)
			if err != nil || response.Error == "" || recorder.rejectedWorkCount() != before+1 {
				t.Fatalf("independent work connection was not rejected by its own missing/mixed proof: %v", err)
			}
		}()
	}
}

func availablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func syntheticProofFile(t *testing.T, value string) string {
	t.Helper()
	if value == "" {
		value = runToken
	}
	path := filepath.Join(t.TempDir(), "synthetic-test-run-proof.txt")
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func certificate(t *testing.T, name string) (string, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certPath, keyPath := filepath.Join(directory, "public-ca.pem"), filepath.Join(directory, "server-key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
