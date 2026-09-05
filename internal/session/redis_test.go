package session_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/session"
	"github.com/redis/go-redis/v9"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fixture struct {
	store  *session.RedisStore
	client *redis.Client
	prefix string
	key    []byte
	clock  *testClock
}

func fixtureOptions(raw string) (*redis.Options, error) {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Path != "/15" {
		return nil, errors.New("invalid isolated Redis fixture URL")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		return nil, errors.New("invalid isolated Redis fixture URL")
	}
	host, _, err := net.SplitHostPort(opts.Addr)
	if err != nil || !net.ParseIP(host).IsLoopback() || opts.Network != "tcp" || opts.DB != 15 {
		return nil, errors.New("fixture requires a loopback IP and database 15")
	}
	opts.MaxRetries = -1
	opts.DialTimeout = time.Second
	opts.ReadTimeout = time.Second
	opts.WriteTimeout = time.Second
	return opts, nil
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	raw := os.Getenv("NODELANE_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("NODELANE_TEST_REDIS_URL is required for isolated real Redis tests")
	}
	opts, err := fixtureOptions(raw)
	if err != nil {
		t.Fatal("unsafe Redis fixture configuration")
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	marker, err := client.Get(ctx, "nodelane:test:marker").Result()
	if err != nil || marker != "bff_fixture_v1" {
		t.Fatal("dedicated Redis fixture marker missing; refusing all mutations")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "nodelane:test:bff:" + hex.EncodeToString(random[:])
	t.Cleanup(func() {
		keys := prefixKeys(t, client, prefix)
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				t.Error("could not remove this test's isolated Redis keys")
			}
		}
	})
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Now().UTC().Truncate(time.Millisecond)}
	store, err := session.NewRedisStore(client, prefix, key, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{store: store, client: client, prefix: prefix, key: key, clock: clock}
}

func prefixKeys(t *testing.T, client *redis.Client, prefix string) []string {
	t.Helper()
	var keys []string
	var cursor uint64
	for {
		batch, next, err := client.Scan(context.Background(), cursor, prefix+":*", 100).Result()
		if err != nil {
			t.Fatal("could not inspect isolated fixture keys")
		}
		for _, key := range batch {
			if !strings.HasPrefix(key, prefix+":") {
				t.Fatal("Redis returned key outside test namespace")
			}
			keys = append(keys, key)
		}
		cursor = next
		if cursor == 0 {
			return keys
		}
	}
}

func recordAt(now time.Time, id string) session.Record {
	return session.Record{
		ID:        id,
		AccountID: "account-one",
		CSRFToken: "csrf-secret-one",
		CreatedAt: now,
		ExpiresAt: now.Add(24 * time.Hour),
		Tokens: identity.OIDCTokens{
			AccessToken:          "access-token-secret-one",
			RefreshToken:         "refresh-token-secret-one",
			IDToken:              "id-token-secret-one",
			AccessTokenExpiresAt: now.Add(time.Hour),
			Identity:             identity.OIDCIdentity{Issuer: "https://issuer.invalid", Subject: "subject-one", ClientID: "client-one", Scopes: []string{"openid", "profile"}, ExpiresAt: now.Add(time.Hour), Name: "Test User", Email: "test@example.invalid"},
		},
	}
}

func hashedKey(prefix, kind, id string) string {
	hash := sha256.Sum256([]byte(id))
	return prefix + ":" + kind + ":" + hex.EncodeToString(hash[:])
}

func requireError(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

func TestFixtureURLRejectsEffectiveAddressAndDatabaseOverrides(t *testing.T) {
	for _, raw := range []string{
		"", "redis://127.0.0.1:6379/0", "redis://192.0.2.1:6379/15", "redis://localhost:6379/15",
		"redis://127.0.0.1:6379/15?db=0", "redis://127.0.0.1:6379/15?addr=192.0.2.1:6379",
		"redis://127.0.0.1:6379/15?", "redis://127.0.0.1:6379/15#fragment", "unix:///tmp/redis.sock?db=15",
	} {
		if _, err := fixtureOptions(raw); err == nil {
			t.Fatalf("unsafe fixture URL accepted: %q", raw)
		}
	}
	for _, raw := range []string{"redis://127.0.0.1:6379/15", "redis://[::1]:6379/15"} {
		if _, err := fixtureOptions(raw); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConstructorRejectsInvalidKeyAndSharedPrefixes(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	for _, n := range []int{0, 16, 24, 31, 33} {
		_, err := session.NewRedisStore(client, "nodelane:test", make([]byte, n), time.Now)
		requireError(t, err, session.ErrInvalid)
	}
	for _, prefix := range []string{"", " ", "nodelane", ":bff", "nodelane:", "nodelane::bff", "nodelane:*", "nodelane:{bff}", "nodelane:bff\n"} {
		_, err := session.NewRedisStore(client, prefix, make([]byte, 32), time.Now)
		requireError(t, err, session.ErrInvalid)
	}
	_, err := session.NewRedisStore(nil, "nodelane:bff", make([]byte, 32), time.Now)
	requireError(t, err, session.ErrInvalid)
}

func TestRecordJSONExcludesCredentials(t *testing.T) {
	record := recordAt(time.Now(), "session-secret-one")
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{record.ID, record.CSRFToken, record.Tokens.AccessToken, record.Tokens.RefreshToken, record.Tokens.IDToken} {
		if strings.Contains(string(data), secret) {
			t.Fatal("session JSON leaked credential material")
		}
	}
}

func TestCreateReadSessionPreservesTokensWithEncryptedStorage(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	record := recordAt(f.clock.Now(), "session-secret-one")
	if err := f.store.CreateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	read, err := f.store.ReadSession(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Version = 1
	if !reflect.DeepEqual(read, record) {
		t.Fatal("session fields or OAuth credentials were not preserved")
	}
	keys := prefixKeys(t, f.client, f.prefix)
	if len(keys) != 1 || keys[0] != hashedKey(f.prefix, "session", record.ID) {
		t.Fatal("session key does not use the isolated namespace and SHA256 identity")
	}
	row, err := f.client.HGetAll(ctx, keys[0]).Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{record.ID, record.AccountID, record.CSRFToken, record.Tokens.AccessToken, record.Tokens.RefreshToken, record.Tokens.IDToken, record.Tokens.Identity.Subject} {
		for key, value := range row {
			if strings.Contains(key, secret) || strings.Contains(value, secret) || strings.Contains(keys[0], secret) {
				t.Fatal("Redis storage leaked plaintext session material")
			}
		}
	}
	ttl, err := f.client.PTTL(ctx, keys[0]).Result()
	if err != nil || ttl <= 0 || ttl > 24*time.Hour {
		t.Fatalf("session TTL = %v, err = %v", ttl, err)
	}
	requireError(t, f.store.CreateSession(ctx, record), session.ErrConflict)
	for i := range f.key {
		f.key[i] ^= 0xff
	}
	if _, err := f.store.ReadSession(ctx, record.ID); err != nil {
		t.Fatal("constructor did not copy encryption key")
	}
}

func TestReadSessionRejectsWrongKeyAndAuthenticatedRowMutations(t *testing.T) {
	for _, mutation := range []string{"wrong-key", "ciphertext", "version", "expiry", "row-swap", "namespace-swap"} {
		t.Run(mutation, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			record := recordAt(f.clock.Now(), "session-secret-one")
			if err := f.store.CreateSession(ctx, record); err != nil {
				t.Fatal(err)
			}
			key := hashedKey(f.prefix, "session", record.ID)
			row, err := f.client.HGetAll(ctx, key).Result()
			if err != nil {
				t.Fatal(err)
			}
			reader := f.store
			switch mutation {
			case "wrong-key":
				wrongKey := append([]byte(nil), f.key...)
				wrongKey[0] ^= 0xff
				reader, err = session.NewRedisStore(f.client, f.prefix, wrongKey, f.clock.Now)
			case "ciphertext":
				data := []byte(row["data"])
				if len(data) == 0 {
					t.Fatal("ciphertext missing")
				}
				data[len(data)-1] ^= 1
				err = f.client.HSet(ctx, key, "data", data).Err()
			case "version":
				err = f.client.HSet(ctx, key, "version", "2").Err()
			case "expiry":
				err = f.client.HSet(ctx, key, "expires", record.ExpiresAt.Add(time.Hour).UnixMilli()).Err()
			case "row-swap":
				other := recordAt(f.clock.Now(), "session-secret-two")
				if err = f.store.CreateSession(ctx, other); err != nil {
					t.Fatal(err)
				}
				err = f.client.HSet(ctx, hashedKey(f.prefix, "session", other.ID), "data", row["data"]).Err()
				record = other
			case "namespace-swap":
				otherPrefix := f.prefix + ":other"
				reader, err = session.NewRedisStore(f.client, otherPrefix, f.key, f.clock.Now)
				if err == nil {
					err = f.client.HSet(ctx, hashedKey(otherPrefix, "session", record.ID), row).Err()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := reader.ReadSession(ctx, record.ID)
			requireError(t, err, session.ErrInvalid)
			if !reflect.DeepEqual(got, session.Record{}) {
				t.Fatal("failed read returned partial credentials")
			}
		})
	}
}

func TestReadSessionChecksAbsoluteExpiryBeforeRedisSweep(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	record := recordAt(f.clock.Now(), "session-expiry-secret")
	if err := f.store.CreateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	key := hashedKey(f.prefix, "session", record.ID)
	if err := f.client.Persist(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(24 * time.Hour)
	got, err := f.store.ReadSession(ctx, record.ID)
	requireError(t, err, session.ErrExpired)
	if !reflect.DeepEqual(got, session.Record{}) {
		t.Fatal("expired session returned partial credentials")
	}
	if n, err := f.client.Exists(ctx, key).Result(); err != nil || n != 1 {
		t.Fatal("test did not retain the expired Redis row")
	}
	_, err = f.store.ReadSession(ctx, "not-present")
	requireError(t, err, session.ErrNotFound)
}

func TestCreateSessionValidatesRecordWithoutAllocatingAlternateIdentity(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, invalid := range []string{"id", "account", "csrf", "created", "future-created", "expired"} {
		record := recordAt(f.clock.Now(), "session-validation-secret")
		want := session.ErrInvalid
		switch invalid {
		case "id":
			record.ID = ""
		case "account":
			record.AccountID = ""
		case "csrf":
			record.CSRFToken = ""
		case "created":
			record.CreatedAt = time.Time{}
		case "future-created":
			record.CreatedAt = f.clock.Now().Add(time.Hour)
		case "expired":
			record.ExpiresAt = f.clock.Now()
			want = session.ErrExpired
		}
		requireError(t, f.store.CreateSession(ctx, record), want)
	}
	if keys := prefixKeys(t, f.client, f.prefix); len(keys) != 0 {
		t.Fatal("invalid create wrote Redis keys")
	}
	record := recordAt(f.clock.Now(), "session-validation-secret")
	record.Version = 99
	if err := f.store.CreateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.ReadSession(ctx, record.ID)
	if err != nil || got.Version != 1 {
		t.Fatal("new session did not start at version one")
	}
}
