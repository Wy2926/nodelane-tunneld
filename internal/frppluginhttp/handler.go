// Package frppluginhttp provides the narrow HTTP transport for the pinned
// frps server-plugin protocol. Authorization and lifecycle decisions remain in
// the dispatcher.
package frppluginhttp

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"reflect"

	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
)

const (
	InvalidRequestReason = "invalid plugin request"
	UnavailableReason    = "authorization unavailable"
	maxQueryBytes        = 256
)

var ErrInvalidConfiguration = errors.New("invalid frp plugin HTTP configuration")

type Dispatcher interface {
	Dispatch(context.Context, frpplugin.Request) (frpplugin.Response, error)
}

type Handler struct {
	dispatcher Dispatcher
}

func New(dispatcher Dispatcher) (*Handler, error) {
	if nilDispatcher(dispatcher) {
		return nil, ErrInvalidConfiguration
	}
	return &Handler{dispatcher: dispatcher}, nil
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setResponseHeaders(writer.Header())
	deferredResponse := true
	defer func() {
		if recover() != nil && deferredResponse {
			writeResponse(writer, http.StatusOK, rejected(UnavailableReason))
		}
	}()

	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		deferredResponse = false
		writeResponse(writer, http.StatusMethodNotAllowed, rejected(InvalidRequestReason))
		return
	}
	if !officialContentType(request.Header) {
		deferredResponse = false
		writeResponse(writer, http.StatusUnsupportedMediaType, rejected(InvalidRequestReason))
		return
	}
	queryVersion, queryOperation, ok := officialQuery(request.URL.RawQuery)
	if !ok || request.ContentLength > frpplugin.MaxRequestBytes {
		deferredResponse = false
		writeResponse(writer, http.StatusOK, rejected(InvalidRequestReason))
		return
	}
	pluginRequest, err := frpplugin.DecodeRequest(request.Body, queryVersion, queryOperation)
	if err != nil {
		deferredResponse = false
		writeResponse(writer, http.StatusOK, rejected(InvalidRequestReason))
		return
	}
	response, err := h.dispatcher.Dispatch(request.Context(), pluginRequest)
	if err != nil {
		response = rejected(UnavailableReason)
	}
	deferredResponse = false
	if !writeResponse(writer, http.StatusOK, response) {
		writeResponse(writer, http.StatusOK, rejected(UnavailableReason))
	}
}

func officialContentType(header http.Header) bool {
	values := header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	return err == nil && mediaType == "application/json" && len(parameters) == 0
}

func officialQuery(rawQuery string) (string, string, bool) {
	if rawQuery == "" || len(rawQuery) > maxQueryBytes {
		return "", "", false
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil || len(values) != 2 || len(values["version"]) != 1 || len(values["op"]) != 1 {
		return "", "", false
	}
	return values["version"][0], values["op"][0], true
}

func rejected(reason string) frpplugin.Response {
	return frpplugin.Response{Reject: true, RejectReason: reason, Unchange: true}
}

func setResponseHeaders(header http.Header) {
	header.Set("Content-Type", "application/json")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
}

func writeResponse(writer http.ResponseWriter, status int, response frpplugin.Response) bool {
	data, err := json.Marshal(response)
	if err != nil {
		return false
	}
	setResponseHeaders(writer.Header())
	writer.WriteHeader(status)
	_, _ = writer.Write(append(data, '\n'))
	return true
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
