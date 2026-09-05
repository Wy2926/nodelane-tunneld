package session

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/redis/go-redis/v9"
)

var (
	ErrNotFound    = errors.New("session record not found")
	ErrExpired     = errors.New("session record expired")
	ErrConflict    = errors.New("session record conflict")
	ErrInvalid     = errors.New("invalid session record")
	ErrUnavailable = errors.New("session store unavailable")
)

type Record struct {
	ID        string `json:"-"`
	AccountID string
	Tokens    identity.OIDCTokens `json:"-"`
	CSRFToken string              `json:"-"`
	CreatedAt time.Time
	ExpiresAt time.Time
	Version   int64
}

type RedisStore struct {
	client *redis.Client
	prefix string
	aead   cipher.AEAD
	now    func() time.Time
}

// The transport types deliberately exclude credentials from JSON. Only these
// private payload types may serialize credential material, before encryption.
type tokenPayload struct {
	AccessToken          string
	RefreshToken         string
	IDToken              string
	AccessTokenExpiresAt time.Time
	Identity             identity.OIDCIdentity
}

type sessionPayload struct {
	AccountID string
	Tokens    tokenPayload
	CSRFToken string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type row struct {
	data    string
	version int64
	expires int64
	binding string
}

type associatedData struct {
	Kind    string
	Key     string
	Version int64
	Expires int64
	Binding string `json:",omitempty"`
}

const maxVersion = int64(1<<53 - 1)

func NewRedisStore(client *redis.Client, prefix string, key []byte, now func() time.Time) (*RedisStore, error) {
	if client == nil || len(key) != 32 || !validPrefix(prefix) {
		return nil, ErrInvalid
	}
	block, err := aes.NewCipher(append([]byte(nil), key...))
	if err != nil {
		return nil, ErrInvalid
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalid
	}
	if now == nil {
		now = time.Now
	}
	return &RedisStore{client: client, prefix: prefix, aead: aead, now: now}, nil
}

func validPrefix(prefix string) bool {
	if len(prefix) > 200 || !strings.Contains(prefix, ":") {
		return false
	}
	for _, part := range strings.Split(prefix, ":") {
		if part == "" {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validIdentifier(value string) bool { return value != "" && len(value) <= 4096 }

func digest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func (s *RedisStore) key(kind, id string) string { return s.prefix + ":" + kind + ":" + digest(id) }

func (s *RedisStore) seal(kind, key string, r row, payload any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil || len(data) > 1<<20 {
		return "", ErrInvalid
	}
	aad, err := json.Marshal(associatedData{Kind: kind, Key: key, Version: r.version, Expires: r.expires, Binding: r.binding})
	if err != nil {
		return "", ErrInvalid
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", ErrUnavailable
	}
	return string(s.aead.Seal(nonce, nonce, data, aad)), nil
}

func (s *RedisStore) open(kind, key string, r row, payload any) error {
	if len(r.data) < s.aead.NonceSize()+s.aead.Overhead() || len(r.data) > (1<<20)+s.aead.NonceSize()+s.aead.Overhead() {
		return ErrInvalid
	}
	aad, err := json.Marshal(associatedData{Kind: kind, Key: key, Version: r.version, Expires: r.expires, Binding: r.binding})
	if err != nil {
		return ErrInvalid
	}
	nonce := []byte(r.data[:s.aead.NonceSize()])
	plaintext, err := s.aead.Open(nil, nonce, []byte(r.data[s.aead.NonceSize():]), aad)
	if err != nil {
		return ErrInvalid
	}
	if err := json.Unmarshal(plaintext, payload); err != nil {
		return ErrInvalid
	}
	return nil
}

func (s *RedisStore) readRow(ctx context.Context, key string) (row, error) {
	values, err := s.client.HGetAll(ctx, key).Result()
	if err != nil {
		return row{}, ErrUnavailable
	}
	if len(values) == 0 {
		return row{}, ErrNotFound
	}
	version, err := strconv.ParseInt(values["version"], 10, 64)
	if err != nil || version < 1 || version > maxVersion {
		return row{}, ErrInvalid
	}
	expires, err := strconv.ParseInt(values["expires"], 10, 64)
	if err != nil || expires < 1 || expires > maxVersion {
		return row{}, ErrInvalid
	}
	if s.now().UnixMilli() >= expires {
		return row{}, ErrExpired
	}
	return row{data: values["data"], version: version, expires: expires, binding: values["binding"]}, nil
}

var createScript = redis.NewScript(`
if redis.call('EXISTS',KEYS[1]) == 1 then return -3 end
redis.call('HSET',KEYS[1],'version',ARGV[1],'expires',ARGV[2],'data',ARGV[3])
if ARGV[5] ~= '' then redis.call('HSET',KEYS[1],'binding',ARGV[5]) end
redis.call('PEXPIRE',KEYS[1],ARGV[4])
return 1
`)

func (s *RedisStore) createRow(ctx context.Context, key string, r row, now time.Time) error {
	result, err := createScript.Run(ctx, s.client, []string{key}, r.version, r.expires, r.data, r.expires-now.UnixMilli(), r.binding).Int()
	if err != nil {
		return ErrUnavailable
	}
	if result == -3 {
		return ErrConflict
	}
	if result != 1 {
		return ErrUnavailable
	}
	return nil
}

func (s *RedisStore) CreateSession(ctx context.Context, record Record) error {
	now := s.now()
	if !validIdentifier(record.ID) || !validIdentifier(record.AccountID) || !validIdentifier(record.CSRFToken) || record.CreatedAt.IsZero() || record.CreatedAt.After(now) {
		return ErrInvalid
	}
	if !record.ExpiresAt.After(now) || record.ExpiresAt.UnixMilli() <= now.UnixMilli() {
		return ErrExpired
	}
	if record.ExpiresAt.UnixMilli() > maxVersion {
		return ErrInvalid
	}
	key := s.key("session", record.ID)
	r := row{version: 1, expires: record.ExpiresAt.UnixMilli()}
	payload := sessionPayload{AccountID: record.AccountID, Tokens: tokenPayload(record.Tokens), CSRFToken: record.CSRFToken, CreatedAt: record.CreatedAt, ExpiresAt: record.ExpiresAt}
	var err error
	r.data, err = s.seal("session", key, r, payload)
	if err != nil {
		return err
	}
	return s.createRow(ctx, key, r, now)
}

func (s *RedisStore) readSession(ctx context.Context, id string) (Record, row, error) {
	if !validIdentifier(id) {
		return Record{}, row{}, ErrInvalid
	}
	key := s.key("session", id)
	r, err := s.readRow(ctx, key)
	if err != nil {
		return Record{}, row{}, err
	}
	var payload sessionPayload
	if err = s.open("session", key, r, &payload); err != nil {
		return Record{}, row{}, err
	}
	if payload.ExpiresAt.UnixMilli() != r.expires || !validIdentifier(payload.AccountID) || !validIdentifier(payload.CSRFToken) || payload.CreatedAt.IsZero() || !payload.ExpiresAt.After(payload.CreatedAt) {
		return Record{}, row{}, ErrInvalid
	}
	if !payload.ExpiresAt.After(s.now()) {
		return Record{}, row{}, ErrExpired
	}
	return Record{ID: id, AccountID: payload.AccountID, Tokens: identity.OIDCTokens(payload.Tokens), CSRFToken: payload.CSRFToken, CreatedAt: payload.CreatedAt, ExpiresAt: payload.ExpiresAt, Version: r.version}, r, nil
}

func (s *RedisStore) ReadSession(ctx context.Context, id string) (Record, error) {
	record, _, err := s.readSession(ctx, id)
	return record, err
}
