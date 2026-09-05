package domain

import "errors"

var (
	ErrRouteNotFound       = errors.New("route not found")
	ErrRouteDeleted        = errors.New("route deleted")
	ErrSubdomainConflict   = errors.New("subdomain conflict")
	ErrRunAlreadyActive    = errors.New("run already active")
	ErrRunStopped          = errors.New("run stopped")
	ErrLaunchCodeExpired   = errors.New("launch code expired")
	ErrLaunchCodeUsed      = errors.New("launch code used")
	ErrLaunchCodeRevoked   = errors.New("launch code revoked")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	ErrInvalidRunProof     = errors.New("invalid run proof")
	ErrRunEvidenceInvalid  = errors.New("run evidence invalid")
)
