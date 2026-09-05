package routes

import (
	"context"
	"strings"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

var reservedSubdomains = map[string]struct{}{
	"www":     {},
	"auth":    {},
	"api":     {},
	"admin":   {},
	"console": {},
	"status":  {},
	"support": {},
	"mail":    {},
	"smtp":    {},
	"frp":     {},
	"tunnel":  {},
}

type RoutePolicy struct {
	MaxRoutes        int
	AllowedProtocols []string
}

func (p RoutePolicy) CheckCreate(protocol string, activeCount int) error {
	if p.MaxRoutes <= 0 || activeCount < 0 {
		return domain.ErrInvalidRequest
	}

	allowed := false
	for _, candidate := range p.AllowedProtocols {
		if protocol == candidate {
			allowed = true
			break
		}
	}
	if !allowed {
		return domain.ErrProtocolNotAllowed
	}
	if activeCount >= p.MaxRoutes {
		return domain.ErrRouteLimitReached
	}
	return nil
}

type RoutePolicyProvider interface {
	Policy(context.Context, string) (RoutePolicy, error)
}

type StaticPolicyProvider struct {
	maxRoutes int
}

func NewStaticPolicyProvider(maxRoutes int) (*StaticPolicyProvider, error) {
	if maxRoutes <= 0 {
		return nil, domain.ErrInvalidRequest
	}
	return &StaticPolicyProvider{maxRoutes: maxRoutes}, nil
}

func (p *StaticPolicyProvider) Policy(context.Context, string) (RoutePolicy, error) {
	return RoutePolicy{
		MaxRoutes:        p.maxRoutes,
		AllowedProtocols: []string{"http"},
	}, nil
}

func ValidateSubdomain(label string) error {
	if len(label) < 3 || len(label) > 32 || strings.HasPrefix(label, "xn--") {
		return domain.ErrSubdomainInvalid
	}
	for i := 0; i < len(label); i++ {
		char := label[i]
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return domain.ErrSubdomainInvalid
		}
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return domain.ErrSubdomainInvalid
	}
	if strings.HasPrefix(label, "anon-") {
		return domain.ErrSubdomainReserved
	}
	if _, reserved := reservedSubdomains[label]; reserved {
		return domain.ErrSubdomainReserved
	}
	return nil
}
