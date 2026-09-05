package identity

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const oidcMaxResponseBytes = 1 << 20

type oidcHTTPTransport struct {
	base   http.RoundTripper
	origin *url.URL
}

func newOIDCHTTPClient(supplied *http.Client, origin *url.URL) *http.Client {
	client := &http.Client{}
	if supplied != nil {
		*client = *supplied
	}
	if client.Timeout <= 0 || client.Timeout > 10*time.Second {
		client.Timeout = 10 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Jar = nil
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = &oidcHTTPTransport{base: base, origin: origin}
	return client
}

func (t *oidcHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	u, err := oidcHTTPSURL(request.URL.String())
	if err != nil || !oidcSameOrigin(u, t.origin) {
		return nil, ErrOIDCConfiguration
	}
	request = request.Clone(request.Context())
	if request.Method == http.MethodPost {
		request.GetBody = nil
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, ErrOIDCUnavailable
	}
	defer response.Body.Close()
	if (response.StatusCode >= 300 && response.StatusCode < 400) ||
		(request.Method == http.MethodGet && response.StatusCode != http.StatusOK) {
		return nil, ErrOIDCUnavailable
	}
	// Read one extra byte here: oauth2's own LimitReader can accept a valid JSON
	// prefix while silently ignoring an oversized response's trailing bytes.
	body, err := io.ReadAll(io.LimitReader(response.Body, oidcMaxResponseBytes+1))
	if err != nil || len(body) > oidcMaxResponseBytes {
		return nil, ErrOIDCUnavailable
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	return response, nil
}

func oidcHTTPSURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || strings.ContainsAny(raw, "?#") {
		return nil, ErrOIDCConfiguration
	}
	return u, nil
}

func oidcSameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() == "" {
			return "443"
		}
		return u.Port()
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func oidcOAuthError(err error) error {
	var responseError *oauth2.RetrieveError
	if !errors.As(err, &responseError) {
		return ErrOIDCUnavailable
	}
	if responseError.Response != nil {
		status := responseError.Response.StatusCode
		if status == http.StatusTooManyRequests || status >= 500 || (status >= 300 && status < 400) {
			return ErrOIDCUnavailable
		}
	}
	switch responseError.ErrorCode {
	case "server_error", "temporarily_unavailable":
		return ErrOIDCUnavailable
	case "invalid_client", "unauthorized_client", "unsupported_grant_type", "invalid_scope":
		return ErrOIDCConfiguration
	default:
		return ErrOIDCUnauthorized
	}
}
