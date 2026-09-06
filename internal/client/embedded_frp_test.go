package client

import (
	"context"
	"io"
	"net"
	"strconv"
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

func TestEmbeddedCloseCancelsBeforeRunWithoutPanic(t *testing.T) {
	client, err := NewEmbeddedFRPClient("serverAddr = \"127.0.0.1\"\nserverPort = 1\nloginFailExit = false\n", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	client.Close()
	done := make(chan error, 1)
	go func() { done <- client.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("closed client continued retrying first login")
	}
}

func TestEmbeddedCloseCancelsAnActiveFirstLogin(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	client, err := NewEmbeddedFRPClient("serverAddr = \"127.0.0.1\"\nserverPort = "+port+"\nloginFailExit = false\ntransport.dialServerTimeout = 1\n", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			accepted <- connection
		}
	}()
	done := make(chan error, 1)
	go func() { done <- client.Run(context.Background()) }()
	select {
	case connection := <-accepted:
		defer connection.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("embedded client did not attempt first login on port " + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("embedded client ignored Close during first login")
	}
}

func TestNewEmbeddedFRPClientRejectsInvalidConfig(t *testing.T) {
	_, err := NewEmbeddedFRPClient("serverPort = \"invalid\"", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "parse embedded frp config") {
		t.Fatalf("unexpected error: %v", err)
	}
}
