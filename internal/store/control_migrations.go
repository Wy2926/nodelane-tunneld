package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var controlMigrations embed.FS

var controlTableNames = []string{
	"network_bans",
	"operation_replays",
	"route_launch_codes",
	"run_credentials",
	"tunnel_accounts",
	"tunnel_routes",
	"tunnel_runs",
}

func MigrateControlDatabase(ctx context.Context, db *sql.DB) error {
	if err := validateControlSchemaState(ctx, db); err != nil {
		return err
	}
	provider, err := newControlMigrationProvider(db)
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}

func newControlMigrationProvider(db *sql.DB) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 30))
	if err != nil {
		return nil, err
	}
	migrations, err := fs.Sub(controlMigrations, "migrations")
	if err != nil {
		return nil, err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations,
		goose.WithSessionLocker(locker), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return nil, err
	}
	return provider, nil
}

func validateControlSchemaState(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin control schema inspection: %w", err)
	}
	defer tx.Rollback()
	if err := validateControlSchemaStateInSnapshot(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("finish control schema inspection: %w", err)
	}
	return nil
}

type controlSchemaQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validateControlSchemaStateInSnapshot(ctx context.Context, queryer controlSchemaQueryer) error {
	rows, err := queryer.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema()
		  AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S', 'c')
		  AND c.relname NOT IN ('goose_db_version', 'goose_db_version_id_seq')
		ORDER BY c.relname`)
	if err != nil {
		return fmt.Errorf("inspect control schema: %w", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return fmt.Errorf("inspect control schema: %w", err)
		}
		if table != "goose_db_version" {
			tables = append(tables, table)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect control schema: %w", err)
	}
	var unsupported bool
	if err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_proc p
			JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = current_schema()
		) OR EXISTS (
			SELECT 1 FROM pg_catalog.pg_type t
			JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
			WHERE n.nspname = current_schema() AND t.typtype IN ('d', 'e')
		)`).Scan(&unsupported); err != nil {
		return fmt.Errorf("inspect control schema objects: %w", err)
	}
	if unsupported {
		return fmt.Errorf("refuse unrelated control schema objects")
	}
	sort.Strings(tables)
	if len(tables) == 0 {
		version, exists, err := currentControlVersion(ctx, queryer)
		if err != nil {
			return err
		}
		if exists && version > 0 {
			return fmt.Errorf("refuse partial control schema at migration version %d", version)
		}
		return nil
	}
	if !equalControlTables(tables, controlTableNames) {
		return fmt.Errorf("refuse non-fresh or partial control schema: found %v", tables)
	}
	version, exists, err := currentControlVersion(ctx, queryer)
	if err != nil {
		return err
	}
	if !exists || version != 1 {
		return fmt.Errorf("refuse unsupported control schema version %d", version)
	}
	return nil
}

func currentControlVersion(ctx context.Context, queryer controlSchemaQueryer) (int64, bool, error) {
	var exists bool
	if err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_class c
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema() AND c.relname = 'goose_db_version' AND c.relkind IN ('r', 'p')
		)`).Scan(&exists); err != nil {
		return 0, false, fmt.Errorf("inspect control migration metadata: %w", err)
	}
	if !exists {
		return 0, false, nil
	}
	var version sql.NullInt64
	if err := queryer.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil {
		return 0, true, fmt.Errorf("inspect control migration version: %w", err)
	}
	return version.Int64, true, nil
}

func equalControlTables(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
