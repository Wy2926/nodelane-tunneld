package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/cliauth"
	"github.com/Wy2926/nodelane-tunneld/internal/runclient"
)

type logoutStore struct {
	loadErr, deleteErr error
	deletes            int
}

func (s *logoutStore) Load(context.Context) (cliauth.Credentials, error) {
	return cliauth.Credentials{}, s.loadErr
}
func (s *logoutStore) Save(context.Context, cliauth.Credentials) error {
	return errors.New("unexpected save")
}
func (s *logoutStore) Delete(context.Context) error { s.deletes++; return s.deleteErr }
func (s *logoutStore) Transaction(_ context.Context, fn func(cliauth.Store) error) error {
	return fn(s)
}

func TestLogoutShowsLocalClearOnlyAfterConfirmedDeletion(t *testing.T) {
	for _, failDelete := range []bool{false, true} {
		out := &bytes.Buffer{}
		ui := newConsoleUI(out, &bytes.Buffer{})
		store := &logoutStore{loadErr: cliauth.ErrCredentialsUnavailable}
		if failDelete {
			store.deleteErr = cliauth.ErrCredentialsUnavailable
		}
		err := logoutWithStore(context.Background(), store, ui)
		if !errors.Is(err, cliauth.ErrRevocationUnconfirmed) || store.deletes != 1 {
			t.Fatalf("cleanup error=%v calls=%d", err, store.deletes)
		}
		if got := strings.Contains(out.String(), ui.text(msgLoggedOut)); got == failDelete {
			t.Fatalf("false cleanup status=%q err=%v", out.String(), err)
		}
	}
}

type failedAccount struct{ err error }

func (a failedAccount) Login(context.Context, func(cliauth.DeviceCode) error) error { return a.err }
func (a failedAccount) AccessToken(context.Context) (string, error)                 { return "", a.err }

func TestRunArgumentsRetainsRecoverableFailureMessages(t *testing.T) {
	deps, api, _, ui := commandFixture(t)
	api.routes = nil
	err := runArguments(context.Background(), []string{"start", "demo", "localhost", "3000"}, ui, environment(nil), deps)
	if err == nil || err.Error() != ui.text(msgRouteSelectionFailed) {
		t.Fatalf("route recovery message=%v", err)
	}
	for _, cause := range []error{cliauth.ErrAuthorizationExpired, cliauth.ErrAuthorizationRevoked} {
		deps.account = func(context.Context, runclient.OIDCConfig) (accountSession, error) { return failedAccount{cause}, nil }
		err = runArguments(context.Background(), []string{"routes"}, ui, environment(nil), deps)
		if err == nil || err.Error() != ui.text(msgLoginRequired) {
			t.Fatalf("login recovery message=%v", err)
		}
	}
}
