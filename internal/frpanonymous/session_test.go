package frpanonymous

import (
	"context"
	"regexp"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
)

const testSessionID = "fcs_dddddddddddddddddddddddddd"

func nativeSessionUser() frpplugin.UserInfo {
	metas := testMetas()
	metas["nodelane_session_id"] = testSessionID
	return frpplugin.UserInfo{RunID: testSessionID, Metas: metas}
}

func TestAnonymousLoginIssuesFreshNativeSessionWithStableLogicalClient(t *testing.T) {
	dispatcher := mustDispatcher(t, &recordingStore{run: testRun(anonymous.ProtocolHTTP)})
	seen := map[string]bool{}
	for range 4 {
		input := frpplugin.LoginContent{Metas: testMetas(), PrivilegeKey: testRunToken, RunID: testSessionID, ClientID: "client-chosen"}
		response, err := dispatcher.Dispatch(context.Background(), request(t, frpplugin.OpLogin, input))
		got, ok := response.Content.(frpplugin.LoginContent)
		if err != nil || response.Reject || !ok || !regexp.MustCompile(`^fcs_[a-z2-7]{26}$`).MatchString(got.RunID) || got.RunID == input.RunID || seen[got.RunID] || got.ClientID != testRunID {
			t.Fatalf("Login did not issue a unique native session: %+v %v", response, err)
		}
		seen[got.RunID] = true
		if len(got.Metas) != 3 || got.Metas["nodelane_session_id"] != got.RunID || got.Metas[frpplugin.MetadataRunID] != testRunID || got.Metas[frpplugin.MetadataRunToken] != testRunToken || len(input.Metas) != 2 {
			t.Fatal("Login failed to bind immutable session metadata")
		}
	}
}

func TestAnonymousPostLoginCallbacksRequireExactServerSession(t *testing.T) {
	for _, mutation := range []struct {
		name   string
		mutate func(*frpplugin.UserInfo)
	}{
		{"valid", func(*frpplugin.UserInfo) {}},
		{"legacy", func(u *frpplugin.UserInfo) { delete(u.Metas, "nodelane_session_id") }},
		{"logical native id", func(u *frpplugin.UserInfo) { u.RunID = testRunID; u.Metas["nodelane_session_id"] = testRunID }},
		{"other native id", func(u *frpplugin.UserInfo) { u.RunID = "fcs_eeeeeeeeeeeeeeeeeeeeeeeeee" }},
		{"extra metadata", func(u *frpplugin.UserInfo) { u.Metas["future"] = "value" }},
	} {
		for _, op := range []frpplugin.Operation{frpplugin.OpNewProxy, frpplugin.OpPing, frpplugin.OpNewWorkConn, frpplugin.OpNewUserConn, frpplugin.OpCloseProxy} {
			t.Run(string(op)+"/"+mutation.name, func(t *testing.T) {
				user := nativeSessionUser()
				mutation.mutate(&user)
				var content any
				switch op {
				case frpplugin.OpNewProxy:
					proxy := validHTTPProxy()
					proxy.User = user
					content = proxy
				case frpplugin.OpPing:
					content = frpplugin.PingContent{User: user, PrivilegeKey: testRunToken}
				case frpplugin.OpNewWorkConn:
					content = frpplugin.NewWorkConnContent{User: user, RunID: user.RunID, PrivilegeKey: testRunToken}
				case frpplugin.OpNewUserConn:
					content = frpplugin.NewUserConnContent{User: user, ProxyName: testProxy, ProxyType: "http"}
				case frpplugin.OpCloseProxy:
					content = frpplugin.CloseProxyContent{User: user, ProxyName: testProxy}
				}
				response, err := mustDispatcher(t, &recordingStore{run: testRun(anonymous.ProtocolHTTP)}).Dispatch(context.Background(), request(t, op, content))
				if err != nil || response.Reject != (mutation.name != "valid") {
					t.Fatalf("session check: %+v %v", response, err)
				}
			})
		}
	}
}

func TestAnonymousNewProxyPersistsGrantOnlyAfterValidExposure(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		store := &recordingStore{run: testRun(anonymous.ProtocolHTTP)}
		content := validHTTPProxy()
		content.User = nativeSessionUser()
		if invalid {
			content.RemotePort = 80
		}
		response, err := mustDispatcher(t, store).Dispatch(context.Background(), request(t, frpplugin.OpNewProxy, content))
		if invalid {
			if err != nil || !response.Reject || len(store.registrations) != 0 {
				t.Fatal("invalid exposure granted registration")
			}
		} else if err != nil || response.Reject || len(store.registrations) != 1 || store.registrations[0] != (proof{runID: testRunID, token: testRunToken, proxyName: testProxy}) {
			t.Fatalf("valid exposure did not persist exact registration proof: %+v %v", response, err)
		}
	}
	store := &recordingStore{run: testRun(anonymous.ProtocolHTTP), registrationErr: anonymous.ErrRunStopped}
	content := validHTTPProxy()
	content.User = nativeSessionUser()
	response, err := mustDispatcher(t, store).Dispatch(context.Background(), request(t, frpplugin.OpNewProxy, content))
	if err != nil || !response.Reject || response.RejectReason != RunStoppedReason {
		t.Fatal("registration-time stop ignored")
	}
}
