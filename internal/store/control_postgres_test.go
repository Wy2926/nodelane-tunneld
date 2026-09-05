package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestControlMigrationInitializesFreshSchemaAndIsRepeatable(t *testing.T) {
	fixture := newControlTestFixture(t)
	ctx := context.Background()
	if err := MigrateControlDatabase(ctx, fixture.DB); err != nil {
		t.Fatal(err)
	}
	if err := MigrateControlDatabase(ctx, fixture.DB); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := fixture.DB.QueryRowContext(ctx, "SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("migration version = %d", version)
	}
	for _, table := range []string{"tunnel_accounts", "tunnel_routes", "route_launch_codes", "tunnel_runs", "run_credentials", "operation_replays", "network_bans"} {
		var exists bool
		if err := fixture.DB.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %s does not exist", table)
		}
	}
}

func TestControlTestGuardRejectsUnsafeEffectiveConfiguration(t *testing.T) {
	base := "postgresql://control_test:fixture-password@127.0.0.1:5432/nodelane_control_test?sslmode=disable"
	for name, dsn := range map[string]string{
		"other database":             base[:strings.LastIndex(base, "/")+1] + "other_database?sslmode=disable",
		"remote host":                strings.Replace(base, "127.0.0.1", "192.0.2.1", 1),
		"host query override":        base + "&host=127.0.0.1",
		"port query override":        base + "&port=5433",
		"user query override":        base + "&user=other",
		"options query override":     base + "&options=-c%20nodelane.test_marker%3Dcontrol_fixture_v1",
		"search path query override": base + "&search_path=public",
		"marker query override":      base + "&nodelane.test_marker=control_fixture_v1",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PGOPTIONS", "")
			if _, err := validateControlTestDSN(dsn); err == nil {
				t.Fatalf("unsafe configuration was accepted")
			}
		})
	}
	t.Run("effective PGOPTIONS", func(t *testing.T) {
		t.Setenv("PGOPTIONS", "-c nodelane.test_marker=control_fixture_v1")
		if _, err := validateControlTestDSN(base); err == nil {
			t.Fatal("PGOPTIONS marker spoof was accepted")
		}
	})
	t.Run("explicit URI resists connection PG environment", func(t *testing.T) {
		t.Setenv("PGOPTIONS", "")
		t.Setenv("PGHOST", "192.0.2.1")
		t.Setenv("PGPORT", "6543")
		t.Setenv("PGDATABASE", "production")
		config, err := validateControlTestDSN(base)
		if err != nil {
			t.Fatal(err)
		}
		if config.Host != "127.0.0.1" || config.Port != 5432 || config.Database != controlTestDatabase {
			t.Fatalf("environment changed explicit URI target: host=%q port=%d database=%q", config.Host, config.Port, config.Database)
		}
	})
}

func TestControlMigrationSerializesConcurrentInitializers(t *testing.T) {
	fixture := newControlTestFixture(t)
	const workers = 6
	errorsByWorker := make(chan error, workers)
	var storesMu sync.Mutex
	var stores []*ControlPostgres
	for range workers {
		go func() {
			store, err := OpenControlPostgres(context.Background(), fixture.DSN, fixture.Options)
			if err == nil {
				storesMu.Lock()
				stores = append(stores, store)
				storesMu.Unlock()
			}
			errorsByWorker <- err
		}()
	}
	for range workers {
		if err := <-errorsByWorker; err != nil {
			t.Errorf("concurrent initializer failed: %v", err)
		}
	}
	for _, store := range stores {
		_ = store.Close()
	}
}

func TestControlMigrationPreflightUsesOneSnapshotAcrossConcurrentInitialization(t *testing.T) {
	fixture := newControlTestFixture(t)
	ctx := context.Background()
	preflight, err := fixture.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer preflight.Rollback()

	var businessTables int
	if err := preflight.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relname = ANY($1)`, controlTableNames).Scan(&businessTables); err != nil {
		t.Fatal(err)
	}
	if businessTables != 0 {
		t.Fatalf("fresh snapshot has %d business tables", businessTables)
	}

	initialized := make(chan error, 1)
	go func() { initialized <- MigrateControlDatabase(ctx, fixture.DB) }()
	select {
	case err := <-initialized:
		if err != nil {
			t.Fatalf("concurrent initializer: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent initializer blocked behind read-only preflight")
	}

	if err := validateControlSchemaStateInSnapshot(ctx, preflight); err != nil {
		t.Fatalf("coherent preflight snapshot reported partial schema: %v", err)
	}
	if err := preflight.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := MigrateControlDatabase(ctx, fixture.DB); err != nil {
		t.Fatalf("initializer after concurrent migration: %v", err)
	}
}

func TestControlMigrationRefusesLegacySchemaWithoutChangingRows(t *testing.T) {
	for _, table := range []string{"clients", "client_tokens", "tunnels"} {
		t.Run(table, func(t *testing.T) {
			fixture := newControlTestFixture(t)
			if _, err := fixture.DB.Exec(`CREATE TABLE ` + table + ` (id text PRIMARY KEY, value text NOT NULL); INSERT INTO ` + table + ` VALUES ('legacy-id', 'keep-me')`); err != nil {
				t.Fatal(err)
			}
			if err := MigrateControlDatabase(context.Background(), fixture.DB); err == nil {
				t.Fatal("legacy schema was accepted")
			}
			var value string
			if err := fixture.DB.QueryRow(`SELECT value FROM ` + table + ` WHERE id = 'legacy-id'`).Scan(&value); err != nil || value != "keep-me" {
				t.Fatalf("legacy row changed: value=%q err=%v", value, err)
			}
			if controlTestTableCount(t, fixture.DB, table) != 1 {
				t.Fatal("legacy rows changed")
			}
		})
	}
}

func TestControlMigrationAcceptsGooseVersionZeroAsFresh(t *testing.T) {
	fixture := newControlTestFixture(t)
	ctx := context.Background()
	if err := MigrateControlDatabase(ctx, fixture.DB); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Exec(`
		DROP TABLE network_bans, operation_replays, run_credentials, tunnel_runs, route_launch_codes, tunnel_routes, tunnel_accounts CASCADE;
		DELETE FROM goose_db_version;
		INSERT INTO goose_db_version (version_id, is_applied, tstamp) VALUES (0, true, now())`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateControlDatabase(ctx, fixture.DB); err != nil {
		t.Fatalf("version-zero schema was refused: %v", err)
	}
}

func TestControlMigrationRefusesUnrelatedAndPartialSchemas(t *testing.T) {
	for _, tc := range []struct{ name, ddl string }{
		{"unrelated", `CREATE TABLE unrelated_data (id integer PRIMARY KEY)`},
		{"unrelated sequence", `CREATE SEQUENCE unrelated_sequence`},
		{"partial", `CREATE TABLE tunnel_accounts (id uuid PRIMARY KEY)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newControlTestFixture(t)
			if _, err := fixture.DB.Exec(tc.ddl); err != nil {
				t.Fatal(err)
			}
			if err := MigrateControlDatabase(context.Background(), fixture.DB); err == nil {
				t.Fatal("non-fresh schema was accepted")
			}
			var exists bool
			if err := fixture.DB.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, strings.Fields(tc.ddl)[2]).Scan(&exists); err != nil || !exists {
				t.Fatalf("pre-existing table changed: exists=%t err=%v", exists, err)
			}
		})
	}
}

func TestControlMigrationRefusesVersionNewerThanEmbedded(t *testing.T) {
	fixture := newControlTestFixture(t)
	ctx := context.Background()
	if err := MigrateControlDatabase(ctx, fixture.DB); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Exec(`INSERT INTO goose_db_version (version_id, is_applied, tstamp) VALUES (2, true, now())`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateControlDatabase(ctx, fixture.DB); err == nil {
		t.Fatal("newer database version was accepted")
	}
}

func TestControlMigrationDownRefusesWithoutChangingVersionOrData(t *testing.T) {
	fixture := newControlTestFixture(t)
	ctx := context.Background()
	if err := MigrateControlDatabase(ctx, fixture.DB); err != nil {
		t.Fatal(err)
	}
	seedControlAccountRoute(t, fixture.DB)
	provider, err := newControlMigrationProvider(fixture.DB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Down(ctx); err == nil || !strings.Contains(err.Error(), "manual restore") {
		t.Fatalf("down migration error=%v", err)
	}
	var version int64
	if err := fixture.DB.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 || controlTestTableCount(t, fixture.DB, "tunnel_accounts") != 1 {
		t.Fatalf("failed down changed schema: version=%d", version)
	}
}

func TestControlOpenRejectsInvalidSecretsBeforeConnecting(t *testing.T) {
	valid := ControlOptions{
		LaunchPepper: []byte("launch-pepper-32-bytes-long-value"),
		RunPepper:    []byte("run-pepper-distinct-32-byte-value"),
		ReplayKey:    []byte("0123456789abcdef0123456789abcdef"),
	}
	cases := map[string]ControlOptions{}
	for name, mutate := range map[string]func(*ControlOptions){
		"short launch pepper": func(v *ControlOptions) { v.LaunchPepper = []byte("short") },
		"short run pepper":    func(v *ControlOptions) { v.RunPepper = []byte("short") },
		"short replay key":    func(v *ControlOptions) { v.ReplayKey = []byte("short") },
		"long replay key":     func(v *ControlOptions) { v.ReplayKey = make([]byte, 33) },
		"reused peppers":      func(v *ControlOptions) { v.RunPepper = bytes.Clone(v.LaunchPepper) },
		"launch equals key":   func(v *ControlOptions) { v.LaunchPepper = bytes.Clone(v.ReplayKey) },
		"run equals key":      func(v *ControlOptions) { v.RunPepper = bytes.Clone(v.ReplayKey) },
	} {
		options := valid
		mutate(&options)
		cases[name] = options
	}
	for name, options := range cases {
		t.Run(name, func(t *testing.T) {
			store, err := OpenControlPostgres(context.Background(), "postgresql://127.0.0.1:1/would_be_unsafe", options)
			if err == nil || store != nil {
				t.Fatalf("invalid secrets returned store=%v err=%v", store, err)
			}
			if strings.Contains(err.Error(), "connect") {
				t.Fatalf("validation happened after connection attempt: %v", err)
			}
		})
	}
}

func TestControlSchemaEnforcesRouteAndRunUniqueness(t *testing.T) {
	_, db := newControlTestStore(t)
	account, route := seedControlAccountRoute(t, db)
	now := route.CreatedAt
	assertControlConstraintError(t, db, "23505", "control_routes_unreleased_name_uq", `INSERT INTO tunnel_routes (id, account_id, protocol, subdomain, proxy_name, status, created_at, updated_at) VALUES ('rte_bbbbbbbbbbbbbbbbbbbbbbbbbb',$1,'http','demo-route','rte_bbbbbbbbbbbbbbbbbbbbbbbbbb','active',$2,$2)`, account.ID, now)
	if _, err := db.Exec(`UPDATE tunnel_routes SET status='deleted', deleted_at=$2, recoverable_until=$2, name_released_at=$2, updated_at=$2 WHERE id=$1`, route.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tunnel_routes (id, account_id, protocol, subdomain, proxy_name, status, created_at, updated_at) VALUES ('rte_bbbbbbbbbbbbbbbbbbbbbbbbbb',$1,'http','demo-route','rte_bbbbbbbbbbbbbbbbbbbbbbbbbb','active',$2,$2)`, account.ID, now); err != nil {
		t.Fatalf("released name was not reusable: %v", err)
	}
	const validRouteID = "rte_abcdefghijklmnopqrstuvwxyz"
	assertControlConstraintError(t, db, "23514", "tunnel_routes_check", `INSERT INTO tunnel_routes (id, account_id, protocol, subdomain, proxy_name, status, created_at, updated_at) VALUES ($1,$2,'http','fresh-label','wrong-proxy','active',$3,$3)`, validRouteID, account.ID, now)
	assertControlConstraintError(t, db, "23514", "tunnel_routes_protocol_check", `INSERT INTO tunnel_routes (id, account_id, protocol, subdomain, proxy_name, status, created_at, updated_at) VALUES ($1,$2,'tcp','fresh-label',$1,'active',$3,$3)`, validRouteID, account.ID, now)
	assertControlConstraintError(t, db, "23514", "tunnel_routes_subdomain_check", `INSERT INTO tunnel_routes (id, account_id, protocol, subdomain, proxy_name, status, created_at, updated_at) VALUES ($1,$2,'http','anon-blocked',$1,'active',$3,$3)`, validRouteID, account.ID, now)
	assertControlConstraintError(t, db, "23514", "control_routes_status_check", `INSERT INTO tunnel_routes (id, account_id, protocol, subdomain, proxy_name, status, created_at, updated_at) VALUES ($1,$2,'http','fresh-label',$1,'invalid',$3,$3)`, validRouteID, account.ID, now)

	activeRoute := "rte_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	insertRun := `INSERT INTO tunnel_runs (id, route_id, started_via, status, desired_state, request_ip, created_at, connect_deadline_at) VALUES ($1,$2,'device_login',$3,'running','192.0.2.1',$4,$5)`
	if _, err := db.Exec(insertRun, "run_aaaaaaaaaaaaaaaaaaaaaaaaaa", activeRoute, "starting", now, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertControlConstraintError(t, db, "23505", "control_runs_active_route_uq", `
		INSERT INTO tunnel_runs
		(id, route_id, started_via, status, desired_state, request_ip, connected_ip, created_at, connected_at, connect_deadline_at, lease_expires_at)
		VALUES ($1,$2,'device_login','online','running','192.0.2.2','192.0.2.3',$3,$3,$4,$5)`,
		"run_bbbbbbbbbbbbbbbbbbbbbbbbbb", activeRoute, now, now.Add(2*time.Minute), now.Add(90*time.Second))
	if _, err := db.Exec(`UPDATE tunnel_runs SET status='offline', desired_state='stopped', stopped_at=$2 WHERE id=$1`, "run_aaaaaaaaaaaaaaaaaaaaaaaaaa", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insertRun, "run_bbbbbbbbbbbbbbbbbbbbbbbbbb", activeRoute, "starting", now, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("offline run retained active slot: %v", err)
	}
}

func TestControlSchemaEnforcesHashesReplayKeysAndTimestampRelationships(t *testing.T) {
	_, db := newControlTestStore(t)
	_, route := seedControlAccountRoute(t, db)
	now := route.CreatedAt
	hash := strings.Repeat("a", 64)
	if _, err := db.Exec(`INSERT INTO operation_replays (id, operation, principal_key, key_hash, request_hash, route_id, response_ciphertext, created_at, expires_at) VALUES ('rpl_aaaaaaaaaaaaaaaaaaaaaaaaaa','create_route','principal',$1,$1,$2,'x',$3,$4)`, hash, route.ID, now, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertControlConstraintError(t, db, "23505", "control_replay_key_uq", `INSERT INTO operation_replays (id, operation, principal_key, key_hash, request_hash, route_id, response_ciphertext, created_at, expires_at) VALUES ('rpl_bbbbbbbbbbbbbbbbbbbbbbbbbb','create_route','principal',$1,$1,$2,'x',$3,$4)`, hash, route.ID, now, now.Add(2*time.Minute))
	assertControlConstraintError(t, db, "23514", "route_launch_codes_secret_hash_check", `INSERT INTO route_launch_codes (id, route_id, secret_hash, created_at, expires_at) VALUES ('nlc_aaaaaaaaaaaaaaaaaaaaaaaaaa',$1,$2,$3,$4)`, route.ID, strings.Repeat("A", 64), now, now.Add(time.Minute))
	assertControlConstraintError(t, db, "23514", "route_launch_codes_check", `INSERT INTO route_launch_codes (id, route_id, secret_hash, created_at, expires_at) VALUES ('nlc_aaaaaaaaaaaaaaaaaaaaaaaaaa',$1,$2,$3,$3)`, route.ID, hash, now)
}

func TestControlTransactionRetriesOnlyRetryableSQLStates(t *testing.T) {
	store, db := newControlTestStore(t)
	var attempts int
	err := store.withControlTx(context.Background(), func(tx *sql.Tx) error {
		attempts++
		if attempts < 3 {
			return &pgconn.PgError{Code: "40001", Message: "synthetic serialization failure"}
		}
		_, err := tx.Exec(`INSERT INTO tunnel_accounts (id, identity_issuer, identity_subject, created_at, last_seen_at) VALUES ('10000000-0000-4000-8000-000000000001','issuer','subject',now(),now())`)
		return err
	})
	if err != nil || attempts != 3 || controlTestTableCount(t, db, "tunnel_accounts") != 1 {
		t.Fatalf("retry result attempts=%d err=%v", attempts, err)
	}
	attempts = 0
	want := &pgconn.PgError{Code: "08006", Message: "uncertain connection"}
	err = store.withControlTx(context.Background(), func(*sql.Tx) error { attempts++; return want })
	if !errors.Is(err, want) || attempts != 1 {
		t.Fatalf("nonretryable result attempts=%d err=%v", attempts, err)
	}
}

func TestControlAccountLocksRejectMissingOwners(t *testing.T) {
	store, db := newControlTestStore(t)
	seedControlAccountRoute(t, db)
	if err := store.withControlTx(context.Background(), func(tx *sql.Tx) error {
		return lockControlAccounts(context.Background(), tx, "10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000001")
	}); err != nil {
		t.Fatalf("existing deduplicated owners rejected: %v", err)
	}
	if err := store.withControlTx(context.Background(), func(tx *sql.Tx) error {
		return lockControlAccounts(context.Background(), tx, "10000000-0000-4000-8000-000000000099")
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing owner error=%v", err)
	}
	if err := store.withControlTx(context.Background(), func(tx *sql.Tx) error {
		return lockControlAccounts(context.Background(), tx, "")
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("empty owner error=%v", err)
	}
}

func TestControlScannersPreserveNullableRunValues(t *testing.T) {
	_, db := newControlTestStore(t)
	_, route := seedControlAccountRoute(t, db)
	now := route.CreatedAt
	if _, err := db.Exec(`INSERT INTO tunnel_runs (id, route_id, started_via, status, desired_state, request_ip, created_at, connect_deadline_at) VALUES ('run_aaaaaaaaaaaaaaaaaaaaaaaaaa',$1,'device_login','starting','running','192.0.2.15',$2,$3)`, route.ID, now, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	gotRoute, err := scanControlRoute(db.QueryRow(`SELECT `+controlRouteColumns+` FROM tunnel_routes WHERE id=$1`, route.ID))
	if err != nil || gotRoute.ID != route.ID || gotRoute.DeletedAt != nil {
		t.Fatalf("route scan=%#v err=%v", gotRoute, err)
	}
	gotRun, err := scanControlRun(db.QueryRow(`SELECT ` + controlRunColumns + ` FROM tunnel_runs WHERE id='run_aaaaaaaaaaaaaaaaaaaaaaaaaa'`))
	if err != nil || gotRun.RequestIP != netip.MustParseAddr("192.0.2.15") || gotRun.ConnectedIP.IsValid() || gotRun.ConnectedAt != nil || gotRun.StopReason != "" {
		t.Fatalf("run scan=%#v err=%v", gotRun, err)
	}
}

func TestControlReplayRoundTripsExactPayloadAndAuthenticatedMetadata(t *testing.T) {
	store, db := newControlTestStore(t)
	_, route := seedControlAccountRoute(t, db)
	now := route.CreatedAt
	replay := domain.OperationReplay{
		ID: "rpl_aaaaaaaaaaaaaaaaaaaaaaaaaa", Operation: domain.OperationCreateRoute,
		PrincipalKey: "10000000-0000-4000-8000-000000000001", KeyHash: controlDigest("key"),
		RequestHash: controlDigest("request"), RouteID: route.ID, CreatedAt: now, ExpiresAt: now.Add(2 * time.Minute),
	}
	payload := []byte("{\"private\":true}\n")
	if err := store.withControlTx(context.Background(), func(tx *sql.Tx) error {
		return store.saveControlReplay(context.Background(), tx, replay, payload)
	}); err != nil {
		t.Fatal(err)
	}
	var stored domain.OperationReplay
	if err := store.withControlTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		stored, err = store.readControlReplay(context.Background(), tx, replay.Operation, replay.PrincipalKey, replay.KeyHash, true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	opened, err := store.openControlReplay(stored, replay.ExpiresAt.Add(-time.Microsecond))
	if err != nil || !bytes.Equal(opened, payload) {
		t.Fatalf("opened=%q err=%v", opened, err)
	}
	stored.PrincipalKey += "-other"
	if _, err := store.openControlReplay(stored, replay.ExpiresAt.Add(-time.Microsecond)); err == nil {
		t.Fatal("modified replay metadata authenticated")
	}
	if err := store.withControlTx(context.Background(), func(tx *sql.Tx) error {
		_, err := store.readControlReplay(context.Background(), tx, "missing", "missing", controlDigest("missing"), false)
		return err
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing replay error=%v", err)
	}
}

func TestControlDigestsAreStableTypedJSONAndClockIsUTCMicroseconds(t *testing.T) {
	if got := controlDigest("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("digest=%q", got)
	}
	type request struct {
		Value string `json:"value"`
	}
	digest, err := controlRequestDigest(request{Value: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	literal, _ := json.Marshal(request{Value: "abc"})
	if digest != controlDigest(string(literal)) {
		t.Fatalf("request digest=%q", digest)
	}
	clock := &controlTestClock{value: time.Date(2026, 9, 5, 12, 0, 0, 123456789, time.FixedZone("UTC+8", 8*60*60))}
	store := &ControlPostgres{clock: clock.Now}
	got := store.nowUTC()
	if got.Location() != time.UTC || got.Nanosecond() != 123456000 {
		t.Fatalf("nowUTC=%s nanos=%d", got, got.Nanosecond())
	}
}

func assertControlConstraintError(t *testing.T, db *sql.DB, sqlState, constraint string, query string, args ...any) {
	t.Helper()
	_, err := db.Exec(query, args...)
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		t.Fatalf("constraint error=%v, want PostgreSQL %s from %s", err, sqlState, constraint)
	}
	if postgresError.Code != sqlState || postgresError.ConstraintName != constraint {
		t.Fatalf("constraint error SQLSTATE=%s constraint=%q, want %s %q: %v", postgresError.Code, postgresError.ConstraintName, sqlState, constraint, err)
	}
}
