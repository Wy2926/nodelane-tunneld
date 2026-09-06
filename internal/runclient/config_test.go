package runclient

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	legacyclient "github.com/Wy2926/nodelane-tunneld/internal/client"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
	frpauth "github.com/fatedier/frp/pkg/auth"
	frpconfig "github.com/fatedier/frp/pkg/config"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/msg"
)

func testBootstrap(t *testing.T) BootstrapConfig {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "runclient test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return BootstrapConfig{FRP: FRPConfig{ServerAddr: "tunnel.test", ServerPort: 7000, TLSServerName: "tunnel.test", TrustedCAPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}))}, OIDC: OIDCConfig{Issuer: "https://auth.test", ClientID: "nt", Resource: "https://tunnel.test"}}
}

func TestValidateBootstrapRejectsMissingTrustInvalidSNIAndPartialOIDC(t *testing.T) {
	valid := testBootstrap(t)
	if err := ValidateBootstrapConfig(valid, ""); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*BootstrapConfig){
		func(c *BootstrapConfig) { c.FRP.TrustedCAPEM = "" },
		func(c *BootstrapConfig) { c.FRP.TrustedCAPEM = "not-a-certificate" },
		func(c *BootstrapConfig) {
			c.FRP.TrustedCAPEM += "\n-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----"
		},
		func(c *BootstrapConfig) { c.FRP.TLSServerName = "" },
		func(c *BootstrapConfig) { c.FRP.ServerAddr = "https://tunnel.test" },
		func(c *BootstrapConfig) { c.FRP.ServerPort = 65536 },
		func(c *BootstrapConfig) { c.OIDC.ClientID = "" },
		func(c *BootstrapConfig) { c.OIDC.Issuer = "http://auth.test" },
		func(c *BootstrapConfig) { c.OIDC.Issuer = "https://secret:password@auth.test" },
	} {
		candidate := valid
		change(&candidate)
		if err := ValidateBootstrapConfig(candidate, ""); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe bootstrap accepted: %v", err)
		}
	}
	valid.OIDC = OIDCConfig{}
	if err := ValidateBootstrapConfig(valid, ""); err != nil {
		t.Fatalf("anonymous-only bootstrap: %v", err)
	}
}

func TestValidateBootstrapAllowsExplicitCAFileWithoutTrustingOtherPEM(t *testing.T) {
	config := testBootstrap(t)
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, []byte(config.FRP.TrustedCAPEM), 0600); err != nil {
		t.Fatal(err)
	}
	config.FRP.TrustedCAPEM = ""
	if err := ValidateBootstrapConfig(config, path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 300<<10)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBootstrapConfig(config, path); err == nil || strings.Contains(err.Error(), path) {
		t.Fatalf("invalid override accepted: %v", err)
	}
}

func TestStructuredFRPConfigUsesOnlyRunProofAndExplicitSecureTransport(t *testing.T) {
	bootstrap := testBootstrap(t)
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, []byte(bootstrap.FRP.TrustedCAPEM), 0600); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(t.TempDir(), "synthetic-run-credential")
	if err := os.WriteFile(credentialPath, []byte("run-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, protocol := range []string{"http", "tcp", "udp"} {
		t.Run(protocol, func(t *testing.T) {
			run := Run{ID: "anr_test", ProxyName: "anp_test", CredentialToken: "run-secret", Protocol: protocol, PublicEndpoint: "tunnel.test:20001", CreatedAt: now, ConnectDeadlineAt: now.Add(2 * time.Minute), HardExpiresAt: now.Add(time.Hour)}
			if protocol == "http" {
				run.PublicEndpoint = "anon-example.tunnel.test"
			}
			configText, err := buildFRPConfig(bootstrap, run, Target{protocol, "127.0.0.1", 3000}, caPath, credentialPath, "")
			if err != nil {
				t.Fatal(err)
			}
			var parsed v1.ClientConfig
			if err := frpconfig.LoadConfigure([]byte(configText), &parsed, true, "toml"); err != nil {
				t.Fatalf("upstream parser rejected generated config: %v", err)
			}
			if err := parsed.ClientCommonConfig.Complete(); err != nil {
				t.Fatal(err)
			}
			if parsed.Auth.Token != "" || parsed.Auth.TokenSource != nil || parsed.Auth.Method != v1.AuthMethodOIDC || parsed.User != "" || parsed.ClientID != run.ID {
				t.Fatal("unexpected native/account authentication")
			}
			source := parsed.Auth.OIDC.TokenSource
			if source == nil || source.Type != "file" || source.File == nil || source.File.Path != credentialPath || source.Exec != nil {
				t.Fatal("native auth did not use the supplied in-memory file source")
			}
			if parsed.Auth.OIDC.ClientID != "" || parsed.Auth.OIDC.ClientSecret != "" || parsed.Auth.OIDC.Audience != "" || parsed.Auth.OIDC.TokenEndpointURL != "" {
				t.Fatal("native auth unexpectedly uses account OIDC")
			}
			nativeAuth, err := frpauth.BuildClientAuth(&parsed.Auth)
			if err != nil {
				t.Fatal(err)
			}
			if len(nativeAuth.EncryptionKey()) != 0 {
				t.Fatal("run credential changed native control encryption key")
			}
			login, ping, work := &msg.Login{}, &msg.Ping{}, &msg.NewWorkConn{}
			if err := nativeAuth.Setter.SetLogin(login); err != nil {
				t.Fatal(err)
			}
			if err := nativeAuth.Setter.SetPing(ping); err != nil {
				t.Fatal(err)
			}
			if err := nativeAuth.Setter.SetNewWorkConn(work); err != nil {
				t.Fatal(err)
			}
			if login.PrivilegeKey != "run-secret" || ping.PrivilegeKey != "run-secret" || work.PrivilegeKey != "run-secret" {
				t.Fatal("native messages did not carry direct per-run proof")
			}
			if len(parsed.Metadatas) != 2 || parsed.Metadatas[frpplugin.MetadataRunID] != run.ID || parsed.Metadatas[frpplugin.MetadataRunToken] != "run-secret" {
				t.Fatal("run proof metadata differs from server contract")
			}
			if parsed.Transport.TCPMux == nil || !*parsed.Transport.TCPMux || parsed.Transport.HeartbeatInterval != 5 || parsed.Transport.HeartbeatTimeout <= 5 || parsed.Transport.HeartbeatTimeout > 30 {
				t.Fatal("frpc heartbeat disabled or unbounded")
			}
			if parsed.Transport.TLS.Enable == nil || !*parsed.Transport.TLS.Enable || parsed.Transport.TLS.TrustedCaFile != caPath || parsed.Transport.TLS.ServerName != "tunnel.test" {
				t.Fatal("TLS trust was not pinned")
			}
			if len(parsed.Proxies) != 1 || parsed.Proxies[0].GetBaseConfig().LocalIP != "127.0.0.1" || parsed.Proxies[0].GetBaseConfig().LocalPort != 3000 || parsed.Proxies[0].GetBaseConfig().Name != "anp_test" || len(parsed.Proxies[0].GetBaseConfig().Metadatas) != 0 {
				t.Fatal("proxy routing or credential scope changed")
			}
			if protocol == "http" && parsed.Proxies[0].ProxyConfigurer.(*v1.HTTPProxyConfig).SubDomain != "anon-example" {
				t.Fatal("anonymous subdomain differs")
			}
			if protocol == "tcp" && parsed.Proxies[0].ProxyConfigurer.(*v1.TCPProxyConfig).RemotePort != 20001 {
				t.Fatal("wrong remote port")
			}
			if _, err := legacyclient.NewEmbeddedFRPClient(configText, io.Discard); err != nil {
				t.Fatalf("embedded engine rejected config: %v", err)
			}
		})
	}
}

func TestFRPConfigRequiresExplicitMemoryCredentialSource(t *testing.T) {
	_, err := buildFRPConfig(testBootstrap(t), initialRun(time.Now()), Target{"http", "127.0.0.1", 3000}, "ca.pem", "", "")
	if err == nil {
		t.Fatal("missing native credential source accepted")
	}
}

func TestProxyOptionAllowsOnlyDirectConnections(t *testing.T) {
	for _, value := range []string{" ", "ftp://proxy.test:1234", "http://proxy.test:8080/path", "http://proxy.test:8080?secret=x", "http://proxy.test", "http://proxy.test:99999", "http://proxy.test:80#secret", "http://127.0.0.1:8080", "https://proxy.test:8443", "socks5://user:password@proxy.test:1080"} {
		if ValidateProxyURL(value) == nil {
			t.Errorf("accepted proxy URL %s", value)
		}
	}
	if err := ValidateProxyURL(""); err != nil {
		t.Fatalf("rejected direct connection: %v", err)
	}
}
