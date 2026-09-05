package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

var (
	ErrInvalidReplayKey        = errors.New("invalid replay key")
	ErrInvalidReplayContext    = errors.New("invalid replay context")
	ErrInvalidReplayCiphertext = errors.New("invalid replay ciphertext")
	ErrReplayExpired           = errors.New("replay expired")
)

type ReplayContext struct {
	Operation    string    `json:"operation"`
	PrincipalKey string    `json:"principal_key"`
	KeyHash      string    `json:"key_hash"`
	RequestHash  string    `json:"request_hash"`
	RouteID      string    `json:"route_id"`
	RunID        string    `json:"run_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type ReplayCipher struct{ aead cipher.AEAD }

func NewReplayCipher(key []byte) (*ReplayCipher, error) {
	if len(key) != 32 {
		return nil, ErrInvalidReplayKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrInvalidReplayKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalidReplayKey
	}
	return &ReplayCipher{aead: aead}, nil
}

func (c *ReplayCipher) Seal(ctx ReplayContext, plaintext []byte) ([]byte, error) {
	aad, err := replayAAD(ctx)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plaintext, aad), nil
}

func (c *ReplayCipher) Open(ctx ReplayContext, ciphertext []byte, now time.Time) ([]byte, error) {
	aad, err := replayAAD(ctx)
	if err != nil {
		return nil, err
	}
	if !now.Before(ctx.ExpiresAt) {
		return nil, ErrReplayExpired
	}
	nonceSize := c.aead.NonceSize()
	if len(ciphertext) < nonceSize+c.aead.Overhead() {
		return nil, ErrInvalidReplayCiphertext
	}
	plaintext, err := c.aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], aad)
	if err != nil {
		return nil, ErrInvalidReplayCiphertext
	}
	return plaintext, nil
}

func replayAAD(ctx ReplayContext) ([]byte, error) {
	switch ctx.Operation {
	case domain.OperationCreateRoute:
	case domain.OperationStartRun, domain.OperationRedeemLaunch:
		if ctx.RunID == "" {
			return nil, ErrInvalidReplayContext
		}
	default:
		return nil, ErrInvalidReplayContext
	}
	if len(ctx.PrincipalKey) == 0 || len(ctx.PrincipalKey) > 256 || !utf8.ValidString(ctx.PrincipalKey) ||
		!validReplayHash(ctx.KeyHash) || !validReplayHash(ctx.RequestHash) ||
		!validOpaqueID(ctx.RouteID, "rte_") || ctx.RunID != "" && !validOpaqueID(ctx.RunID, "run_") ||
		ctx.ExpiresAt.IsZero() || ctx.ExpiresAt.Nanosecond()%1000 != 0 {
		return nil, ErrInvalidReplayContext
	}
	ctx.ExpiresAt = ctx.ExpiresAt.UTC()
	aad, err := json.Marshal(ctx)
	if err != nil {
		return nil, ErrInvalidReplayContext
	}
	return aad, nil
}

func validReplayHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for i := range hash {
		if !(hash[i] >= '0' && hash[i] <= '9' || hash[i] >= 'a' && hash[i] <= 'f') {
			return false
		}
	}
	return true
}
