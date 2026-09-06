// Package frpanonymous authorizes anonymous frp callbacks against the
// short-lived Redis run state. It deliberately keeps no authorization cache.
package frpanonymous

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"reflect"
	"strconv"
	"strings"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
	configtypes "github.com/fatedier/frp/pkg/config/types"
)

const (
	InvalidRequestReason    = "invalid plugin request"
	InvalidCredentialReason = "invalid run credential"
	RunStoppedReason        = "run stopped"
	UnavailableReason       = "authorization unavailable"
)

var (
	ErrInvalidConfiguration     = errors.New("invalid anonymous frp dispatcher configuration")
	ErrAuthorizationUnavailable = errors.New("anonymous frp authorization is unavailable")
)

type Store interface {
	AuthorizeLogin(context.Context, string, string) (anonymous.Run, error)
	Authorize(context.Context, string, string, string) (anonymous.Run, error)
}

type Dispatcher struct {
	store          Store
	bandwidthLimit string
}

var _ Store = (*anonymous.Store)(nil)

func New(store Store, bandwidthLimit string) (*Dispatcher, error) {
	if nilStore(store) || bandwidthLimit == "" || strings.TrimSpace(bandwidthLimit) != bandwidthLimit ||
		len(bandwidthLimit) > 64 || strings.ContainsAny(bandwidthLimit, "\x00\r\n") {
		return nil, ErrInvalidConfiguration
	}
	quantity, err := configtypes.NewBandwidthQuantity(bandwidthLimit)
	if err != nil || quantity.Bytes() <= 0 {
		return nil, ErrInvalidConfiguration
	}
	return &Dispatcher{store: store, bandwidthLimit: bandwidthLimit}, nil
}

func (d *Dispatcher) Dispatch(ctx context.Context, request frpplugin.Request) (frpplugin.Response, error) {
	if d == nil || nilStore(d.store) {
		return unavailable()
	}

	switch request.Op {
	case frpplugin.OpLogin:
		var content frpplugin.LoginContent
		if request.DecodeContent(&content) != nil {
			return rejected(InvalidRequestReason), nil
		}
		run, response, err := d.authorizeLogin(ctx, content.Metas)
		if response.Reject || err != nil {
			return response, err
		}
		if content.User != "" {
			return rejected(InvalidCredentialReason), nil
		}
		content.RunID = run.RunID
		content.ClientID = run.RunID
		// frps derives User.Metas for every later callback from Login.Metas, so
		// these credentials must remain in its session state.
		return frpplugin.Response{Unchange: false, Content: content}, nil

	case frpplugin.OpNewProxy:
		var content frpplugin.NewProxyContent
		if request.DecodeContent(&content) != nil {
			return rejected(InvalidRequestReason), nil
		}
		run, response, err := d.authorizeProxy(ctx, content.User.Metas, content.ProxyName)
		if response.Reject || err != nil {
			return response, err
		}
		if !validNewProxy(content, run) {
			return rejected(InvalidCredentialReason), nil
		}
		content.BandwidthLimit = d.bandwidthLimit
		content.BandwidthLimitMode = "server"
		content.User.Metas = nil
		return frpplugin.Response{Unchange: false, Content: content}, nil

	case frpplugin.OpPing:
		var content frpplugin.PingContent
		if request.DecodeContent(&content) != nil {
			return rejected(InvalidRequestReason), nil
		}
		run, response, err := d.authorizeLogin(ctx, content.User.Metas)
		if response.Reject || err != nil {
			return response, err
		}
		if !validUser(content.User, run) {
			return rejected(InvalidCredentialReason), nil
		}
		return unchanged(), nil

	case frpplugin.OpNewWorkConn:
		var content frpplugin.NewWorkConnContent
		if request.DecodeContent(&content) != nil {
			return rejected(InvalidRequestReason), nil
		}
		run, response, err := d.authorizeLogin(ctx, content.User.Metas)
		if response.Reject || err != nil {
			return response, err
		}
		if !validUser(content.User, run) || content.RunID != run.RunID {
			return rejected(InvalidCredentialReason), nil
		}
		return unchanged(), nil

	case frpplugin.OpNewUserConn:
		var content frpplugin.NewUserConnContent
		if request.DecodeContent(&content) != nil {
			return rejected(InvalidRequestReason), nil
		}
		run, response, err := d.authorizeProxy(ctx, content.User.Metas, content.ProxyName)
		if response.Reject || err != nil {
			return response, err
		}
		if !validUser(content.User, run) || content.ProxyName != run.ProxyName || content.ProxyType != string(run.Protocol) {
			return rejected(InvalidCredentialReason), nil
		}
		return unchanged(), nil

	case frpplugin.OpCloseProxy:
		var content frpplugin.CloseProxyContent
		if request.DecodeContent(&content) != nil {
			return rejected(InvalidRequestReason), nil
		}
		run, response, err := d.authorizeProxy(ctx, content.User.Metas, content.ProxyName)
		if response.Reject || err != nil {
			return response, err
		}
		if !validUser(content.User, run) || content.ProxyName != run.ProxyName {
			return rejected(InvalidCredentialReason), nil
		}
		// CloseProxy is only an authenticated notification. Native frps state
		// verification, not this callback, confirms resource release.
		return unchanged(), nil

	default:
		return rejected(InvalidRequestReason), nil
	}
}

func (d *Dispatcher) authorizeLogin(ctx context.Context, metas map[string]string) (anonymous.Run, frpplugin.Response, error) {
	runID, token, ok := runProof(metas)
	if !ok {
		return anonymous.Run{}, rejected(InvalidCredentialReason), nil
	}
	run, err := d.store.AuthorizeLogin(ctx, runID, token)
	if err != nil {
		response, mapped := authorizationError(err)
		return anonymous.Run{}, response, mapped
	}
	if !validRun(runID, run) {
		response, mapped := unavailable()
		return anonymous.Run{}, response, mapped
	}
	return run, frpplugin.Response{}, nil
}

func (d *Dispatcher) authorizeProxy(ctx context.Context, metas map[string]string, proxyName string) (anonymous.Run, frpplugin.Response, error) {
	runID, token, ok := runProof(metas)
	if !ok || !validIdentifier(proxyName, "anon_") {
		return anonymous.Run{}, rejected(InvalidCredentialReason), nil
	}
	run, err := d.store.Authorize(ctx, runID, token, proxyName)
	if err != nil {
		response, mapped := authorizationError(err)
		return anonymous.Run{}, response, mapped
	}
	if !validRun(runID, run) {
		response, mapped := unavailable()
		return anonymous.Run{}, response, mapped
	}
	if run.ProxyName != proxyName {
		return anonymous.Run{}, rejected(InvalidCredentialReason), nil
	}
	return run, frpplugin.Response{}, nil
}

func authorizationError(err error) (frpplugin.Response, error) {
	switch {
	case errors.Is(err, anonymous.ErrInvalidCredential), errors.Is(err, anonymous.ErrRunNotFound), errors.Is(err, anonymous.ErrInvalidRequest):
		return rejected(InvalidCredentialReason), nil
	case errors.Is(err, anonymous.ErrRunExpired), errors.Is(err, anonymous.ErrRunStopped):
		return rejected(RunStoppedReason), nil
	default:
		return unavailable()
	}
}

func runProof(metas map[string]string) (string, string, bool) {
	if len(metas) != 2 {
		return "", "", false
	}
	runID, hasRunID := metas[frpplugin.MetadataRunID]
	token, hasToken := metas[frpplugin.MetadataRunToken]
	if !hasRunID || !hasToken || !validIdentifier(runID, "anr_") || !validCredential(token) {
		return "", "", false
	}
	return runID, token, true
}

func validCredential(token string) bool {
	credentialID, secret, found := strings.Cut(token, ".")
	if !found || strings.Contains(secret, ".") || !validIdentifier(credentialID, "nac_") || len(secret) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == secret
}

func validRun(runID string, run anonymous.Run) bool {
	if run.RunID != runID || !validIdentifier(run.RunID, "anr_") || !validIdentifier(run.ProxyName, "anon_") ||
		run.State != anonymous.StateReserved && run.State != anonymous.StateOnline || run.DesiredState != anonymous.DesiredRunning {
		return false
	}
	_, _, ok := publicAllocation(run)
	return ok
}

func validUser(user frpplugin.UserInfo, run anonymous.Run) bool {
	return user.User == "" && user.RunID == run.RunID
}

func validNewProxy(content frpplugin.NewProxyContent, run anonymous.Run) bool {
	subdomain, remotePort, valid := publicAllocation(run)
	if !valid || !validUser(content.User, run) || content.ProxyName != run.ProxyName || content.ProxyType != string(run.Protocol) ||
		content.Subdomain != subdomain || content.RemotePort != remotePort || len(content.CustomDomains) != 0 || len(content.Locations) != 0 ||
		content.Group != "" || content.GroupKey != "" || content.HTTPUser != "" || content.HTTPPwd != "" ||
		content.HostHeaderRewrite != "" || len(content.Headers) != 0 || len(content.ResponseHeaders) != 0 ||
		content.RouteByHTTPUser != "" || content.SecretKey != "" || len(content.AllowUsers) != 0 ||
		content.Multiplexer != "" || len(content.Metas) != 0 || len(content.Annotations) != 0 {
		return false
	}
	return true
}

func publicAllocation(run anonymous.Run) (subdomain string, remotePort int, ok bool) {
	switch run.Protocol {
	case anonymous.ProtocolHTTP:
		label, domain, found := strings.Cut(run.PublicEndpoint, ".")
		if !found || !validIdentifier(label, "anon-") || !validDomain(domain) || !validDomain(run.PublicEndpoint) {
			return "", 0, false
		}
		return label, 0, true
	case anonymous.ProtocolTCP, anonymous.ProtocolUDP:
		host, portText, err := net.SplitHostPort(run.PublicEndpoint)
		if err != nil || !validDomain(host) {
			return "", 0, false
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 || portText != strconv.Itoa(port) || run.PublicEndpoint != host+":"+portText {
			return "", 0, false
		}
		return "", port, true
	default:
		return "", 0, false
	}
}

func validIdentifier(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+26 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if !(character >= 'a' && character <= 'z' || character >= '2' && character <= '7') {
			return false
		}
	}
	return true
}

func validDomain(value string) bool {
	if value == "" || len(value) > 253 || value != strings.ToLower(value) || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
				return false
			}
		}
	}
	return true
}

func unavailable() (frpplugin.Response, error) {
	return rejected(UnavailableReason), ErrAuthorizationUnavailable
}

func rejected(reason string) frpplugin.Response {
	return frpplugin.Response{Reject: true, RejectReason: reason, Unchange: true}
}

func unchanged() frpplugin.Response {
	return frpplugin.Response{Unchange: true}
}

func nilStore(store Store) bool {
	if store == nil {
		return true
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
