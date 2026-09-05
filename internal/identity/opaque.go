package identity

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
)

var ErrInvalidCredential = errors.New("invalid credential")

type OpaqueCredential struct {
	ID    string
	Token string `json:"-"`
}

func NewRouteID() (string, error) { return NewID("rte_", 16) }
func NewRunID() (string, error)   { return NewID("run_", 16) }

func NewLaunchCredential() (OpaqueCredential, error)     { return newOpaqueCredential("nlc_") }
func NewRunCredential() (OpaqueCredential, error)        { return newOpaqueCredential("nrc_") }
func ParseLaunchCredential(token string) (string, error) { return parseOpaqueCredential(token, "nlc_") }
func ParseRunCredential(token string) (string, error)    { return parseOpaqueCredential(token, "nrc_") }

func newOpaqueCredential(prefix string) (OpaqueCredential, error) {
	id, err := NewID(prefix, 16)
	if err != nil {
		return OpaqueCredential{}, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return OpaqueCredential{}, err
	}
	return OpaqueCredential{ID: id, Token: id + "." + base64.RawURLEncoding.EncodeToString(secret)}, nil
}

func parseOpaqueCredential(token, prefix string) (string, error) {
	if len(token) != 30+1+43 {
		return "", ErrInvalidCredential
	}
	id, encoded, ok := strings.Cut(token, ".")
	if !ok || !validOpaqueID(id, prefix) || len(encoded) != 43 {
		return "", ErrInvalidCredential
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(secret) != 32 || base64.RawURLEncoding.EncodeToString(secret) != encoded {
		return "", ErrInvalidCredential
	}
	return id, nil
}

func validOpaqueID(id, prefix string) bool {
	if len(id) != len(prefix)+26 || !strings.HasPrefix(id, prefix) {
		return false
	}
	for i := len(prefix); i < len(id); i++ {
		if !(id[i] >= 'a' && id[i] <= 'z' || id[i] >= '2' && id[i] <= '7') {
			return false
		}
	}
	return true
}
