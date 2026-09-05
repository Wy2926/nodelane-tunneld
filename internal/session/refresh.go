package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strconv"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/redis/go-redis/v9"
)

type RefreshLease struct {
	SessionID string `json:"-"`
	Version   int64
	Token     string `json:"-"`
	ExpiresAt time.Time
}

const maxRefreshLeaseLifetime = 30 * time.Second

var acquireRefreshScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return -1 end
if redis.call('HGET', KEYS[1], 'version') ~= ARGV[1] then return -2 end
local expires = tonumber(redis.call('HGET', KEYS[1], 'expires'))
if not expires then return -5 end
if expires <= tonumber(ARGV[4]) then return -4 end
if redis.call('SET', KEYS[2], ARGV[2], 'NX', 'PX', ARGV[3]) == false then return -3 end
return 1
`)

func (s *RedisStore) AcquireRefresh(ctx context.Context, id string, version int64, ttl time.Duration) (RefreshLease, error) {
	if !validIdentifier(id) || version < 1 || version > maxVersion || ttl < time.Millisecond || ttl > maxRefreshLeaseLifetime || ttl%time.Millisecond != 0 {
		return RefreshLease{}, ErrInvalid
	}
	record, _, err := s.readSession(ctx, id)
	if err != nil {
		return RefreshLease{}, err
	}
	if record.Version != version {
		return RefreshLease{}, ErrConflict
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return RefreshLease{}, ErrUnavailable
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	owner := digest(token) + ":" + strconv.FormatInt(version, 10)
	now := s.now()
	result, err := acquireRefreshScript.Run(
		ctx,
		s.client,
		[]string{s.key("session", id), s.key("refresh", id)},
		version,
		owner,
		ttl.Milliseconds(),
		now.UnixMilli(),
	).Int()
	if err != nil {
		return RefreshLease{}, ErrUnavailable
	}
	switch result {
	case 1:
		return RefreshLease{SessionID: id, Version: version, Token: token, ExpiresAt: now.Add(ttl)}, nil
	case -1:
		return RefreshLease{}, ErrNotFound
	case -2, -3:
		return RefreshLease{}, ErrConflict
	case -4:
		return RefreshLease{}, ErrExpired
	default:
		return RefreshLease{}, ErrInvalid
	}
}

var commitRefreshScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return -1 end
local expires = tonumber(redis.call('HGET', KEYS[1], 'expires'))
if not expires then return -5 end
if expires <= tonumber(ARGV[5]) then return -4 end
if redis.call('HGET', KEYS[1], 'version') ~= ARGV[1] then return -2 end
if redis.call('EXISTS', KEYS[2]) == 0 or redis.call('PTTL', KEYS[2]) <= 0 then return -1 end
if redis.call('GET', KEYS[2]) ~= ARGV[2] then return -3 end
redis.call('HSET', KEYS[1], 'version', ARGV[3], 'data', ARGV[4])
redis.call('DEL', KEYS[2])
return 1
`)

func (s *RedisStore) CommitRefresh(ctx context.Context, lease RefreshLease, tokens identity.OIDCTokens) (Record, error) {
	if !validRefreshLease(lease) {
		return Record{}, ErrInvalid
	}
	now := s.now()
	if !lease.ExpiresAt.After(now) {
		return Record{}, ErrExpired
	}
	record, oldRow, err := s.readSession(ctx, lease.SessionID)
	if err != nil {
		return Record{}, err
	}
	if record.Version != lease.Version || lease.Version == maxVersion {
		return Record{}, ErrConflict
	}
	record.Tokens = tokens
	record.Version++
	newRow := row{version: record.Version, expires: oldRow.expires}
	payload := sessionPayload{
		AccountID: record.AccountID,
		Tokens:    tokenPayload(record.Tokens),
		CSRFToken: record.CSRFToken,
		CreatedAt: record.CreatedAt,
		ExpiresAt: record.ExpiresAt,
	}
	key := s.key("session", lease.SessionID)
	newRow.data, err = s.seal("session", key, newRow, payload)
	if err != nil {
		return Record{}, err
	}
	owner := digest(lease.Token) + ":" + strconv.FormatInt(lease.Version, 10)
	result, err := commitRefreshScript.Run(
		ctx,
		s.client,
		[]string{key, s.key("refresh", lease.SessionID)},
		lease.Version,
		owner,
		record.Version,
		newRow.data,
		now.UnixMilli(),
	).Int()
	if err != nil {
		return Record{}, ErrUnavailable
	}
	switch result {
	case 1:
		return record, nil
	case -1:
		return Record{}, ErrNotFound
	case -2, -3:
		return Record{}, ErrConflict
	case -4:
		return Record{}, ErrExpired
	default:
		return Record{}, ErrInvalid
	}
}

var releaseRefreshScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return -1 end
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return -3 end
redis.call('DEL', KEYS[1])
return 1
`)

func (s *RedisStore) ReleaseRefresh(ctx context.Context, lease RefreshLease) error {
	if !validRefreshLease(lease) {
		return ErrInvalid
	}
	owner := digest(lease.Token) + ":" + strconv.FormatInt(lease.Version, 10)
	result, err := releaseRefreshScript.Run(ctx, s.client, []string{s.key("refresh", lease.SessionID)}, owner).Int()
	if err != nil {
		return ErrUnavailable
	}
	switch result {
	case 1:
		return nil
	case -1:
		return ErrNotFound
	case -3:
		return ErrConflict
	default:
		return ErrInvalid
	}
}

func validRefreshLease(lease RefreshLease) bool {
	return validIdentifier(lease.SessionID) && validIdentifier(lease.Token) && lease.Version >= 1 && lease.Version <= maxVersion && !lease.ExpiresAt.IsZero()
}

var deleteSessionScript = redis.NewScript(`
local existed = redis.call('EXISTS', KEYS[1])
redis.call('DEL', KEYS[1], KEYS[2])
return existed
`)

func (s *RedisStore) DeleteSession(ctx context.Context, id string) error {
	if !validIdentifier(id) {
		return ErrInvalid
	}
	result, err := deleteSessionScript.Run(ctx, s.client, []string{s.key("session", id), s.key("refresh", id)}).Int()
	if err != nil {
		return ErrUnavailable
	}
	if result == 0 {
		return ErrNotFound
	}
	if result != 1 {
		return ErrUnavailable
	}
	return nil
}
