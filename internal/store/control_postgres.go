package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/routes"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type ControlOptions struct {
	Now                                func() time.Time
	Policy                             routes.RoutePolicyProvider
	LaunchPepper, RunPepper, ReplayKey []byte
}

type ControlPostgres struct {
	db                      *sql.DB
	clock                   func() time.Time
	policy                  routes.RoutePolicyProvider
	replayCipher            *identity.ReplayCipher
	launchPepper, runPepper string
}

func OpenControlPostgres(ctx context.Context, dsn string, opts ControlOptions) (*ControlPostgres, error) {
	if len(opts.LaunchPepper) < 32 || len(opts.RunPepper) < 32 || len(opts.ReplayKey) != 32 {
		return nil, errors.New("invalid control secret configuration")
	}
	if bytes.Equal(opts.LaunchPepper, opts.RunPepper) || bytes.Equal(opts.LaunchPepper, opts.ReplayKey) || bytes.Equal(opts.RunPepper, opts.ReplayKey) {
		return nil, errors.New("control secret purposes must be distinct")
	}
	replayCipher, err := identity.NewReplayCipher(bytes.Clone(opts.ReplayKey))
	if err != nil {
		return nil, fmt.Errorf("configure replay cipher: %w", err)
	}
	clock := opts.Now
	if clock == nil {
		clock = time.Now
	}
	policy := opts.Policy
	if policy == nil {
		policy, err = routes.NewStaticPolicyProvider(5)
		if err != nil {
			return nil, fmt.Errorf("configure route policy: %w", err)
		}
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect control postgres: %w", err)
	}
	if err := MigrateControlDatabase(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate control postgres: %w", err)
	}
	return &ControlPostgres{
		db: db, clock: clock, policy: policy, replayCipher: replayCipher,
		launchPepper: string(bytes.Clone(opts.LaunchPepper)), runPepper: string(bytes.Clone(opts.RunPepper)),
	}, nil
}

func (p *ControlPostgres) Close() error { return p.db.Close() }

func (p *ControlPostgres) nowUTC() time.Time {
	return p.clock().UTC().Truncate(time.Microsecond)
}
