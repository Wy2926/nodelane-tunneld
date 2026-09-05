package domain

import (
	"net/netip"
	"time"
)

type RouteStatus string

const (
	RouteActive  RouteStatus = "active"
	RouteDeleted RouteStatus = "deleted"
)

type RunStatus string

const (
	RunStarting RunStatus = "starting"
	RunOnline   RunStatus = "online"
	RunStopping RunStatus = "stopping"
	RunOffline  RunStatus = "offline"
)

type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
)

type StartedVia string

const (
	StartedViaDeviceLogin StartedVia = "device_login"
	StartedViaLaunchCode  StartedVia = "launch_code"
)

const (
	OperationCreateRoute  = "create_route"
	OperationStartRun     = "start_run"
	OperationRedeemLaunch = "redeem_launch"
)

type Account struct {
	ID              string    `json:"id"`
	IdentityIssuer  string    `json:"identity_issuer"`
	IdentitySubject string    `json:"identity_subject"`
	CreatedAt       time.Time `json:"created_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
}

type Route struct {
	ID               string      `json:"id"`
	AccountID        string      `json:"account_id"`
	Protocol         string      `json:"protocol"`
	Subdomain        string      `json:"subdomain"`
	ProxyName        string      `json:"proxy_name"`
	Status           RouteStatus `json:"status"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
	DeletedAt        *time.Time  `json:"deleted_at,omitempty"`
	RecoverableUntil *time.Time  `json:"recoverable_until,omitempty"`
	NameReleasedAt   *time.Time  `json:"name_released_at,omitempty"`
}

type LaunchCode struct {
	ID         string     `json:"id"`
	RouteID    string     `json:"route_id"`
	SecretHash string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RedeemedAt *time.Time `json:"redeemed_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type Run struct {
	ID                string       `json:"id"`
	RouteID           string       `json:"route_id"`
	StartedVia        StartedVia   `json:"started_via"`
	Status            RunStatus    `json:"status"`
	DesiredState      DesiredState `json:"desired_state"`
	RequestIP         netip.Addr   `json:"request_ip"`
	ConnectedIP       netip.Addr   `json:"connected_ip,omitempty"`
	CreatedAt         time.Time    `json:"created_at"`
	ConnectedAt       *time.Time   `json:"connected_at,omitempty"`
	LastHeartbeatAt   *time.Time   `json:"last_heartbeat_at,omitempty"`
	StopRequestedAt   *time.Time   `json:"stop_requested_at,omitempty"`
	StoppedAt         *time.Time   `json:"stopped_at,omitempty"`
	ConnectDeadlineAt time.Time    `json:"connect_deadline_at"`
	LeaseExpiresAt    *time.Time   `json:"lease_expires_at,omitempty"`
	StopReason        string       `json:"stop_reason,omitempty"`
}

type RunCredential struct {
	ID         string     `json:"id"`
	RunID      string     `json:"run_id"`
	SecretHash string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type OperationReplay struct {
	ID                 string    `json:"id"`
	Operation          string    `json:"operation"`
	PrincipalKey       string    `json:"principal_key"`
	KeyHash            string    `json:"key_hash"`
	RequestHash        string    `json:"request_hash"`
	RouteID            string    `json:"route_id,omitempty"`
	RunID              string    `json:"run_id,omitempty"`
	ResponseCiphertext []byte    `json:"-"`
	CreatedAt          time.Time `json:"created_at"`
	ExpiresAt          time.Time `json:"expires_at"`
}
