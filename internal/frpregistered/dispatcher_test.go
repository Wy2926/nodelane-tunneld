package frpregistered

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/frpauth"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
	"github.com/fatedier/frp/pkg/auth"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/msg"
)

func TestAuthorizedConnectionProofsBecomeStockCompatibleWithoutChangingTimestamp(t *testing.T) {
	verifier := auth.NewTokenAuth([]v1.AuthScope{v1.AuthScopeHeartBeats, v1.AuthScopeNewWorkConns}, "")
	for _, timestamp := range []int64{0, 1788652800} {
		for _, operation := range []frpplugin.Operation{frpplugin.OpLogin, frpplugin.OpPing, frpplugin.OpNewWorkConn} {
			authorizer := &recordingAuthorizer{login: frpplugin.LoginContent{PrivilegeKey: "direct-run-proof", Timestamp: timestamp}}
			content, err := json.Marshal(map[string]any{"privilege_key": "direct-run-proof", "timestamp": timestamp})
			if err != nil {
				t.Fatal(err)
			}
			response, err := mustDispatcher(t, authorizer).Dispatch(context.Background(), frpplugin.Request{Op: operation, Content: content})
			if err != nil || response.Reject || response.Unchange || response.Content == nil {
				t.Fatalf("authorized %s proof was not converted: %+v %v", operation, response, err)
			}
			encoded, err := json.Marshal(response.Content)
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case frpplugin.OpLogin:
				var got msg.Login
				if json.Unmarshal(encoded, &got) != nil || got.Timestamp != timestamp || verifier.VerifyLogin(&got) != nil {
					t.Fatal("Login proof is incompatible with stock verifier")
				}
			case frpplugin.OpPing:
				var got msg.Ping
				if json.Unmarshal(encoded, &got) != nil || got.Timestamp != timestamp || verifier.VerifyPing(&got) != nil {
					t.Fatal("Ping proof is incompatible with stock verifier")
				}
			case frpplugin.OpNewWorkConn:
				var got msg.NewWorkConn
				if json.Unmarshal(encoded, &got) != nil || got.Timestamp != timestamp || verifier.VerifyNewWorkConn(&got) != nil {
					t.Fatal("NewWorkConn proof is incompatible with stock verifier")
				}
			}
		}
	}
}

type recordingAuthorizer struct {
	err      error
	calls    []frpplugin.Operation
	login    frpplugin.LoginContent
	modified frpplugin.NewProxyContent
}

func (a *recordingAuthorizer) Login(context.Context, frpplugin.LoginContent) (frpplugin.LoginContent, domain.RunAuthorization, error) {
	a.calls = append(a.calls, frpplugin.OpLogin)
	return a.login, domain.RunAuthorization{}, a.err
}

func (a *recordingAuthorizer) NewProxy(context.Context, frpplugin.NewProxyContent) (frpplugin.NewProxyContent, domain.RunAuthorization, error) {
	a.calls = append(a.calls, frpplugin.OpNewProxy)
	return a.modified, domain.RunAuthorization{}, a.err
}

func (a *recordingAuthorizer) Ping(context.Context, frpplugin.PingContent) (domain.RunAuthorization, error) {
	a.calls = append(a.calls, frpplugin.OpPing)
	return domain.RunAuthorization{}, a.err
}

func (a *recordingAuthorizer) NewWorkConn(context.Context, frpplugin.NewWorkConnContent) (domain.RunAuthorization, error) {
	a.calls = append(a.calls, frpplugin.OpNewWorkConn)
	return domain.RunAuthorization{}, a.err
}

func (a *recordingAuthorizer) NewUserConn(context.Context, frpplugin.NewUserConnContent) (domain.RunAuthorization, error) {
	a.calls = append(a.calls, frpplugin.OpNewUserConn)
	return domain.RunAuthorization{}, a.err
}

func (a *recordingAuthorizer) CloseProxy(context.Context, frpplugin.CloseProxyContent) (domain.RunAuthorization, error) {
	a.calls = append(a.calls, frpplugin.OpCloseProxy)
	return domain.RunAuthorization{}, a.err
}

func TestDispatcherInvokesRegisteredAuthorizerForEveryAuthorizationCallback(t *testing.T) {
	operations := []struct {
		op      frpplugin.Operation
		content string
	}{
		{frpplugin.OpPing, `{"user":{"metas":{}}}`},
		{frpplugin.OpNewWorkConn, `{"user":{"metas":{}}}`},
		{frpplugin.OpNewUserConn, `{"user":{"metas":{}},"proxy_name":"rte_test","proxy_type":"http","remote_addr":"192.0.2.1:1234"}`},
	}
	for _, test := range operations {
		t.Run(string(test.op), func(t *testing.T) {
			authorizer := &recordingAuthorizer{}
			dispatcher := mustDispatcher(t, authorizer)
			response, err := dispatcher.Dispatch(context.Background(), frpplugin.Request{Op: test.op, Content: json.RawMessage(test.content)})
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			changed := test.op == frpplugin.OpPing || test.op == frpplugin.OpNewWorkConn
			if response.Reject || response.Unchange == changed || (response.Content != nil) != changed {
				t.Fatalf("response = %#v, unexpected native proof transformation", response)
			}
			if len(authorizer.calls) != 1 || authorizer.calls[0] != test.op {
				t.Fatalf("calls = %#v, want %s", authorizer.calls, test.op)
			}
		})
	}
}

func TestDispatcherReturnsNormalizedChangedLogin(t *testing.T) {
	modified := frpplugin.LoginContent{RunID: "run_business", ClientID: "run_business", Metas: map[string]string{"nodelane_run_token": "secret"}}
	authorizer := &recordingAuthorizer{login: modified}
	dispatcher := mustDispatcher(t, authorizer)
	response, err := dispatcher.Dispatch(context.Background(), frpplugin.Request{Op: frpplugin.OpLogin, Content: json.RawMessage(`{"metas":{}}`)})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if response.Reject || response.Unchange {
		t.Fatalf("response = %#v, want allowed changed", response)
	}
	got, ok := response.Content.(frpplugin.LoginContent)
	if !ok || got.PrivilegeKey == "" {
		t.Fatal("normalized Login has no stock native proof")
	}
	got.PrivilegeKey = modified.PrivilegeKey
	if !reflect.DeepEqual(got, modified) {
		t.Fatalf("response content = %#v, want %#v", response.Content, modified)
	}
}

func TestDispatcherReturnsSanitizedChangedNewProxy(t *testing.T) {
	modified := frpplugin.NewProxyContent{ProxyName: "rte_test", ProxyType: "http", Subdomain: "demo", BandwidthLimit: "5MB", BandwidthLimitMode: "server"}
	authorizer := &recordingAuthorizer{modified: modified}
	dispatcher := mustDispatcher(t, authorizer)
	response, err := dispatcher.Dispatch(context.Background(), frpplugin.Request{Op: frpplugin.OpNewProxy, Content: json.RawMessage(`{"user":{"metas":{}}}`)})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if response.Reject || response.Unchange {
		t.Fatalf("response = %#v, want allowed changed", response)
	}
	got, ok := response.Content.(frpplugin.NewProxyContent)
	if !ok || !reflect.DeepEqual(got, modified) {
		t.Fatalf("response content = %#v, want %#v", response.Content, modified)
	}
}

func TestDispatcherMapsRegisteredErrorsWithoutLeakingDetails(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason string
		wantError  bool
	}{
		{name: "invalid credential", err: frpauth.ErrInvalidCredential, wantReason: InvalidCredentialReason},
		{name: "stopped", err: frpauth.ErrRunStopped, wantReason: RunStoppedReason},
		{name: "dependency", err: frpauth.ErrDependencyUnavailable, wantError: true},
		{name: "unexpected", err: errors.New("secret internal failure"), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := mustDispatcher(t, &recordingAuthorizer{err: test.err})
			response, err := dispatcher.Dispatch(context.Background(), frpplugin.Request{Op: frpplugin.OpPing, Content: json.RawMessage(`{"user":{"metas":{}}}`)})
			if test.wantError {
				if !errors.Is(err, ErrAuthorizationUnavailable) || !response.Reject || response.RejectReason != UnavailableReason {
					t.Fatalf("Dispatch = (%#v, %v), want unavailable", response, err)
				}
				if err.Error() != ErrAuthorizationUnavailable.Error() {
					t.Fatalf("error leaked cause: %q", err)
				}
				return
			}
			if err != nil || !response.Reject || response.RejectReason != test.wantReason || !response.Unchange || response.Content != nil {
				t.Fatalf("Dispatch = (%#v, %v), want rejection %q", response, err, test.wantReason)
			}
		})
	}
}

func TestDispatcherRejectsMalformedContentBeforeAuthorization(t *testing.T) {
	authorizer := &recordingAuthorizer{}
	dispatcher := mustDispatcher(t, authorizer)
	response, err := dispatcher.Dispatch(context.Background(), frpplugin.Request{Op: frpplugin.OpLogin, Content: json.RawMessage(`{"unknown":"secret"}`)})
	if err != nil || !response.Reject || response.RejectReason != InvalidRequestReason {
		t.Fatalf("Dispatch = (%#v, %v), want invalid request rejection", response, err)
	}
	if len(authorizer.calls) != 0 {
		t.Fatalf("authorizer calls = %#v, want none", authorizer.calls)
	}
}

func TestDispatcherTreatsCloseProxyAsAuthorizedNotificationOnly(t *testing.T) {
	authorizer := &recordingAuthorizer{}
	dispatcher := mustDispatcher(t, authorizer)
	response, err := dispatcher.Dispatch(context.Background(), frpplugin.Request{Op: frpplugin.OpCloseProxy, Content: json.RawMessage(`{"user":{"metas":{"nodelane_run_token":"secret"}},"proxy_name":"rte_test"}`)})
	if err != nil || response.Reject || !response.Unchange {
		t.Fatalf("Dispatch = (%#v, %v), want ignored notification", response, err)
	}
	if len(authorizer.calls) != 1 || authorizer.calls[0] != frpplugin.OpCloseProxy {
		t.Fatalf("CloseProxy bypassed authorization: %#v", authorizer.calls)
	}
}

func TestDispatcherRejectsUnauthorizedCloseNotification(t *testing.T) {
	dispatcher := mustDispatcher(t, &recordingAuthorizer{err: frpauth.ErrInvalidCredential})
	response, err := dispatcher.Dispatch(context.Background(), frpplugin.Request{Op: frpplugin.OpCloseProxy, Content: json.RawMessage(`{"user":{"metas":{}},"proxy_name":"rte_test"}`)})
	if err != nil || !response.Reject || response.RejectReason != InvalidCredentialReason {
		t.Fatal("CloseProxy ignored invalid session")
	}
}

func TestNewRejectsNilAuthorizer(t *testing.T) {
	if dispatcher, err := New(nil); dispatcher != nil || !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("New(nil) = (%v, %v), want nil and ErrInvalidConfiguration", dispatcher, err)
	}
	var typedNil *recordingAuthorizer
	if dispatcher, err := New(typedNil); dispatcher != nil || !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("New(typed nil) = (%v, %v), want nil and ErrInvalidConfiguration", dispatcher, err)
	}
}

func mustDispatcher(t *testing.T, authorizer Authorizer) *Dispatcher {
	t.Helper()
	dispatcher, err := New(authorizer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return dispatcher
}
