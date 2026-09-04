package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHTTPMonitorForwardsAndLogsRequest(t *testing.T) {
	requestSeen := make(chan *http.Request, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestSeen <- request.Clone(request.Context())
		_, _ = io.WriteString(writer, "hello")
	}))
	defer upstream.Close()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)

	var output bytes.Buffer
	ui := newConsoleUI(&output, &output)
	ctx, cancel := context.WithCancel(context.Background())
	monitor, err := startHTTPMonitor(ctx, "localhost", port, ui)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = monitor.Close()
	}()

	request, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(monitor.port)+"/api/items?q=one", strings.NewReader("payload"))
	request.Host = "demo.tunnel.nodelane.net"
	request.Header.Set("X-Real-IP", "203.0.113.9")
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case seen := <-requestSeen:
		if seen.Host != request.Host {
			t.Fatalf("upstream Host = %q, want %q", seen.Host, request.Host)
		}
		if got := seen.Header.Get("X-Forwarded-For"); got != "203.0.113.9" {
			t.Fatalf("upstream X-Forwarded-For = %q, want original client IP", got)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive request")
	}
	logLine := output.String()
	for _, value := range []string{"203.0.113.9", "POST", "demo.tunnel.nodelane.net/api/items?q=one"} {
		if !strings.Contains(logLine, value) {
			t.Errorf("request log %q does not contain %q", logLine, value)
		}
	}
}

func TestTCPMonitorCountsBidirectionalTraffic(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		connection, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		buffer := make([]byte, 4)
		if _, readErr := io.ReadFull(connection, buffer); readErr == nil {
			_, _ = connection.Write(buffer)
		}
	}()

	var output bytes.Buffer
	ui := newConsoleUI(&output, &output)
	ctx, cancel := context.WithCancel(context.Background())
	monitor, err := startTCPMonitor(ctx, "127.0.0.1", upstream.Addr().(*net.TCPAddr).Port, ui)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = monitor.Close()
	}()
	connection, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(monitor.port)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(connection, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "ping" {
		t.Fatalf("reply = %q", reply)
	}
	snapshot := waitForTraffic(t, monitor, 4, 4)
	if snapshot.ActiveConnections != 1 || snapshot.TotalConnections != 1 || snapshot.ReceivedBytes != 4 || snapshot.SentBytes != 4 {
		t.Fatalf("unexpected TCP snapshot: %+v", snapshot)
	}
	_ = connection.Close()
}

func TestUDPMonitorCountsBidirectionalTraffic(t *testing.T) {
	upstream, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		buffer := make([]byte, 64)
		n, address, readErr := upstream.ReadFromUDP(buffer)
		if readErr == nil {
			_, _ = upstream.WriteToUDP(buffer[:n], address)
		}
	}()

	var output bytes.Buffer
	ui := newConsoleUI(&output, &output)
	ctx, cancel := context.WithCancel(context.Background())
	monitor, err := startUDPMonitor(ctx, "127.0.0.1", upstream.LocalAddr().(*net.UDPAddr).Port, ui)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = monitor.Close()
	}()
	connection, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: monitor.port})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	if _, err := connection.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 5)
	if _, err := io.ReadFull(connection, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "hello" {
		t.Fatalf("reply = %q", reply)
	}
	snapshot := waitForTraffic(t, monitor, 5, 5)
	if snapshot.ActiveConnections != 1 || snapshot.TotalConnections != 1 || snapshot.ReceivedBytes != 5 || snapshot.SentBytes != 5 {
		t.Fatalf("unexpected UDP snapshot: %+v", snapshot)
	}
}

func waitForTraffic(t *testing.T, monitor *trafficMonitor, received, sent uint64) trafficSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot := monitor.Snapshot()
		if snapshot.ReceivedBytes == received && snapshot.SentBytes == sent {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("traffic counters did not reach received=%d sent=%d; last snapshot: %+v", received, sent, snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}
