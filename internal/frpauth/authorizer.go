// Package frpauth authorizes registered frp callbacks against the current
// PostgreSQL run state. It deliberately holds no authorization cache.
package frpauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"reflect"
	"strings"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
	configtypes "github.com/fatedier/frp/pkg/config/types"
)

const (
	MetadataRunID    = frpplugin.MetadataRunID
	MetadataRunToken = frpplugin.MetadataRunToken
)

var (
	ErrInvalidConfiguration  = errors.New("invalid registered frp authorization configuration")
	ErrInvalidCredential     = errors.New("invalid registered run credential")
	ErrRunStopped            = errors.New("registered run is stopped")
	ErrDependencyUnavailable = errors.New("registered run authorization is unavailable")
)

type Repository interface {
	AuthorizeRun(context.Context, domain.RunProof) (domain.RunAuthorization, error)
	AuthorizeProxyRegistration(context.Context, domain.RunProof) (domain.RunAuthorization, error)
}

type Authorizer struct {
	repository     Repository
	bandwidthLimit string
}

func New(repository Repository, bandwidthLimit string) (*Authorizer, error) {
	if nilRepository(repository) || bandwidthLimit == "" || strings.TrimSpace(bandwidthLimit) != bandwidthLimit ||
		len(bandwidthLimit) > 64 || strings.ContainsAny(bandwidthLimit, "\x00\r\n") {
		return nil, ErrInvalidConfiguration
	}
	quantity, err := configtypes.NewBandwidthQuantity(bandwidthLimit)
	if err != nil || quantity.Bytes() <= 0 {
		return nil, ErrInvalidConfiguration
	}
	return &Authorizer{repository: repository, bandwidthLimit: bandwidthLimit}, nil
}

func nilRepository(repository Repository) bool {
	if repository == nil {
		return true
	}
	value := reflect.ValueOf(repository)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (a *Authorizer) Login(ctx context.Context, content frpplugin.LoginContent) (frpplugin.LoginContent, domain.RunAuthorization, error) {
	if len(content.Metas) != 2 || !directProofMatches(content.PrivilegeKey, content.Metas) {
		return frpplugin.LoginContent{}, domain.RunAuthorization{}, ErrInvalidCredential
	}
	authorization, err := a.authorize(ctx, content.Metas)
	if err != nil {
		return frpplugin.LoginContent{}, domain.RunAuthorization{}, err
	}
	if content.User != "" {
		return frpplugin.LoginContent{}, domain.RunAuthorization{}, ErrInvalidCredential
	}
	content, err = frpplugin.BindSession(content, authorization.Run.ID)
	if err != nil {
		return frpplugin.LoginContent{}, domain.RunAuthorization{}, ErrDependencyUnavailable
	}
	return content, authorization, nil
}

func (a *Authorizer) NewProxy(ctx context.Context, content frpplugin.NewProxyContent) (frpplugin.NewProxyContent, domain.RunAuthorization, error) {
	if !frpplugin.ValidSessionUser(content.User) {
		return frpplugin.NewProxyContent{}, domain.RunAuthorization{}, ErrInvalidCredential
	}
	authorization, err := a.authorize(ctx, content.User.Metas)
	if err != nil {
		return frpplugin.NewProxyContent{}, domain.RunAuthorization{}, err
	}
	if !validNewProxy(content, authorization.Route) {
		return frpplugin.NewProxyContent{}, domain.RunAuthorization{}, ErrInvalidCredential
	}
	authorization, err = a.authorizeUsing(ctx, content.User.Metas, a.repository.AuthorizeProxyRegistration)
	if err != nil {
		return frpplugin.NewProxyContent{}, domain.RunAuthorization{}, err
	}
	if !validNewProxy(content, authorization.Route) {
		return frpplugin.NewProxyContent{}, domain.RunAuthorization{}, ErrInvalidCredential
	}

	content.BandwidthLimit = a.bandwidthLimit
	content.BandwidthLimitMode = "server"
	content.User.Metas = nil
	return content, authorization, nil
}

func (a *Authorizer) Ping(ctx context.Context, content frpplugin.PingContent) (domain.RunAuthorization, error) {
	if !frpplugin.ValidSessionUser(content.User) || !directProofMatches(content.PrivilegeKey, content.User.Metas) {
		return domain.RunAuthorization{}, ErrInvalidCredential
	}
	authorization, err := a.authorize(ctx, content.User.Metas)
	if err != nil {
		return domain.RunAuthorization{}, err
	}
	return authorization, nil
}

func (a *Authorizer) NewWorkConn(ctx context.Context, content frpplugin.NewWorkConnContent) (domain.RunAuthorization, error) {
	if !frpplugin.ValidSessionUser(content.User) || content.RunID != content.User.RunID || !directProofMatches(content.PrivilegeKey, content.User.Metas) {
		return domain.RunAuthorization{}, ErrInvalidCredential
	}
	authorization, err := a.authorize(ctx, content.User.Metas)
	if err != nil {
		return domain.RunAuthorization{}, err
	}
	return authorization, nil
}

// NewWorkConn inherits User.Metas from frps's existing session. Only its own
// PrivilegeKey demonstrates that the arriving connection possesses the secret.
func directProofMatches(token string, metas map[string]string) bool {
	proof, ok := runProof(metas)
	return ok && token != "" && len(token) == len(proof.Token) && subtle.ConstantTimeCompare([]byte(token), []byte(proof.Token)) == 1
}

func (a *Authorizer) NewUserConn(ctx context.Context, content frpplugin.NewUserConnContent) (domain.RunAuthorization, error) {
	if !frpplugin.ValidSessionUser(content.User) {
		return domain.RunAuthorization{}, ErrInvalidCredential
	}
	authorization, err := a.authorize(ctx, content.User.Metas)
	if err != nil {
		return domain.RunAuthorization{}, err
	}
	if content.User.User != "" || content.ProxyName != authorization.Route.ProxyName || content.ProxyType != authorization.Route.Protocol {
		return domain.RunAuthorization{}, ErrInvalidCredential
	}
	return authorization, nil
}

func (a *Authorizer) CloseProxy(ctx context.Context, content frpplugin.CloseProxyContent) (domain.RunAuthorization, error) {
	if !frpplugin.ValidSessionUser(content.User) {
		return domain.RunAuthorization{}, ErrInvalidCredential
	}
	authorization, err := a.authorize(ctx, content.User.Metas)
	if err != nil {
		return domain.RunAuthorization{}, err
	}
	if content.ProxyName != authorization.Route.ProxyName {
		return domain.RunAuthorization{}, ErrInvalidCredential
	}
	return authorization, nil
}

func (a *Authorizer) authorize(ctx context.Context, metas map[string]string) (domain.RunAuthorization, error) {
	return a.authorizeUsing(ctx, metas, a.repository.AuthorizeRun)
}

func (a *Authorizer) authorizeUsing(ctx context.Context, metas map[string]string, lookup func(context.Context, domain.RunProof) (domain.RunAuthorization, error)) (domain.RunAuthorization, error) {
	proof, ok := runProof(metas)
	if !ok {
		return domain.RunAuthorization{}, ErrInvalidCredential
	}
	authorization, err := lookup(ctx, proof)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidRunProof):
			return domain.RunAuthorization{}, ErrInvalidCredential
		case errors.Is(err, domain.ErrRunStopped):
			return domain.RunAuthorization{}, ErrRunStopped
		default:
			return domain.RunAuthorization{}, ErrDependencyUnavailable
		}
	}
	if !validAuthorization(proof, authorization) {
		return domain.RunAuthorization{}, ErrDependencyUnavailable
	}
	return authorization, nil
}

func runProof(metas map[string]string) (domain.RunProof, bool) {
	if len(metas) != 2 && (len(metas) != 3 || !frpplugin.ValidSessionID(metas[frpplugin.MetadataSessionID])) {
		return domain.RunProof{}, false
	}
	runID, hasRunID := metas[MetadataRunID]
	token, hasToken := metas[MetadataRunToken]
	if !hasRunID || !hasToken || runID == "" || token == "" ||
		strings.TrimSpace(runID) != runID || strings.TrimSpace(token) != token ||
		len(runID) > 256 || len(token) > 4096 || strings.ContainsAny(runID, "\x00\r\n") || strings.ContainsAny(token, "\x00\r\n") {
		return domain.RunProof{}, false
	}
	return domain.RunProof{RunID: runID, Token: token}, true
}

func validAuthorization(proof domain.RunProof, authorization domain.RunAuthorization) bool {
	return authorization.Run.ID == proof.RunID &&
		authorization.Run.RouteID != "" && authorization.Run.RouteID == authorization.Route.ID &&
		authorization.Route.Protocol == "http" &&
		authorization.Route.ProxyName != "" && authorization.Route.ProxyName == authorization.Route.ID &&
		authorization.Route.Subdomain != "" && authorization.CredentialID != ""
}

func validNewProxy(content frpplugin.NewProxyContent, route domain.Route) bool {
	return content.User.User == "" &&
		content.ProxyName == route.ProxyName && content.ProxyType == route.Protocol && content.Subdomain == route.Subdomain &&
		content.RemotePort == 0 && len(content.CustomDomains) == 0 && len(content.Locations) == 0 &&
		content.Group == "" && content.GroupKey == "" && content.HTTPUser == "" && content.HTTPPwd == "" &&
		content.HostHeaderRewrite == "" && len(content.Headers) == 0 && len(content.ResponseHeaders) == 0 &&
		content.RouteByHTTPUser == "" && content.SecretKey == "" && len(content.AllowUsers) == 0 &&
		content.Multiplexer == "" && len(content.Metas) == 0 && len(content.Annotations) == 0
}
