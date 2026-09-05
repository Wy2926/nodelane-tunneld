package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/routes"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	controlRouteRecoveryPeriod = 7 * 24 * time.Hour
	controlReplayRetention     = 2 * time.Minute
)

type createRouteReplayRequest struct {
	Protocol string `json:"protocol"`
	Label    string `json:"label"`
}

type controlRouteCandidate struct {
	RouteID   string
	AccountID string
}

type retryControlDiscoveryError struct{}

func (retryControlDiscoveryError) Error() string    { return "control candidate changed during locking" }
func (retryControlDiscoveryError) SQLState() string { return "40001" }

func (p *ControlPostgres) ListRoutes(ctx context.Context, accountID string, query domain.RouteQuery) ([]domain.Route, error) {
	status := domain.RouteActive
	if query.Deleted {
		status = domain.RouteDeleted
	}
	rows, err := p.db.QueryContext(ctx, `SELECT `+controlRouteColumns+`
		FROM tunnel_routes WHERE account_id=$1 AND status=$2 ORDER BY created_at DESC, id`, accountID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]domain.Route, 0)
	for rows.Next() {
		route, err := scanControlRoute(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, route)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (p *ControlPostgres) GetRoute(ctx context.Context, accountID, routeID string) (domain.Route, error) {
	route, err := scanControlRoute(p.db.QueryRowContext(ctx, `SELECT `+controlRouteColumns+`
		FROM tunnel_routes WHERE account_id=$1 AND id=$2`, accountID, routeID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Route{}, domain.ErrRouteNotFound
	}
	return route, err
}

func (p *ControlPostgres) CreateRoute(ctx context.Context, cmd domain.CreateRouteCommand) (domain.CreateRouteResult, error) {
	if cmd.AccountID == "" || cmd.IdempotencyKey == "" {
		return domain.CreateRouteResult{}, domain.ErrInvalidRequest
	}
	requestHash, err := controlRequestDigest(createRouteReplayRequest{Protocol: cmd.Protocol, Label: cmd.Subdomain})
	if err != nil {
		return domain.CreateRouteResult{}, err
	}
	routeID, err := identity.NewRouteID()
	if err != nil {
		return domain.CreateRouteResult{}, err
	}
	replayID, err := identity.NewID("rpl_", 16)
	if err != nil {
		return domain.CreateRouteResult{}, err
	}
	keyHash := controlDigest(cmd.IdempotencyKey)

	var result domain.CreateRouteResult
	err = p.withControlTx(ctx, func(tx *sql.Tx) error {
		result = domain.CreateRouteResult{}
		replayCandidate, err := p.discoverCreateReplay(ctx, tx, cmd.AccountID, keyHash)
		if err != nil {
			return err
		}
		nameCandidate, err := discoverControlNameHolder(ctx, tx, cmd.Subdomain)
		if err != nil {
			return err
		}
		accountIDs := []string{cmd.AccountID}
		if nameCandidate.AccountID != "" {
			accountIDs = append(accountIDs, nameCandidate.AccountID)
		}
		if err := lockControlAccounts(ctx, tx, accountIDs...); err != nil {
			return err
		}
		if err := routes.ValidateSubdomain(cmd.Subdomain); err != nil {
			return err
		}

		currentReplay, err := p.discoverCreateReplay(ctx, tx, cmd.AccountID, keyHash)
		if err != nil {
			return err
		}
		currentHolder, err := discoverControlNameHolder(ctx, tx, cmd.Subdomain)
		if err != nil {
			return err
		}
		if currentHolder.AccountID != "" && !containsControlID(accountIDs, currentHolder.AccountID) {
			return retryControlDiscoveryError{}
		}

		routeIDs := make([]string, 0, 2)
		if currentReplay.RouteID != "" {
			routeIDs = append(routeIDs, currentReplay.RouteID)
		} else if replayCandidate.RouteID != "" {
			routeIDs = append(routeIDs, replayCandidate.RouteID)
		}
		if currentHolder.RouteID != "" {
			routeIDs = append(routeIDs, currentHolder.RouteID)
		} else if nameCandidate.RouteID != "" {
			routeIDs = append(routeIDs, nameCandidate.RouteID)
		}
		if err := lockControlRoutes(ctx, tx, routeIDs...); err != nil {
			return err
		}
		now := p.nowUTC()

		if currentReplay.RouteID != "" {
			replay, err := p.readControlReplay(ctx, tx, domain.OperationCreateRoute, cmd.AccountID, keyHash, true)
			if err != nil {
				return err
			}
			if replay.RequestHash != requestHash {
				return domain.ErrIdempotencyConflict
			}
			var owner string
			if err := tx.QueryRowContext(ctx, `SELECT account_id::text FROM tunnel_routes WHERE id=$1`, replay.RouteID).Scan(&owner); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return domain.ErrRouteNotFound
				}
				return err
			}
			if owner != cmd.AccountID {
				return domain.ErrRouteNotFound
			}
			plaintext, err := p.openControlReplay(replay, now)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(plaintext, &result); err != nil {
				return err
			}
			result.Replayed = true
			return nil
		}

		if currentHolder.RouteID != "" {
			holder, err := scanControlRoute(tx.QueryRowContext(ctx, `SELECT `+controlRouteColumns+` FROM tunnel_routes WHERE id=$1`, currentHolder.RouteID))
			if err != nil {
				return err
			}
			if holder.NameReleasedAt == nil && holder.Status == domain.RouteDeleted && holder.RecoverableUntil != nil && !now.Before(*holder.RecoverableUntil) {
				if _, err := tx.ExecContext(ctx, `UPDATE tunnel_routes SET name_released_at=$2, updated_at=$2
					WHERE id=$1 AND status='deleted' AND name_released_at IS NULL AND recoverable_until <= $2`, holder.ID, now); err != nil {
					return err
				}
			} else if holder.NameReleasedAt == nil {
				return domain.ErrSubdomainConflict
			}
		}

		policy, err := p.policy.Policy(ctx, cmd.AccountID)
		if err != nil {
			return err
		}
		var activeCount int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tunnel_routes WHERE account_id=$1 AND status='active'`, cmd.AccountID).Scan(&activeCount); err != nil {
			return err
		}
		if err := policy.CheckCreate(cmd.Protocol, activeCount); err != nil {
			return err
		}

		route := domain.Route{
			ID: routeID, AccountID: cmd.AccountID, Protocol: cmd.Protocol, Subdomain: cmd.Subdomain,
			ProxyName: routeID, Status: domain.RouteActive, CreatedAt: now, UpdatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tunnel_routes
			(id, account_id, protocol, subdomain, proxy_name, status, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,'active',$6,$6)`, route.ID, route.AccountID, route.Protocol, route.Subdomain, route.ProxyName, now); err != nil {
			return mapControlRoutePersistenceError(err)
		}
		result = domain.CreateRouteResult{Route: route}
		plaintext, err := json.Marshal(result)
		if err != nil {
			return err
		}
		replay := domain.OperationReplay{
			ID: replayID, Operation: domain.OperationCreateRoute, PrincipalKey: cmd.AccountID,
			KeyHash: keyHash, RequestHash: requestHash, RouteID: route.ID,
			CreatedAt: now, ExpiresAt: now.Add(controlReplayRetention),
		}
		if err := p.saveControlReplay(ctx, tx, replay, plaintext); err != nil {
			return mapControlRoutePersistenceError(err)
		}
		return nil
	})
	if err != nil {
		return domain.CreateRouteResult{}, err
	}
	return result, nil
}

func (p *ControlPostgres) DeleteRoute(ctx context.Context, accountID, routeID string) (domain.Route, error) {
	var deleted domain.Route
	err := p.withControlTx(ctx, func(tx *sql.Tx) error {
		deleted = domain.Route{}
		if err := lockControlAccounts(ctx, tx, accountID); err != nil {
			return err
		}
		route, err := lockOwnedControlRoute(ctx, tx, accountID, routeID)
		if err != nil {
			return err
		}
		if route.Status == domain.RouteDeleted {
			return domain.ErrRouteDeleted
		}
		if err := lockUnusedControlLaunchCodes(ctx, tx, routeID); err != nil {
			return err
		}
		if err := lockActiveControlRun(ctx, tx, routeID); err != nil {
			return err
		}
		now := p.nowUTC()
		deleted, err = scanControlRoute(tx.QueryRowContext(ctx, `UPDATE tunnel_routes
			SET status='deleted', updated_at=$3, deleted_at=$3, recoverable_until=$4
			WHERE account_id=$1 AND id=$2 RETURNING `+controlRouteColumns,
			accountID, routeID, now, now.Add(controlRouteRecoveryPeriod)))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE route_launch_codes SET revoked_at=$2
			WHERE route_id=$1 AND redeemed_at IS NULL AND revoked_at IS NULL`, routeID, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE tunnel_runs
			SET status='stopping', desired_state='stopped', stop_requested_at=COALESCE(stop_requested_at,$2),
				stop_reason=COALESCE(stop_reason,'route_deleted')
			WHERE route_id=$1 AND status IN ('starting','online','stopping')`, routeID, now)
		return err
	})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Route{}, domain.ErrRouteNotFound
		}
		return domain.Route{}, err
	}
	return deleted, nil
}

func (p *ControlPostgres) RestoreRoute(ctx context.Context, accountID, routeID string) (domain.Route, error) {
	var restored domain.Route
	err := p.withControlTx(ctx, func(tx *sql.Tx) error {
		restored = domain.Route{}
		if err := lockControlAccounts(ctx, tx, accountID); err != nil {
			return err
		}
		route, err := lockOwnedControlRoute(ctx, tx, accountID, routeID)
		if err != nil {
			return err
		}
		if route.Status != domain.RouteDeleted || route.NameReleasedAt != nil || route.RecoverableUntil == nil {
			return domain.ErrRouteDeleted
		}
		now := p.nowUTC()
		if !now.Before(*route.RecoverableUntil) {
			return domain.ErrRouteDeleted
		}
		policy, err := p.policy.Policy(ctx, accountID)
		if err != nil {
			return err
		}
		var activeCount int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tunnel_routes WHERE account_id=$1 AND status='active'`, accountID).Scan(&activeCount); err != nil {
			return err
		}
		if err := policy.CheckCreate(route.Protocol, activeCount); err != nil {
			return err
		}
		restored, err = scanControlRoute(tx.QueryRowContext(ctx, `UPDATE tunnel_routes
			SET status='active', updated_at=$3, deleted_at=NULL, recoverable_until=NULL, name_released_at=NULL
			WHERE account_id=$1 AND id=$2 RETURNING `+controlRouteColumns, accountID, routeID, now))
		return mapControlRoutePersistenceError(err)
	})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Route{}, domain.ErrRouteNotFound
		}
		return domain.Route{}, err
	}
	return restored, nil
}

func (p *ControlPostgres) ReleaseExpiredNames(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, domain.ErrInvalidRequest
	}
	var released int
	err := p.withControlTx(ctx, func(tx *sql.Tx) error {
		released = 0
		rows, err := tx.QueryContext(ctx, `SELECT id, account_id::text FROM tunnel_routes
			WHERE status='deleted' AND name_released_at IS NULL
			ORDER BY recoverable_until, id LIMIT $1`, limit)
		if err != nil {
			return err
		}
		var candidates []controlRouteCandidate
		for rows.Next() {
			var candidate controlRouteCandidate
			if err := rows.Scan(&candidate.RouteID, &candidate.AccountID); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, candidate)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		accountIDs := make([]string, 0, len(candidates))
		routeIDs := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			accountIDs = append(accountIDs, candidate.AccountID)
			routeIDs = append(routeIDs, candidate.RouteID)
		}
		if err := lockControlAccounts(ctx, tx, accountIDs...); err != nil {
			return err
		}
		if err := lockControlRoutes(ctx, tx, routeIDs...); err != nil {
			return err
		}
		now := p.nowUTC()
		for _, candidate := range candidates {
			result, err := tx.ExecContext(ctx, `UPDATE tunnel_routes SET name_released_at=$2, updated_at=$2
				WHERE id=$1 AND status='deleted' AND name_released_at IS NULL AND recoverable_until <= $2`, candidate.RouteID, now)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			released += int(changed)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return released, nil
}

func (p *ControlPostgres) discoverCreateReplay(ctx context.Context, tx *sql.Tx, principal, keyHash string) (domain.OperationReplay, error) {
	replay, err := p.readControlReplay(ctx, tx, domain.OperationCreateRoute, principal, keyHash, false)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OperationReplay{}, nil
	}
	return replay, err
}

func discoverControlNameHolder(ctx context.Context, tx *sql.Tx, subdomain string) (controlRouteCandidate, error) {
	var candidate controlRouteCandidate
	err := tx.QueryRowContext(ctx, `SELECT id, account_id::text FROM tunnel_routes
		WHERE subdomain=$1 AND name_released_at IS NULL ORDER BY id LIMIT 1`, subdomain).Scan(&candidate.RouteID, &candidate.AccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return controlRouteCandidate{}, nil
	}
	return candidate, err
}

func lockControlRoutes(ctx context.Context, tx *sql.Tx, ids ...string) error {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	last := ""
	for _, id := range sorted {
		if id == "" || id == last {
			continue
		}
		last = id
		var locked string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM tunnel_routes WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err != nil {
			return err
		}
	}
	return nil
}

func lockOwnedControlRoute(ctx context.Context, tx *sql.Tx, accountID, routeID string) (domain.Route, error) {
	route, err := scanControlRoute(tx.QueryRowContext(ctx, `SELECT `+controlRouteColumns+`
		FROM tunnel_routes WHERE account_id=$1 AND id=$2 FOR UPDATE`, accountID, routeID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Route{}, domain.ErrRouteNotFound
	}
	return route, err
}

func lockUnusedControlLaunchCodes(ctx context.Context, tx *sql.Tx, routeID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM route_launch_codes
		WHERE route_id=$1 AND redeemed_at IS NULL AND revoked_at IS NULL ORDER BY id FOR UPDATE`, routeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func lockActiveControlRun(ctx context.Context, tx *sql.Tx, routeID string) error {
	var runID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM tunnel_runs
		WHERE route_id=$1 AND status IN ('starting','online','stopping') ORDER BY id LIMIT 1 FOR UPDATE`, routeID).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func containsControlID(ids []string, wanted string) bool {
	for _, id := range ids {
		if id == wanted {
			return true
		}
	}
	return false
}

func mapControlRoutePersistenceError(err error) error {
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		switch postgresError.ConstraintName {
		case "control_routes_unreleased_name_uq":
			return domain.ErrSubdomainConflict
		case "control_replay_key_uq":
			return domain.ErrIdempotencyConflict
		}
	}
	return err
}
