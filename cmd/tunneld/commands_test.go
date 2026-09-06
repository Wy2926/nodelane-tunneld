package main

import "testing"

func TestServiceCommandsAreExplicitAndRejectAccidentalInitialization(t *testing.T) {
	for _, test := range []struct {
		args  []string
		want  serviceCommand
		valid bool
	}{
		{nil, commandServe, true},
		{[]string{"--version"}, commandVersion, true},
		{[]string{"anonymous-resources", "init", "--confirm-clean-data-plane"}, commandInitAnonymous, true},
		{[]string{"anonymous-resources", "init"}, 0, false},
		{[]string{"anonymous-resources", "init", "--force"}, 0, false},
		{[]string{"anonymous-resources", "reset"}, 0, false},
		{[]string{"serve", "unknown"}, 0, false},
	} {
		got, err := parseServiceCommand(test.args)
		if (err == nil) != test.valid || test.valid && got != test.want {
			t.Fatalf("args=%v got=%v err=%v", test.args, got, err)
		}
	}
}
