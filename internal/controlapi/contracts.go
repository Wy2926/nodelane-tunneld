package controlapi

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

var (
	ErrUnauthorized = errors.New("control API authentication failed")
	ErrUnavailable  = errors.New("control API authentication unavailable")
)

type PrincipalKind string

const (
	PrincipalKindWeb    PrincipalKind = "web"
	PrincipalKindNative PrincipalKind = "native"
)

type Principal struct {
	AccountID string
	Kind      PrincipalKind
	Scopes    []string
	CSRFToken string
}

type Authenticator interface {
	Authenticate(context.Context, *http.Request) (Principal, error)
}

type RouteRepository interface {
	ListRouteViews(context.Context, string, domain.RouteQuery) ([]domain.RouteView, error)
	GetRouteView(context.Context, string, string) (domain.RouteView, error)
	CreateRoute(context.Context, domain.CreateRouteCommand) (domain.CreateRouteResult, error)
	DeleteRoute(context.Context, string, string) (domain.Route, error)
	RestoreRoute(context.Context, string, string) (domain.Route, error)
	IssueLaunchCode(context.Context, domain.IssueLaunchCodeCommand) (domain.IssuedLaunchCode, error)
}

type RunRepository interface {
	StartAccountRun(context.Context, domain.AccountStartCommand) (domain.StartResult, error)
	RedeemLaunchCode(context.Context, domain.LaunchRedeemCommand) (domain.StartResult, error)
	AuthorizeRun(context.Context, domain.RunProof) (domain.RunAuthorization, error)
	Heartbeat(context.Context, domain.RunProof) (domain.HeartbeatResult, error)
	RequestOwnedStop(context.Context, string, string) (domain.Run, error)
	RequestCredentialStop(context.Context, domain.RunProof) (domain.Run, error)
}

type SourceIPFunc func(*http.Request) (netip.Addr, error)
type BanChecker func(context.Context, netip.Addr) (bool, error)
type RateLimiter func(context.Context, string, string, int, time.Duration) (time.Duration, error)

type Options struct {
	PublicOrigin  string
	PublicDomain  string
	Authenticator Authenticator
	Routes        RouteRepository
	Runs          RunRepository
	SourceIP      SourceIPFunc
	Banned        BanChecker
	RateLimit     RateLimiter
	Now           func() time.Time
}
