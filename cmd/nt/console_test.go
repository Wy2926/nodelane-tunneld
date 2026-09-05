package main

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
)

func TestColorWriterPreservesTerminalFileContract(t *testing.T) {
	wrapped := colorWriter(os.Stdout)
	file, ok := wrapped.(interface {
		io.ReadWriteCloser
		Fd() uintptr
	})
	if !ok {
		t.Fatal("color writer hid the underlying terminal file contract")
	}
	if got, want := file.Fd(), os.Stdout.Fd(); got != want {
		t.Fatalf("color writer descriptor = %d, want %d", got, want)
	}
}

func TestWindowsFallbackConsoleInputSupportsRawMode(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires the Windows console device")
	}
	input, err := openConsoleDevice()
	if err != nil {
		t.Skipf("console device unavailable: %v", err)
	}
	defer input.Close()
	file, ok := input.(interface{ Fd() uintptr })
	if !ok {
		t.Fatal("console input does not expose its Windows handle")
	}
	state, err := term.MakeRaw(file.Fd())
	if err != nil {
		t.Fatalf("fallback console input cannot enter raw mode: %v", err)
	}
	if err := term.Restore(file.Fd(), state); err != nil {
		t.Fatalf("restore console mode: %v", err)
	}
}

func TestInteractiveInputPrefersUsableStdinBeforeOpeningConsoleDevice(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	stdin := strings.NewReader("stdin")
	opened := false
	input, closeInput, err := selectInteractiveInput(
		newConsoleUI(&bytes.Buffer{}, &bytes.Buffer{}),
		stdin,
		true,
		func() (io.ReadCloser, error) {
			opened = true
			return io.NopCloser(strings.NewReader("console")), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer closeInput()
	if opened {
		t.Fatal("console device opened even though stdin is an interactive terminal")
	}
	if input != stdin {
		t.Fatal("interactive input did not preserve the usable stdin reader")
	}
}

func TestInteractiveInputFallsBackToConsoleDeviceForPipedStdin(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	opened := false
	input, closeInput, err := selectInteractiveInput(
		newConsoleUI(&bytes.Buffer{}, &bytes.Buffer{}),
		strings.NewReader("pipe"),
		false,
		func() (io.ReadCloser, error) {
			opened = true
			return io.NopCloser(strings.NewReader("console")), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer closeInput()
	if !opened {
		t.Fatal("console device was not opened for redirected stdin")
	}
	contents, err := io.ReadAll(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "console" {
		t.Fatalf("interactive input = %q, want console device", contents)
	}
}

func TestConsoleRoutesHumanWarningsToStdoutAndFailuresToStderr(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("FORCE_COLOR", "")
	var stdout, stderr bytes.Buffer
	ui := newConsoleUI(&stdout, &stderr)
	ui.banner()
	ui.banner()
	ui.warning("retry later")
	ui.failure("cannot connect")

	out := stdout.String()
	errOut := stderr.String()
	if strings.Count(out, "NodeLane Tunnel") != 1 {
		t.Fatalf("stdout brand count = %d; output: %q", strings.Count(out, "NodeLane Tunnel"), out)
	}
	if !strings.Contains(out, "run nt") {
		t.Fatalf("direct nt command hint missing from stdout: %q", out)
	}
	for _, art := range []string{"_   _ _____", "|_| \\_| |_|"} {
		if !strings.Contains(out, art) {
			t.Fatalf("brand art is missing %q from stdout: %q", art, out)
		}
	}
	if !strings.Contains(out, "retry later") {
		t.Fatalf("warning missing from stdout: %q", out)
	}
	if strings.Contains(errOut, "retry later") {
		t.Fatalf("warning leaked to stderr: %q", errOut)
	}
	if !strings.Contains(errOut, "cannot connect") {
		t.Fatalf("failure missing from stderr: %q", errOut)
	}
	if strings.Contains(out+errOut, "\x1b[") {
		t.Fatalf("plain output contains ANSI: %q", out+errOut)
	}
}

func TestNodeLaneFormThemeUsesSemanticFocusColors(t *testing.T) {
	styles := nodeLaneFormTheme().Theme(true)
	if got := styles.Focused.Title.GetForeground(); !reflect.DeepEqual(got, lipgloss.BrightCyan) {
		t.Fatalf("focused title color = %v, want bright cyan", got)
	}
	if got := styles.Focused.ErrorMessage.GetForeground(); !reflect.DeepEqual(got, lipgloss.BrightRed) {
		t.Fatalf("error color = %v, want bright red", got)
	}
	if got := styles.Blurred.TextInput.Text.GetForeground(); !reflect.DeepEqual(got, lipgloss.BrightGreen) {
		t.Fatalf("completed input color = %v, want bright green", got)
	}
}

func TestConsoleForcedColorUsesStyledOutput(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("FORCE_COLOR", "1")
	var stdout bytes.Buffer
	ui := newConsoleUI(&stdout, &bytes.Buffer{})
	ui.banner()
	ui.success("connected")
	if !strings.Contains(stdout.String(), "\x1b[") {
		t.Fatalf("forced-color output is unstyled: %q", stdout.String())
	}
}

func TestConsoleRequestIncludesHTTPStatus(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var stdout bytes.Buffer
	ui := newConsoleUI(&stdout, &bytes.Buffer{})
	ui.request(time.Date(2026, time.September, 5, 12, 0, 0, 0, time.Local), "203.0.113.9", "GET", 404, "demo.example/missing")
	for _, value := range []string{"203.0.113.9", "GET", "404", "demo.example/missing"} {
		if !strings.Contains(stdout.String(), value) {
			t.Fatalf("request output %q does not contain %q", stdout.String(), value)
		}
	}
}

func TestConsoleRedirectedStatsUseStableLines(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var stdout bytes.Buffer
	ui := newConsoleUI(&stdout, &bytes.Buffer{})
	ui.stats("tcp", trafficSnapshot{ActiveConnections: 1, TotalConnections: 2})
	ui.stats("tcp", trafficSnapshot{ActiveConnections: 2, TotalConnections: 3})
	ui.endStats()
	if strings.Contains(stdout.String(), "\r") {
		t.Fatalf("redirected stats contain cursor rewrites: %q", stdout.String())
	}
	if got := strings.Count(stdout.String(), "TCP"); got != 2 {
		t.Fatalf("stats lines = %d, want 2; output: %q", got, stdout.String())
	}
}

func TestWarningGateSuppressesDuplicatesUntilReset(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var stdout bytes.Buffer
	ui := newConsoleUI(&stdout, &bytes.Buffer{})
	ui.warningOnce("http-upstream", "offline")
	ui.warningOnce("http-upstream", "offline again")
	if got := strings.Count(stdout.String(), "WARN"); got != 1 {
		t.Fatalf("warning count = %d, want 1; output: %q", got, stdout.String())
	}
	ui.resetWarning("http-upstream")
	ui.warningOnce("http-upstream", "offline later")
	if got := strings.Count(stdout.String(), "WARN"); got != 2 {
		t.Fatalf("warning count after reset = %d, want 2; output: %q", got, stdout.String())
	}
}
