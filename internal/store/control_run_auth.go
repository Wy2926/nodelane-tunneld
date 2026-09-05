package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
)

func (p *ControlPostgres) AuthorizeRun(ctx context.Context, proof domain.RunProof) (domain.RunAuthorization, error) {
	credentialID, err := identity.ParseRunCredential(proof.Token)
	if err != nil || proof.RunID == "" {
		return domain.RunAuthorization{}, domain.ErrInvalidRunProof
	}
	var result domain.RunAuthorization
	err = p.withControlTx(ctx, func(tx *sql.Tx) error {
		route, run, credential, err := lockControlProofRun(ctx, tx, proof.RunID)
		if err != nil {
			return err
		}
		if !p.matchesControlRunProof(proof, credentialID, run, credential) {
			return domain.ErrInvalidRunProof
		}
		if !run.AllowsConnectionAt(route, credential, p.nowUTC()) {
			return domain.ErrRunStopped
		}
		result = domain.RunAuthorization{Route: route, Run: run, CredentialID: credential.ID}
		return nil
	})
	if err != nil {
		return domain.RunAuthorization{}, err
	}
	return result, nil
}

func (p *ControlPostgres) matchesControlRunProof(proof domain.RunProof, credentialID string, run domain.Run, credential domain.RunCredential) bool {
	return proof.RunID == run.ID && credential.RunID == run.ID && credential.ID == credentialID &&
		identity.TokenHashEqual(credential.SecretHash, identity.HashToken(p.runPepper, proof.Token))
}

func lockControlProofRun(ctx context.Context, tx *sql.Tx, runID string) (domain.Route, domain.Run, domain.RunCredential, error) {
	var routeID string
	if err := tx.QueryRowContext(ctx, `SELECT route_id FROM tunnel_runs WHERE id=$1`, runID).Scan(&routeID); err != nil {
		return domain.Route{}, domain.Run{}, domain.RunCredential{}, controlRunMissingAs(err, domain.ErrInvalidRunProof)
	}
	route, err := lockRunParentRoute(ctx, tx, routeID, "")
	if err != nil {
		return domain.Route{}, domain.Run{}, domain.RunCredential{}, err
	}
	run, credential, err := lockControlRunAndCredential(ctx, tx, runID, route.ID)
	if err != nil {
		return domain.Route{}, domain.Run{}, domain.RunCredential{}, controlRunMissingAs(err, domain.ErrInvalidRunProof)
	}
	return route, run, credential, nil
}

func lockRunParentRoute(ctx context.Context, tx *sql.Tx, routeID, accountID string) (domain.Route, error) {
	if accountID == "" {
		if err := tx.QueryRowContext(ctx, `SELECT account_id::text FROM tunnel_routes WHERE id=$1`, routeID).Scan(&accountID); err != nil {
			return domain.Route{}, controlRunMissingAs(err, domain.ErrRouteNotFound)
		}
	}
	if err := lockControlAccounts(ctx, tx, accountID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Route{}, domain.ErrRouteNotFound
		}
		return domain.Route{}, err
	}
	route, err := scanControlRoute(tx.QueryRowContext(ctx, `SELECT `+controlRouteColumns+` FROM tunnel_routes WHERE id=$1 AND account_id=$2 FOR UPDATE`, routeID, accountID))
	if err != nil {
		return domain.Route{}, controlRunMissingAs(err, domain.ErrRouteNotFound)
	}
	return route, nil
}

func lockControlRunAndCredential(ctx context.Context, tx *sql.Tx, runID, routeID string) (domain.Run, domain.RunCredential, error) {
	run, err := scanControlRun(tx.QueryRowContext(ctx, `SELECT `+controlRunColumns+` FROM tunnel_runs WHERE id=$1 AND route_id=$2 FOR UPDATE`, runID, routeID))
	if err != nil {
		return domain.Run{}, domain.RunCredential{}, err
	}
	var credential domain.RunCredential
	err = tx.QueryRowContext(ctx, `SELECT id,run_id,secret_hash,created_at,revoked_at FROM run_credentials WHERE run_id=$1 FOR UPDATE`, run.ID).Scan(
		&credential.ID, &credential.RunID, &credential.SecretHash, &credential.CreatedAt, &credential.RevokedAt)
	if err != nil {
		return domain.Run{}, domain.RunCredential{}, err
	}
	return run, credential, nil
}

func controlRunMissingAs(err, missing error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return missing
	}
	return err
}
