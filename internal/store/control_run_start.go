package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"time"
	"unicode/utf8"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
)

const (
	controlRunConnectWindow = 2 * time.Minute
	controlRunLeaseWindow   = 90 * time.Second
	controlRunReplayWindow  = 2 * time.Minute
)

// This payload is only ever encrypted; the domain result deliberately omits its token from JSON.
type controlStartReplayPayload struct {
	Run             domain.Run `json:"run"`
	CredentialID    string     `json:"credential_id"`
	CredentialToken string     `json:"credential_token"`
}

func (p *ControlPostgres) StartAccountRun(ctx context.Context, cmd domain.AccountStartCommand) (domain.StartResult, error) {
	if cmd.AccountID == "" || cmd.RouteID == "" || !validControlRunKey(cmd.IdempotencyKey) || !validControlRunIP(cmd.RequestIP) {
		return domain.StartResult{}, domain.ErrInvalidRequest
	}
	requestHash, err := controlRequestDigest(struct {
		RouteID string `json:"route_id"`
	}{cmd.RouteID})
	if err != nil {
		return domain.StartResult{}, err
	}
	var result domain.StartResult
	err = p.withControlTx(ctx, func(tx *sql.Tx) error {
		route, err := lockRunParentRoute(ctx, tx, cmd.RouteID, cmd.AccountID)
		if err != nil {
			return err
		}
		if route.Status != domain.RouteActive {
			return domain.ErrRouteDeleted
		}
		replay, err := p.readControlReplay(ctx, tx, domain.OperationStartRun, route.AccountID, controlDigest(cmd.IdempotencyKey), false)
		if err == nil {
			result, err = p.replayControlStart(ctx, tx, route, replay, requestHash)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := ensureControlRunSlotAvailable(ctx, tx, route.ID); err != nil {
			return err
		}
		now := p.nowUTC()
		result, err = p.createControlRun(ctx, tx, route, domain.StartedViaDeviceLogin, cmd.RequestIP,
			domain.OperationStartRun, route.AccountID, controlDigest(cmd.IdempotencyKey), requestHash, now)
		return err
	})
	if err != nil {
		return domain.StartResult{}, err
	}
	return result, nil
}

func (p *ControlPostgres) RedeemLaunchCode(ctx context.Context, cmd domain.LaunchRedeemCommand) (domain.StartResult, error) {
	codeID, err := identity.ParseLaunchCredential(cmd.Token)
	if err != nil {
		return domain.StartResult{}, identity.ErrInvalidCredential
	}
	if !validControlRunKey(cmd.Nonce) || !validControlRunIP(cmd.RequestIP) {
		return domain.StartResult{}, domain.ErrInvalidRequest
	}
	secretHash := identity.HashToken(p.launchPepper, cmd.Token)
	var result domain.StartResult
	err = p.withControlTx(ctx, func(tx *sql.Tx) error {
		var routeID string
		if err := tx.QueryRowContext(ctx, `SELECT route_id FROM route_launch_codes WHERE id=$1`, codeID).Scan(&routeID); err != nil {
			return controlRunMissingAs(err, identity.ErrInvalidCredential)
		}
		route, err := lockRunParentRoute(ctx, tx, routeID, "")
		if err != nil {
			return err
		}
		code, err := lockRunLaunchCode(ctx, tx, codeID, route.ID)
		if err != nil {
			return controlRunMissingAs(err, identity.ErrInvalidCredential)
		}
		if !identity.TokenHashEqual(code.SecretHash, secretHash) {
			return identity.ErrInvalidCredential
		}
		if route.Status != domain.RouteActive {
			return domain.ErrRouteDeleted
		}
		if code.RevokedAt != nil {
			return domain.ErrLaunchCodeRevoked
		}
		requestHash, err := controlRequestDigest(struct {
			CodeID  string `json:"code_id"`
			RouteID string `json:"route_id"`
		}{code.ID, route.ID})
		if err != nil {
			return err
		}
		replay, err := p.readControlReplay(ctx, tx, domain.OperationRedeemLaunch, code.ID, controlDigest(cmd.Nonce), false)
		if err == nil {
			if code.RedeemedAt == nil {
				return domain.ErrIdempotencyConflict
			}
			result, err = p.replayControlStart(ctx, tx, route, replay, requestHash)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if code.RedeemedAt != nil {
			return domain.ErrLaunchCodeUsed
		}
		if err := ensureControlRunSlotAvailable(ctx, tx, route.ID); err != nil {
			return err
		}
		now := p.nowUTC()
		if !now.Before(code.ExpiresAt) {
			return domain.ErrLaunchCodeExpired
		}
		if _, err := tx.ExecContext(ctx, `UPDATE route_launch_codes SET redeemed_at=$2 WHERE id=$1`, code.ID, now); err != nil {
			return err
		}
		result, err = p.createControlRun(ctx, tx, route, domain.StartedViaLaunchCode, cmd.RequestIP,
			domain.OperationRedeemLaunch, code.ID, controlDigest(cmd.Nonce), requestHash, now)
		return err
	})
	if err != nil {
		return domain.StartResult{}, err
	}
	return result, nil
}

func (p *ControlPostgres) createControlRun(ctx context.Context, tx *sql.Tx, route domain.Route, via domain.StartedVia, requestIP netip.Addr, operation, principal, keyHash, requestHash string, now time.Time) (domain.StartResult, error) {
	runID, err := identity.NewRunID()
	if err != nil {
		return domain.StartResult{}, err
	}
	credential, err := identity.NewRunCredential()
	if err != nil {
		return domain.StartResult{}, err
	}
	replayID, err := identity.NewID("rpl_", 16)
	if err != nil {
		return domain.StartResult{}, err
	}
	run := domain.Run{
		ID: runID, RouteID: route.ID, StartedVia: via, Status: domain.RunStarting,
		DesiredState: domain.DesiredRunning, RequestIP: requestIP.Unmap(), CreatedAt: now,
		ConnectDeadlineAt: now.Add(controlRunConnectWindow),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tunnel_runs
		(id,route_id,started_via,status,desired_state,request_ip,created_at,connect_deadline_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, run.ID, run.RouteID, run.StartedVia, run.Status,
		run.DesiredState, run.RequestIP.String(), run.CreatedAt, run.ConnectDeadlineAt); err != nil {
		return domain.StartResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_credentials (id,run_id,secret_hash,created_at) VALUES ($1,$2,$3,$4)`,
		credential.ID, run.ID, identity.HashToken(p.runPepper, credential.Token), now); err != nil {
		return domain.StartResult{}, err
	}
	payload, err := json.Marshal(controlStartReplayPayload{Run: run, CredentialID: credential.ID, CredentialToken: credential.Token})
	if err != nil {
		return domain.StartResult{}, err
	}
	replay := domain.OperationReplay{ID: replayID, Operation: operation, PrincipalKey: principal,
		KeyHash: keyHash, RequestHash: requestHash, RouteID: route.ID, RunID: run.ID, CreatedAt: now, ExpiresAt: now.Add(controlRunReplayWindow)}
	if err := p.saveControlReplay(ctx, tx, replay, payload); err != nil {
		return domain.StartResult{}, err
	}
	return domain.StartResult{Run: run, CredentialID: credential.ID, CredentialToken: credential.Token}, nil
}

func (p *ControlPostgres) replayControlStart(ctx context.Context, tx *sql.Tx, route domain.Route, candidate domain.OperationReplay, requestHash string) (domain.StartResult, error) {
	if candidate.RequestHash != requestHash || candidate.RouteID != route.ID || candidate.RunID == "" {
		return domain.StartResult{}, domain.ErrIdempotencyConflict
	}
	run, credential, err := lockControlRunAndCredential(ctx, tx, candidate.RunID, route.ID)
	if err != nil {
		return domain.StartResult{}, controlRunMissingAs(err, domain.ErrRunStopped)
	}
	replay, err := p.readControlReplay(ctx, tx, candidate.Operation, candidate.PrincipalKey, candidate.KeyHash, true)
	if err != nil {
		return domain.StartResult{}, err
	}
	if replay.ID != candidate.ID || replay.RequestHash != requestHash || replay.RouteID != route.ID || replay.RunID != run.ID {
		return domain.StartResult{}, domain.ErrIdempotencyConflict
	}
	now := p.nowUTC()
	if !run.AllowsConnectionAt(route, credential, now) {
		return domain.StartResult{}, domain.ErrRunStopped
	}
	plaintext, err := p.openControlReplay(replay, now)
	if err != nil {
		return domain.StartResult{}, err
	}
	var payload controlStartReplayPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return domain.StartResult{}, identity.ErrInvalidReplayCiphertext
	}
	credentialID, err := identity.ParseRunCredential(payload.CredentialToken)
	if err != nil || payload.Run.ID != run.ID || payload.Run.RouteID != route.ID || payload.CredentialID != credential.ID || credentialID != credential.ID ||
		!identity.TokenHashEqual(credential.SecretHash, identity.HashToken(p.runPepper, payload.CredentialToken)) {
		return domain.StartResult{}, identity.ErrInvalidReplayCiphertext
	}
	return domain.StartResult{Run: payload.Run, CredentialID: payload.CredentialID, CredentialToken: payload.CredentialToken, Replayed: true}, nil
}

func ensureControlRunSlotAvailable(ctx context.Context, tx *sql.Tx, routeID string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM tunnel_runs WHERE route_id=$1 AND status IN ('starting','online','stopping') FOR UPDATE`, routeID).Scan(&id)
	if err == nil {
		return domain.ErrRunAlreadyActive
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func lockRunLaunchCode(ctx context.Context, tx *sql.Tx, codeID, routeID string) (domain.LaunchCode, error) {
	var code domain.LaunchCode
	err := tx.QueryRowContext(ctx, `SELECT id,route_id,secret_hash,created_at,expires_at,redeemed_at,revoked_at
		FROM route_launch_codes WHERE id=$1 AND route_id=$2 FOR UPDATE`, codeID, routeID).Scan(
		&code.ID, &code.RouteID, &code.SecretHash, &code.CreatedAt, &code.ExpiresAt, &code.RedeemedAt, &code.RevokedAt)
	return code, err
}

func validControlRunKey(key string) bool {
	return len(key) > 0 && len(key) <= 256 && utf8.ValidString(key)
}

func validControlRunIP(ip netip.Addr) bool {
	return ip.IsValid() && ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast()
}
