package frpauth

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
)

const (
	testRunID    = "run_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	testRouteID  = "rte_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	testRunToken = "nrc_cccccccccccccccccccccccccc.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

type recordingRepository struct {
	authorization domain.RunAuthorization
	err           error
	proofs        []domain.RunProof
}

func (r *recordingRepository) AuthorizeRun(_ context.Context, proof domain.RunProof) (domain.RunAuthorization, error) {
	r.proofs = append(r.proofs, proof)
	return r.authorization, r.err
}

func testAuthorization() domain.RunAuthorization {
	return domain.RunAuthorization{
		Route: domain.Route{
			ID: testRouteID, AccountID: "acct_private", Protocol: "http", Subdomain: "demo", ProxyName: testRouteID,
			Status: domain.RouteActive,
		},
		Run:          domain.Run{ID: testRunID, RouteID: testRouteID, Status: domain.RunStarting, DesiredState: domain.DesiredRunning},
		CredentialID: "nrc_cccccccccccccccccccccccccc",
	}
}

func testMetas() map[string]string {
	return map[string]string{MetadataRunID: testRunID, MetadataRunToken: testRunToken}
}

func newTestAuthorizer(t *testing.T, repository Repository) *Authorizer {
	t.Helper()
	authorizer, err := New(repository, "5MB")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return authorizer
}

func TestEveryRegisteredCallbackReauthorizesItsRunProof(t *testing.T) {
	repository := &recordingRepository{authorization: testAuthorization()}
	authorizer := newTestAuthorizer(t, repository)
	callbacks := []struct {
		name string
		call func() error
	}{
		{"Login", func() error {
			_, err := authorizer.Login(context.Background(), frpplugin.LoginContent{Metas: testMetas()})
			return err
		}},
		{"NewProxy", func() error {
			_, _, err := authorizer.NewProxy(context.Background(), frpplugin.NewProxyContent{
				User: frpplugin.UserInfo{Metas: testMetas()}, ProxyName: testRouteID, ProxyType: "http", Subdomain: "demo",
			})
			return err
		}},
		{"Ping", func() error {
			_, err := authorizer.Ping(context.Background(), frpplugin.PingContent{User: frpplugin.UserInfo{Metas: testMetas()}})
			return err
		}},
		{"NewWorkConn", func() error {
			_, err := authorizer.NewWorkConn(context.Background(), frpplugin.NewWorkConnContent{User: frpplugin.UserInfo{Metas: testMetas()}})
			return err
		}},
		{"NewUserConn", func() error {
			_, err := authorizer.NewUserConn(context.Background(), frpplugin.NewUserConnContent{
				User: frpplugin.UserInfo{Metas: testMetas()}, ProxyName: testRouteID, ProxyType: "http",
			})
			return err
		}},
	}

	for _, callback := range callbacks {
		t.Run(callback.name, func(t *testing.T) {
			before := len(repository.proofs)
			if err := callback.call(); err != nil {
				t.Fatalf("first callback: %v", err)
			}
			if err := callback.call(); err != nil {
				t.Fatalf("second callback: %v", err)
			}
			if got := len(repository.proofs) - before; got != 2 {
				t.Fatalf("AuthorizeRun calls = %d, want 2", got)
			}
			for _, proof := range repository.proofs[before:] {
				if proof.RunID != testRunID || proof.Token != testRunToken {
					t.Fatalf("AuthorizeRun proof = %#v", proof)
				}
			}
		})
	}
}

func TestCredentialMetadataIsAnExactTwoFieldContract(t *testing.T) {
	tests := []struct {
		name  string
		metas map[string]string
	}{
		{"missing run id", map[string]string{MetadataRunToken: testRunToken}},
		{"missing run token", map[string]string{MetadataRunID: testRunID}},
		{"blank run id", map[string]string{MetadataRunID: " ", MetadataRunToken: testRunToken}},
		{"blank run token", map[string]string{MetadataRunID: testRunID, MetadataRunToken: "\t"}},
		{"case variant duplicates run id", map[string]string{MetadataRunID: testRunID, MetadataRunToken: testRunToken, "NODELANE_RUN_ID": testRunID}},
		{"old credential alias", map[string]string{MetadataRunID: testRunID, MetadataRunToken: testRunToken, "tunnel_token": "legacy-secret"}},
		{"account access token", map[string]string{MetadataRunID: testRunID, MetadataRunToken: testRunToken, "access_token": "account-secret"}},
		{"authorization header metadata", map[string]string{MetadataRunID: testRunID, MetadataRunToken: testRunToken, "authorization": "Bearer account-secret"}},
		{"unknown metadata", map[string]string{MetadataRunID: testRunID, MetadataRunToken: testRunToken, "future_auth": "secret"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &recordingRepository{authorization: testAuthorization()}
			authorizer := newTestAuthorizer(t, repository)
			_, err := authorizer.Login(context.Background(), frpplugin.LoginContent{Metas: test.metas})
			if !errors.Is(err, ErrInvalidCredential) {
				t.Fatalf("Login error = %v, want ErrInvalidCredential", err)
			}
			if len(repository.proofs) != 0 {
				t.Fatalf("AuthorizeRun called with rejected metadata: %#v", repository.proofs)
			}
		})
	}
}

func TestRepositoryErrorsHaveStableSecretFreeClasses(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"invalid proof", domain.ErrInvalidRunProof, ErrInvalidCredential},
		{"stopped run", domain.ErrRunStopped, ErrRunStopped},
		{"dependency failure", errors.New("database failure includes " + testRunToken), ErrDependencyUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &recordingRepository{err: test.err}
			_, err := newTestAuthorizer(t, repository).Ping(context.Background(), frpplugin.PingContent{User: frpplugin.UserInfo{Metas: testMetas()}})
			if !errors.Is(err, test.want) {
				t.Fatalf("Ping error = %v, want %v", err, test.want)
			}
			encoded, marshalErr := json.Marshal(err)
			if marshalErr != nil {
				t.Fatalf("marshal error: %v", marshalErr)
			}
			if strings.Contains(err.Error(), testRunToken) || strings.Contains(string(encoded), testRunToken) {
				t.Fatalf("error leaked run token: error=%q json=%s", err, encoded)
			}
		})
	}
}

func TestStoppedRunIsRejectedByEveryRegisteredCallback(t *testing.T) {
	repository := &recordingRepository{err: domain.ErrRunStopped}
	authorizer := newTestAuthorizer(t, repository)
	checks := []struct {
		name string
		call func() error
	}{
		{"Login", func() error {
			_, err := authorizer.Login(context.Background(), frpplugin.LoginContent{Metas: testMetas()})
			return err
		}},
		{"NewProxy", func() error {
			_, _, err := authorizer.NewProxy(context.Background(), frpplugin.NewProxyContent{User: frpplugin.UserInfo{Metas: testMetas()}, ProxyName: testRouteID, ProxyType: "http", Subdomain: "demo"})
			return err
		}},
		{"Ping", func() error {
			_, err := authorizer.Ping(context.Background(), frpplugin.PingContent{User: frpplugin.UserInfo{Metas: testMetas()}})
			return err
		}},
		{"NewWorkConn", func() error {
			_, err := authorizer.NewWorkConn(context.Background(), frpplugin.NewWorkConnContent{User: frpplugin.UserInfo{Metas: testMetas()}})
			return err
		}},
		{"NewUserConn", func() error {
			_, err := authorizer.NewUserConn(context.Background(), frpplugin.NewUserConnContent{User: frpplugin.UserInfo{Metas: testMetas()}, ProxyName: testRouteID, ProxyType: "http"})
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, ErrRunStopped) {
				t.Fatalf("error = %v, want ErrRunStopped", err)
			}
		})
	}
	if len(repository.proofs) != len(checks) {
		t.Fatalf("AuthorizeRun calls = %d, want %d", len(repository.proofs), len(checks))
	}
}

func TestNewProxyUsesOnlyTheAuthorizedHTTPRouteAndServerBandwidth(t *testing.T) {
	repository := &recordingRepository{authorization: testAuthorization()}
	authorizer := newTestAuthorizer(t, repository)
	input := frpplugin.NewProxyContent{
		User:      frpplugin.UserInfo{Metas: testMetas(), RunID: "frps-session"},
		ProxyName: testRouteID, ProxyType: "http", Subdomain: "demo",
		UseEncryption: true, UseCompression: true, BandwidthLimit: "100GB", BandwidthLimitMode: "client",
	}
	output, authorization, err := authorizer.NewProxy(context.Background(), input)
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	if authorization.Route.ID != testRouteID || authorization.Run.ID != testRunID {
		t.Fatalf("authorization = %#v", authorization)
	}
	if output.BandwidthLimit != "5MB" || output.BandwidthLimitMode != "server" {
		t.Fatalf("bandwidth = %q/%q", output.BandwidthLimit, output.BandwidthLimitMode)
	}
	if !output.UseEncryption || !output.UseCompression {
		t.Fatal("transport encryption/compression flags were unexpectedly changed")
	}
	if output.User.Metas != nil {
		t.Fatalf("credential metadata returned in modified content: %#v", output.User.Metas)
	}
	encoded, marshalErr := json.Marshal(output)
	if marshalErr != nil {
		t.Fatalf("marshal output: %v", marshalErr)
	}
	if strings.Contains(string(encoded), testRunToken) {
		t.Fatalf("modified content leaked token: %s", encoded)
	}
}

func TestNewProxyRejectsEveryUnapprovedRouteMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*frpplugin.NewProxyContent)
	}{
		{"frp user namespace", func(c *frpplugin.NewProxyContent) { c.User.User = "account" }},
		{"cross route proxy name", func(c *frpplugin.NewProxyContent) { c.ProxyName = "rte_dddddddddddddddddddddddddd" }},
		{"TCP protocol", func(c *frpplugin.NewProxyContent) { c.ProxyType = "tcp" }},
		{"different subdomain", func(c *frpplugin.NewProxyContent) { c.Subdomain = "other" }},
		{"custom domain", func(c *frpplugin.NewProxyContent) { c.CustomDomains = []string{"other.example"} }},
		{"remote port", func(c *frpplugin.NewProxyContent) { c.RemotePort = 443 }},
		{"load balancing group", func(c *frpplugin.NewProxyContent) { c.Group = "shared" }},
		{"load balancing key", func(c *frpplugin.NewProxyContent) { c.GroupKey = "secret" }},
		{"location routing", func(c *frpplugin.NewProxyContent) { c.Locations = []string{"/admin"} }},
		{"HTTP username", func(c *frpplugin.NewProxyContent) { c.HTTPUser = "user" }},
		{"HTTP password", func(c *frpplugin.NewProxyContent) { c.HTTPPwd = "password" }},
		{"host rewrite", func(c *frpplugin.NewProxyContent) { c.HostHeaderRewrite = "internal" }},
		{"request headers", func(c *frpplugin.NewProxyContent) { c.Headers = map[string]string{"x-added": "yes"} }},
		{"response headers", func(c *frpplugin.NewProxyContent) { c.ResponseHeaders = map[string]string{"x-added": "yes"} }},
		{"route by HTTP user", func(c *frpplugin.NewProxyContent) { c.RouteByHTTPUser = "user" }},
		{"visitor secret", func(c *frpplugin.NewProxyContent) { c.SecretKey = "secret" }},
		{"visitor allowlist", func(c *frpplugin.NewProxyContent) { c.AllowUsers = []string{"user"} }},
		{"TCP multiplexer", func(c *frpplugin.NewProxyContent) { c.Multiplexer = "httpconnect" }},
		{"proxy metadata", func(c *frpplugin.NewProxyContent) { c.Metas = map[string]string{"access_token": "secret"} }},
		{"proxy annotations", func(c *frpplugin.NewProxyContent) { c.Annotations = map[string]string{"route": "other"} }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &recordingRepository{authorization: testAuthorization()}
			authorizer := newTestAuthorizer(t, repository)
			content := frpplugin.NewProxyContent{User: frpplugin.UserInfo{Metas: testMetas()}, ProxyName: testRouteID, ProxyType: "http", Subdomain: "demo"}
			test.mutate(&content)
			output, authorization, err := authorizer.NewProxy(context.Background(), content)
			if !errors.Is(err, ErrInvalidCredential) {
				t.Fatalf("error = %v, want ErrInvalidCredential", err)
			}
			if !reflect.DeepEqual(output, frpplugin.NewProxyContent{}) || authorization != (domain.RunAuthorization{}) {
				t.Fatalf("rejected request returned content/authorization: %#v %#v", output, authorization)
			}
			if len(repository.proofs) != 1 {
				t.Fatalf("AuthorizeRun calls = %d, want 1", len(repository.proofs))
			}
		})
	}
}

func TestNewUserConnMatchesTheFreshlyAuthorizedProxy(t *testing.T) {
	tests := []struct {
		name      string
		proxyName string
		proxyType string
		user      string
		wantErr   bool
	}{
		{"exact route", testRouteID, "http", "", false},
		{"cross route", "rte_dddddddddddddddddddddddddd", "http", "", true},
		{"wrong type", testRouteID, "tcp", "", true},
		{"user namespace", testRouteID, "http", "account", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &recordingRepository{authorization: testAuthorization()}
			authorizer := newTestAuthorizer(t, repository)
			_, err := authorizer.NewUserConn(context.Background(), frpplugin.NewUserConnContent{
				User: frpplugin.UserInfo{User: test.user, Metas: testMetas()}, ProxyName: test.proxyName, ProxyType: test.proxyType,
			})
			if test.wantErr && !errors.Is(err, ErrInvalidCredential) {
				t.Fatalf("error = %v, want ErrInvalidCredential", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("NewUserConn: %v", err)
			}
			if len(repository.proofs) != 1 {
				t.Fatalf("AuthorizeRun calls = %d, want 1", len(repository.proofs))
			}
		})
	}
}

func TestNewRejectsMissingDependenciesAndUnsafeBandwidth(t *testing.T) {
	repository := &recordingRepository{}
	var typedNilRepository *recordingRepository
	for _, test := range []struct {
		name       string
		repository Repository
		bandwidth  string
	}{
		{"nil repository", nil, "5MB"},
		{"typed nil repository", typedNilRepository, "5MB"},
		{"blank bandwidth", repository, ""},
		{"padded bandwidth", repository, " 5MB"},
		{"control character", repository, "5MB\nother"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := New(test.repository, test.bandwidth); got != nil || !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("New = %#v, %v; want nil, ErrInvalidConfiguration", got, err)
			}
		})
	}
}

func TestRepositoryCannotAuthorizeADifferentRunOrRoute(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.RunAuthorization)
	}{
		{"different run", func(a *domain.RunAuthorization) { a.Run.ID = "run_dddddddddddddddddddddddddd" }},
		{"different parent route", func(a *domain.RunAuthorization) { a.Run.RouteID = "rte_dddddddddddddddddddddddddd" }},
		{"unsupported stored protocol", func(a *domain.RunAuthorization) { a.Route.Protocol = "tcp" }},
		{"empty stored proxy name", func(a *domain.RunAuthorization) { a.Route.ProxyName = "" }},
		{"empty stored subdomain", func(a *domain.RunAuthorization) { a.Route.Subdomain = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorization := testAuthorization()
			test.mutate(&authorization)
			repository := &recordingRepository{authorization: authorization}
			_, err := newTestAuthorizer(t, repository).Login(context.Background(), frpplugin.LoginContent{Metas: testMetas()})
			if !errors.Is(err, ErrDependencyUnavailable) {
				t.Fatalf("error = %v, want ErrDependencyUnavailable", err)
			}
		})
	}
}
