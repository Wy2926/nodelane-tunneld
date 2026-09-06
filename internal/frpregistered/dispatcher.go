// Package frpregistered adapts registered-run authorization decisions to the
// pinned frps HTTP plugin response contract.
package frpregistered

import (
	"context"
	"errors"
	"reflect"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/frpauth"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
	frputil "github.com/fatedier/frp/pkg/util/util"
)

const (
	InvalidRequestReason    = "invalid plugin request"
	InvalidCredentialReason = "invalid run credential"
	RunStoppedReason        = "run stopped"
	UnavailableReason       = "authorization unavailable"
)

var (
	ErrInvalidConfiguration     = errors.New("invalid registered frp dispatcher configuration")
	ErrAuthorizationUnavailable = errors.New("registered frp authorization is unavailable")
)

type Authorizer interface {
	Login(context.Context, frpplugin.LoginContent) (frpplugin.LoginContent, domain.RunAuthorization, error)
	NewProxy(context.Context, frpplugin.NewProxyContent) (frpplugin.NewProxyContent, domain.RunAuthorization, error)
	Ping(context.Context, frpplugin.PingContent) (domain.RunAuthorization, error)
	NewWorkConn(context.Context, frpplugin.NewWorkConnContent) (domain.RunAuthorization, error)
	NewUserConn(context.Context, frpplugin.NewUserConnContent) (domain.RunAuthorization, error)
	CloseProxy(context.Context, frpplugin.CloseProxyContent) (domain.RunAuthorization, error)
}

type Dispatcher struct {
	authorizer Authorizer
}

var _ Authorizer = (*frpauth.Authorizer)(nil)

func New(authorizer Authorizer) (*Dispatcher, error) {
	if nilAuthorizer(authorizer) {
		return nil, ErrInvalidConfiguration
	}
	return &Dispatcher{authorizer: authorizer}, nil
}

func (d *Dispatcher) Dispatch(ctx context.Context, request frpplugin.Request) (frpplugin.Response, error) {
	if d == nil || nilAuthorizer(d.authorizer) {
		return unavailable()
	}

	switch request.Op {
	case frpplugin.OpLogin:
		var content frpplugin.LoginContent
		if err := request.DecodeContent(&content); err != nil {
			return rejected(InvalidRequestReason), nil
		}
		modified, _, err := d.authorizer.Login(ctx, content)
		if err != nil {
			return authorizationError(err)
		}
		modified.PrivilegeKey = frputil.GetAuthKey("", modified.Timestamp)
		return frpplugin.Response{Unchange: false, Content: modified}, nil

	case frpplugin.OpNewProxy:
		var content frpplugin.NewProxyContent
		if err := request.DecodeContent(&content); err != nil {
			return rejected(InvalidRequestReason), nil
		}
		modified, _, err := d.authorizer.NewProxy(ctx, content)
		if err != nil {
			return authorizationError(err)
		}
		return frpplugin.Response{Unchange: false, Content: modified}, nil

	case frpplugin.OpPing:
		var content frpplugin.PingContent
		if err := request.DecodeContent(&content); err != nil {
			return rejected(InvalidRequestReason), nil
		}
		_, err := d.authorizer.Ping(ctx, content)
		if err != nil {
			return authorizationError(err)
		}
		content.PrivilegeKey = frputil.GetAuthKey("", content.Timestamp)
		return frpplugin.Response{Unchange: false, Content: content}, nil

	case frpplugin.OpNewWorkConn:
		var content frpplugin.NewWorkConnContent
		if err := request.DecodeContent(&content); err != nil {
			return rejected(InvalidRequestReason), nil
		}
		_, err := d.authorizer.NewWorkConn(ctx, content)
		if err != nil {
			return authorizationError(err)
		}
		content.PrivilegeKey = frputil.GetAuthKey("", content.Timestamp)
		return frpplugin.Response{Unchange: false, Content: content}, nil

	case frpplugin.OpNewUserConn:
		var content frpplugin.NewUserConnContent
		if err := request.DecodeContent(&content); err != nil {
			return rejected(InvalidRequestReason), nil
		}
		_, err := d.authorizer.NewUserConn(ctx, content)
		if err != nil {
			return authorizationError(err)
		}
		return unchanged(), nil

	case frpplugin.OpCloseProxy:
		var content frpplugin.CloseProxyContent
		if err := request.DecodeContent(&content); err != nil {
			return rejected(InvalidRequestReason), nil
		}
		// CloseProxy is only a notification. The control plane waits for
		// trusted native data-plane evidence before marking a run offline.
		if _, err := d.authorizer.CloseProxy(ctx, content); err != nil {
			return authorizationError(err)
		}
		return unchanged(), nil

	default:
		return rejected(InvalidRequestReason), nil
	}
}

func authorizationError(err error) (frpplugin.Response, error) {
	switch {
	case errors.Is(err, frpauth.ErrInvalidCredential):
		return rejected(InvalidCredentialReason), nil
	case errors.Is(err, frpauth.ErrRunStopped):
		return rejected(RunStoppedReason), nil
	default:
		return unavailable()
	}
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

func nilAuthorizer(authorizer Authorizer) bool {
	if authorizer == nil {
		return true
	}
	value := reflect.ValueOf(authorizer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
