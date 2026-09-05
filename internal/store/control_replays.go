package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
)

func controlDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func controlRequestDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return controlDigest(string(encoded)), nil
}

func (p *ControlPostgres) readControlReplay(ctx context.Context, tx *sql.Tx, operation, principal, keyHash string, forUpdate bool) (domain.OperationReplay, error) {
	query := `SELECT id, operation, principal_key, key_hash, request_hash, route_id, run_id, response_ciphertext, created_at, expires_at
		FROM operation_replays WHERE operation=$1 AND principal_key=$2 AND key_hash=$3`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var replay domain.OperationReplay
	var routeID, runID sql.NullString
	err := tx.QueryRowContext(ctx, query, operation, principal, keyHash).Scan(
		&replay.ID, &replay.Operation, &replay.PrincipalKey, &replay.KeyHash, &replay.RequestHash,
		&routeID, &runID, &replay.ResponseCiphertext, &replay.CreatedAt, &replay.ExpiresAt,
	)
	if err != nil {
		return domain.OperationReplay{}, err
	}
	replay.RouteID = routeID.String
	replay.RunID = runID.String
	return replay, nil
}

func (p *ControlPostgres) saveControlReplay(ctx context.Context, tx *sql.Tx, replay domain.OperationReplay, plaintext []byte) error {
	ciphertext, err := p.replayCipher.Seal(controlReplayContext(replay), plaintext)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO operation_replays
		(id, operation, principal_key, key_hash, request_hash, route_id, run_id, response_ciphertext, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),$8,$9,$10)`,
		replay.ID, replay.Operation, replay.PrincipalKey, replay.KeyHash, replay.RequestHash,
		replay.RouteID, replay.RunID, ciphertext, replay.CreatedAt, replay.ExpiresAt)
	return err
}

func (p *ControlPostgres) openControlReplay(replay domain.OperationReplay, now time.Time) ([]byte, error) {
	return p.replayCipher.Open(controlReplayContext(replay), replay.ResponseCiphertext, now)
}

func controlReplayContext(replay domain.OperationReplay) identity.ReplayContext {
	return identity.ReplayContext{
		Operation: replay.Operation, PrincipalKey: replay.PrincipalKey, KeyHash: replay.KeyHash,
		RequestHash: replay.RequestHash, RouteID: replay.RouteID, RunID: replay.RunID, ExpiresAt: replay.ExpiresAt,
	}
}
