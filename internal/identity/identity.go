package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"strings"
)

var rawBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func NewID(prefix string, bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + strings.ToLower(rawBase32.EncodeToString(b)), nil
}

func HashToken(pepper, token string) string {
	h := hmac.New(sha256.New, []byte(pepper))
	_, _ = h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}

func TokenHashEqual(expected, actual string) bool {
	return hmac.Equal([]byte(expected), []byte(actual))
}
