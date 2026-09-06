// Package frpmux routes frps callbacks to the authoritative registered or
// anonymous state store by the opaque run credential namespace.
package frpmux

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
)

const InvalidCredentialReason = "invalid run credential"

var ErrInvalidConfiguration = errors.New("invalid frp callback multiplexer configuration")

type Dispatcher interface {
	Dispatch(context.Context, frpplugin.Request) (frpplugin.Response, error)
}

type Mux struct {
	registered Dispatcher
	anonymous  Dispatcher
}

func New(registered, anonymous Dispatcher) (*Mux, error) {
	if nilDispatcher(registered) || nilDispatcher(anonymous) {
		return nil, ErrInvalidConfiguration
	}
	return &Mux{registered: registered, anonymous: anonymous}, nil
}

func (m *Mux) Dispatch(ctx context.Context, request frpplugin.Request) (frpplugin.Response, error) {
	if m == nil || nilDispatcher(m.registered) || nilDispatcher(m.anonymous) {
		return rejected(), nil
	}
	token, ok := callbackToken(request)
	if !ok {
		return rejected(), nil
	}
	switch {
	case strings.HasPrefix(token, "nrc_"):
		return m.registered.Dispatch(ctx, request)
	case strings.HasPrefix(token, "nac_"):
		return m.anonymous.Dispatch(ctx, request)
	default:
		return rejected(), nil
	}
}

func callbackToken(request frpplugin.Request) (string, bool) {
	var metadata map[string]string
	switch request.Op {
	case frpplugin.OpLogin:
		var content frpplugin.LoginContent
		if request.DecodeContent(&content) != nil {
			return "", false
		}
		metadata = content.Metas
	case frpplugin.OpNewProxy:
		var content frpplugin.NewProxyContent
		if request.DecodeContent(&content) != nil {
			return "", false
		}
		metadata = content.User.Metas
	case frpplugin.OpCloseProxy:
		var content frpplugin.CloseProxyContent
		if request.DecodeContent(&content) != nil {
			return "", false
		}
		metadata = content.User.Metas
	case frpplugin.OpPing:
		var content frpplugin.PingContent
		if request.DecodeContent(&content) != nil {
			return "", false
		}
		metadata = content.User.Metas
	case frpplugin.OpNewWorkConn:
		var content frpplugin.NewWorkConnContent
		if request.DecodeContent(&content) != nil {
			return "", false
		}
		metadata = content.User.Metas
	case frpplugin.OpNewUserConn:
		var content frpplugin.NewUserConnContent
		if request.DecodeContent(&content) != nil {
			return "", false
		}
		metadata = content.User.Metas
	default:
		return "", false
	}
	token, exists := metadata[frpplugin.MetadataRunToken]
	return token, exists && token != ""
}

func rejected() frpplugin.Response {
	return frpplugin.Response{Reject: true, RejectReason: InvalidCredentialReason, Unchange: true}
}

func nilDispatcher(dispatcher Dispatcher) bool {
	if dispatcher == nil {
		return true
	}
	value := reflect.ValueOf(dispatcher)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
