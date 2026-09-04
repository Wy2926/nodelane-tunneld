package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestConsoleRoutesHumanWarningsToStdoutAndFailuresToStderr(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("FORCE_COLOR", "")
	var stdout, stderr bytes.Buffer
	ui := newConsoleUI(&stdout, &stderr)
	ui.banner()
	ui.warning("retry later")
	ui.failure("cannot connect")

	out := stdout.String()
	errOut := stderr.String()
	if strings.Count(out, "NodeLane Tunnel") != 1 {
		t.Fatalf("stdout brand count = %d; output: %q", strings.Count(out, "NodeLane Tunnel"), out)
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
