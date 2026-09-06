package runclient

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestPreflightChecksRealTCPServiceBeforeAnyAllocation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, encodedPort, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(encodedPort)
	if err := Preflight(context.Background(), Target{Protocol: "http", LocalHost: "127.0.0.1", LocalPort: port}); err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()
	err = Preflight(context.Background(), Target{Protocol: "tcp", LocalHost: "127.0.0.1", LocalPort: port})
	if !errors.Is(err, ErrLocalUnavailable) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("unsafe local error: %v", err)
	}
}

func TestPreflightInitializesUDPSocketWithoutClaimingServiceHealth(t *testing.T) {
	if err := Preflight(context.Background(), Target{Protocol: "udp", LocalHost: "127.0.0.1", LocalPort: 3000}); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightRejectsInvalidTargetsAndCancellation(t *testing.T) {
	for _, target := range []Target{{"http", "https://localhost", 80}, {"https", "localhost", 80}, {"tcp", "localhost", 0}, {"udp", "localhost", 65536}, {"http", "localhost\nsecret", 80}, {"tcp", "host:80", 80}} {
		if err := Preflight(context.Background(), target); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid target accepted: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Preflight(ctx, Target{"tcp", "127.0.0.1", 3000}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
