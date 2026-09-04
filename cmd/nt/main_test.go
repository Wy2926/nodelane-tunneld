package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestTailBufferKeepsBoundedSuffix(t *testing.T) {
	buffer := newTailBuffer(8)
	_, _ = buffer.Write([]byte("12345"))
	_, _ = buffer.Write([]byte("67890"))
	if got := buffer.String(); got != "34567890" {
		t.Fatalf("tail buffer = %q, want %q", got, "34567890")
	}
	_, _ = buffer.Write([]byte("abcdefghijk"))
	if got := buffer.String(); got != "defghijk" {
		t.Fatalf("tail buffer after large write = %q", got)
	}
}

func TestResolveArgsPromptsForEveryValue(t *testing.T) {
	var output bytes.Buffer
	ui := newConsoleUI(&output, &output)
	target, err := resolveArgs(nil, strings.NewReader("2\n\n8080\n"), ui)
	if err != nil {
		t.Fatal(err)
	}
	if target.protocol != "tcp" || target.host != "localhost" || target.port != 8080 {
		t.Fatalf("resolved %+v, want tcp/localhost/8080", target)
	}
}

func TestResolveArgsPromptsForCustomAddress(t *testing.T) {
	var output bytes.Buffer
	ui := newConsoleUI(&output, &output)
	target, err := resolveArgs(nil, strings.NewReader("http\ndevice.local\n3000\n"), ui)
	if err != nil {
		t.Fatal(err)
	}
	if target.protocol != "http" || target.host != "device.local" || target.port != 3000 {
		t.Fatalf("resolved %+v, want http/device.local/3000", target)
	}
}

func TestResolveArgsPromptsOnlyForMissingValue(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		input        string
		wantProtocol string
		wantHost     string
		wantPort     int
	}{
		{name: "missing port", args: []string{"http"}, input: "3000\n", wantProtocol: "http", wantHost: "localhost", wantPort: 3000},
		{name: "missing protocol", args: []string{"5353"}, input: "3\n", wantProtocol: "udp", wantHost: "localhost", wantPort: 5353},
		{name: "default address", args: []string{"tcp", "22"}, wantProtocol: "tcp", wantHost: "localhost", wantPort: 22},
		{name: "explicit address", args: []string{"http", "192.168.1.20", "8080"}, wantProtocol: "http", wantHost: "192.168.1.20", wantPort: 8080},
		{name: "IPv6 address", args: []string{"tcp", "[::1]", "22"}, wantProtocol: "tcp", wantHost: "::1", wantPort: 22},
		{name: "address with prompted port", args: []string{"udp", "device.local"}, input: "5353\n", wantProtocol: "udp", wantHost: "device.local", wantPort: 5353},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			ui := newConsoleUI(&output, &output)
			target, err := resolveArgs(test.args, strings.NewReader(test.input), ui)
			if err != nil {
				t.Fatal(err)
			}
			if target.protocol != test.wantProtocol || target.host != test.wantHost || target.port != test.wantPort {
				t.Fatalf("resolved %+v, want %s/%s/%d", target, test.wantProtocol, test.wantHost, test.wantPort)
			}
		})
	}
}

func TestResolveArgsRejectsInvalidAddress(t *testing.T) {
	ui := newConsoleUI(&bytes.Buffer{}, &bytes.Buffer{})
	for _, address := range []string{"", "http://localhost", "host/path", "host name", "localhost:3000"} {
		_, err := resolveArgs([]string{"http", address, "3000"}, nil, ui)
		if err == nil {
			t.Errorf("address %q was accepted", address)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	for value, want := range map[uint64]string{0: "0 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 1024 * 1024: "1.0 MiB"} {
		if got := formatBytes(value); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", value, got, want)
		}
	}
}

func TestSafeConsoleFieldRemovesControlCharacters(t *testing.T) {
	if got := safeConsoleField("203.0.113.9\x1b[31m\n", 64); got != "203.0.113.9[31m" {
		t.Fatalf("safe console field = %q", got)
	}
}
