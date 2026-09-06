package frppluginhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
)

type recordingDispatcher struct {
	response frpplugin.Response
	err      error
	panic    bool
	requests []frpplugin.Request
}

func (d *recordingDispatcher) Dispatch(_ context.Context, request frpplugin.Request) (frpplugin.Response, error) {
	if d.panic {
		panic("dispatcher panic contains secret-token")
	}
	d.requests = append(d.requests, request)
	return d.response, d.err
}

func TestHandlerServesOfficialFRPRequest(t *testing.T) {
	dispatcher := &recordingDispatcher{response: frpplugin.Response{Unchange: true}}
	handler := mustHandler(t, dispatcher)
	request := officialRequest(`{"version":"0.1.0","op":"Login","content":{"client_id":"client"}}`)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if recorder.Header().Get("Content-Type") != "application/json" || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", recorder.Header())
	}
	if len(dispatcher.requests) != 1 || dispatcher.requests[0].Op != frpplugin.OpLogin {
		t.Fatalf("dispatched requests = %#v", dispatcher.requests)
	}
	var response frpplugin.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Reject || !response.Unchange {
		t.Fatalf("response = %#v, want allowed unchanged", response)
	}
}

func TestHandlerRejectsMalformedPluginRequestsWithoutDispatch(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		query       string
		contentType []string
		body        string
		wantStatus  int
	}{
		{name: "wrong method", method: http.MethodGet, query: "op=Login&version=0.1.0", contentType: []string{"application/json"}, body: `{}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "missing content type", method: http.MethodPost, query: "op=Login&version=0.1.0", body: `{}`, wantStatus: http.StatusUnsupportedMediaType},
		{name: "duplicate content type", method: http.MethodPost, query: "op=Login&version=0.1.0", contentType: []string{"application/json", "application/json"}, body: `{}`, wantStatus: http.StatusUnsupportedMediaType},
		{name: "content type parameters", method: http.MethodPost, query: "op=Login&version=0.1.0", contentType: []string{"application/json; charset=utf-8"}, body: `{}`, wantStatus: http.StatusUnsupportedMediaType},
		{name: "duplicate operation query", method: http.MethodPost, query: "op=Login&op=Ping&version=0.1.0", contentType: []string{"application/json"}, body: `{}`, wantStatus: http.StatusOK},
		{name: "unknown query", method: http.MethodPost, query: "op=Login&version=0.1.0&token=secret", contentType: []string{"application/json"}, body: `{}`, wantStatus: http.StatusOK},
		{name: "malformed query", method: http.MethodPost, query: "op=Login&version=%zz", contentType: []string{"application/json"}, body: `{}`, wantStatus: http.StatusOK},
		{name: "operation mismatch", method: http.MethodPost, query: "op=Ping&version=0.1.0", contentType: []string{"application/json"}, body: `{"version":"0.1.0","op":"Login","content":{}}`, wantStatus: http.StatusOK},
		{name: "invalid JSON", method: http.MethodPost, query: "op=Login&version=0.1.0", contentType: []string{"application/json"}, body: `{`, wantStatus: http.StatusOK},
		{name: "oversized body", method: http.MethodPost, query: "op=Login&version=0.1.0", contentType: []string{"application/json"}, body: strings.Repeat("x", frpplugin.MaxRequestBytes+1), wantStatus: http.StatusOK},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &recordingDispatcher{}
			handler := mustHandler(t, dispatcher)
			request := httptest.NewRequest(test.method, "/frp?"+test.query, strings.NewReader(test.body))
			request.Header.Del("Content-Type")
			for _, value := range test.contentType {
				request.Header.Add("Content-Type", value)
			}
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			if len(dispatcher.requests) != 0 {
				t.Fatalf("dispatcher called for malformed request: %#v", dispatcher.requests)
			}
			if test.wantStatus == http.StatusOK {
				assertRejectedReason(t, recorder, InvalidRequestReason)
			}
		})
	}
}

func TestHandlerRedactsDispatcherFailuresAndPanics(t *testing.T) {
	for _, test := range []struct {
		name       string
		dispatcher *recordingDispatcher
	}{
		{name: "error", dispatcher: &recordingDispatcher{err: errors.New("database failed with secret-token")}},
		{name: "panic", dispatcher: &recordingDispatcher{panic: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := mustHandler(t, test.dispatcher)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, officialRequest(`{"version":"0.1.0","op":"Login","content":{}}`))

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			assertRejectedReason(t, recorder, UnavailableReason)
			if strings.Contains(recorder.Body.String(), "secret-token") {
				t.Fatalf("response leaked failure detail: %s", recorder.Body.String())
			}
		})
	}
}

func TestHandlerBoundsDispatchTimeAndCancelsDependencyContext(t *testing.T) {
	dispatcher := &blockingDispatcher{canceled: make(chan struct{})}
	handler, err := New(Options{Dispatcher: dispatcher, DispatchTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	started := time.Now()

	handler.ServeHTTP(recorder, officialRequest(`{"version":"0.1.0","op":"Login","content":{}}`))

	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("handler exceeded bounded dispatch time: %s", elapsed)
	}
	assertRejectedReason(t, recorder, UnavailableReason)
	select {
	case <-dispatcher.canceled:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not receive context cancellation")
	}
}

func TestHandlerRecoversResponseMarshalPanicWithGenericRejection(t *testing.T) {
	dispatcher := &recordingDispatcher{response: frpplugin.Response{Content: panicJSONMarshaler{}}}
	handler := mustHandler(t, dispatcher)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, officialRequest(`{"version":"0.1.0","op":"Login","content":{}}`))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	assertRejectedReason(t, recorder, UnavailableReason)
	if strings.Contains(recorder.Body.String(), "marshal-secret") {
		t.Fatalf("response leaked marshal panic: %s", recorder.Body.String())
	}
}

func TestHandlerRejectsNilDispatcher(t *testing.T) {
	if handler, err := New(Options{}); handler != nil || !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("New(nil) = (%v, %v), want nil and ErrInvalidConfiguration", handler, err)
	}
	var typedNil *recordingDispatcher
	if handler, err := New(Options{Dispatcher: typedNil}); handler != nil || !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("New(typed nil) = (%v, %v), want nil and ErrInvalidConfiguration", handler, err)
	}
	if handler, err := New(Options{Dispatcher: &recordingDispatcher{}, DispatchTimeout: -time.Second}); handler != nil || !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("New(negative timeout) = (%v, %v), want nil and ErrInvalidConfiguration", handler, err)
	}
}

func officialRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/frp?op=Login&version=0.1.0", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func mustHandler(t *testing.T, dispatcher Dispatcher) *Handler {
	t.Helper()
	handler, err := New(Options{Dispatcher: dispatcher})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return handler
}

type blockingDispatcher struct{ canceled chan struct{} }

func (d *blockingDispatcher) Dispatch(ctx context.Context, _ frpplugin.Request) (frpplugin.Response, error) {
	<-ctx.Done()
	close(d.canceled)
	return frpplugin.Response{}, ctx.Err()
}

type panicJSONMarshaler struct{}

func (panicJSONMarshaler) MarshalJSON() ([]byte, error) {
	panic("marshal-secret")
}

func assertRejectedReason(t *testing.T, recorder *httptest.ResponseRecorder, reason string) {
	t.Helper()
	var response frpplugin.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body=%q", err, recorder.Body.String())
	}
	if !response.Reject || response.RejectReason != reason {
		t.Fatalf("response = %#v, want rejection %q", response, reason)
	}
}
