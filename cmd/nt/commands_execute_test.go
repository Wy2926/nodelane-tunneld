package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/cliauth"
	"github.com/Wy2926/nodelane-tunneld/internal/runclient"
)

type commandAPISpy struct {
	commandAPI
	calls  *[]string
	run    runclient.Run
	config runclient.BootstrapConfig
	routes []runclient.Route
}

func (a *commandAPISpy) Bootstrap(context.Context) (runclient.BootstrapConfig, error) {
	*a.calls = append(*a.calls, "bootstrap")
	return a.config, nil
}

func (a *commandAPISpy) Redeem(_ context.Context, code, nonce string) (runclient.Run, error) {
	if code != commandLaunchCode || nonce != "one-request-key" {
		return runclient.Run{}, errors.New("incorrect redemption proof")
	}
	*a.calls = append(*a.calls, "redeem")
	return a.run, nil
}

func (a *commandAPISpy) Allocate(_ context.Context, install, protocol, host string, port int, key string) (runclient.Run, error) {
	if install != "installation-only" || protocol != "http" || host != "localhost" || port != 3000 || key != "one-request-key" {
		return runclient.Run{}, errors.New("incorrect anonymous request")
	}
	*a.calls = append(*a.calls, "allocate")
	return a.run, nil
}

func (a *commandAPISpy) Routes(_ context.Context, token string) ([]runclient.Route, error) {
	if token != "account-access-only" {
		return nil, errors.New("wrong account token")
	}
	*a.calls = append(*a.calls, "routes")
	return a.routes, nil
}

func (a *commandAPISpy) Start(_ context.Context, id, token, key string) (runclient.Run, error) {
	if id != "rte_aaaaaaaaaaaaaaaaaaaaaaaaaa" || token != "account-access-only" || key != "one-request-key" {
		return runclient.Run{}, errors.New("incorrect account start")
	}
	*a.calls = append(*a.calls, "start")
	return a.run, nil
}

type commandAccountSpy struct{ calls *[]string }

func (a commandAccountSpy) Login(_ context.Context, display func(cliauth.DeviceCode) error) error {
	*a.calls = append(*a.calls, "login")
	return display(cliauth.DeviceCode{UserCode: "ABCD-EFGH", VerificationURI: "https://auth.test/device"})
}
func (a commandAccountSpy) AccessToken(context.Context) (string, error) {
	*a.calls = append(*a.calls, "access-token")
	return "account-access-only", nil
}

func commandFixture(t *testing.T) (commandDependencies, *commandAPISpy, *[]string, *consoleUI) {
	t.Helper()
	calls := []string{}
	api := &commandAPISpy{calls: &calls, run: runclient.Run{ID: "run_aaaaaaaaaaaaaaaaaaaaaaaaaa", CredentialToken: "run-credential-only"}, routes: []runclient.Route{{ID: "rte_aaaaaaaaaaaaaaaaaaaaaaaaaa", Subdomain: "demo", Status: "active", Protocol: "http"}}}
	ui := newConsoleUI(&bytes.Buffer{}, &bytes.Buffer{})
	deps := commandDependencies{
		api: func() (commandAPI, error) { calls = append(calls, "api"); return api, nil },
		account: func(context.Context, runclient.OIDCConfig) (accountSession, error) {
			calls = append(calls, "account-store")
			return commandAccountSpy{&calls}, nil
		},
		logout:            func(context.Context) error { calls = append(calls, "logout"); return nil },
		preflight:         func(context.Context, runclient.Target) error { calls = append(calls, "preflight"); return nil },
		validateBootstrap: func(runclient.BootstrapConfig) error { calls = append(calls, "validate"); return nil },
		installationID:    func() (string, error) { calls = append(calls, "installation"); return "installation-only", nil },
		requestKey:        func() (string, error) { calls = append(calls, "key"); return "one-request-key", nil },
		run: func(_ context.Context, _ commandAPI, _ runclient.BootstrapConfig, run runclient.Run, target runclient.Target) error {
			if run.CredentialToken != "run-credential-only" || target.LocalHost != "localhost" {
				t.Fatal("runner received the wrong proof/target")
			}
			calls = append(calls, "run")
			return nil
		},
	}
	return deps, api, &calls, ui
}

func TestExecuteCommandSelectsOnlyItsCredentialDependencies(t *testing.T) {
	for _, test := range []struct {
		args []string
		want []string
	}{
		{[]string{"launch", commandLaunchCode, "localhost", "3000"}, []string{"preflight", "api", "bootstrap", "validate", "key", "redeem", "run"}},
		{[]string{"anonymous", "http", "localhost", "3000"}, []string{"preflight", "api", "bootstrap", "validate", "installation", "key", "allocate", "run"}},
		{[]string{"start", "demo", "localhost", "3000"}, []string{"preflight", "api", "bootstrap", "validate", "account-store", "access-token", "routes", "key", "start", "run"}},
		{[]string{"routes"}, []string{"api", "bootstrap", "account-store", "access-token", "routes"}},
		{[]string{"login"}, []string{"api", "bootstrap", "account-store", "login"}},
		{[]string{"logout"}, []string{"logout"}},
	} {
		t.Run(test.args[0], func(t *testing.T) {
			deps, _, calls, ui := commandFixture(t)
			command, err := parseCommand(test.args, ui)
			if err != nil {
				t.Fatal(err)
			}
			if err = executeCommand(context.Background(), command, ui, deps); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*calls, test.want) {
				t.Fatalf("calls=%v want=%v", *calls, test.want)
			}
		})
	}
}

func TestExecutePreflightAndBootstrapFailureNeverReserveOrOpenCredentials(t *testing.T) {
	for _, kind := range []string{"anonymous", "launch", "start"} {
		for _, stage := range []string{"preflight", "bootstrap"} {
			t.Run(kind+"/"+stage, func(t *testing.T) {
				deps, _, calls, ui := commandFixture(t)
				failure := errors.New("controlled failure")
				want := []string{"preflight"}
				if stage == "preflight" {
					deps.preflight = func(context.Context, runclient.Target) error { *calls = append(*calls, "preflight"); return failure }
				} else {
					deps.validateBootstrap = func(runclient.BootstrapConfig) error { *calls = append(*calls, "validate"); return failure }
					want = []string{"preflight", "api", "bootstrap", "validate"}
				}
				command := cliCommand{kind: kind, target: tunnelTarget{"http", "localhost", 3000}, route: "demo", launchCode: commandLaunchCode}
				if err := executeCommand(context.Background(), command, ui, deps); !errors.Is(err, failure) {
					t.Fatalf("err=%v", err)
				}
				if !reflect.DeepEqual(*calls, want) {
					t.Fatalf("failure side effects=%v", *calls)
				}
			})
		}
	}
}

func TestExecuteCanceledAndUnknownCommandsDoNotInitializeDependencies(t *testing.T) {
	deps, _, calls, ui := commandFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := executeCommand(ctx, cliCommand{kind: "login"}, ui, deps); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if err := executeCommand(context.Background(), cliCommand{kind: "unknown"}, ui, deps); err == nil {
		t.Fatal("unknown command accepted")
	}
	if len(*calls) != 0 {
		t.Fatalf("unexpected initialization=%v", *calls)
	}
}

func TestStartRejectsMissingOrAmbiguousRouteBeforeMutation(t *testing.T) {
	for _, count := range []int{0, 2} {
		deps, api, calls, ui := commandFixture(t)
		if count == 0 {
			api.routes = nil
		} else {
			api.routes = append(api.routes, api.routes[0])
		}
		command := cliCommand{kind: "start", target: tunnelTarget{"http", "localhost", 3000}, route: "demo"}
		if err := executeCommand(context.Background(), command, ui, deps); err == nil {
			t.Fatal("invalid route selection accepted")
		}
		if len(*calls) != 7 {
			t.Fatalf("mutated or unexpected calls=%v", *calls)
		}
	}
}
