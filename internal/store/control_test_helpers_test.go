package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	controlTestDatabase = "nodelane_control_test"
	controlTestMarker   = "control_fixture_v1"
)

type controlTestFixture struct {
	DB      *sql.DB
	DSN     string
	Options ControlOptions
}

func newControlTestFixture(t *testing.T) controlTestFixture {
	t.Helper()
	dsn := os.Getenv("NODELANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("integration test skipped: NODELANE_TEST_DATABASE_URL is unset")
	}
	guardControlTestDSN(t, dsn)

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		t.Fatalf("connect guarded test database: %v", err)
	}
	var database, marker string
	var serverAddress sql.NullString
	if err := admin.QueryRowContext(ctx, `SELECT current_database(), current_setting('nodelane.test_marker', true), host(inet_server_addr())`).Scan(&database, &marker, &serverAddress); err != nil {
		_ = admin.Close()
		t.Fatalf("verify guarded test database: %v", err)
	}
	if database != controlTestDatabase || marker != controlTestMarker || !serverAddress.Valid || !controlTestServerAddressAllowed(serverAddress.String) {
		_ = admin.Close()
		t.Fatalf("refusing unsafe test database (database=%q marker=%q server loopback/private=%t)", database, marker, serverAddress.Valid && controlTestServerAddressAllowed(serverAddress.String))
	}

	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	schema := "ctl_test_" + hex.EncodeToString(random)
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	schemaDSN := parsed.String()
	db, err := sql.Open("pgx", schemaDSN)
	if err != nil {
		_, _ = admin.ExecContext(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		_ = admin.Close()
		t.Fatal(err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		_, _ = admin.ExecContext(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.ExecContext(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop generated test schema: %v", err)
		}
		_ = admin.Close()
	})

	clock := &controlTestClock{value: time.Date(2026, 9, 5, 12, 34, 56, 123456000, time.FixedZone("UTC+8", 8*60*60))}
	return controlTestFixture{
		DB:  db,
		DSN: schemaDSN,
		Options: ControlOptions{
			Now:          clock.Now,
			LaunchPepper: []byte("launch-pepper-32-bytes-long-value"),
			RunPepper:    []byte("run-pepper-distinct-32-byte-value"),
			ReplayKey:    []byte("0123456789abcdef0123456789abcdef"),
		},
	}
}

func guardControlTestDSN(t *testing.T, dsn string) {
	t.Helper()
	if _, err := validateControlTestDSN(dsn); err != nil {
		t.Fatal(err)
	}
}

func validateControlTestDSN(dsn string) (*pgx.ConnConfig, error) {
	parsedURL, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse test database URL: %w", err)
	}
	if parsedURL.Scheme != "postgres" && parsedURL.Scheme != "postgresql" {
		return nil, fmt.Errorf("refusing non-PostgreSQL test database URL")
	}
	for key := range parsedURL.Query() {
		lower := strings.ToLower(key)
		switch lower {
		case "host", "hostaddr", "port", "dbname", "database", "user", "password", "options", "search_path":
			return nil, fmt.Errorf("refusing test database URL query override %q", key)
		}
		if lower == "nodelane.test_marker" {
			return nil, fmt.Errorf("refusing test marker override")
		}
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse effective test database config: %w", err)
	}
	if config.Database != controlTestDatabase {
		return nil, fmt.Errorf("refusing test database %q", config.Database)
	}
	if !controlTestHostAllowed(config.Host) {
		return nil, fmt.Errorf("refusing non-loopback test database host %q", config.Host)
	}
	for _, fallback := range config.Fallbacks {
		if !controlTestHostAllowed(fallback.Host) {
			return nil, fmt.Errorf("refusing non-loopback fallback host %q", fallback.Host)
		}
	}
	for key := range config.RuntimeParams {
		if strings.EqualFold(key, "options") || strings.EqualFold(key, "search_path") || strings.EqualFold(key, "nodelane.test_marker") {
			return nil, fmt.Errorf("refusing test database startup override %q", key)
		}
	}
	return config, nil
}

func controlTestHostAllowed(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.IsLoopback()
	}
	addresses, err := net.LookupIP(host)
	if err != nil || len(addresses) == 0 {
		return false
	}
	for _, address := range addresses {
		if !address.IsLoopback() {
			return false
		}
	}
	return true
}

func controlTestServerAddressAllowed(value string) bool {
	address, err := netip.ParseAddr(value)
	return err == nil && (address.IsLoopback() || address.IsPrivate())
}

func newControlTestStore(t *testing.T) (*ControlPostgres, *sql.DB) {
	t.Helper()
	fixture := newControlTestFixture(t)
	store, err := OpenControlPostgres(context.Background(), fixture.DSN, fixture.Options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, fixture.DB
}

func seedControlAccountRoute(t *testing.T, db *sql.DB) (domain.Account, domain.Route) {
	t.Helper()
	now := time.Date(2026, 9, 5, 4, 34, 56, 123456000, time.UTC)
	account := domain.Account{
		ID: "10000000-0000-4000-8000-000000000001", IdentityIssuer: "https://issuer.test",
		IdentitySubject: "subject-1", CreatedAt: now, LastSeenAt: now,
	}
	routeID, err := identity.NewRouteID()
	if err != nil {
		t.Fatal(err)
	}
	route := domain.Route{
		ID: routeID, AccountID: account.ID, Protocol: "http", Subdomain: "demo-route",
		ProxyName: routeID, Status: domain.RouteActive, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := db.Exec(`INSERT INTO tunnel_accounts (id, identity_issuer, identity_subject, created_at, last_seen_at) VALUES ($1, $2, $3, $4, $5)`,
		account.ID, account.IdentityIssuer, account.IdentitySubject, account.CreatedAt, account.LastSeenAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tunnel_routes (id, account_id, protocol, subdomain, proxy_name, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		route.ID, route.AccountID, route.Protocol, route.Subdomain, route.ProxyName, route.Status, route.CreatedAt, route.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	return account, route
}

type controlTestClock struct {
	mu    sync.Mutex
	value time.Time
}

func (c *controlTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func (c *controlTestClock) Set(value time.Time) {
	c.mu.Lock()
	c.value = value
	c.mu.Unlock()
}

func controlTestTableCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(fmt.Sprintf("SELECT count(*) FROM %s", pgx.Identifier{table}.Sanitize())).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
