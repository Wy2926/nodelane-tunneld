package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
)

const controlLaunchCodeLifetime = 10 * time.Minute

func (p *ControlPostgres) IssueLaunchCode(ctx context.Context, cmd domain.IssueLaunchCodeCommand) (domain.IssuedLaunchCode, error) {
	if cmd.AccountID == "" || cmd.RouteID == "" {
		return domain.IssuedLaunchCode{}, domain.ErrInvalidRequest
	}
	credential, err := identity.NewLaunchCredential()
	if err != nil {
		return domain.IssuedLaunchCode{}, err
	}

	var issued domain.IssuedLaunchCode
	err = p.withControlTx(ctx, func(tx *sql.Tx) error {
		issued = domain.IssuedLaunchCode{}
		if err := lockControlAccounts(ctx, tx, cmd.AccountID); err != nil {
			return err
		}
		route, err := lockOwnedControlRoute(ctx, tx, cmd.AccountID, cmd.RouteID)
		if err != nil {
			return err
		}
		if route.Status == domain.RouteDeleted {
			return domain.ErrRouteDeleted
		}
		var runID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM tunnel_runs
			WHERE route_id=$1 AND status IN ('starting','online','stopping') ORDER BY id LIMIT 1 FOR UPDATE`, cmd.RouteID).Scan(&runID)
		if err == nil {
			return domain.ErrRunAlreadyActive
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := p.nowUTC()
		code := domain.LaunchCode{
			ID: credential.ID, RouteID: route.ID, CreatedAt: now, ExpiresAt: now.Add(controlLaunchCodeLifetime),
		}
		secretHash := identity.HashToken(p.launchPepper, credential.Token)
		if _, err := tx.ExecContext(ctx, `INSERT INTO route_launch_codes
			(id, route_id, secret_hash, created_at, expires_at) VALUES ($1,$2,$3,$4,$5)`,
			code.ID, code.RouteID, secretHash, code.CreatedAt, code.ExpiresAt); err != nil {
			return err
		}
		issued = domain.IssuedLaunchCode{Code: code, Token: credential.Token}
		return nil
	})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.IssuedLaunchCode{}, domain.ErrRouteNotFound
		}
		return domain.IssuedLaunchCode{}, err
	}
	return issued, nil
}
