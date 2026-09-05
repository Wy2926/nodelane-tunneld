package domain

import "errors"

var (
	ErrInvalidRequest     = errors.New("invalid request")
	ErrSubdomainInvalid   = errors.New("subdomain invalid")
	ErrSubdomainReserved  = errors.New("subdomain reserved")
	ErrRouteLimitReached  = errors.New("route limit reached")
	ErrProtocolNotAllowed = errors.New("protocol not allowed")
)
