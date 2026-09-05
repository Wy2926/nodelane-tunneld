package identity

import (
	"errors"
	"time"
)

var (
	ErrOIDCUnauthorized  = errors.New("OIDC authentication rejected")
	ErrOIDCUnavailable   = errors.New("OIDC provider unavailable")
	ErrOIDCConfiguration = errors.New("invalid OIDC configuration")
)

type OIDCIdentity struct {
	Issuer    string    `json:"issuer"`
	Subject   string    `json:"subject"`
	ClientID  string    `json:"client_id"`
	Scopes    []string  `json:"scopes,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	Name      string    `json:"name,omitempty"`
	Email     string    `json:"email,omitempty"`
}

type OIDCTokens struct {
	AccessToken          string `json:"-"`
	RefreshToken         string `json:"-"`
	IDToken              string `json:"-"`
	AccessTokenExpiresAt time.Time
	Identity             OIDCIdentity
}
