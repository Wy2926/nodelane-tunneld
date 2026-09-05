package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

type controlSweepCandidate struct {
	id, routeID string
}

func (p *ControlPostgres) Sweep(ctx context.Context, limit int) (domain.SweepResult, error) {
	if limit <= 0 || limit > 10000 {
		return domain.SweepResult{}, domain.ErrInvalidRequest
	}
	var result domain.SweepResult
	remaining := limit
	// Discovery never locks rows. Each candidate reacquires its parent locks and samples time again.
	queries := []struct {
		query string
		apply func(context.Context, controlSweepCandidate) (domain.SweepResult, error)
	}{
		{`SELECT r.id,r.route_id FROM tunnel_runs r JOIN tunnel_routes t ON t.id=r.route_id
			JOIN run_credentials c ON c.run_id=r.id WHERE r.status IN ('starting','online') AND
			(t.status<>'active' OR c.revoked_at IS NOT NULL OR r.desired_state<>'running' OR
			(r.connected_at IS NULL AND r.connect_deadline_at<=$1) OR
			(r.connected_at IS NOT NULL AND (r.lease_expires_at IS NULL OR r.lease_expires_at<=$1)))
			ORDER BY r.created_at,r.id LIMIT $2`, p.sweepControlRun},
		{`SELECT id,route_id FROM tunnel_runs WHERE status='offline' AND stopped_at<=$1::timestamptz-interval '2 minutes'
			AND NOT EXISTS(SELECT 1 FROM operation_replays p WHERE p.run_id=tunnel_runs.id AND p.expires_at>$1)
			ORDER BY stopped_at,id LIMIT $2`, p.sweepControlRun},
		{`SELECT id,route_id FROM operation_replays WHERE expires_at<=$1 ORDER BY expires_at,id LIMIT $2`, p.sweepControlReplay},
		{`SELECT id,route_id FROM route_launch_codes WHERE (expires_at<=$1 OR revoked_at IS NOT NULL)
			AND NOT EXISTS(SELECT 1 FROM operation_replays p WHERE p.operation='redeem_launch'
			AND p.principal_key=route_launch_codes.id AND p.expires_at>$1)
			ORDER BY expires_at,id LIMIT $2`, p.sweepControlCode},
	}
	for _, phase := range queries {
		if remaining == 0 {
			break
		}
		candidates, err := p.discoverControlSweep(ctx, phase.query, p.nowUTC(), remaining)
		if err != nil {
			return result, err
		}
		for _, candidate := range candidates {
			change, err := phase.apply(ctx, candidate)
			if err != nil {
				return result, err
			}
			result.ExpiredRuns += change.ExpiredRuns
			result.DeletedRuns += change.DeletedRuns
			result.DeletedCodes += change.DeletedCodes
			result.DeletedReplays += change.DeletedReplays
			remaining--
		}
	}
	return result, nil
}

func (p *ControlPostgres) discoverControlSweep(ctx context.Context, query string, now time.Time, limit int) ([]controlSweepCandidate, error) {
	rows, err := p.db.QueryContext(ctx, query, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []controlSweepCandidate
	for rows.Next() {
		var candidate controlSweepCandidate
		var routeID sql.NullString
		if err := rows.Scan(&candidate.id, &routeID); err != nil {
			return nil, err
		}
		candidate.routeID = routeID.String
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func (p *ControlPostgres) sweepControlRun(ctx context.Context, candidate controlSweepCandidate) (domain.SweepResult, error) {
	var change domain.SweepResult
	err := p.withControlTx(ctx, func(tx *sql.Tx) error {
		change = domain.SweepResult{}
		route, err := lockRunParentRoute(ctx, tx, candidate.routeID, "")
		if err != nil {
			return err
		}
		run, credential, err := lockControlRunAndCredential(ctx, tx, candidate.id, route.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		now := p.nowUTC()
		if run.Status == domain.RunStarting || run.Status == domain.RunOnline {
			if run.AllowsConnectionAt(route, credential, now) {
				return nil
			}
			if _, err := stopControlRun(ctx, tx, run, now, controlRunStopReason(route, run, credential, now)); err != nil {
				return err
			}
			change.ExpiredRuns = 1
			return nil
		}
		if run.Status != domain.RunOffline || run.StoppedAt == nil || now.Before(run.StoppedAt.Add(controlRunReplayWindow)) {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `SELECT expires_at FROM operation_replays WHERE run_id=$1 ORDER BY id FOR UPDATE`, run.ID)
		if err != nil {
			return err
		}
		var expiries []time.Time
		for rows.Next() {
			var expiry time.Time
			if err := rows.Scan(&expiry); err != nil {
				_ = rows.Close()
				return err
			}
			expiries = append(expiries, expiry)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		now = p.nowUTC()
		if now.Before(run.StoppedAt.Add(controlRunReplayWindow)) {
			return nil
		}
		for _, expiry := range expiries {
			if now.Before(expiry) {
				return nil
			}
		}
		deleted, err := tx.ExecContext(ctx, `DELETE FROM operation_replays WHERE run_id=$1`, run.ID)
		if err != nil {
			return err
		}
		count, err := deleted.RowsAffected()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM run_credentials WHERE run_id=$1`, run.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tunnel_runs WHERE id=$1`, run.ID); err != nil {
			return err
		}
		change.DeletedReplays, change.DeletedRuns = int(count), 1
		return nil
	})
	if err != nil {
		return domain.SweepResult{}, err
	}
	return change, nil
}

func (p *ControlPostgres) sweepControlReplay(ctx context.Context, candidate controlSweepCandidate) (domain.SweepResult, error) {
	var change domain.SweepResult
	err := p.withControlTx(ctx, func(tx *sql.Tx) error {
		change = domain.SweepResult{}
		if candidate.routeID != "" {
			if _, err := lockRunParentRoute(ctx, tx, candidate.routeID, ""); err != nil {
				return err
			}
		}
		var routeID, runID sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT route_id,run_id FROM operation_replays WHERE id=$1`, candidate.id).Scan(&routeID, &runID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if routeID.String != candidate.routeID {
			return nil
		}
		if runID.Valid {
			if _, _, err := lockControlRunAndCredential(ctx, tx, runID.String, routeID.String); err != nil {
				return err
			}
		}
		var expires time.Time
		var lockedRoute, lockedRun sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT route_id,run_id,expires_at FROM operation_replays WHERE id=$1 FOR UPDATE`, candidate.id).Scan(&lockedRoute, &lockedRun, &expires); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if lockedRoute != routeID || lockedRun != runID || p.nowUTC().Before(expires) {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM operation_replays WHERE id=$1`, candidate.id); err != nil {
			return err
		}
		change.DeletedReplays = 1
		return nil
	})
	if err != nil {
		return domain.SweepResult{}, err
	}
	return change, nil
}

func (p *ControlPostgres) sweepControlCode(ctx context.Context, candidate controlSweepCandidate) (domain.SweepResult, error) {
	var change domain.SweepResult
	err := p.withControlTx(ctx, func(tx *sql.Tx) error {
		change = domain.SweepResult{}
		if _, err := lockRunParentRoute(ctx, tx, candidate.routeID, ""); err != nil {
			return err
		}
		code, err := lockRunLaunchCode(ctx, tx, candidate.id, candidate.routeID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		now := p.nowUTC()
		if now.Before(code.ExpiresAt) && code.RevokedAt == nil {
			return nil
		}
		var unexpired bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM operation_replays
			WHERE operation='redeem_launch' AND principal_key=$1 AND expires_at>$2)`, code.ID, now).Scan(&unexpired); err != nil {
			return err
		}
		if unexpired {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM route_launch_codes WHERE id=$1`, code.ID); err != nil {
			return err
		}
		change.DeletedCodes = 1
		return nil
	})
	if err != nil {
		return domain.SweepResult{}, err
	}
	return change, nil
}
