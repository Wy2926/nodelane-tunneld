package domain

import "net/netip"

type RouteQuery struct{ Deleted bool }

type CreateRouteCommand struct {
	AccountID      string
	Protocol       string
	Subdomain      string
	IdempotencyKey string `json:"-"`
}

type CreateRouteResult struct {
	Route    Route
	Replayed bool
}

type IssueLaunchCodeCommand struct {
	AccountID string
	RouteID   string
}

type IssuedLaunchCode struct {
	Code  LaunchCode
	Token string `json:"-"`
}

type AccountStartCommand struct {
	AccountID      string
	RouteID        string
	IdempotencyKey string `json:"-"`
	RequestIP      netip.Addr
}

type LaunchRedeemCommand struct {
	Token     string `json:"-"`
	Nonce     string `json:"-"`
	RequestIP netip.Addr
}

type StartResult struct {
	Run             Run
	CredentialID    string
	CredentialToken string `json:"-"`
	Replayed        bool
}

type RunProof struct {
	RunID string
	Token string `json:"-"`
}

type RunAuthorization struct {
	Route        Route
	Run          Run
	CredentialID string
}

type HeartbeatResult struct {
	Run     Run
	Stopped bool
}

type RunRegistrationEvidence struct {
	RunID          string
	RouteID        string
	ProxyName      string
	ConnectedIP    netip.Addr
	ObservedOnline bool
}

type RunDisconnectEvidence struct {
	RunID              string
	RouteID            string
	ProxyName          string
	ObservedOffline    bool
	CurrentConnections int64
}

type SweepResult struct {
	ExpiredRuns    int
	DeletedRuns    int
	DeletedCodes   int
	DeletedReplays int
}
