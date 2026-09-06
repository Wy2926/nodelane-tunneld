package cliauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"golang.org/x/oauth2"
)

func TestDeviceAuthorizationRejectsNonObjectSuccess(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		body string
	}{
		{"null", "null"},
		{"whitespace_null", " \r\nnull\t"},
		{"string", `"LEAK-provider-secret"`},
		{"number", "42"},
		{"boolean", "false"},
		{"array", "[]"},
		{"empty", ""},
		{"malformed", `{"device_code":`},
		{"trailing_value", "{} null"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newAuthFixture(t)
			previous := Credentials{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, RefreshToken: "previous-refresh"}
			if err := f.store.Save(context.Background(), previous); err != nil {
				t.Fatal(err)
			}
			f.device = func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, test.body)
			}
			displayed := false
			err := f.client.Login(context.Background(), func(DeviceCode) error {
				displayed = true
				return nil
			})
			if !errors.Is(err, ErrProviderUnavailable) {
				t.Fatalf("malformed device response must fail safely: %v", err)
			}
			if displayed || f.tokenCalls.Load() != 0 {
				t.Fatal("malformed device response reached display or token endpoint")
			}
			got, err := f.store.Load(context.Background())
			if err != nil || got != previous {
				t.Fatalf("malformed device response changed saved credentials: %v", err)
			}
		})
	}
}

func TestDeviceAuthorizationPreservesOAuthErrorResponses(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		contentType string
		body        string
	}{
		{"json", "application/json", `{"error":"access_denied","error_description":"provider denied authorization"}`},
		{"form", "application/x-www-form-urlencoded", "error=access_denied&error_description=provider+denied+authorization"},
		{"text", "text/plain", "error=access_denied&error_description=provider+denied+authorization"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newAuthFixture(t)
			f.device = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(w, test.body)
			}
			ctx := context.Background()
			if err := f.client.discover(ctx); err != nil {
				t.Fatal(err)
			}
			oauthCtx := context.WithValue(ctx, oauth2.HTTPClient, f.client.httpClient(ctx))
			device, err := f.client.config.DeviceAuth(oauthCtx, oauth2.SetAuthURLParam("resource", testResource))
			var oauthError *oauth2.RetrieveError
			if device != nil || !errors.As(err, &oauthError) {
				t.Fatalf("standard provider error was not preserved: %v", err)
			}
			if oauthError.ErrorCode != "access_denied" || oauthError.ErrorDescription != "provider denied authorization" || oauthError.Response.StatusCode != http.StatusBadRequest || string(oauthError.Body) != test.body {
				t.Fatalf("standard provider error changed: %+v", oauthError)
			}
		})
	}
}
