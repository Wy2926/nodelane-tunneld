package session

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type LoginTransaction struct {
	State     string `json:"-"`
	Nonce     string `json:"-"`
	Verifier  string `json:"-"`
	Binding   string `json:"-"`
	ReturnTo  string `json:"-"`
	Locale    string `json:"-"`
	ExpiresAt time.Time
}

type loginPayload struct {
	Nonce     string
	Verifier  string
	Binding   string
	ReturnTo  string
	Locale    string
	ExpiresAt time.Time
}

const maxLoginLifetime = 5 * time.Minute

func (s *RedisStore) PutLogin(ctx context.Context, login LoginTransaction) error {
	now := s.now()
	if !validIdentifier(login.State) || !validIdentifier(login.Nonce) || !validIdentifier(login.Verifier) || !validIdentifier(login.Binding) {
		return ErrInvalid
	}
	if !login.ExpiresAt.After(now) || login.ExpiresAt.UnixMilli() <= now.UnixMilli() {
		return ErrExpired
	}
	if login.ExpiresAt.After(now.Add(maxLoginLifetime)) || login.ExpiresAt.UnixMilli() > maxVersion {
		return ErrInvalid
	}
	key := s.key("login", login.State)
	r := row{version: 1, expires: login.ExpiresAt.UnixMilli(), binding: digest(login.Binding)}
	payload := loginPayload{
		Nonce: login.Nonce, Verifier: login.Verifier, Binding: login.Binding,
		ReturnTo: login.ReturnTo, Locale: login.Locale, ExpiresAt: login.ExpiresAt,
	}
	var err error
	r.data, err = s.seal("login", key, r, payload)
	if err != nil {
		return err
	}
	return s.createRow(ctx, key, r, now)
}

var consumeLoginScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return {} end
local binding = redis.call('HGET', KEYS[1], 'binding')
if not binding or binding ~= ARGV[1] then return {} end
local result = {
  redis.call('HGET', KEYS[1], 'version'),
  redis.call('HGET', KEYS[1], 'expires'),
  redis.call('HGET', KEYS[1], 'data'),
  binding
}
redis.call('DEL', KEYS[1])
return result
`)

func (s *RedisStore) ConsumeLogin(ctx context.Context, state, binding string) (LoginTransaction, error) {
	if !validIdentifier(state) || !validIdentifier(binding) {
		return LoginTransaction{}, ErrInvalid
	}
	key := s.key("login", state)
	values, err := consumeLoginScript.Run(ctx, s.client, []string{key}, digest(binding)).Slice()
	if err != nil {
		return LoginTransaction{}, ErrUnavailable
	}
	if len(values) == 0 {
		return LoginTransaction{}, ErrNotFound
	}
	if len(values) != 4 {
		return LoginTransaction{}, ErrInvalid
	}
	fields := make([]string, len(values))
	for i, value := range values {
		var ok bool
		fields[i], ok = value.(string)
		if !ok {
			return LoginTransaction{}, ErrInvalid
		}
	}
	version, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || version < 1 || version > maxVersion {
		return LoginTransaction{}, ErrInvalid
	}
	expires, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || expires < 1 || expires > maxVersion {
		return LoginTransaction{}, ErrInvalid
	}
	r := row{version: version, expires: expires, data: fields[2], binding: fields[3]}
	if s.now().UnixMilli() >= expires {
		return LoginTransaction{}, ErrExpired
	}
	var payload loginPayload
	if err := s.open("login", key, r, &payload); err != nil {
		return LoginTransaction{}, err
	}
	if !validIdentifier(payload.Nonce) || !validIdentifier(payload.Verifier) || !validIdentifier(payload.Binding) || digest(payload.Binding) != r.binding || payload.ExpiresAt.UnixMilli() != r.expires {
		return LoginTransaction{}, ErrInvalid
	}
	return LoginTransaction{
		State: state, Nonce: payload.Nonce, Verifier: payload.Verifier, Binding: payload.Binding,
		ReturnTo: payload.ReturnTo, Locale: payload.Locale, ExpiresAt: payload.ExpiresAt,
	}, nil
}
