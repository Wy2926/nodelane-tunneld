package cliauth

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var (
	ErrNoCredentials          = errors.New("not logged in; run nt login")
	ErrInvalidConfiguration   = errors.New("invalid native authentication configuration")
	ErrCredentialsUnavailable = errors.New("account credential storage is unavailable")
	ErrCredentialsMismatch    = errors.New("stored account credentials belong to a different authentication configuration")
	ErrProviderUnavailable    = errors.New("identity provider request failed")
	ErrInvalidResponse        = errors.New("identity provider returned an invalid authentication response")
	ErrRevocationUnconfirmed  = errors.New("refresh token revocation was not confirmed")
	ErrAuthorizationDenied    = errors.New("device authorization was denied")
	ErrAuthorizationExpired   = errors.New("device authorization expired; run nt login again")
	ErrAuthorizationRevoked   = errors.New("account authorization expired or was revoked; run nt login again")
)

const nativeScopes = "openid offline_access routes:read runs:start"

type Options struct {
	Issuer                string
	ClientID              string
	Resource              string
	HTTPClient            *http.Client
	Store                 Store
	AllowInsecureLoopback bool
}

// Credentials contains no access token, ID token, device code, or run secret.
type Credentials struct {
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	Resource     string `json:"resource"`
	RefreshToken string `json:"refresh_token"`
}

type Store interface {
	Load(context.Context) (Credentials, error)
	Save(context.Context, Credentials) error
	Delete(context.Context) error
}

// DeviceCode deliberately excludes the private OAuth device_code.
type DeviceCode struct {
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	Expiry                  time.Time
}

type Client struct {
	gate          chan struct{}
	store         TransactionalStore
	options       Options
	issuer        *url.URL
	config        *oauth2.Config
	revocationURL string
	cached        *oauth2.Token
	pending       *oauth2.Token
	pendingFrom   Credentials
}

// New validates configuration without contacting the provider or credential store.
func New(ctx context.Context, options Options) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	issuer, err := parseEndpoint(options.Issuer, options.AllowInsecureLoopback, false)
	if err != nil || !safeValue(options.ClientID, 256) || options.Store == nil {
		return nil, ErrInvalidConfiguration
	}
	if _, err = parseEndpoint(options.Resource, options.AllowInsecureLoopback, false); err != nil {
		return nil, ErrInvalidConfiguration
	}
	store, ok := options.Store.(TransactionalStore)
	if !ok {
		return nil, ErrInvalidConfiguration
	}
	return &Client{options: options, issuer: issuer, store: store, gate: make(chan struct{}, 1)}, nil
}

func (c *Client) Login(ctx context.Context, display func(DeviceCode) error) error {
	if display == nil {
		return errors.New("device authorization display is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	return c.transaction(ctx, func(store Store) error { return c.login(ctx, store, display) })
}

func (c *Client) transaction(ctx context.Context, action func(Store) error) error {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return c.store.Transaction(ctx, action)
}

func (c *Client) login(ctx context.Context, store Store, display func(DeviceCode) error) error {
	if err := c.discover(ctx); err != nil {
		return err
	}
	oauthCtx := context.WithValue(ctx, oauth2.HTTPClient, c.httpClient(ctx))
	device, err := c.config.DeviceAuth(oauthCtx, oauth2.SetAuthURLParam("resource", c.options.Resource))
	if err != nil {
		return sanitizeProviderError(ctx, err)
	}
	if !c.validDeviceResponse(device) {
		return ErrInvalidResponse
	}
	visible := DeviceCode{UserCode: device.UserCode, VerificationURI: device.VerificationURI, VerificationURIComplete: device.VerificationURIComplete, Expiry: device.Expiry}
	if err = display(visible); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("device authorization display failed")
	}
	token, err := c.config.DeviceAccessToken(oauthCtx, device, oauth2.SetAuthURLParam("resource", c.options.Resource))
	if err != nil {
		return sanitizeProviderError(ctx, err)
	}
	if !validToken(token) {
		return ErrInvalidResponse
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = store.Save(ctx, c.credentials(token.RefreshToken)); err != nil {
		return ErrCredentialsUnavailable
	}
	c.cached, c.pending = token, nil
	return nil
}

func (c *Client) AccessToken(ctx context.Context) (string, error) {
	var token string
	err := c.transaction(ctx, func(store Store) error { var err error; token, err = c.accessToken(ctx, store); return err })
	return token, err
}

func (c *Client) accessToken(ctx context.Context, store Store) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	stored, err := store.Load(ctx)
	if errors.Is(err, ErrNoCredentials) {
		c.cached, c.pending = nil, nil
		return "", ErrNoCredentials
	}
	if err != nil {
		return "", ErrCredentialsUnavailable
	}
	if !c.matches(stored) {
		c.cached, c.pending = nil, nil
		return "", ErrCredentialsMismatch
	}
	if !validCredentials(stored) {
		return "", ErrCredentialsUnavailable
	}
	// A rotated refresh token must reach durable storage before access is granted.
	// Keep an unsuccessful write in memory so a retry never reuses the old token.
	if c.pending != nil && stored != c.pendingFrom {
		c.cached, c.pending = nil, nil
	}
	if c.pending != nil {
		if err := store.Save(ctx, c.credentials(c.pending.RefreshToken)); err != nil {
			return "", ErrCredentialsUnavailable
		}
		stored = c.credentials(c.pending.RefreshToken)
		c.cached, c.pending = c.pending, nil
	}
	if c.cached != nil && c.cached.RefreshToken == stored.RefreshToken && c.cached.Valid() {
		return c.cached.AccessToken, nil
	}
	if err = c.discover(ctx); err != nil {
		return "", err
	}
	oauthCtx := context.WithValue(ctx, oauth2.HTTPClient, c.httpClient(ctx))
	token, err := c.config.TokenSource(oauthCtx, &oauth2.Token{RefreshToken: stored.RefreshToken}).Token()
	if err != nil {
		return "", sanitizeProviderError(ctx, err)
	}
	if !validToken(token) {
		return "", ErrInvalidResponse
	}
	c.pending, c.pendingFrom = token, stored
	if err = store.Save(ctx, c.credentials(token.RefreshToken)); err != nil {
		return "", ErrCredentialsUnavailable
	}
	c.cached, c.pending = token, nil
	return token.AccessToken, nil
}

func (c *Client) Logout(ctx context.Context) error {
	lockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return c.transaction(lockCtx, func(store Store) error { return c.logout(ctx, store) })
}

func (c *Client) logout(ctx context.Context, store Store) error {
	// Give local reads and deletion independent deadlines: a slow failed remote
	// revocation must not consume the time reserved for removing credentials.
	loadCtx, cancelLoad := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	stored, loadErr := store.Load(loadCtx)
	cancelLoad()
	var remoteErr error
	if c.pending != nil && loadErr == nil && stored == c.pendingFrom {
		stored, loadErr = c.credentials(c.pending.RefreshToken), nil
	}
	if loadErr != nil && !errors.Is(loadErr, ErrNoCredentials) {
		remoteErr = ErrRevocationUnconfirmed
	}
	if loadErr == nil {
		if !c.matches(stored) || !validCredentials(stored) {
			remoteErr = ErrRevocationUnconfirmed
		} else if err := c.discover(ctx); err != nil {
			remoteErr = ErrRevocationUnconfirmed
		} else if err = c.revoke(ctx, stored.RefreshToken); err != nil {
			remoteErr = ErrRevocationUnconfirmed
		}
	}
	c.cached, c.pending = nil, nil
	deleteCtx, cancelDelete := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelDelete()
	deleteErr := store.Delete(deleteCtx)
	if deleteErr != nil && !errors.Is(deleteErr, ErrNoCredentials) {
		return errors.Join(ErrCredentialsUnavailable, remoteErr)
	}
	return remoteErr
}

func (c *Client) discover(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.config != nil {
		return nil
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, c.httpClient(ctx)), c.options.Issuer)
	if err != nil {
		return sanitizeProviderError(ctx, err)
	}
	var metadata struct {
		Issuer             string   `json:"issuer"`
		RevocationEndpoint string   `json:"revocation_endpoint"`
		TokenAuthMethods   []string `json:"token_endpoint_auth_methods_supported"`
	}
	if err = provider.Claims(&metadata); err != nil || metadata.Issuer != c.options.Issuer {
		return ErrInvalidResponse
	}
	endpoint := provider.Endpoint()
	for _, raw := range []string{endpoint.TokenURL, endpoint.DeviceAuthURL, metadata.RevocationEndpoint} {
		if !c.sameOriginEndpoint(raw, false) {
			return ErrInvalidResponse
		}
	}
	if len(metadata.TokenAuthMethods) > 0 {
		hasNone := false
		for _, method := range metadata.TokenAuthMethods {
			if method == "none" {
				hasNone = true
			}
		}
		if !hasNone {
			return ErrInvalidResponse
		}
	}
	endpoint.AuthStyle = oauth2.AuthStyleInParams
	c.config = &oauth2.Config{ClientID: c.options.ClientID, Scopes: strings.Fields(nativeScopes), Endpoint: endpoint}
	c.revocationURL = metadata.RevocationEndpoint
	return nil
}

func (c *Client) credentials(refreshToken string) Credentials {
	return Credentials{Issuer: c.options.Issuer, ClientID: c.options.ClientID, Resource: c.options.Resource, RefreshToken: refreshToken}
}

func (c *Client) matches(credentials Credentials) bool {
	return credentials.Issuer == c.options.Issuer && credentials.ClientID == c.options.ClientID && credentials.Resource == c.options.Resource
}

func (c *Client) validDeviceResponse(device *oauth2.DeviceAuthResponse) bool {
	if device == nil || !safeValue(device.DeviceCode, 16<<10) || !safeValue(device.UserCode, 128) || !device.Expiry.After(time.Now()) || device.Expiry.After(time.Now().Add(15*time.Minute)) || device.Interval < 0 || device.Interval > 300 {
		return false
	}
	for _, character := range device.UserCode {
		if !(character >= 'A' && character <= 'Z') && !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' {
			return false
		}
	}
	if !c.sameOriginEndpoint(device.VerificationURI, true) || strings.Contains(device.VerificationURI, device.DeviceCode) {
		return false
	}
	if device.VerificationURIComplete != "" && (!c.sameOriginEndpoint(device.VerificationURIComplete, true) || strings.Contains(device.VerificationURIComplete, device.DeviceCode)) {
		return false
	}
	return !strings.Contains(device.UserCode, device.DeviceCode)
}

func (c *Client) sameOriginEndpoint(raw string, allowQuery bool) bool {
	endpoint, err := parseEndpoint(raw, c.options.AllowInsecureLoopback, allowQuery)
	return err == nil && endpoint.Scheme == c.issuer.Scheme && strings.EqualFold(endpoint.Host, c.issuer.Host)
}

func parseEndpoint(raw string, allowLoopback, allowQuery bool) (*url.URL, error) {
	if !safeValue(raw, 4096) || strings.Contains(raw, "\\") {
		return nil, ErrInvalidConfiguration
	}
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Opaque != "" || endpoint.User != nil || endpoint.Hostname() == "" || endpoint.Fragment != "" || (!allowQuery && (endpoint.RawQuery != "" || endpoint.ForceQuery)) {
		return nil, ErrInvalidConfiguration
	}
	if port := endpoint.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, ErrInvalidConfiguration
		}
	}
	if endpoint.Scheme != "https" {
		address, err := netip.ParseAddr(endpoint.Hostname())
		if endpoint.Scheme != "http" || !allowLoopback || err != nil || !address.IsLoopback() {
			return nil, ErrInvalidConfiguration
		}
	}
	return endpoint, nil
}

func safeValue(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, character := range value {
		if character <= 32 || character >= 127 {
			return false
		}
	}
	return true
}

func validCredentials(credentials Credentials) bool {
	return safeValue(credentials.Issuer, 4096) && safeValue(credentials.ClientID, 256) && safeValue(credentials.Resource, 4096) && safeValue(credentials.RefreshToken, 16<<10)
}

func validToken(token *oauth2.Token) bool {
	return token != nil && strings.EqualFold(token.TokenType, "Bearer") && safeValue(token.AccessToken, 16<<10) && safeValue(token.RefreshToken, 16<<10) && !token.Expiry.IsZero() && token.Valid()
}

func sanitizeProviderError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var oauthError *oauth2.RetrieveError
	if errors.As(err, &oauthError) {
		switch oauthError.ErrorCode {
		case "access_denied":
			return ErrAuthorizationDenied
		case "expired_token":
			return ErrAuthorizationExpired
		case "invalid_grant":
			return ErrAuthorizationRevoked
		}
	}
	return ErrProviderUnavailable
}
