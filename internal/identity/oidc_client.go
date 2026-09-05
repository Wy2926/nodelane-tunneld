package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDCOptions struct {
	Issuer          string
	PublicOrigin    string
	APIResource     string
	WebClientID     string
	WebClientSecret string
	NativeClientID  string
	HTTPClient      *http.Client
	Now             func() time.Time
}

type OIDCClient struct {
	issuer        string
	publicOrigin  string
	apiResource   string
	nativeID      string
	revocationURL string
	endSessionURL string
	http          *http.Client
	oauth         oauth2.Config
	keys          *oidc.RemoteKeySet
	now           func() time.Time
}

func NewOIDCClient(ctx context.Context, opts OIDCOptions) (*OIDCClient, error) {
	issuer, err := oidcHTTPSURL(opts.Issuer)
	if err != nil {
		return nil, ErrOIDCConfiguration
	}
	origin, err := oidcHTTPSURL(opts.PublicOrigin)
	if err != nil || (origin.Path != "" && origin.Path != "/") {
		return nil, ErrOIDCConfiguration
	}
	if _, err := oidcHTTPSURL(opts.APIResource); err != nil {
		return nil, ErrOIDCConfiguration
	}
	if strings.TrimSpace(opts.WebClientID) == "" || strings.TrimSpace(opts.NativeClientID) == "" ||
		opts.WebClientID == opts.NativeClientID || strings.TrimSpace(opts.WebClientSecret) == "" {
		return nil, ErrOIDCConfiguration
	}
	client := newOIDCHTTPClient(opts.HTTPClient, issuer)
	httpCtx := oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(httpCtx, opts.Issuer)
	if err != nil {
		var mismatch *oidc.IssuerMismatchError
		if errors.As(err, &mismatch) {
			return nil, ErrOIDCConfiguration
		}
		return nil, ErrOIDCUnavailable
	}
	var metadata map[string]json.RawMessage
	if provider.Claims(&metadata) != nil {
		return nil, ErrOIDCConfiguration
	}
	endpoints := make(map[string]string)
	for name, raw := range metadata {
		if name != "jwks_uri" && !strings.HasSuffix(name, "_endpoint") {
			continue
		}
		var endpoint string
		if json.Unmarshal(raw, &endpoint) != nil {
			return nil, ErrOIDCConfiguration
		}
		u, err := oidcHTTPSURL(endpoint)
		if err != nil || !oidcSameOrigin(u, issuer) {
			return nil, ErrOIDCConfiguration
		}
		endpoints[name] = endpoint
	}
	for _, name := range []string{"authorization_endpoint", "token_endpoint", "jwks_uri", "revocation_endpoint", "end_session_endpoint"} {
		if endpoints[name] == "" {
			return nil, ErrOIDCConfiguration
		}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	publicOrigin := strings.TrimSuffix(opts.PublicOrigin, "/")
	endpoint := provider.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInHeader
	return &OIDCClient{
		issuer: opts.Issuer, publicOrigin: publicOrigin, apiResource: opts.APIResource, nativeID: opts.NativeClientID,
		revocationURL: endpoints["revocation_endpoint"], endSessionURL: endpoints["end_session_endpoint"],
		http: client, now: now, keys: oidc.NewRemoteKeySet(httpCtx, endpoints["jwks_uri"]),
		oauth: oauth2.Config{
			ClientID: opts.WebClientID, ClientSecret: opts.WebClientSecret, Endpoint: endpoint,
			RedirectURL: publicOrigin + "/auth/callback", Scopes: []string{"openid", "profile", "email", "offline_access"},
		},
	}, nil
}

// AuthorizationURL leaves state generation and browser binding to the caller.
func (c *OIDCClient) AuthorizationURL(state, nonce, verifier, locale string) (string, error) {
	if strings.TrimSpace(state) == "" || strings.TrimSpace(nonce) == "" || !validOIDCVerifier(verifier) {
		return "", ErrOIDCUnauthorized
	}
	options := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)}
	if locale != "" {
		options = append(options, oauth2.SetAuthURLParam("ui_locales", locale))
	}
	return c.oauth.AuthCodeURL(state, options...), nil
}

func (c *OIDCClient) Exchange(ctx context.Context, code, verifier, nonce string) (OIDCTokens, error) {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(nonce) == "" || !validOIDCVerifier(verifier) {
		return OIDCTokens{}, ErrOIDCUnauthorized
	}
	token, err := c.oauth.Exchange(oidc.ClientContext(ctx, c.http), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return OIDCTokens{}, oidcOAuthError(err)
	}
	idToken, ok := token.Extra("id_token").(string)
	if !ok || idToken == "" {
		return OIDCTokens{}, ErrOIDCUnauthorized
	}
	identity, err := c.verifyWeb(ctx, idToken, nonce)
	if err != nil {
		return OIDCTokens{}, err
	}
	return c.tokens(token, idToken, identity)
}

// Refresh accepts only prior identity data from a verified, server-side session.
// The caller owns cross-request coordination of refresh-token rotation.
func (c *OIDCClient) Refresh(ctx context.Context, previous OIDCTokens) (OIDCTokens, error) {
	if strings.TrimSpace(previous.RefreshToken) == "" || strings.TrimSpace(previous.Identity.Subject) == "" ||
		previous.Identity.Issuer != c.issuer || previous.Identity.ClientID != c.oauth.ClientID {
		return OIDCTokens{}, ErrOIDCUnauthorized
	}
	// An access-token-free source forces exactly one refresh, even before expiry.
	source := c.oauth.TokenSource(oidc.ClientContext(ctx, c.http), &oauth2.Token{RefreshToken: previous.RefreshToken})
	token, err := source.Token()
	if err != nil {
		return OIDCTokens{}, oidcOAuthError(err)
	}
	identity, idToken := previous.Identity, previous.IDToken
	if extra := token.Extra("id_token"); extra != nil {
		var ok bool
		idToken, ok = extra.(string)
		if !ok || idToken == "" {
			return OIDCTokens{}, ErrOIDCUnauthorized
		}
		identity, err = c.verifyWeb(ctx, idToken, "")
		if err != nil {
			return OIDCTokens{}, err
		}
		if identity.Subject != previous.Identity.Subject {
			return OIDCTokens{}, ErrOIDCUnauthorized
		}
	}
	if token.RefreshToken == "" {
		token.RefreshToken = previous.RefreshToken
	}
	return c.tokens(token, idToken, identity)
}

func (c *OIDCClient) Revoke(ctx context.Context, refreshToken string) error {
	if strings.TrimSpace(refreshToken) == "" {
		return ErrOIDCUnauthorized
	}
	form := url.Values{"token": {refreshToken}, "token_type_hint": {"refresh_token"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.revocationURL, strings.NewReader(form.Encode()))
	if err != nil {
		return ErrOIDCConfiguration
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(c.oauth.ClientID), url.QueryEscape(c.oauth.ClientSecret))
	response, err := c.http.Do(req)
	if err != nil {
		return ErrOIDCUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(response.Body).Decode(&body)
	return oidcOAuthError(&oauth2.RetrieveError{Response: response, ErrorCode: body.Error})
}

func (c *OIDCClient) EndSessionURL(locale string) (string, error) {
	u, err := url.Parse(c.endSessionURL)
	if err != nil {
		return "", ErrOIDCConfiguration
	}
	query := url.Values{"client_id": {c.oauth.ClientID}, "post_logout_redirect_uri": {c.publicOrigin + "/"}}
	if locale != "" {
		query.Set("ui_locales", locale)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (c *OIDCClient) tokens(token *oauth2.Token, idToken string, identity OIDCIdentity) (OIDCTokens, error) {
	const maxExpirySeconds = int64((1<<63 - 1) / time.Second)
	if token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") || token.ExpiresIn <= 0 || token.ExpiresIn > maxExpirySeconds {
		return OIDCTokens{}, ErrOIDCUnavailable
	}
	return OIDCTokens{
		AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, IDToken: idToken,
		AccessTokenExpiresAt: c.now().Add(time.Duration(token.ExpiresIn) * time.Second), Identity: identity,
	}, nil
}

func validOIDCVerifier(verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, ch := range verifier {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || strings.ContainsRune("-._~", ch) {
			continue
		}
		return false
	}
	return true
}
