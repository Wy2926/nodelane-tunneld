package client

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestNewEmbeddedFRPClientAcceptsGeneratedConfig(t *testing.T) {
	config := (FRPConfig{
		ClientID: "cli_test",
		Tunnel: Tunnel{
			ID: "tun_test", Protocol: "http", ProxyName: "tun_test", Subdomain: "calm-panda-test",
			TunnelToken: "short-lived-token", BandwidthLimit: "5MB", ExpiresAt: time.Now().Add(time.Hour),
			FRP: FRPConnection{ServerAddr: "tunnel.nodelane.net", ServerPort: 7000, AuthToken: "frp-token"},
		},
		LocalPort: 3000,
	}).TOML()
	client, err := NewEmbeddedFRPClient(config, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if client == nil {
		t.Fatal("embedded frp client is nil")
	}
}

func TestNewEmbeddedFRPClientRejectsInvalidConfig(t *testing.T) {
	_, err := NewEmbeddedFRPClient("serverPort = \"invalid\"", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "parse embedded frp config") {
		t.Fatalf("unexpected error: %v", err)
	}
}
