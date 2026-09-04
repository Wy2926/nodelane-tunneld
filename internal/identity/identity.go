package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
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

func NewClientCredential() (clientID, tokenID, token string, err error) {
	clientID, err = NewID("cli_", 10)
	if err != nil {
		return "", "", "", err
	}
	tokenID, err = NewID("ctk_", 10)
	if err != nil {
		return "", "", "", err
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	token = "ftc." + tokenID + "." + secret
	return
}

func ParseClientToken(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "ftc" || !strings.HasPrefix(parts[1], "ctk_") || len(parts[2]) < 32 {
		return "", errors.New("invalid client token")
	}
	return parts[1], nil
}

func HashToken(pepper, token string) string {
	h := hmac.New(sha256.New, []byte(pepper))
	_, _ = h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}

func TokenHashEqual(expected, actual string) bool {
	return hmac.Equal([]byte(expected), []byte(actual))
}

var slugWords = []string{
	"amber", "blue", "brisk", "calm", "clear", "cool", "coral", "fast",
	"fresh", "green", "happy", "kind", "lucky", "mellow", "quiet", "rapid",
	"silver", "soft", "solar", "swift", "tiny", "warm", "wild", "young",
	"badger", "bear", "cedar", "comet", "dolphin", "falcon", "fox", "heron",
	"koala", "maple", "otter", "panda", "pine", "raven", "river", "sparrow",
	"star", "tiger", "wave", "willow", "wolf", "wren", "yak", "zebra",
}

func NewSlug() (string, error) {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	left := slugWords[int(b[0])%24]
	right := slugWords[24+int(b[1])%24]
	suffix := hex.EncodeToString(b[2:])
	return left + "-" + right + "-" + suffix, nil
}
