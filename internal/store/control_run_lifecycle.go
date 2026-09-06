package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
)

func (p *ControlPostgres) Heartbeat(ctx context.Context, proof domain.RunProof) (domain.HeartbeatResult, error) {
	credentialID, err := identity.ParseRunCredential(proof.Token)
	if err != nil || proof.RunID == "" {
		return domain.HeartbeatResult{}, domain.ErrInvalidRunProof
	}
	var result domain.HeartbeatResult
	err = p.withControlTx(ctx, func(tx *sql.Tx) error {
		route, run, credential, err := lockControlProofRun(ctx, tx, proof.RunID)
		if err != nil {
			return err
		}
		if !p.matchesControlRunProof(proof, credentialID, run, credential) {
			return domain.ErrInvalidRunProof
		}
		now := p.nowUTC()
		if !run.AllowsConnectionAt(route, credential, now) {
			run, err = stopControlRun(ctx, tx, run, now, controlRunStopReason(route, run, credential, now))
			if err != nil {
				return err
			}
			result = domain.HeartbeatResult{Run: run, Stopped: true}
			return nil
		}
		lease := run.LeaseExpiresAt
		if run.ConnectedAt != nil {
			expires := now.Add(controlRunLeaseWindow)
			lease = &expires
		}
		run, err = scanControlRun(tx.QueryRowContext(ctx, `UPDATE tunnel_runs SET last_heartbeat_at=$2,lease_expires_at=$3 WHERE id=$1 RETURNING `+controlRunColumns, run.ID, now, lease))
		if err != nil {
			return err
		}
		result = domain.HeartbeatResult{Run: run}
		return nil
	})
	if err != nil {
		return domain.HeartbeatResult{}, err
	}
	return result, nil
}

func (p *ControlPostgres) RequestOwnedStop(ctx context.Context, accountID, routeID string) (domain.Run, error) {
	if accountID == "" || routeID == "" {
		return domain.Run{}, domain.ErrInvalidRequest
	}
	var result domain.Run
	err := p.withControlTx(ctx, func(tx *sql.Tx) error {
		route, err := lockRunParentRoute(ctx, tx, routeID, accountID)
		if err != nil {
			return err
		}
		var runID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM tunnel_runs WHERE route_id=$1 ORDER BY (status='offline'),created_at DESC,id DESC LIMIT 1`, route.ID).Scan(&runID)
		if err != nil {
			return controlRunMissingAs(err, domain.ErrNotFound)
		}
		run, _, err := lockControlRunAndCredential(ctx, tx, runID, route.ID)
		if err != nil {
			return controlRunMissingAs(err, domain.ErrNotFound)
		}
		result, err = stopControlRun(ctx, tx, run, p.nowUTC(), "owner_stop")
		return err
	})
	if err != nil {
		return domain.Run{}, err
	}
	return result, nil
}

func (p *ControlPostgres) RequestCredentialStop(ctx context.Context, proof domain.RunProof) (domain.Run, error) {
	credentialID, err := identity.ParseRunCredential(proof.Token)
	if err != nil || proof.RunID == "" {
		return domain.Run{}, domain.ErrInvalidRunProof
	}
	var result domain.Run
	err = p.withControlTx(ctx, func(tx *sql.Tx) error {
		_, run, credential, err := lockControlProofRun(ctx, tx, proof.RunID)
		if err != nil {
			return err
		}
		if !p.matchesControlRunProof(proof, credentialID, run, credential) {
			return domain.ErrInvalidRunProof
		}
		result, err = stopControlRun(ctx, tx, run, p.nowUTC(), "credential_stop")
		return err
	})
	if err != nil {
		return domain.Run{}, err
	}
	return result, nil
}

func (p *ControlPostgres) ConfirmOnline(ctx context.Context, evidence domain.RunRegistrationEvidence) (domain.Run, error) {
	if !evidence.ObservedOnline || !validControlRunIP(evidence.ConnectedIP) || evidence.RunID == "" || evidence.RouteID == "" || evidence.ProxyName == "" {
		return domain.Run{}, domain.ErrRunEvidenceInvalid
	}
	var result domain.Run
	err := p.withControlTx(ctx, func(tx *sql.Tx) error {
		route, run, credential, err := lockControlEvidenceRun(ctx, tx, evidence.RunID, evidence.RouteID, evidence.ProxyName)
		if err != nil {
			return err
		}
		now := p.nowUTC()
		if !run.AllowsConnectionAt(route, credential, now) {
			return domain.ErrRunStopped
		}
		connectedAt, lease := run.ConnectedAt, run.LeaseExpiresAt
		if connectedAt == nil {
			connectedAt = &now
			expires := now.Add(controlRunLeaseWindow)
			lease = &expires
		}
		result, err = scanControlRun(tx.QueryRowContext(ctx, `UPDATE tunnel_runs
			SET status='online',connected_at=$2,connected_ip=$3,lease_expires_at=$4,
				proxy_registration_granted=TRUE
			WHERE id=$1 RETURNING `+controlRunColumns,
			run.ID, connectedAt, evidence.ConnectedIP.Unmap().String(), lease))
		return err
	})
	if err != nil {
		return domain.Run{}, err
	}
	return result, nil
}

func (p *ControlPostgres) ConfirmOffline(ctx context.Context, evidence domain.RunDisconnectEvidence) (domain.Run, error) {
	offlineSample := evidence.ObservedOffline && !evidence.ProxyNotObserved && !evidence.ConfirmedClientDisconnected
	drainedAbsent := !evidence.ObservedOffline && evidence.ProxyNotObserved && evidence.ConfirmedClientDisconnected
	if (!offlineSample && !drainedAbsent) || evidence.CurrentConnections != 0 || evidence.RunID == "" || evidence.RouteID == "" || evidence.ProxyName == "" {
		return domain.Run{}, domain.ErrRunEvidenceInvalid
	}
	var result domain.Run
	err := p.withControlTx(ctx, func(tx *sql.Tx) error {
		route, run, credential, err := lockControlEvidenceRun(ctx, tx, evidence.RunID, evidence.RouteID, evidence.ProxyName)
		if err != nil {
			return err
		}
		if drainedAbsent && !run.ProxyRegistrationGranted {
			return domain.ErrRunEvidenceInvalid
		}
		if run.Status == domain.RunOffline {
			result = run
			return nil
		}
		now := p.nowUTC()
		if run.AllowsConnectionAt(route, credential, now) {
			return domain.ErrRunEvidenceInvalid
		}
		run, err = stopControlRun(ctx, tx, run, now, controlRunStopReason(route, run, credential, now))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_credentials SET revoked_at=$2 WHERE id=$1 AND revoked_at IS NULL`, credential.ID, now); err != nil {
			return err
		}
		result, err = scanControlRun(tx.QueryRowContext(ctx, `UPDATE tunnel_runs SET status='offline',stopped_at=$2 WHERE id=$1 RETURNING `+controlRunColumns, run.ID, now))
		return err
	})
	if err != nil {
		return domain.Run{}, err
	}
	return result, nil
}

func lockControlEvidenceRun(ctx context.Context, tx *sql.Tx, runID, routeID, proxyName string) (domain.Route, domain.Run, domain.RunCredential, error) {
	route, run, credential, err := lockControlProofRun(ctx, tx, runID)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidRunProof) || errors.Is(err, domain.ErrRouteNotFound) {
			err = domain.ErrRunEvidenceInvalid
		}
		return domain.Route{}, domain.Run{}, domain.RunCredential{}, err
	}
	if route.ID != routeID || run.RouteID != routeID || route.ProxyName != proxyName {
		return domain.Route{}, domain.Run{}, domain.RunCredential{}, domain.ErrRunEvidenceInvalid
	}
	return route, run, credential, nil
}

func stopControlRun(ctx context.Context, tx *sql.Tx, run domain.Run, now time.Time, reason string) (domain.Run, error) {
	if run.Status == domain.RunOffline || run.Status == domain.RunStopping && run.DesiredState == domain.DesiredStopped {
		return run, nil
	}
	return scanControlRun(tx.QueryRowContext(ctx, `UPDATE tunnel_runs SET status='stopping',desired_state='stopped',
		stop_requested_at=COALESCE(stop_requested_at,$2),stop_reason=COALESCE(stop_reason,$3) WHERE id=$1 RETURNING `+controlRunColumns, run.ID, now, reason))
}

func controlRunStopReason(route domain.Route, run domain.Run, credential domain.RunCredential, now time.Time) string {
	if route.Status != domain.RouteActive {
		return "route_deleted"
	}
	if credential.RevokedAt != nil {
		return "credential_revoked"
	}
	if run.ConnectedAt == nil && !now.Before(run.ConnectDeadlineAt) {
		return "connect_timeout"
	}
	if run.ConnectedAt != nil && (run.LeaseExpiresAt == nil || !now.Before(*run.LeaseExpiresAt)) {
		return "lease_expired"
	}
	return "stop_requested"
}
