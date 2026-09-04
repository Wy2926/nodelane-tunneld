package main

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestPrepareTargetDecisionTable(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        targetFormValues
		interactive bool
	}{
		{name: "none", want: targetFormValues{Protocol: "http", Port: "3000", Focus: targetProtocol}, interactive: true},
		{name: "protocol", args: []string{"http"}, want: targetFormValues{Protocol: "http", Port: "3000", Focus: targetHost}, interactive: true},
		{name: "host", args: []string{"http", "localhost"}, want: targetFormValues{Protocol: "http", Host: "localhost", Port: "3000", Focus: targetPort}, interactive: true},
		{name: "complete", args: []string{"http", "localhost", "3000"}, want: targetFormValues{Protocol: "http", Host: "localhost", Port: "3000"}},
	}

	ui := newConsoleUI(&bytes.Buffer{}, &bytes.Buffer{})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, interactive, err := prepareTarget(test.args, ui)
			if err != nil {
				t.Fatal(err)
			}
			if interactive != test.interactive {
				t.Fatalf("interactive = %t, want %t", interactive, test.interactive)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("values = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestPrepareTargetRejectsCompleteInvalidInput(t *testing.T) {
	ui := newConsoleUI(&bytes.Buffer{}, &bytes.Buffer{})
	tests := [][]string{
		{"smtp", "localhost", "3000"},
		{"http", "host/path", "3000"},
		{"http", "localhost", "0"},
		{"http", "localhost", "65536"},
		{"http", "localhost", "abc"},
		{"http", "localhost", "3000", "extra"},
	}
	for _, args := range tests {
		if _, _, err := prepareTarget(args, ui); err == nil {
			t.Errorf("prepareTarget(%q) succeeded", args)
		}
	}
}

func TestTargetFormValuesNormalizesHostAndPortBoundaries(t *testing.T) {
	ui := newConsoleUI(&bytes.Buffer{}, &bytes.Buffer{})
	for _, port := range []string{"1", "65535"} {
		target, err := (targetFormValues{Protocol: "HTTP", Port: port}).target(ui)
		if err != nil {
			t.Fatalf("port %s: %v", port, err)
		}
		if target.protocol != "http" || target.host != "localhost" {
			t.Fatalf("target = %+v", target)
		}
	}
}

func TestBuildTargetFormFocusesRequestedField(t *testing.T) {
	ui := newConsoleUI(&bytes.Buffer{}, &bytes.Buffer{})
	for _, test := range []struct {
		focus targetField
		key   string
	}{
		{focus: targetProtocol, key: "protocol"},
		{focus: targetHost, key: "host"},
		{focus: targetPort, key: "port"},
	} {
		values := targetFormValues{Protocol: "http", Port: "3000", Focus: test.focus}
		form := buildTargetForm(&values, ui)
		if got := form.GetFocusedField().GetKey(); got != test.key {
			t.Fatalf("focus %d selected %q, want %q", test.focus, got, test.key)
		}
	}
}

func TestTargetFormValidatorsApplyFieldRules(t *testing.T) {
	ui := newConsoleUI(&bytes.Buffer{}, &bytes.Buffer{})
	for _, port := range []string{"1", "65535"} {
		if err := validatePortText(port, ui); err != nil {
			t.Errorf("port %q rejected: %v", port, err)
		}
	}
	for _, port := range []string{"", "0", "65536", "abc"} {
		if err := validatePortText(port, ui); err == nil {
			t.Errorf("port %q accepted", port)
		}
	}
	for _, host := range []string{"", "localhost", "::1"} {
		if err := validateOptionalHost(host, ui); err != nil {
			t.Errorf("host %q rejected: %v", host, err)
		}
	}
	if err := validateOptionalHost("http://localhost", ui); err == nil {
		t.Fatal("URL was accepted as a host")
	}
}

func TestRunTargetFormReturnsQuietCancellation(t *testing.T) {
	var output bytes.Buffer
	ui := newConsoleUI(&output, &bytes.Buffer{})
	values := targetFormValues{Protocol: "http", Port: "3000", Focus: targetProtocol}
	err := runTargetForm(&values, strings.NewReader("\x03"), &output, ui)
	if !errors.Is(err, errTargetFormCanceled) {
		t.Fatalf("error = %v, want errTargetFormCanceled", err)
	}
	if !strings.Contains(output.String(), "NodeLane Tunnel") {
		t.Fatalf("interactive output is missing the brand: %q", output.String())
	}
}

func TestArgumentsNeedPromptWheneverAnyPositionIsMissing(t *testing.T) {
	for _, args := range [][]string{nil, {"http"}, {"http", "3000"}, {"http", "localhost"}} {
		if !argumentsNeedPrompt(args) {
			t.Errorf("argumentsNeedPrompt(%q) = false", args)
		}
	}
	if argumentsNeedPrompt([]string{"http", "localhost", "3000"}) {
		t.Fatal("complete arguments requested a prompt")
	}
}
