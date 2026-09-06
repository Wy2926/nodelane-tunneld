package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/cliauth"
	"github.com/Wy2926/nodelane-tunneld/internal/runclient"
)

type loginDisplayAccount struct {
	accountSession
	device    cliauth.DeviceCode
	completed bool
}

func (a *loginDisplayAccount) Login(ctx context.Context, display func(cliauth.DeviceCode) error) error {
	if err := display(a.device); err != nil {
		return err
	}
	a.completed = true
	return ctx.Err()
}

func TestLoginDisplaysExpiryAndOpensVerifiedAuthorizationURL(t *testing.T) {
	for _, complete := range []string{"", "https://auth.test/device?user_code=ABCD-EFGH"} {
		t.Run(complete, func(t *testing.T) {
			deps, _, _, ui := commandFixture(t)
			output := &bytes.Buffer{}
			ui.out = output
			account := &loginDisplayAccount{device: cliauth.DeviceCode{
				UserCode: "ABCD-EFGH", VerificationURI: "https://auth.test/device", VerificationURIComplete: complete,
				Expiry: time.Date(2026, 9, 6, 12, 30, 0, 0, time.UTC),
			}}
			deps.account = func(context.Context, runclient.OIDCConfig) (accountSession, error) { return account, nil }
			opened := ""
			deps.openBrowser = func(ctx context.Context, target string) error {
				opened = target
				deadline, bounded := ctx.Deadline()
				if !bounded || time.Until(deadline) <= 0 || time.Until(deadline) > deviceBrowserTimeout {
					t.Fatal("browser launch did not receive a finite deadline")
				}
				for _, field := range []string{account.device.VerificationURI, account.device.UserCode, account.device.Expiry.Local().Format("2006-01-02 15:04:05 MST")} {
					if !strings.Contains(output.String(), field) {
						t.Fatal("manual authorization details were not printed before browser launch")
					}
				}
				return nil
			}
			if err := executeCommand(context.Background(), cliCommand{kind: "login"}, ui, deps); err != nil || !account.completed {
				t.Fatalf("device login failed: %v", err)
			}
			want := account.device.VerificationURI
			if complete != "" {
				want = complete
			}
			if opened != want || !strings.Contains(output.String(), ui.text(msgExpiresAtLabel)) {
				t.Fatal("login did not use the expected verified URL and localized expiry label")
			}
		})
	}
}

func TestLoginBrowserFailurePreservesManualAuthorization(t *testing.T) {
	for _, failure := range []error{errors.New("private browser launcher details"), context.DeadlineExceeded} {
		deps, _, _, ui := commandFixture(t)
		output := &bytes.Buffer{}
		ui.out = output
		account := &loginDisplayAccount{device: cliauth.DeviceCode{UserCode: "ABCD-EFGH", VerificationURI: "https://auth.test/device"}}
		deps.account = func(context.Context, runclient.OIDCConfig) (accountSession, error) { return account, nil }
		deps.openBrowser = func(context.Context, string) error { return failure }
		if err := executeCommand(context.Background(), cliCommand{kind: "login"}, ui, deps); err != nil || !account.completed {
			t.Fatalf("optional browser failure prevented manual authorization: %v", err)
		}
		if strings.Contains(output.String(), failure.Error()) {
			t.Fatal("browser launcher error leaked into CLI output")
		}
	}
}

func TestLoginBrowserCancellationCancelsAuthorization(t *testing.T) {
	deps, _, _, ui := commandFixture(t)
	account := &loginDisplayAccount{device: cliauth.DeviceCode{UserCode: "ABCD-EFGH", VerificationURI: "https://auth.test/device"}}
	deps.account = func(context.Context, runclient.OIDCConfig) (accountSession, error) { return account, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps.openBrowser = func(openCtx context.Context, _ string) error {
		cancel()
		if !errors.Is(openCtx.Err(), context.Canceled) {
			t.Fatal("browser launch did not inherit login cancellation")
		}
		return openCtx.Err()
	}
	if err := executeCommand(ctx, cliCommand{kind: "login"}, ui, deps); !errors.Is(err, context.Canceled) || account.completed {
		t.Fatalf("canceled login continued authorization: %v", err)
	}
}
