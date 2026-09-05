package session

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type RateLimit struct {
	Allowed    bool
	Remaining  int64
	RetryAfter time.Duration
	ResetAt    time.Time
}

const (
	maxRateLimit  = int64(1_000_000)
	minRateWindow = time.Second
	maxRateWindow = 24 * time.Hour
)

var allowScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 or redis.call('PTTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

func (s *RedisStore) Allow(ctx context.Context, bucket, subject string, limit int64, window time.Duration) (RateLimit, error) {
	if !validIdentifier(bucket) || !validIdentifier(subject) || limit < 1 || limit > maxRateLimit || window < minRateWindow || window > maxRateWindow || window%time.Millisecond != 0 {
		return RateLimit{}, ErrInvalid
	}
	now := s.now()
	nowMillis := now.UnixMilli()
	windowMillis := window.Milliseconds()
	if now.IsZero() || nowMillis < 0 || windowMillis < 1 || nowMillis > maxVersion-windowMillis {
		return RateLimit{}, ErrInvalid
	}
	windowIndex := nowMillis / windowMillis
	resetMillis := (windowIndex + 1) * windowMillis
	ttlMillis := resetMillis - nowMillis
	keyMaterial := strconv.Itoa(len(bucket)) + ":" + bucket + strconv.Itoa(len(subject)) + ":" + subject + strconv.FormatInt(windowIndex, 10)
	count, err := allowScript.Run(ctx, s.client, []string{s.key("rate", keyMaterial)}, ttlMillis).Int64()
	if err != nil || count < 1 {
		return RateLimit{}, ErrUnavailable
	}
	remaining := limit - count
	if remaining < 0 {
		remaining = 0
	}
	result := RateLimit{
		Allowed:   count <= limit,
		Remaining: remaining,
		ResetAt:   time.UnixMilli(resetMillis).UTC(),
	}
	if !result.Allowed {
		result.RetryAfter = time.Duration(ttlMillis) * time.Millisecond
	}
	return result, nil
}
