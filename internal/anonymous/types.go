package anonymous

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrInvalidConfiguration = errors.New("invalid anonymous store configuration")
	ErrInvalidRequest       = errors.New("invalid anonymous allocation request")
	ErrResourcesUnverified  = errors.New("anonymous resource state is not verified")
	ErrIdempotencyConflict  = errors.New("anonymous idempotency conflict")
	ErrInstallationLimit    = errors.New("anonymous installation concurrency limit reached")
	ErrNetworkLimit         = errors.New("anonymous network concurrency limit reached")
	ErrRateLimited          = errors.New("anonymous allocation rate limited")
	ErrResourceUnavailable  = errors.New("anonymous public resource unavailable")
	ErrInvalidCredential    = errors.New("invalid anonymous run credential")
	ErrRunNotFound          = errors.New("anonymous run not found")
	ErrRunExpired           = errors.New("anonymous run authorization expired")
	ErrRunStopped           = errors.New("anonymous run stopped")
	ErrUnavailable          = errors.New("anonymous store unavailable")
	ErrInvalidState         = errors.New("invalid anonymous run state")
	ErrReleaseUnconfirmed   = errors.New("anonymous release is not confirmed")
	ErrFenceConflict        = errors.New("anonymous resource fence conflict")
	ErrVerificationCorrupt  = errors.New("anonymous verification work is corrupt")
)

type Protocol string

const (
	ProtocolHTTP Protocol = "http"
	ProtocolTCP  Protocol = "tcp"
	ProtocolUDP  Protocol = "udp"
)

type State string

const (
	StateReserved  State = "reserved"
	StateOnline    State = "online"
	StateStopping  State = "stopping"
	StateVerifying State = "verifying"
	StateReleased  State = "released"
)

type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
)

type LimitScope string

const (
	LimitInstallation LimitScope = "installation"
	LimitNetwork      LimitScope = "network"
)

type RateLimitError struct {
	Scope      LimitScope
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s: %s retry after %s", ErrRateLimited, e.Scope, e.RetryAfter)
}

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

type ConcurrencyLimitError struct {
	Scope      LimitScope
	RetryAfter time.Duration
}

func (e *ConcurrencyLimitError) Error() string {
	return fmt.Sprintf("anonymous %s concurrency limit reached: retry after %s", e.Scope, e.RetryAfter)
}

func (e *ConcurrencyLimitError) Is(target error) bool {
	return e.Scope == LimitInstallation && target == ErrInstallationLimit ||
		e.Scope == LimitNetwork && target == ErrNetworkLimit
}

type Config struct {
	Client           *redis.Client
	Prefix           string
	CredentialPepper []byte
	ReplayKey        []byte
	FenceOwnerToken  []byte
	Clock            func() time.Time
	Random           io.Reader
	PublicDomain     string
	TCPPorts         []uint16
	UDPPorts         []uint16
}

// ResourceFence is an observation of the Redis process identity and the
// current resource-verification revision. Callers must reconcile the data
// plane after observing it and pass the exact value to MarkResourcesVerified.
type ResourceFence struct {
	RedisRunID string `json:"redis_run_id"`
	Revision   string `json:"revision"`
	Generation int64  `json:"generation"`
}

type AllocateRequest struct {
	InstallationID string
	NetworkKey     string
	IdempotencyKey string `json:"-"`
	Protocol       Protocol
	LocalHost      string
	LocalPort      uint16
}

type Allocation struct {
	RunID             string    `json:"run_id"`
	ProxyName         string    `json:"proxy_name"`
	PublicEndpoint    string    `json:"public_endpoint"`
	CredentialToken   string    `json:"-"`
	Protocol          Protocol  `json:"protocol"`
	CreatedAt         time.Time `json:"created_at"`
	ConnectDeadlineAt time.Time `json:"connect_deadline_at"`
	HardExpiresAt     time.Time `json:"hard_expires_at"`
	Replayed          bool      `json:"replayed"`
}

type Run struct {
	RunID                    string       `json:"run_id"`
	ProxyName                string       `json:"proxy_name"`
	PublicEndpoint           string       `json:"public_endpoint"`
	Protocol                 Protocol     `json:"protocol"`
	State                    State        `json:"state"`
	DesiredState             DesiredState `json:"desired_state"`
	CreatedAt                time.Time    `json:"created_at"`
	ConnectDeadlineAt        time.Time    `json:"connect_deadline_at"`
	LeaseExpiresAt           time.Time    `json:"lease_expires_at,omitempty"`
	HardExpiresAt            time.Time    `json:"hard_expires_at"`
	ProxyRegistrationGranted bool         `json:"-"`
}

type HeartbeatResult struct {
	RunID          string       `json:"run_id"`
	DesiredState   DesiredState `json:"desired_state"`
	LeaseExpiresAt time.Time    `json:"lease_expires_at,omitempty"`
	HardExpiresAt  time.Time    `json:"hard_expires_at"`
}

type VerificationItem struct {
	RunID                    string    `json:"run_id"`
	ProxyName                string    `json:"proxy_name"`
	PublicEndpoint           string    `json:"public_endpoint"`
	Protocol                 Protocol  `json:"protocol"`
	DueAt                    time.Time `json:"due_at"`
	ProxyRegistrationGranted bool      `json:"-"`
}

type ReleaseEvidenceKind string

const (
	ReleaseEvidenceOfflineSample      ReleaseEvidenceKind = "offline_sample"
	ReleaseEvidenceNeverRegistered    ReleaseEvidenceKind = "never_registered"
	ReleaseEvidenceDrainedAbsentProxy ReleaseEvidenceKind = "drained_absent_proxy"
)

type ReleaseEvidence struct {
	Kind                        ReleaseEvidenceKind `json:"kind"`
	RunID                       string              `json:"run_id"`
	ProxyName                   string              `json:"proxy_name"`
	ObservedOffline             bool                `json:"observed_offline"`
	ProxyNotObserved            bool                `json:"proxy_not_observed"`
	SampleAvailable             bool                `json:"sample_available"`
	CurrentConnections          int64               `json:"current_connections"`
	ConfirmedNeverRegistered    bool                `json:"confirmed_never_registered"`
	ConfirmedClientDisconnected bool                `json:"confirmed_client_disconnected"`
}

type VerificationCorruptionError struct {
	Count int
}

func (e *VerificationCorruptionError) Error() string {
	return fmt.Sprintf("%s: %d item(s)", ErrVerificationCorrupt, e.Count)
}

func (e *VerificationCorruptionError) Unwrap() error { return ErrVerificationCorrupt }

const (
	connectLifetime        = 2 * time.Minute
	heartbeatLease         = 90 * time.Second
	hardLifetime           = time.Hour
	replayLifetime         = 2 * time.Minute
	rateWindow             = 10 * time.Minute
	installationActiveMax  = 1
	networkActiveMax       = 2
	installationRateMax    = 5
	networkRateMax         = 20
	maxResourceAttempts    = 32
	verificationClaimLease = 15 * time.Second
)
