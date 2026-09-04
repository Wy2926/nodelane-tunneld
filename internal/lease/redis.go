package lease

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/redis/go-redis/v9"
)

type Redis struct {
	client *redis.Client
	prefix string
}

func OpenRedis(ctx context.Context, address, password, prefix string) (*Redis, error) {
	client := redis.NewClient(&redis.Options{Addr: address, Password: password})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect redis: %w", err)
	}
	return &Redis{client: client, prefix: prefix}, nil
}

var reserveScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local expires = tonumber(ARGV[2])
local tunnel = ARGV[3]
local resourceValue = ARGV[4]
local maxClient = tonumber(ARGV[5])
local maxIP = tonumber(ARGV[6])
local ttl = math.max(1, expires - now)

redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)

if maxClient > 0 and redis.call('ZCARD', KEYS[1]) >= maxClient then return -1 end
if maxIP > 0 and redis.call('ZCARD', KEYS[2]) >= maxIP then return -2 end
if redis.call('EXISTS', KEYS[3]) == 1 then return -3 end

redis.call('ZADD', KEYS[1], expires, tunnel)
redis.call('PEXPIRE', KEYS[1], ttl)
redis.call('ZADD', KEYS[2], expires, tunnel)
redis.call('PEXPIRE', KEYS[2], ttl)
redis.call('SET', KEYS[3], resourceValue, 'PX', ttl)
return 1
`)

var releaseScript = redis.NewScript(`
local tunnel = ARGV[1]
redis.call('ZREM', KEYS[1], tunnel)
redis.call('ZREM', KEYS[2], tunnel)
if redis.call('GET', KEYS[3]) == tunnel then redis.call('DEL', KEYS[3]) end
return 1
`)

func (r *Redis) Reserve(ctx context.Context, clientID, ipKey, tunnelID, resourceKey string, expiresAt time.Time, maxPerClient, maxPerIP int) error {
	keys := r.keys(clientID, ipKey, resourceKey)
	result, err := reserveScript.Run(ctx, r.client, keys,
		time.Now().UnixMilli(), expiresAt.UnixMilli(), tunnelID, tunnelID, maxPerClient, maxPerIP).Int()
	if err != nil {
		return err
	}
	switch result {
	case -1, -2:
		return domain.ErrLimitReached
	case -3:
		return domain.ErrConflict
	case 1:
		return nil
	default:
		return fmt.Errorf("unexpected redis reserve result: %d", result)
	}
}

func (r *Redis) Release(ctx context.Context, clientID, ipKey, tunnelID, resourceKey string) error {
	keys := r.keys(clientID, ipKey, resourceKey)
	_, err := releaseScript.Run(ctx, r.client, keys, tunnelID).Result()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	return err
}

func (r *Redis) Close() error { return r.client.Close() }

func (r *Redis) keys(clientID, ipKey, resourceKey string) []string {
	return []string{
		r.prefix + ":active:client:" + clientID,
		r.prefix + ":active:ip:" + ipKey,
		r.prefix + ":resource:" + resourceKey,
	}
}
