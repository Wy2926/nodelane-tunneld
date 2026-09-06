package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/runclient"
)

const commandLaunchCode = "nlc_aaaaaaaaaaaaaaaaaaaaaaaaaa.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestParseCommandSeparatesCredentialFlows(t *testing.T) {
	ui := newConsoleUI(&bytes.Buffer{}, &bytes.Buffer{})
	for _, test := range []struct {
		args   []string
		kind   string
		target tunnelTarget
		prompt bool
	}{
		{nil, "menu", tunnelTarget{}, false},
		{[]string{"login"}, "login", tunnelTarget{}, false},
		{[]string{"logout"}, "logout", tunnelTarget{}, false},
		{[]string{"routes"}, "routes", tunnelTarget{}, false},
		{[]string{"--help"}, "help", tunnelTarget{}, false},
		{[]string{"version"}, "version", tunnelTarget{}, false},
		{[]string{"languages"}, "languages", tunnelTarget{}, false},
		{[]string{"anonymous"}, "anonymous", tunnelTarget{}, true},
		{[]string{"anonymous", "tcp", "localhost", "22"}, "anonymous", tunnelTarget{"tcp", "localhost", 22}, false},
		{[]string{"anonymous", "udp", "[::1]", "5353"}, "anonymous", tunnelTarget{"udp", "::1", 5353}, false},
		{[]string{"start", "demo", "localhost", "3000"}, "start", tunnelTarget{"http", "localhost", 3000}, false},
		{[]string{"launch", commandLaunchCode, "localhost", "3000"}, "launch", tunnelTarget{"http", "localhost", 3000}, false},
	} {
		got, err := parseCommand(test.args, ui)
		if err != nil || got.kind != test.kind || got.target != test.target || got.interactive != test.prompt {
			t.Fatalf("kind=%s command=%s target=%v prompt=%v err=%v", test.kind, got, got.target, got.interactive, err)
		}
		if got.kind == "launch" && got.launchCode != commandLaunchCode {
			t.Fatal("launch code was not preserved")
		}
		for _, formatted := range []string{fmt.Sprintf("%+v", got), fmt.Sprintf("%#v", got)} {
			if strings.Contains(formatted, commandLaunchCode) {
				t.Fatal("formatted command exposes launch secret")
			}
		}
	}
}

func TestParseCommandRejectsLegacyAndAmbiguousArguments(t *testing.T) {
	ui := newConsoleUI(&bytes.Buffer{}, &bytes.Buffer{})
	for _, args := range [][]string{
		{"http", "localhost", "3000"}, {"login", "unexpected"}, {"logout", "extra"},
		{"start"}, {"start", "https://demo.test", "localhost", "3000"},
		{"start", "anon-hidden", "localhost", "3000"}, {"start", "demo", "tcp://localhost", "3000"},
		{"launch", "bad-code", "localhost", "3000"}, {"launch", commandLaunchCode, "localhost"},
		{"launch", commandLaunchCode, "localhost", "3000", "extra"},
		{"anonymous", "http", "localhost", "0"}, {"anonymous", "http", "localhost", "65536"},
		{"anonymous", "https", "localhost", "3000"}, {"unknown"}, {"--version", "extra"},
	} {
		if _, err := parseCommand(args, ui); err == nil {
			t.Fatalf("invalid command %q was accepted", args[0])
		} else if strings.Contains(err.Error(), commandLaunchCode) {
			t.Fatal("command error exposes launch secret")
		}
	}
}

func TestEveryLocaleIncludesNewCommandLabelsAndUsage(t *testing.T) {
	for _, locale := range supportedLocales {
		localizer := newLocalizer(locale)
		for _, id := range []messageID{
			msgChooseMode, msgAnonymousMode, msgAccountMode, msgLoggedIn, msgLoggedOut,
			msgLoginURL, msgLoginCode, msgNoRoutes, msgRoutesLabel, msgRouteIDLabel,
			msgRunIDLabel, msgStateLabel, msgRouteSelectionFailed, msgLoginRequired,
			msgOperationFailed, msgRetryAfter, msgRunStopping, msgRunStopped, msgRunReconnecting,
		} {
			if strings.TrimSpace(localizer.text(id)) == "" {
				t.Errorf("locale %s lacks command label %s", locale, id)
			}
		}
		for _, command := range []string{"nt anonymous", "nt launch", "nt start", "nt login", "nt logout", "nt routes"} {
			if !strings.Contains(localizer.text(msgUsage), command) {
				t.Errorf("locale %s usage omits %s", locale, command)
			}
		}
	}
}

func TestRunArgumentsDispatchesWithoutCredentialInitializationForPublicCommands(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}, {"languages"}, {"http", "localhost", "3000"}} {
		deps, _, calls, ui := commandFixture(t)
		err := runArguments(context.Background(), args, ui, environment(nil), deps)
		if args[0] == "http" {
			if err == nil {
				t.Fatal("legacy command accepted")
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 0 {
			t.Fatalf("public command initialized dependencies: %v", *calls)
		}
	}
}

func TestRunArgumentsAppliesLanguageBeforeDispatch(t *testing.T) {
	deps, _, calls, ui := commandFixture(t)
	if err := runArguments(context.Background(), []string{"--lang", "zh-CN", "login"}, ui, environment(nil), deps); err != nil {
		t.Fatal(err)
	}
	if ui.localizer.locale != "zh-CN" || len(*calls) != 4 {
		t.Fatalf("locale=%s calls=%v", ui.localizer.locale, *calls)
	}
}

func TestCommandErrorsNeverPrintRawDependencyDetails(t *testing.T) {
	deps, _, _, ui := commandFixture(t)
	deps.preflight = func(context.Context, runclient.Target) error {
		return errors.New("secret-token http://private:password@example.test")
	}
	err := runArguments(context.Background(), []string{"launch", commandLaunchCode, "localhost", "3000"}, ui, environment(nil), deps)
	if err == nil || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "password") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestProxyConfigurationFailsBeforeAllocation(t *testing.T) {
	t.Setenv("NT_FRP_PROXY_URL", "http://127.0.0.1:3456")
	deps, _, calls, ui := commandFixture(t)
	deps.validateBootstrap = defaultCommandDependencies(ui).validateBootstrap
	command := cliCommand{kind: "anonymous", target: tunnelTarget{"http", "localhost", 3000}}
	if err := executeCommand(context.Background(), command, ui, deps); !errors.Is(err, runclient.ErrProxyUnsupported) {
		t.Fatalf("proxy configuration error = %v", err)
	}
	if len(*calls) != 3 {
		t.Fatalf("proxy error allocated resources: %v", *calls)
	}
}
