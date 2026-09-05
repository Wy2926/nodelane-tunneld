package identity

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type oidcTokenClaims struct {
	jwt.Claims
	ClientID        string          `json:"client_id"`
	Scope           *string         `json:"scope"`
	Nonce           string          `json:"nonce"`
	AuthorizedParty json.RawMessage `json:"azp"`
	Name            string          `json:"name"`
	Email           string          `json:"email"`
}

func (c *OIDCClient) VerifyNative(ctx context.Context, raw string) (OIDCIdentity, error) {
	claims, err := c.verifyClaims(ctx, raw, c.apiResource, "at+jwt")
	if err != nil {
		return OIDCIdentity{}, err
	}
	if claims.ClientID != c.nativeID || claims.Subject == c.nativeID || claims.Scope == nil {
		return OIDCIdentity{}, ErrOIDCUnauthorized
	}
	scopes, err := oidcScopes(*claims.Scope)
	if err != nil {
		return OIDCIdentity{}, err
	}
	identity := claims.identity(c.nativeID)
	identity.Scopes = scopes
	return identity, nil
}

func (c *OIDCClient) verifyWeb(ctx context.Context, raw, nonce string) (OIDCIdentity, error) {
	claims, err := c.verifyClaims(ctx, raw, c.oauth.ClientID, "", "JWT")
	if err != nil {
		return OIDCIdentity{}, err
	}
	if len(claims.Audience) > 1 || len(claims.AuthorizedParty) > 0 {
		var party string
		if json.Unmarshal(claims.AuthorizedParty, &party) != nil || party != c.oauth.ClientID {
			return OIDCIdentity{}, ErrOIDCUnauthorized
		}
	}
	if nonce != "" && subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonce)) != 1 {
		return OIDCIdentity{}, ErrOIDCUnauthorized
	}
	return claims.identity(c.oauth.ClientID), nil
}

func (c *OIDCClient) verifyClaims(ctx context.Context, raw, audience string, allowedTokenTypes ...string) (oidcTokenClaims, error) {
	var claims oidcTokenClaims
	if raw == "" || len(raw) > oidcMaxResponseBytes {
		return claims, ErrOIDCUnauthorized
	}
	jws, err := jose.ParseSignedCompact(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil || len(jws.Signatures) != 1 || jws.Signatures[0].Protected.Algorithm != string(jose.RS256) {
		return claims, ErrOIDCUnauthorized
	}
	if !oidcTokenTypeAllowed(jws.Signatures[0].Protected.ExtraHeaders[jose.HeaderType], allowedTokenTypes) {
		return claims, ErrOIDCUnauthorized
	}
	payload, err := c.keys.VerifySignature(ctx, raw)
	if err != nil {
		// The pinned RemoteKeySet wraps fetch failures, but returns an unwrapped
		// error for an invalid signature. Never propagate either provider message.
		if errors.Unwrap(err) != nil || ctx.Err() != nil {
			return claims, ErrOIDCUnavailable
		}
		return claims, ErrOIDCUnauthorized
	}
	if json.Unmarshal(payload, &claims) != nil {
		return oidcTokenClaims{}, ErrOIDCUnauthorized
	}
	now := c.now()
	if claims.Issuer != c.issuer || !claims.Audience.Contains(audience) || strings.TrimSpace(claims.Subject) == "" ||
		claims.Expiry == nil || !claims.Expiry.Time().After(now) || claims.IssuedAt == nil ||
		claims.IssuedAt.Time().After(now.Add(30*time.Second)) ||
		(claims.NotBefore != nil && claims.NotBefore.Time().After(now.Add(30*time.Second))) {
		return oidcTokenClaims{}, ErrOIDCUnauthorized
	}
	return claims, nil
}

func oidcTokenTypeAllowed(raw any, allowed []string) bool {
	tokenType := ""
	if raw != nil {
		var ok bool
		tokenType, ok = raw.(string)
		if !ok {
			return false
		}
	}
	for _, allowedType := range allowed {
		if tokenType == allowedType {
			return true
		}
	}
	return false
}

func (c oidcTokenClaims) identity(clientID string) OIDCIdentity {
	return OIDCIdentity{
		Issuer: c.Issuer, Subject: c.Subject, ClientID: clientID, ExpiresAt: c.Expiry.Time(), Name: c.Name, Email: c.Email,
	}
}

func oidcScopes(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	scopes := strings.Split(raw, " ")
	for _, scope := range scopes {
		if scope == "" {
			return nil, ErrOIDCUnauthorized
		}
		for _, ch := range scope {
			if ch < 0x21 || ch > 0x7e || ch == '"' || ch == '\\' {
				return nil, ErrOIDCUnauthorized
			}
		}
	}
	return scopes, nil
}
