package controlserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/controlapi"
)

type consoleAuthStub struct {
	principal controlapi.Principal
	err       error
	calls     int
}

func (a *consoleAuthStub) Authenticate(context.Context, *http.Request) (controlapi.Principal, error) {
	a.calls++
	return a.principal, a.err
}

func TestConsoleRequiresWebSessionBeforeReadingLocalizedShell(t *testing.T) {
	auth := &consoleAuthStub{principal: controlapi.Principal{AccountID: "acct-test", Kind: controlapi.PrincipalKindWeb}}
	var locale string
	handler := newConsoleHandler(auth, func(selected string) ([]byte, error) { locale = selected; return []byte("<html>console</html>"), nil })
	request := httptest.NewRequest(http.MethodGet, "/console/tunnels/rte_aaaaaaaaaaaaaaaaaaaaaaaaaa?lang=zh-CN&view=deleted", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || locale != "zh-CN" || response.Body.String() != "<html>console</html>" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response=%d locale=%s headers=%v", response.Code, locale, response.Header())
	}
	if response.Header().Get("Content-Type") != "text/html; charset=utf-8" || response.Header().Get("Vary") != "Cookie" {
		t.Fatal("missing private HTML headers")
	}
}

func TestConsoleResponsesPreventFramingAndRestrictActiveContent(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, authErr := range []error{nil, controlapi.ErrUnauthorized, controlapi.ErrUnavailable} {
			auth := &consoleAuthStub{principal: controlapi.Principal{AccountID: "acct-test", Kind: controlapi.PrincipalKindWeb}, err: authErr}
			handler := newConsoleHandler(auth, func(string) ([]byte, error) { return []byte("<html>console</html>"), nil })
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, "/console/tunnels", nil))
			policy := response.Header().Get("Content-Security-Policy")
			for _, directive := range []string{"frame-ancestors 'none'", "script-src 'self'", "object-src 'none'", "base-uri 'none'", "form-action 'self'"} {
				if !strings.Contains(policy, directive) {
					t.Errorf("%s status=%d missing directive %q", method, response.Code, directive)
				}
			}
			if response.Header().Get("X-Frame-Options") != "DENY" || strings.Contains(policy, "unsafe-inline") {
				t.Errorf("%s status=%d unsafe console policy", method, response.Code)
			}
		}
	}
}

func TestConsoleLoginRedirectPreservesRouteLocaleAndView(t *testing.T) {
	auth := &consoleAuthStub{err: controlapi.ErrUnauthorized}
	handler := newConsoleHandler(auth, func(string) ([]byte, error) { t.Fatal("read shell without session"); return nil, nil })
	request := httptest.NewRequest(http.MethodGet, "/console/tunnels/rte_aaaaaaaaaaaaaaaaaaaaaaaaaa?lang=ar&view=deleted", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil || response.Code != http.StatusSeeOther || location.IsAbs() || location.Path != "/auth/login" {
		t.Fatalf("unsafe login response=%d location=%v err=%v", response.Code, location, err)
	}
	if location.Query().Get("locale") != "ar" || location.Query().Get("return_to") != request.URL.RequestURI() {
		t.Fatalf("lost return target=%s", location)
	}
}

func TestConsoleRejectsNativeTokensAndPrivateAssetPaths(t *testing.T) {
	for _, path := range []string{"/console/_shells/en/index.html", "/console/tunnels/../_shells/en/index.html", "/console/tunnels?lang=unknown", "/console/tunnels?lang=en&lang=ar", "/console/tunnels?return_to=https://other.test", "/console/tunnels/%72te_aaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		auth := &consoleAuthStub{principal: controlapi.Principal{AccountID: "acct-test", Kind: controlapi.PrincipalKindWeb}}
		handler := newConsoleHandler(auth, func(string) ([]byte, error) { t.Fatal("unsafe path read shell"); return nil, nil })
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code < 400 || auth.calls != 0 {
			t.Fatalf("unsafe path accepted=%s code=%d auth=%d", path, response.Code, auth.calls)
		}
	}
	auth := &consoleAuthStub{principal: controlapi.Principal{AccountID: "acct-test", Kind: controlapi.PrincipalKindNative}}
	handler := newConsoleHandler(auth, func(string) ([]byte, error) { t.Fatal("native token read shell"); return nil, nil })
	request := httptest.NewRequest(http.MethodGet, "/console/tunnels", nil)
	request.Header.Set("Authorization", "Bearer opaque")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || auth.calls != 0 {
		t.Fatalf("native bearer response=%d auth=%d", response.Code, auth.calls)
	}
}

func TestConsoleDependencyFailureDoesNotRedirectOrExposeDetails(t *testing.T) {
	for _, authErr := range []error{controlapi.ErrUnavailable, nil} {
		auth := &consoleAuthStub{principal: controlapi.Principal{AccountID: "acct-test", Kind: controlapi.PrincipalKindWeb}, err: authErr}
		handler := newConsoleHandler(auth, func(string) ([]byte, error) { return nil, errors.New("private path and secret") })
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/console/tunnels", nil))
		if response.Code != 503 || response.Header().Get("Location") != "" || strings.Contains(response.Body.String(), "secret") {
			t.Fatalf("bad unavailable response=%d %s", response.Code, response.Body.String())
		}
	}
}

func TestConsoleNamespaceCannotFallThroughToPublicAssets(t *testing.T) {
	guarded, public := 0, 0
	handler := consoleOrPublic(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { guarded++; w.WriteHeader(401) }), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { public++; w.WriteHeader(204) }))
	for _, path := range []string{"/console", "/console/tunnels", "/console/_shells/en/index.html", "/console/anything"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != 401 {
			t.Fatalf("console path fell through: %s", path)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/console.js", nil))
	if response.Code != 204 || guarded != 4 || public != 1 {
		t.Fatalf("routing counts: console=%d public=%d", guarded, public)
	}
}
