package main

import (
	"bytes"
	"reflect"
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
