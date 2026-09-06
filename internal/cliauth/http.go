package cliauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxProviderBody = 64 << 10

type providerTransport struct {
	client  *Client
	ctx     context.Context
	base    http.RoundTripper
	timeout time.Duration
}

func (c *Client) httpClient(ctx context.Context) *http.Client {
	base := http.DefaultTransport
	timeout := 10 * time.Second
	if configured := c.options.HTTPClient; configured != nil {
		if configured.Transport != nil {
			base = configured.Transport
		}
		if configured.Timeout > 0 && configured.Timeout < timeout {
			timeout = configured.Timeout
		}
	}
	return &http.Client{Transport: &providerTransport{client: c, ctx: ctx, base: base, timeout: timeout}, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("identity provider redirects are not allowed")
	}}
}

func (t *providerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !t.allowed(request) {
		return nil, ErrInvalidResponse
	}
	// DeviceAuth in the pinned oauth2 version does not attach its context to the
	// request. Bind both operation cancellation and a finite request deadline here.
	ctx, cancel := context.WithTimeout(t.ctx, t.timeout)
	defer cancel()
	stop := context.AfterFunc(request.Context(), cancel)
	defer stop()
	clone := request.Clone(ctx)
	clone.Header.Del("Cookie")
	clone.Header.Del("Authorization")
	if t.client.config != nil && request.URL.String() == t.client.config.Endpoint.TokenURL {
		body, err := io.ReadAll(io.LimitReader(request.Body, maxProviderBody+1))
		_ = request.Body.Close()
		if err != nil || len(body) > maxProviderBody {
			return nil, ErrInvalidResponse
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, ErrInvalidResponse
		}
		if form.Get("grant_type") == "refresh_token" {
			// oauth2.Config.TokenSource has no parameter hook for RFC 8707 resource.
			form.Set("resource", t.client.options.Resource)
			form.Set("scope", nativeScopes)
		}
		encoded := form.Encode()
		clone.Body = io.NopCloser(strings.NewReader(encoded))
		clone.ContentLength = int64(len(encoded))
		clone.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(encoded)), nil }
	}
	response, err := t.base.RoundTrip(clone)
	if err != nil {
		return nil, err
	}
	if response.Body == nil {
		return nil, ErrInvalidResponse
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProviderBody+1))
	if err != nil || len(body) > maxProviderBody {
		return nil, ErrInvalidResponse
	}
	if t.client.config != nil && request.URL.String() == t.client.config.Endpoint.DeviceAuthURL && response.StatusCode >= 200 && response.StatusCode <= 299 {
		// The pinned oauth2 device decoder dereferences its result even when a
		// successful JSON null response replaces that result with a nil pointer.
		var object map[string]json.RawMessage
		if err := json.Unmarshal(body, &object); err != nil || object == nil {
			return nil, ErrInvalidResponse
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	return response, nil
}

func (t *providerTransport) allowed(request *http.Request) bool {
	if !t.client.sameOriginEndpoint(request.URL.String(), false) {
		return false
	}
	if request.Method == http.MethodGet && request.URL.String() == strings.TrimSuffix(t.client.options.Issuer, "/")+"/.well-known/openid-configuration" {
		return true
	}
	if t.client.config == nil || request.Method != http.MethodPost {
		return false
	}
	raw := request.URL.String()
	return raw == t.client.config.Endpoint.DeviceAuthURL || raw == t.client.config.Endpoint.TokenURL || raw == t.client.revocationURL
}

func (c *Client) revoke(ctx context.Context, token string) error {
	form := url.Values{"client_id": {c.options.ClientID}, "token": {token}, "token_type_hint": {"refresh_token"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.revocationURL, strings.NewReader(form.Encode()))
	if err != nil {
		return ErrRevocationUnconfirmed
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient(ctx).Do(request)
	if err != nil {
		return ErrRevocationUnconfirmed
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrRevocationUnconfirmed
	}
	return nil
}
