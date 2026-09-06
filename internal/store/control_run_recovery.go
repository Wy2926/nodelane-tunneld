package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

const maxControlReconciliationBatch = 1000

func (p *ControlPostgres) ReleaseNeverGranted(ctx context.Context, runID string) (domain.Run, error) {
	if runID == "" {
		return domain.Run{}, domain.ErrRunEvidenceInvalid
	}
	var result domain.Run
	err := p.withControlTx(ctx, func(tx *sql.Tx) error {
		route, run, credential, err := lockControlProofRun(ctx, tx, runID)
		if err != nil {
			if errors.Is(err, domain.ErrInvalidRunProof) || errors.Is(err, domain.ErrRouteNotFound) {
				return domain.ErrRunEvidenceInvalid
			}
			return err
		}
		if run.ProxyRegistrationGranted {
			return domain.ErrRunEvidenceInvalid
		}
		now := p.nowUTC()
		if run.Status == domain.RunOffline {
			if run.DesiredState != domain.DesiredStopped || run.StoppedAt == nil {
				return domain.ErrRunEvidenceInvalid
			}
		} else {
			if run.AllowsConnectionAt(route, credential, now) {
				return domain.ErrRunEvidenceInvalid
			}
			run, err = stopControlRun(ctx, tx, run, now, controlRunStopReason(route, run, credential, now))
			if err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_credentials SET revoked_at=COALESCE(revoked_at,$2) WHERE id=$1`, credential.ID, now); err != nil {
			return err
		}
		result, err = scanControlRun(tx.QueryRowContext(ctx, `UPDATE tunnel_runs SET status='offline',stopped_at=COALESCE(stopped_at,$2)
			WHERE id=$1 RETURNING `+controlRunColumns, run.ID, now))
		return err
	})
	if err != nil {
		return domain.Run{}, err
	}
	return result, nil
}

func (p *ControlPostgres) PendingRunReconciliation(ctx context.Context, limit int) ([]domain.RunAuthorization, error) {
	if limit < 1 || limit > maxControlReconciliationBatch {
		return nil, domain.ErrInvalidRequest
	}
	now := p.nowUTC()
	rows, err := p.db.QueryContext(ctx, `SELECT r.id,r.route_id,r.reconciliation_claimed_at FROM tunnel_runs r
		JOIN tunnel_routes t ON t.id=r.route_id
		JOIN run_credentials c ON c.run_id=r.id
		WHERE r.status IN ('starting','online','stopping') AND (
			r.status='stopping' OR t.status<>'active' OR c.revoked_at IS NOT NULL OR r.desired_state<>'running' OR
			(r.connected_at IS NULL AND r.connect_deadline_at <= $1) OR
			(r.connected_at IS NOT NULL AND (r.lease_expires_at IS NULL OR r.lease_expires_at <= $1)))
		ORDER BY r.reconciliation_claimed_at NULLS FIRST,
			COALESCE(r.stop_requested_at,
				CASE WHEN r.connected_at IS NULL THEN r.connect_deadline_at ELSE r.lease_expires_at END,
				r.created_at),r.created_at,r.id LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		runID, routeID string
		claimedAt      sql.NullTime
	}
	candidates := make([]candidate, 0, limit)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.runID, &item.routeID, &item.claimedAt); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	result := make([]domain.RunAuthorization, 0, len(candidates))
	for _, candidate := range candidates {
		var item *domain.RunAuthorization
		err := p.withControlTx(ctx, func(tx *sql.Tx) error {
			route, run, credential, err := lockControlProofRun(ctx, tx, candidate.runID)
			if errors.Is(err, domain.ErrInvalidRunProof) || errors.Is(err, domain.ErrRouteNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if route.ID != candidate.routeID || !controlRunNeedsReconciliation(route, run, credential, p.nowUTC()) {
				return nil
			}
			var currentClaim sql.NullTime
			if err := tx.QueryRowContext(ctx, `SELECT reconciliation_claimed_at FROM tunnel_runs WHERE id=$1`, run.ID).Scan(&currentClaim); err != nil {
				return err
			}
			if currentClaim.Valid != candidate.claimedAt.Valid || currentClaim.Valid && !currentClaim.Time.Equal(candidate.claimedAt.Time) {
				return nil
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tunnel_runs SET reconciliation_claimed_at=$2 WHERE id=$1`, run.ID, p.nowUTC()); err != nil {
				return err
			}
			value := domain.RunAuthorization{Route: route, Run: run, CredentialID: credential.ID}
			item = &value
			return nil
		})
		if err != nil {
			return nil, err
		}
		if item != nil {
			result = append(result, *item)
		}
	}
	return result, nil
}

func controlRunNeedsReconciliation(route domain.Route, run domain.Run, credential domain.RunCredential, now time.Time) bool {
	return (run.Status == domain.RunStarting || run.Status == domain.RunOnline || run.Status == domain.RunStopping) &&
		!run.AllowsConnectionAt(route, credential, now)
}
