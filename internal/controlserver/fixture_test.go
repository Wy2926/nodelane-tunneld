package controlserver

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

type serviceFixture struct {
	cfg   Config
	db    *sql.DB
	redis *redis.Client
}

func isolatedFixture(t *testing.T) serviceFixture {
	t.Helper()
	dsn, rawRedis := os.Getenv("NODELANE_TEST_DATABASE_URL"), os.Getenv("NODELANE_TEST_REDIS_URL")
	if dsn == "" || rawRedis == "" {
		t.Skip("isolated PostgreSQL and Redis fixture URLs are required")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("invalid test database URL")
	}
	for key := range parsed.Query() {
		if strings.ToLower(key) != "sslmode" {
			t.Fatal("test database query overrides are not permitted")
		}
	}
	effective, err := pgx.ParseConfig(dsn)
	if err != nil || effective.Database != "nodelane_control_test" || !literalLoopback(effective.Host) {
		t.Fatal("test database must target the exact isolated loopback database")
	}
	for _, fallback := range effective.Fallbacks {
		if !literalLoopback(fallback.Host) {
			t.Fatal("non-loopback database fallback")
		}
	}
	for key := range effective.RuntimeParams {
		if strings.EqualFold(key, "options") || strings.EqualFold(key, "search_path") || strings.EqualFold(key, "nodelane.test_marker") {
			t.Fatal("unsafe database startup override")
		}
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("open test database failed")
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var database, marker string
	var address sql.NullString
	if err := admin.QueryRowContext(ctx, `SELECT current_database(),current_setting('nodelane.test_marker',true),host(inet_server_addr())`).Scan(&database, &marker, &address); err != nil {
		t.Fatal("test database marker read failed")
	}
	ip, err := netip.ParseAddr(address.String)
	if database != "nodelane_control_test" || marker != "control_fixture_v1" || !address.Valid || err != nil || (!ip.IsLoopback() && !ip.IsPrivate()) {
		t.Fatal("test database marker or server address mismatch")
	}
	redisURL, err := url.Parse(rawRedis)
	if err != nil || redisURL.RawQuery != "" || redisURL.Fragment != "" || redisURL.ForceQuery || redisURL.Path != "/15" {
		t.Fatal("invalid isolated Redis URL")
	}
	redisOptions, err := redis.ParseURL(rawRedis)
	if err != nil {
		t.Fatal("invalid isolated Redis options")
	}
	host, _, err := net.SplitHostPort(redisOptions.Addr)
	if err != nil || !literalLoopback(host) || redisOptions.DB != 15 || redisOptions.Network != "tcp" {
		t.Fatal("Redis fixture must use loopback and database 15")
	}
	redisClient := redis.NewClient(redisOptions)
	t.Cleanup(func() { _ = redisClient.Close() })
	if value, err := redisClient.Get(ctx, "nodelane:test:marker").Result(); err != nil || value != "bff_fixture_v1" {
		t.Fatal("Redis fixture marker is missing")
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random[:])
	schema := "ctl_test_" + suffix
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("create owned test schema failed")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal("open owned test schema failed")
	}
	t.Cleanup(func() {
		_ = db.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if !strings.HasPrefix(schema, "ctl_test_") || len(schema) != 33 {
			t.Error("unsafe test schema cleanup target")
			return
		}
		if _, err := admin.ExecContext(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("drop owned test schema failed")
		}
	})
	cfg, stock := preparedConfig(t)
	cfg.DatabaseURL, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB = parsed.String(), redisOptions.Addr, redisOptions.Password, redisOptions.DB
	cfg.RedisPrefix = "ctl_test:controlserver:" + suffix
	writeStockConfig(t, cfg.FRPSConfigFile, stock)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var cursor uint64
		for {
			keys, next, err := redisClient.Scan(cleanup, cursor, cfg.RedisPrefix+":*", 100).Result()
			if err != nil {
				t.Error("scan owned Redis keys failed")
				return
			}
			for _, key := range keys {
				if !strings.HasPrefix(key, cfg.RedisPrefix+":") {
					t.Error("refusing unowned Redis key cleanup")
					return
				}
			}
			if len(keys) > 0 && redisClient.Unlink(cleanup, keys...).Err() != nil {
				t.Error("remove owned Redis keys failed")
				return
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	})
	return serviceFixture{cfg: cfg, db: db, redis: redisClient}
}

func literalLoopback(host string) bool {
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
