package routes

import (
	"context"
	"errors"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

func TestValidateSubdomainAllowsLowercaseASCIILabels(t *testing.T) {
	for _, label := range []string{
		"demo",
		"a-1",
		"abcdefghijklmnopqrstuvwxyzabcdef",
	} {
		t.Run(label, func(t *testing.T) {
			if err := ValidateSubdomain(label); err != nil {
				t.Fatalf("ValidateSubdomain(%q) error = %v, want nil", label, err)
			}
		})
	}
}

func TestValidateSubdomainRejectsInvalidSyntax(t *testing.T) {
	tests := []struct {
		name  string
		label string
	}{
		{name: "uppercase", label: "Demo"},
		{name: "leading hyphen", label: "-demo"},
		{name: "trailing hyphen", label: "demo-"},
		{name: "too short", label: "ab"},
		{name: "too long", label: "abcdefghijklmnopqrstuvwxyzabcdefg"},
		{name: "unicode", label: "démo"},
		{name: "dot", label: "demo.test"},
		{name: "full host", label: "demo.tunnel.nodelane.net"},
		{name: "punycode prefix", label: "xn--demo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateSubdomain(tt.label); !errors.Is(err, domain.ErrSubdomainInvalid) {
				t.Fatalf("ValidateSubdomain(%q) error = %v, want ErrSubdomainInvalid", tt.label, err)
			}
		})
	}
}

func TestValidateSubdomainRejectsReservedLabelsAndAnonymousPrefix(t *testing.T) {
	for _, label := range []string{
		"www",
		"auth",
		"api",
		"admin",
		"console",
		"status",
		"support",
		"mail",
		"smtp",
		"frp",
		"tunnel",
		"anon-demo",
	} {
		t.Run(label, func(t *testing.T) {
			if err := ValidateSubdomain(label); !errors.Is(err, domain.ErrSubdomainReserved) {
				t.Fatalf("ValidateSubdomain(%q) error = %v, want ErrSubdomainReserved", label, err)
			}
		})
	}
}

func TestRoutePolicyCheckCreateEnforcesCapacityAndProtocol(t *testing.T) {
	policy := RoutePolicy{MaxRoutes: 5, AllowedProtocols: []string{"http"}}
	tests := []struct {
		name        string
		protocol    string
		activeCount int
		want        error
	}{
		{name: "below capacity", protocol: "http", activeCount: 4},
		{name: "at capacity", protocol: "http", activeCount: 5, want: domain.ErrRouteLimitReached},
		{name: "over capacity", protocol: "http", activeCount: 6, want: domain.ErrRouteLimitReached},
		{name: "tcp with capacity", protocol: "tcp", activeCount: 0, want: domain.ErrProtocolNotAllowed},
		{name: "udp with capacity", protocol: "udp", activeCount: 0, want: domain.ErrProtocolNotAllowed},
		{name: "non-positive maximum", protocol: "http", activeCount: 0, want: domain.ErrInvalidRequest},
		{name: "negative active count", protocol: "http", activeCount: -1, want: domain.ErrInvalidRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := policy
			if tt.name == "non-positive maximum" {
				current.MaxRoutes = 0
			}
			if err := current.CheckCreate(tt.protocol, tt.activeCount); !errors.Is(err, tt.want) {
				t.Fatalf("CheckCreate(%q, %d) error = %v, want %v", tt.protocol, tt.activeCount, err, tt.want)
			}
		})
	}
}

func TestNewStaticPolicyProviderRejectsNonPositiveMaximum(t *testing.T) {
	for _, maxRoutes := range []int{0, -1} {
		if _, err := NewStaticPolicyProvider(maxRoutes); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("NewStaticPolicyProvider(%d) error = %v, want ErrInvalidRequest", maxRoutes, err)
		}
	}
}

func TestStaticPolicyProviderReturnsIndependentHTTPOnlyPolicies(t *testing.T) {
	provider, err := NewStaticPolicyProvider(5)
	if err != nil {
		t.Fatalf("NewStaticPolicyProvider() error = %v", err)
	}

	first, err := provider.Policy(context.Background(), "account-one")
	if err != nil {
		t.Fatalf("first Policy() error = %v", err)
	}
	if first.MaxRoutes != 5 || len(first.AllowedProtocols) != 1 || first.AllowedProtocols[0] != "http" {
		t.Fatalf("first Policy() = %#v, want max five and HTTP only", first)
	}
	first.AllowedProtocols[0] = "tcp"

	second, err := provider.Policy(context.Background(), "account-two")
	if err != nil {
		t.Fatalf("second Policy() error = %v", err)
	}
	if second.MaxRoutes != 5 || len(second.AllowedProtocols) != 1 || second.AllowedProtocols[0] != "http" {
		t.Fatalf("second Policy() = %#v after caller mutation, want max five and HTTP only", second)
	}
}
