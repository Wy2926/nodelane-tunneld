package frpmux

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
)

type recordingDispatcher struct {
	marker   string
	err      error
	requests []frpplugin.Request
}

func (d *recordingDispatcher) Dispatch(_ context.Context, request frpplugin.Request) (frpplugin.Response, error) {
	d.requests = append(d.requests, request)
	if d.err != nil {
		return frpplugin.Response{}, d.err
	}
	return frpplugin.Response{Unchange: true, Content: d.marker}, d.err
}

func TestMuxRoutesEveryCallbackByRunCredentialNamespace(t *testing.T) {
	operations := []struct {
		op      frpplugin.Operation
		content func(string) string
	}{
		{frpplugin.OpLogin, loginContent},
		{frpplugin.OpNewProxy, userContent},
		{frpplugin.OpPing, userContent},
		{frpplugin.OpNewWorkConn, userContent},
		{frpplugin.OpNewUserConn, userContent},
		{frpplugin.OpCloseProxy, userContent},
	}
	for _, operation := range operations {
		for _, credential := range []struct {
			name   string
			token  string
			marker string
		}{
			{name: "registered", token: "nrc_credential.secret", marker: "registered"},
			{name: "anonymous", token: "nac_credential.secret", marker: "anonymous"},
		} {
			t.Run(string(operation.op)+"/"+credential.name, func(t *testing.T) {
				registered := &recordingDispatcher{marker: "registered"}
				anonymous := &recordingDispatcher{marker: "anonymous"}
				mux := mustMux(t, registered, anonymous)
				request := frpplugin.Request{Op: operation.op, Content: json.RawMessage(operation.content(credential.token))}

				response, err := mux.Dispatch(context.Background(), request)
				if err != nil {
					t.Fatalf("Dispatch: %v", err)
				}
				if response.Content != credential.marker {
					t.Fatalf("response marker = %#v, want %q", response.Content, credential.marker)
				}
				wantRegistered := 0
				wantAnonymous := 0
				if credential.name == "registered" {
					wantRegistered = 1
				} else {
					wantAnonymous = 1
				}
				if len(registered.requests) != wantRegistered || len(anonymous.requests) != wantAnonymous {
					t.Fatalf("dispatcher calls registered=%d anonymous=%d", len(registered.requests), len(anonymous.requests))
				}
			})
		}
	}
}

func TestMuxRejectsUnknownOrAmbiguousCredentialWithoutBackendCall(t *testing.T) {
	tests := []struct {
		name    string
		op      frpplugin.Operation
		content string
	}{
		{name: "missing token", op: frpplugin.OpLogin, content: `{"metas":{"nodelane_run_id":"run"}}`},
		{name: "account token", op: frpplugin.OpLogin, content: loginContent("eyJ.account.token")},
		{name: "legacy token", op: frpplugin.OpLogin, content: `{"metas":{"tunnel_token":"legacy"}}`},
		{name: "case variant field", op: frpplugin.OpLogin, content: `{"METAS":{"nodelane_run_token":"nrc_value.secret"}}`},
		{name: "malformed JSON", op: frpplugin.OpPing, content: `{"user":`},
		{name: "unsupported operation", op: frpplugin.Operation("Unknown"), content: userContent("nrc_value.secret")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registered := &recordingDispatcher{}
			anonymous := &recordingDispatcher{}
			mux := mustMux(t, registered, anonymous)
			response, err := mux.Dispatch(context.Background(), frpplugin.Request{Op: test.op, Content: json.RawMessage(test.content)})
			if err != nil || !response.Reject || response.RejectReason != InvalidCredentialReason || !response.Unchange {
				t.Fatalf("Dispatch = (%#v, %v), want invalid credential rejection", response, err)
			}
			if len(registered.requests) != 0 || len(anonymous.requests) != 0 {
				t.Fatalf("backend called registered=%d anonymous=%d", len(registered.requests), len(anonymous.requests))
			}
		})
	}
}

func TestMuxPropagatesSelectedDispatcherError(t *testing.T) {
	dependencyErr := errors.New("registered dependency unavailable")
	registered := &recordingDispatcher{err: dependencyErr}
	anonymous := &recordingDispatcher{}
	mux := mustMux(t, registered, anonymous)
	response, err := mux.Dispatch(context.Background(), frpplugin.Request{Op: frpplugin.OpPing, Content: json.RawMessage(userContent("nrc_value.secret"))})
	if !errors.Is(err, dependencyErr) || response.Content != nil {
		t.Fatalf("Dispatch = (%#v, %v), want selected dispatcher result/error", response, err)
	}
	if len(registered.requests) != 1 || len(anonymous.requests) != 0 {
		t.Fatalf("backend calls registered=%d anonymous=%d", len(registered.requests), len(anonymous.requests))
	}
}

func TestNewRejectsNilDispatchers(t *testing.T) {
	valid := &recordingDispatcher{}
	var typedNil *recordingDispatcher
	for _, test := range []struct {
		name       string
		registered Dispatcher
		anonymous  Dispatcher
	}{
		{name: "nil registered", anonymous: valid},
		{name: "typed nil registered", registered: typedNil, anonymous: valid},
		{name: "nil anonymous", registered: valid},
		{name: "typed nil anonymous", registered: valid, anonymous: typedNil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if mux, err := New(test.registered, test.anonymous); mux != nil || !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("New = (%v, %v), want nil and ErrInvalidConfiguration", mux, err)
			}
		})
	}
}

func mustMux(t *testing.T, registered, anonymous Dispatcher) *Mux {
	t.Helper()
	mux, err := New(registered, anonymous)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return mux
}

func loginContent(token string) string {
	return `{"metas":{"nodelane_run_id":"run","nodelane_run_token":"` + token + `"}}`
}

func userContent(token string) string {
	return `{"user":{"metas":{"nodelane_run_id":"run","nodelane_run_token":"` + token + `"}}}`
}
