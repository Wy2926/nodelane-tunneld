package controlserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/fatedier/frp/pkg/config/v1"
)

func preparedConfig(t *testing.T) (Config, v1.ServerConfig) {
	t.Helper()
	values := configEnvironment()
	cfg, err := parseConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	cfg.FRPTrustedCAFile = filepath.Join(directory, "public-ca.pem")
	cfg.FRPSConfigFile = filepath.Join(directory, "frps.json")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cfg.FRPTLSServerName}, DNSNames: []string{cfg.FRPTLSServerName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, IsCA: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.FRPTrustedCAFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	stock := v1.ServerConfig{
		BindAddr: "127.0.0.1", BindPort: cfg.frpsBindPort(), VhostHTTPPort: 8080, SubDomainHost: cfg.PublicDomain,
		Auth:        v1.AuthServerConfig{Method: "token", AdditionalScopes: []v1.AuthScope{v1.AuthScopeHeartBeats, v1.AuthScopeNewWorkConns}},
		Transport:   v1.ServerTransportConfig{HeartbeatTimeout: 45, TLS: v1.TLSServerConfig{Force: true, TLSConfig: v1.TLSConfig{CertFile: cfg.FRPTrustedCAFile, KeyFile: filepath.Join(directory, "server-only-private-key.pem")}}},
		WebServer:   v1.WebServerConfig{Addr: "127.0.0.1", Port: 7500, User: cfg.FRPSAdminUsername, Password: cfg.FRPSAdminPassword},
		HTTPPlugins: []v1.HTTPPluginOptions{{Name: "nodelane", Addr: "http://" + cfg.PluginListenAddr, Path: "/internal/frp", Ops: []string{"Login", "NewProxy", "CloseProxy", "Ping", "NewWorkConn", "NewUserConn"}}},
	}
	writeStockConfig(t, cfg.FRPSConfigFile, stock)
	return cfg, stock
}

func writeStockConfig(t *testing.T, path string, cfg v1.ServerConfig) {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightRequiresMatchingStockFRPAuthorizationAndTLSConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*v1.ServerConfig)
	}{
		{"shared token", func(c *v1.ServerConfig) { c.Auth.Token = "private-bad-token" }},
		{"Prometheus enabled", func(c *v1.ServerConfig) { c.EnablePrometheus = true }},
		{"TLS optional", func(c *v1.ServerConfig) { c.Transport.TLS.Force = false }},
		{"plugins omitted", func(c *v1.ServerConfig) { c.HTTPPlugins = nil }},
		{"Ping omitted", func(c *v1.ServerConfig) {
			c.HTTPPlugins[0].Ops = []string{"Login", "NewProxy", "CloseProxy", "NewWorkConn", "NewUserConn"}
		}},
		{"public plugin", func(c *v1.ServerConfig) { c.HTTPPlugins[0].Addr = "http://192.0.2.1:9001" }},
		{"heartbeat disabled", func(c *v1.ServerConfig) { c.Transport.HeartbeatTimeout = -1 }},
		{"different port", func(c *v1.ServerConfig) { c.BindPort++ }},
		{"different domain", func(c *v1.ServerConfig) { c.SubDomainHost = "other.test" }},
		{"different admin credentials", func(c *v1.ServerConfig) { c.WebServer.Password = "private-bad-password" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, stock := preparedConfig(t)
			cfg.FRPServerPort = 7001
			test.mutate(&stock)
			writeStockConfig(t, cfg.FRPSConfigFile, stock)
			if _, err := preflight(cfg); err == nil || strings.Contains(err.Error(), "private-bad") {
				t.Fatalf("unsafe stock configuration accepted or leaked: %v", err)
			}
		})
	}
}

func TestPreflightPublishesCertificatesOnlyAndChecksSNI(t *testing.T) {
	cfg, _ := preparedConfig(t)
	public, err := preflight(cfg)
	if err != nil || !strings.HasPrefix(public, "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("valid public CA rejected: %v", err)
	}
	cfg.FRPTLSServerName = "different.test"
	if _, err := preflight(cfg); err == nil {
		t.Fatal("accepted a certificate for another SNI")
	}
	if err := os.WriteFile(cfg.FRPTrustedCAFile, []byte("-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := preflight(cfg); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("private material accepted or leaked: %v", err)
	}
}

func TestPreflightAcceptsTCPForwardedPublicPort(t *testing.T) {
	cfg, stock := preparedConfig(t)
	cfg.FRPServerPort, cfg.FRPSBindPort = 7001, 7000
	if _, err := preflight(cfg); err != nil {
		t.Fatalf("public port forwarded to the configured internal listener was rejected: %v", err)
	}
	stock.BindPort = 7001
	writeStockConfig(t, cfg.FRPSConfigFile, stock)
	if _, err := preflight(cfg); err == nil {
		t.Fatal("public port incorrectly substituted for the configured internal listener")
	}
}

func TestPreflightDefaultsOmittedProgrammaticBindPortToPublicPort(t *testing.T) {
	cfg, stock := preparedConfig(t)
	cfg.FRPServerPort, cfg.FRPSBindPort = 7001, 0
	stock.BindPort = 7001
	writeStockConfig(t, cfg.FRPSConfigFile, stock)
	if _, err := preflight(cfg); err != nil {
		t.Fatalf("omitted bind port did not match the public-port default: %v", err)
	}
}
