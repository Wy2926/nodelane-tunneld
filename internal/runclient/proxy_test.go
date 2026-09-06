package runclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	frpclient "github.com/fatedier/frp/client"
	v1 "github.com/fatedier/frp/pkg/config/v1"
)

func TestStockProxyNegotiationDoesNotHonorCancellationBeforeOpenReturns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStockProxyNegotiationProcessHelper$")
	command.Env = append(os.Environ(), "NT_RUNCLIENT_PROXY_CHARACTERIZATION=1")
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "proxy-open-remains-blocked") {
		t.Fatalf("upstream cancellation characterization: %v %s", err, output)
	}
}

func TestStockProxyNegotiationProcessHelper(t *testing.T) {
	if os.Getenv("NT_RUNCLIENT_PROXY_CHARACTERIZATION") != "1" {
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	enabled := true
	config := &v1.ClientCommonConfig{ServerAddr: "tunnel.invalid", ServerPort: 7000, Transport: v1.ClientTransportConfig{Protocol: "tcp", TCPMux: &enabled, ProxyURL: "http://" + listener.Addr().String(), DialServerTimeout: 1}}
	if err := config.Complete(); err != nil {
		t.Fatal(err)
	}
	connector := frpclient.NewConnector(ctx, config)
	done := make(chan error, 1)
	go func() { done <- connector.Open() }()
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	request, err := http.ReadRequest(bufio.NewReader(connection))
	if err != nil || request.Method != http.MethodConnect {
		t.Fatalf("proxy did not receive CONNECT: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("upstream proxy cancellation behavior changed: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	if err := connection.SetReadDeadline(time.Now().Add(40 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	_, err = connection.Read(one[:])
	var netError net.Error
	if !errors.As(err, &netError) || !netError.Timeout() {
		t.Fatalf("proxy socket was not still open: %v", err)
	}
	_ = connection.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("owned proxy cleanup did not release Open")
	}
	_ = connector.Close()
	fmt.Fprint(os.Stdout, "proxy-open-remains-blocked")
}

func TestNewRuntimeRejectsProxyBeforeAnyNativeConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	connected := make(chan struct{})
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			_ = connection.Close()
			close(connected)
		}
	}()
	backend := testBackend{heartbeat: func(context.Context, string, string) (HeartbeatResult, error) {
		t.Error("proxy rejection called heartbeat")
		return HeartbeatResult{}, nil
	}, stop: func(context.Context, string, string) (Run, error) {
		t.Error("proxy rejection called stop")
		return Run{}, nil
	}}
	proxyURL := "http://secret:password@" + listener.Addr().String()
	_, err = NewRunner(RunnerOptions{Backend: backend, ProxyURL: proxyURL, EngineFactory: func(string) (Engine, error) { t.Error("proxy rejection started engine"); return nil, io.EOF }})
	if !errors.Is(err, ErrProxyUnsupported) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("proxy not rejected before runtime: %v", err)
	}
	select {
	case <-connected:
		t.Fatal("unsupported proxy received native connection")
	case <-time.After(20 * time.Millisecond):
	}
	if err := ValidateProxyURL(proxyURL); !errors.Is(err, ErrProxyUnsupported) {
		t.Fatalf("preallocation validation differs: %v", err)
	}
}

func TestFRPConfigCannotBypassProxyRejection(t *testing.T) {
	bootstrap := testBootstrap(t)
	_, err := buildFRPConfig(bootstrap, initialRun(time.Now()), Target{"http", "127.0.0.1", 3000}, "ca.pem", "synthetic-memory-source", "socks5://secret@127.0.0.1:1080")
	if !errors.Is(err, ErrProxyUnsupported) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("builder bypassed proxy rejection: %v", err)
	}
}
