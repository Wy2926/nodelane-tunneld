package frpauth

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
	"github.com/fatedier/frp/server/registry"
)

const testSessionID = "fcs_dddddddddddddddddddddddddd"

func sessionUser() frpplugin.UserInfo {
	metas := testMetas()
	metas["nodelane_session_id"] = testSessionID
	return frpplugin.UserInfo{RunID: testSessionID, Metas: metas}
}

func TestEachAuthorizedLoginIssuesANewNativeSessionAndKeepsLogicalClient(t *testing.T) {
	authorizer := newTestAuthorizer(t, &recordingRepository{authorization: testAuthorization()})
	input := frpplugin.LoginContent{Metas: testMetas(), PrivilegeKey: testRunToken, RunID: testSessionID, ClientID: "chosen-by-client"}
	seen := map[string]bool{}
	for range 4 {
		got, _, err := authorizer.Login(context.Background(), input)
		if err != nil || !regexp.MustCompile(`^fcs_[a-z2-7]{26}$`).MatchString(got.RunID) || got.RunID == input.RunID || seen[got.RunID] || got.ClientID != testRunID {
			t.Fatalf("Login did not issue a unique native identity: run=%q client=%q err=%v", got.RunID, got.ClientID, err)
		}
		seen[got.RunID] = true
		if len(got.Metas) != 3 || got.Metas["nodelane_session_id"] != got.RunID || got.Metas[MetadataRunID] != testRunID || got.Metas[MetadataRunToken] != testRunToken || len(input.Metas) != 2 {
			t.Fatal("Login did not bind its own immutable session metadata")
		}
	}
}

func TestRegisteredPostLoginCallbacksRequireExactServerSession(t *testing.T) {
	for _, mutation := range []struct {
		name   string
		mutate func(*frpplugin.UserInfo)
	}{
		{"valid", func(*frpplugin.UserInfo) {}},
		{"legacy metadata", func(u *frpplugin.UserInfo) { delete(u.Metas, "nodelane_session_id") }},
		{"logical native id", func(u *frpplugin.UserInfo) { u.RunID = testRunID; u.Metas["nodelane_session_id"] = testRunID }},
		{"other session", func(u *frpplugin.UserInfo) { u.RunID = "fcs_eeeeeeeeeeeeeeeeeeeeeeeeee" }},
		{"extra metadata", func(u *frpplugin.UserInfo) { u.Metas["future"] = "value" }},
		{"session alias", func(u *frpplugin.UserInfo) {
			delete(u.Metas, "nodelane_session_id")
			u.Metas["NODELANE_SESSION_ID"] = testSessionID
		}},
	} {
		for _, op := range []string{"NewProxy", "Ping", "NewWorkConn", "NewUserConn", "CloseProxy"} {
			t.Run(op+"/"+mutation.name, func(t *testing.T) {
				repository := &recordingRepository{authorization: testAuthorization()}
				authorizer := newTestAuthorizer(t, repository)
				user := sessionUser()
				mutation.mutate(&user)
				var err error
				switch op {
				case "NewProxy":
					_, _, err = authorizer.NewProxy(context.Background(), frpplugin.NewProxyContent{User: user, ProxyName: testRouteID, ProxyType: "http", Subdomain: "demo"})
				case "Ping":
					_, err = authorizer.Ping(context.Background(), frpplugin.PingContent{User: user, PrivilegeKey: testRunToken})
				case "NewWorkConn":
					_, err = authorizer.NewWorkConn(context.Background(), frpplugin.NewWorkConnContent{User: user, RunID: user.RunID, PrivilegeKey: testRunToken})
				case "NewUserConn":
					_, err = authorizer.NewUserConn(context.Background(), frpplugin.NewUserConnContent{User: user, ProxyName: testRouteID, ProxyType: "http"})
				case "CloseProxy":
					_, err = authorizer.CloseProxy(context.Background(), frpplugin.CloseProxyContent{User: user, ProxyName: testRouteID})
				}
				if mutation.name == "valid" && err != nil || mutation.name != "valid" && !errors.Is(err, ErrInvalidCredential) {
					t.Fatalf("session validation error=%v", err)
				}
			})
		}
	}
}

func TestRegisteredNewProxyGrantsOnlyAfterValidatedExposureAndRechecksStore(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		repository := &recordingRepository{authorization: testAuthorization()}
		content := frpplugin.NewProxyContent{User: sessionUser(), ProxyName: testRouteID, ProxyType: "http", Subdomain: "demo"}
		if invalid {
			content.RemotePort = 80
		}
		_, _, err := newTestAuthorizer(t, repository).NewProxy(context.Background(), content)
		if invalid {
			if !errors.Is(err, ErrInvalidCredential) || len(repository.registrations) != 0 {
				t.Fatal("invalid exposure received a registration grant")
			}
		} else if err != nil || len(repository.registrations) != 1 || repository.registrations[0] != (domain.RunProof{RunID: testRunID, Token: testRunToken}) {
			t.Fatalf("valid registration did not persist its exact proof: %v", err)
		}
	}
	repository := &recordingRepository{authorization: testAuthorization(), registrationErr: domain.ErrRunStopped}
	_, _, err := newTestAuthorizer(t, repository).NewProxy(context.Background(), frpplugin.NewProxyContent{User: sessionUser(), ProxyName: testRouteID, ProxyType: "http", Subdomain: "demo"})
	if !errors.Is(err, ErrRunStopped) {
		t.Fatalf("registration-time revocation ignored: %v", err)
	}
}

func TestIssuedSessionsPreventLateStockOfflineWriterFromClobberingReconnect(t *testing.T) {
	authorizer := newTestAuthorizer(t, &recordingRepository{authorization: testAuthorization()})
	login := func() frpplugin.LoginContent {
		got, _, err := authorizer.Login(context.Background(), frpplugin.LoginContent{Metas: testMetas(), PrivilegeKey: testRunToken})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	a, b := login(), login()
	clients := registry.NewClientRegistry()
	_, conflict := clients.Register("", a.ClientID, a.RunID, "host", "0.70.0", "127.0.0.1", "v2")
	if conflict {
		t.Fatal("initial client conflicts")
	}
	if _, conflict := clients.Register("", b.ClientID, b.RunID, "host", "0.70.0", "127.0.0.1", "v2"); !conflict {
		t.Fatal("same logical client re-registered while old control was online")
	}
	clients.MarkOfflineByRunID(a.RunID)
	if _, conflict := clients.Register("", b.ClientID, b.RunID, "host", "0.70.0", "127.0.0.1", "v2"); conflict {
		t.Fatal("offline logical client could not reconnect")
	}
	clients.MarkOfflineByRunID(a.RunID)
	got, exists := clients.GetByKey(testRunID)
	if !exists || !got.Online || got.RunID != b.RunID {
		t.Fatal("late old-session offline writer clobbered new connection")
	}
}
