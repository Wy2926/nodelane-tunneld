package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type TunnelClaims struct {
	Issuer     string `json:"iss"`
	Audience   string `json:"aud"`
	Subject    string `json:"sub"`
	SessionID  string `json:"sid"`
	TokenID    string `json:"jti"`
	Node       string `json:"node"`
	Protocol   string `json:"protocol"`
	ProxyName  string `json:"proxy_name"`
	Subdomain  string `json:"subdomain,omitempty"`
	RemotePort int    `json:"remote_port,omitempty"`
	IssuedAt   int64  `json:"iat"`
	ExpiresAt  int64  `json:"exp"`
}

var jwtEncoding = base64.RawURLEncoding

func SignTunnelToken(secret []byte, claims TunnelClaims) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := jwtEncoding.EncodeToString(header) + "." + jwtEncoding.EncodeToString(payload)
	sig := signJWT(secret, unsigned)
	return unsigned + "." + jwtEncoding.EncodeToString(sig), nil
}

func VerifyTunnelToken(secret []byte, token, issuer, audience string, now time.Time) (TunnelClaims, error) {
	var claims TunnelClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims, errors.New("invalid tunnel token")
	}
	unsigned := parts[0] + "." + parts[1]
	sig, err := jwtEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, signJWT(secret, unsigned)) {
		return claims, errors.New("invalid tunnel token signature")
	}
	payload, err := jwtEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &claims) != nil {
		return claims, errors.New("invalid tunnel token payload")
	}
	if claims.Issuer != issuer || claims.Audience != audience {
		return claims, errors.New("invalid tunnel token issuer or audience")
	}
	unix := now.Unix()
	if claims.ExpiresAt <= unix || claims.IssuedAt > unix+30 {
		return claims, errors.New("tunnel token expired or not active")
	}
	if claims.Subject == "" || claims.SessionID == "" || claims.TokenID == "" || claims.ProxyName == "" {
		return claims, fmt.Errorf("incomplete tunnel token claims")
	}
	return claims, nil
}

func signJWT(secret []byte, unsigned string) []byte {
	h := hmac.New(sha256.New, secret)
	_, _ = h.Write([]byte(unsigned))
	return h.Sum(nil)
}
