package anonymous

import "github.com/redis/go-redis/v9"

var allocateScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local connect_deadline = tonumber(ARGV[2])
local hard_expires = tonumber(ARGV[3])
local replay_expires = tonumber(ARGV[4])
local rate_window = tonumber(ARGV[5])
local install_active_max = tonumber(ARGV[6])
local network_active_max = tonumber(ARGV[7])
local install_rate_max = tonumber(ARGV[8])
local network_rate_max = tonumber(ARGV[9])

if redis.call('GET', KEYS[1]) ~= 'anonymous_resources_verified_v1' then
  return {-1}
end

if redis.call('EXISTS', KEYS[2]) == 1 then
  local stored_hash = redis.call('HGET', KEYS[2], 'request_hash')
  if not stored_hash or stored_hash ~= ARGV[11] then
    return {-2}
  end
  local stored_run_key = redis.call('HGET', KEYS[2], 'run_key')
  local stored_run_id = redis.call('HGET', KEYS[2], 'run_id')
  local stored_expires = tonumber(redis.call('HGET', KEYS[2], 'expires_at'))
  local stored_ciphertext = redis.call('HGET', KEYS[2], 'ciphertext')
  if not stored_run_key or string.len(stored_run_key) ~= string.len(ARGV[22]) + 64 or
     string.sub(stored_run_key, 1, string.len(ARGV[22])) ~= ARGV[22] or
     not stored_run_id or not stored_expires or not stored_ciphertext then
    return {-8}
  end
  if now >= stored_expires then
    local state = redis.call('HGET', stored_run_key, 'state')
    if state and state ~= 'released' then
      redis.call('HSET', stored_run_key, 'state', 'verifying', 'desired_state', 'stopped')
      redis.call('ZADD', KEYS[9], now, stored_run_key)
    end
    return {-9}
  end
  local state = redis.call('HGET', stored_run_key, 'state')
  local current_hard_expires = tonumber(redis.call('HGET', stored_run_key, 'hard_expires_at'))
  if redis.call('HGET', stored_run_key, 'run_id') ~= stored_run_id or redis.call('HGET', stored_run_key, 'replay_key') ~= KEYS[2] then
    return {-10}
  end
  if not state or not current_hard_expires or now >= current_hard_expires then
    if state and state ~= 'released' then
      redis.call('HSET', stored_run_key, 'state', 'verifying', 'desired_state', 'stopped')
      redis.call('ZADD', KEYS[9], now, stored_run_key)
    end
    return {-9}
  end
  if state == 'reserved' then
    local current_connect_deadline = tonumber(redis.call('HGET', stored_run_key, 'connect_deadline_at'))
    if not current_connect_deadline or now >= current_connect_deadline then
      redis.call('HSET', stored_run_key, 'state', 'verifying', 'desired_state', 'stopped')
      redis.call('ZADD', KEYS[9], now, stored_run_key)
      return {-9}
    end
  elseif state == 'online' then
    local current_lease_expires = tonumber(redis.call('HGET', stored_run_key, 'lease_expires_at'))
    if not current_lease_expires or now >= current_lease_expires then
      redis.call('HSET', stored_run_key, 'state', 'verifying', 'desired_state', 'stopped')
      redis.call('ZADD', KEYS[9], now, stored_run_key)
      return {-9}
    end
  else
    return {-8}
  end
  return {2, stored_run_id, stored_expires, stored_hash, stored_ciphertext,
    redis.call('HGET', stored_run_key, 'credential_id'),
    redis.call('HGET', stored_run_key, 'credential_hash'),
    redis.call('HGET', stored_run_key, 'proxy_name'),
    redis.call('HGET', stored_run_key, 'protocol'),
    redis.call('HGET', stored_run_key, 'public_endpoint'),
    redis.call('HGET', stored_run_key, 'created_at'),
    redis.call('HGET', stored_run_key, 'connect_deadline_at'),
    redis.call('HGET', stored_run_key, 'hard_expires_at')}
end

redis.call('ZREMRANGEBYSCORE', KEYS[5], '-inf', now - rate_window)
redis.call('ZREMRANGEBYSCORE', KEYS[6], '-inf', now - rate_window)

if redis.call('SCARD', KEYS[3]) >= install_active_max then
  return {-3}
end
if redis.call('SCARD', KEYS[4]) >= network_active_max then
  return {-4}
end
if redis.call('ZCARD', KEYS[5]) >= install_rate_max then
  local oldest = redis.call('ZRANGE', KEYS[5], 0, 0, 'WITHSCORES')
  return {-5, oldest[2]}
end
if redis.call('ZCARD', KEYS[6]) >= network_rate_max then
  local oldest = redis.call('ZRANGE', KEYS[6], 0, 0, 'WITHSCORES')
  return {-6, oldest[2]}
end
if redis.call('EXISTS', KEYS[7]) == 1 or redis.call('EXISTS', KEYS[8]) == 1 or redis.call('EXISTS', KEYS[10]) == 1 then
  return {-7}
end

redis.call('HSET', KEYS[8],
  'run_id', ARGV[10],
  'request_hash', ARGV[11],
  'credential_id', ARGV[13],
  'credential_hash', ARGV[14],
  'proxy_name', ARGV[15],
  'protocol', ARGV[16],
  'public_endpoint', ARGV[17],
  'installation_hash', ARGV[18],
  'network_hash', ARGV[19],
  'state', 'reserved',
  'desired_state', 'running',
  'created_at', ARGV[20],
  'connect_deadline_at', ARGV[2],
  'lease_expires_at', '0',
  'hard_expires_at', ARGV[3],
  'resource_key', KEYS[7],
  'proxy_key', KEYS[10],
  'installation_active_key', KEYS[3],
  'network_active_key', KEYS[4],
  'replay_key', KEYS[2])
redis.call('SET', KEYS[7], ARGV[10])
redis.call('SET', KEYS[10], ARGV[10])
redis.call('SADD', KEYS[3], ARGV[10])
redis.call('SADD', KEYS[4], ARGV[10])
redis.call('ZADD', KEYS[5], now, ARGV[10])
redis.call('ZADD', KEYS[6], now, ARGV[10])
redis.call('PEXPIRE', KEYS[5], rate_window)
redis.call('PEXPIRE', KEYS[6], rate_window)
redis.call('ZADD', KEYS[9], connect_deadline, KEYS[8])
redis.call('HSET', KEYS[2],
  'request_hash', ARGV[11],
  'run_key', KEYS[8],
  'run_id', ARGV[10],
  'expires_at', ARGV[4],
  'ciphertext', ARGV[12])
redis.call('PEXPIRE', KEYS[2], ARGV[21])

return {1, ARGV[10], ARGV[4], ARGV[11], ARGV[12], ARGV[13], ARGV[14],
  ARGV[15], ARGV[16], ARGV[17], ARGV[20], ARGV[2], ARGV[3]}
`)

var runOperationScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local action = ARGV[2]
if redis.call('EXISTS', KEYS[1]) == 0 then
  return {-1}
end
if redis.call('HGET', KEYS[1], 'credential_id') ~= ARGV[3] or
   redis.call('HGET', KEYS[1], 'credential_hash') ~= ARGV[4] then
  return {-2}
end
if ARGV[5] ~= '' and redis.call('HGET', KEYS[1], 'proxy_name') ~= ARGV[5] then
  return {-2}
end

local state = redis.call('HGET', KEYS[1], 'state')
if state ~= 'reserved' and state ~= 'online' and state ~= 'stopping' and state ~= 'verifying' and state ~= 'released' then
  return {-6}
end
local hard_expires = tonumber(redis.call('HGET', KEYS[1], 'hard_expires_at'))
local expired = not hard_expires or now >= hard_expires
if state == 'reserved' then
  local connect_deadline = tonumber(redis.call('HGET', KEYS[1], 'connect_deadline_at'))
  expired = expired or not connect_deadline or now >= connect_deadline
elseif state == 'online' then
  local lease_expires = tonumber(redis.call('HGET', KEYS[1], 'lease_expires_at'))
  expired = expired or not lease_expires or now >= lease_expires
end

if action == 'stop' then
  if state == 'released' then
    return {-5}
  end
  if expired and state ~= 'stopping' and state ~= 'verifying' then
    state = 'verifying'
  elseif state ~= 'stopping' and state ~= 'verifying' then
    state = 'stopping'
  end
  redis.call('HSET', KEYS[1], 'state', state, 'desired_state', 'stopped')
  redis.call('ZADD', KEYS[2], now, KEYS[1])
elseif state == 'stopping' or state == 'verifying' or state == 'released' then
  return {-5}
elseif expired then
  redis.call('HSET', KEYS[1], 'state', 'verifying', 'desired_state', 'stopped')
  redis.call('ZADD', KEYS[2], now, KEYS[1])
  return {-4}
elseif action == 'heartbeat' then
  if state == 'online' then
    local lease_expires = now + tonumber(ARGV[6])
    if lease_expires > hard_expires then lease_expires = hard_expires end
    redis.call('HSET', KEYS[1], 'lease_expires_at', lease_expires)
    redis.call('ZADD', KEYS[2], lease_expires, KEYS[1])
  elseif state ~= 'reserved' then
    return {-3}
  end
elseif action ~= 'authorize' then
  return {-3}
end

return {1,
  redis.call('HGET', KEYS[1], 'run_id'),
  redis.call('HGET', KEYS[1], 'proxy_name'),
  redis.call('HGET', KEYS[1], 'public_endpoint'),
  redis.call('HGET', KEYS[1], 'protocol'),
  redis.call('HGET', KEYS[1], 'state'),
  redis.call('HGET', KEYS[1], 'desired_state'),
  redis.call('HGET', KEYS[1], 'created_at'),
  redis.call('HGET', KEYS[1], 'connect_deadline_at'),
  redis.call('HGET', KEYS[1], 'lease_expires_at'),
  redis.call('HGET', KEYS[1], 'hard_expires_at')}
`)

var markConnectedScript = redis.NewScript(`
local now = tonumber(ARGV[1])
if redis.call('EXISTS', KEYS[1]) == 0 then
  return {-1}
end
if redis.call('HGET', KEYS[1], 'run_id') ~= ARGV[2] or redis.call('HGET', KEYS[1], 'proxy_name') ~= ARGV[3] then
  return {-2}
end
local state = redis.call('HGET', KEYS[1], 'state')
if state == 'stopping' or state == 'verifying' or state == 'released' then
  return {-3}
end
local hard_expires = tonumber(redis.call('HGET', KEYS[1], 'hard_expires_at'))
if not hard_expires or now >= hard_expires then
  redis.call('HSET', KEYS[1], 'state', 'verifying', 'desired_state', 'stopped')
  redis.call('ZADD', KEYS[2], now, KEYS[1])
  return {-4}
end
if state == 'reserved' then
  local connect_deadline = tonumber(redis.call('HGET', KEYS[1], 'connect_deadline_at'))
  if not connect_deadline or now >= connect_deadline then
    redis.call('HSET', KEYS[1], 'state', 'verifying', 'desired_state', 'stopped')
    redis.call('ZADD', KEYS[2], now, KEYS[1])
    return {-4}
  end
  local lease_expires = now + tonumber(ARGV[4])
  if lease_expires > hard_expires then lease_expires = hard_expires end
  redis.call('HSET', KEYS[1], 'state', 'online', 'lease_expires_at', lease_expires)
  redis.call('ZADD', KEYS[2], lease_expires, KEYS[1])
elseif state == 'online' then
  local lease_expires = tonumber(redis.call('HGET', KEYS[1], 'lease_expires_at'))
  if not lease_expires or now >= lease_expires then
    redis.call('HSET', KEYS[1], 'state', 'verifying', 'desired_state', 'stopped')
    redis.call('ZADD', KEYS[2], now, KEYS[1])
    return {-4}
  end
else
  return {-3}
end
return {1,
  redis.call('HGET', KEYS[1], 'run_id'),
  redis.call('HGET', KEYS[1], 'proxy_name'),
  redis.call('HGET', KEYS[1], 'public_endpoint'),
  redis.call('HGET', KEYS[1], 'protocol'),
  redis.call('HGET', KEYS[1], 'state'),
  redis.call('HGET', KEYS[1], 'desired_state'),
  redis.call('HGET', KEYS[1], 'created_at'),
  redis.call('HGET', KEYS[1], 'connect_deadline_at'),
  redis.call('HGET', KEYS[1], 'lease_expires_at'),
  redis.call('HGET', KEYS[1], 'hard_expires_at')}
`)

var confirmReleasedScript = redis.NewScript(`
local now = tonumber(ARGV[1])
if redis.call('EXISTS', KEYS[1]) == 0 then
  return {-1}
end
if redis.call('HGET', KEYS[1], 'run_id') ~= ARGV[2] or redis.call('HGET', KEYS[1], 'proxy_name') ~= ARGV[3] then
  return {-2}
end
local state = redis.call('HGET', KEYS[1], 'state')
if state == 'released' then
  return {1}
end
local hard_expires = tonumber(redis.call('HGET', KEYS[1], 'hard_expires_at'))
local expired = not hard_expires or now >= hard_expires
if state == 'reserved' then
  local connect_deadline = tonumber(redis.call('HGET', KEYS[1], 'connect_deadline_at'))
  expired = expired or not connect_deadline or now >= connect_deadline
elseif state == 'online' then
  local lease_expires = tonumber(redis.call('HGET', KEYS[1], 'lease_expires_at'))
  expired = expired or not lease_expires or now >= lease_expires
end
if state ~= 'stopping' and state ~= 'verifying' and not expired then
  return {-3}
end

local resource_key = redis.call('HGET', KEYS[1], 'resource_key')
local proxy_key = redis.call('HGET', KEYS[1], 'proxy_key')
local installation_key = redis.call('HGET', KEYS[1], 'installation_active_key')
local network_key = redis.call('HGET', KEYS[1], 'network_active_key')
local replay_key = redis.call('HGET', KEYS[1], 'replay_key')
local protocol = redis.call('HGET', KEYS[1], 'protocol')
local resource_prefix = ARGV[5] .. protocol .. ':'
if not resource_key or string.len(resource_key) ~= string.len(resource_prefix) + 64 or string.sub(resource_key, 1, string.len(resource_prefix)) ~= resource_prefix or
   not proxy_key or string.len(proxy_key) ~= string.len(ARGV[6]) + 64 or string.sub(proxy_key, 1, string.len(ARGV[6])) ~= ARGV[6] or
   not installation_key or string.len(installation_key) ~= string.len(ARGV[7]) + 64 or string.sub(installation_key, 1, string.len(ARGV[7])) ~= ARGV[7] or
   not network_key or string.len(network_key) ~= string.len(ARGV[8]) + 64 or string.sub(network_key, 1, string.len(ARGV[8])) ~= ARGV[8] or
   not replay_key or string.len(replay_key) ~= string.len(ARGV[9]) + 64 or string.sub(replay_key, 1, string.len(ARGV[9])) ~= ARGV[9] then
  return {-4}
end
if redis.call('GET', resource_key) ~= ARGV[2] or redis.call('GET', proxy_key) ~= ARGV[2] or
   redis.call('SISMEMBER', installation_key, ARGV[2]) ~= 1 or redis.call('SISMEMBER', network_key, ARGV[2]) ~= 1 then
  return {-4}
end
local replay_exists = redis.call('EXISTS', replay_key)
local created_at = tonumber(redis.call('HGET', KEYS[1], 'created_at'))
if not created_at or (now < created_at + tonumber(ARGV[4]) and replay_exists ~= 1) then
  return {-4}
end
if replay_exists == 1 and (redis.call('HGET', replay_key, 'run_id') ~= ARGV[2] or redis.call('HGET', replay_key, 'run_key') ~= KEYS[1]) then
  return {-4}
end
redis.call('DEL', resource_key)
redis.call('DEL', proxy_key)
redis.call('SREM', installation_key, ARGV[2])
redis.call('SREM', network_key, ARGV[2])
redis.call('ZREM', KEYS[2], KEYS[1])
-- Keep the replay identity and its original TTL as a terminal tombstone so a
-- retry cannot become a second successful allocation. Removing ciphertext
-- makes the stopped credential unrecoverable without extending the window.
if replay_exists == 1 then redis.call('HDEL', replay_key, 'ciphertext') end
redis.call('HSET', KEYS[1], 'state', 'released', 'desired_state', 'stopped', 'released_at', now)
redis.call('PEXPIRE', KEYS[1], ARGV[4])
return {1}
`)
