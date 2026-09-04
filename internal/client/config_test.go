package client

import (
	"strings"
	"testing"
	"time"
)

func TestFRPConfigKeepsLongLivedClientTokenOut(t *testing.T) {
	credentials := Credentials{ClientID: "cli_test", ClientToken: "ftc.ctk_test.long-lived-secret"}
	tunnel := Tunnel{
		ID: "tun_test", Protocol: "http", ProxyName: "tun_test", Subdomain: "calm-panda-test",
		TunnelToken: "short-lived-tunnel-jwt", BandwidthLimit: "5MB", ExpiresAt: time.Now().Add(time.Hour),
		FRP: FRPConnection{ServerAddr: "tunnel.nodelane.net", ServerPort: 7000, AuthToken: "frp-protocol-token", TLSServerName: "tunnel.nodelane.net"},
	}
	config := (FRPConfig{ClientID: credentials.ClientID, Tunnel: tunnel, LocalPort: 3000, CAFile: "C:\\bundle\\ca.pem"}).TOML()
	for _, expected := range []string{
		`serverAddr = "tunnel.nodelane.net"`, `localPort = 3000`, `subdomain = "calm-panda-test"`,
		`metadatas.tunnel_token = "short-lived-tunnel-jwt"`, `transport.bandwidthLimitMode = "server"`,
		`clientID = "cli_test"`, `transport.protocol = "tcp"`, `transport.wireProtocol = "v1"`,
		`transport.tcpMux = true`, `transport.tls.disableCustomTLSFirstByte = true`,
		`auth.additionalScopes = ["HeartBeats", "NewWorkConns"]`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config missing %q:\n%s", expected, config)
		}
	}
	if strings.Contains(config, credentials.ClientToken) {
		t.Fatal("long-lived client token leaked into frp config")
	}
	if strings.Contains(config, "user =") {
		t.Fatal("user field would cause frp to prefix the reserved proxy name")
	}
}

func TestFRPConfigUsesDefaultTLSWithoutCustomCA(t *testing.T) {
	config := (FRPConfig{ClientID: "cli_test", Tunnel: Tunnel{
		Protocol: "tcp", ProxyName: "tun_test", RemotePort: 20001,
		TunnelToken: "tunnel-token",
		FRP:         FRPConnection{ServerAddr: "tunnel.nodelane.net", ServerPort: 7000, AuthToken: "frp-token", TLSServerName: "tunnel.nodelane.net"},
	}, LocalPort: 22}).TOML()
	if !strings.Contains(config, "transport.tls.enable = true") {
		t.Fatal("default TLS was not enabled")
	}
	if strings.Contains(config, "trustedCaFile") || strings.Contains(config, "serverName") {
		t.Fatalf("custom certificate verification was enabled without a CA:\n%s", config)
	}
}
