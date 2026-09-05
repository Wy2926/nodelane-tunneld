package controladapters

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/session"
)

type recordingRateStore struct {
	result          session.RateLimit
	err             error
	bucket, subject string
	limit           int64
	window          time.Duration
}

func (s *recordingRateStore) Allow(_ context.Context, bucket, subject string, limit int64, window time.Duration) (session.RateLimit, error) {
	s.bucket = bucket
	s.subject = subject
	s.limit = limit
	s.window = window
	return s.result, s.err
}

type recordingBanStore struct {
	banned bool
	err    error
	ip     netip.Addr
	scope  string
	now    time.Time
}

func (s *recordingBanStore) IsIPBanned(_ context.Context, ip netip.Addr, scope string, now time.Time) (bool, error) {
	s.ip = ip
	s.scope = scope
	s.now = now
	return s.banned, s.err
}

func TestRateAdapterMapsStoreDecision(t *testing.T) {
	t.Run("allowed", func(t *testing.T) {
		store := &recordingRateStore{result: session.RateLimit{Allowed: true, Remaining: 3}}
		adapter, err := NewRateAdapter(store)
		if err != nil {
			t.Fatal(err)
		}

		retryAfter, err := adapter.Limit(context.Background(), "start_run", "acct_1:v4:192.0.2.1", 5, 10*time.Minute)
		if err != nil || retryAfter != 0 {
			t.Fatalf("Limit() = (%s, %v), want (0, nil)", retryAfter, err)
		}
		if store.bucket != "start_run" || store.subject != "acct_1:v4:192.0.2.1" || store.limit != 5 || store.window != 10*time.Minute {
			t.Fatalf("unexpected Allow arguments: %#v", store)
		}
	})

	t.Run("denied", func(t *testing.T) {
		store := &recordingRateStore{result: session.RateLimit{Allowed: false, RetryAfter: 37 * time.Second}}
		adapter, err := NewRateAdapter(store)
		if err != nil {
			t.Fatal(err)
		}

		retryAfter, err := adapter.Limit(context.Background(), "create_route", "acct_2:v6:2001:db8::/64", 20, time.Minute)
		if err != nil || retryAfter != 37*time.Second {
			t.Fatalf("Limit() = (%s, %v), want (37s, nil)", retryAfter, err)
		}
	})
}

func TestRateAdapterFailsClosedOnDependencyAndInvalidDecision(t *testing.T) {
	dependencyErr := errors.New("redis unavailable")
	tests := []struct {
		name   string
		result session.RateLimit
		err    error
	}{
		{name: "dependency error", err: dependencyErr},
		{name: "allowed with retry", result: session.RateLimit{Allowed: true, RetryAfter: time.Second}},
		{name: "denied without retry", result: session.RateLimit{Allowed: false}},
		{name: "denied with negative retry", result: session.RateLimit{Allowed: false, RetryAfter: -time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, err := NewRateAdapter(&recordingRateStore{result: tt.result, err: tt.err})
			if err != nil {
				t.Fatal(err)
			}
			if retryAfter, err := adapter.Limit(context.Background(), "bucket", "subject", 1, time.Second); err == nil || retryAfter != 0 {
				t.Fatalf("Limit() = (%s, %v), want fail-closed error", retryAfter, err)
			} else if tt.err != nil && !errors.Is(err, dependencyErr) {
				t.Fatalf("Limit() error = %v, want dependency error", err)
			}
		})
	}
}

func TestBanAdapterUsesFixedTunnelScopeAndUTCClock(t *testing.T) {
	location := time.FixedZone("test", 8*60*60)
	now := time.Date(2026, 9, 6, 15, 4, 5, 0, location)
	store := &recordingBanStore{banned: true}
	adapter, err := NewBanAdapter(store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	banned, err := adapter.Check(context.Background(), netip.MustParseAddr("::ffff:192.0.2.8"))
	if err != nil || !banned {
		t.Fatalf("Check() = (%v, %v), want (true, nil)", banned, err)
	}
	if store.ip != netip.MustParseAddr("192.0.2.8") {
		t.Fatalf("IP = %s, want unmapped IPv4", store.ip)
	}
	if store.scope != TunnelClientBanScope {
		t.Fatalf("scope = %q, want %q", store.scope, TunnelClientBanScope)
	}
	if store.now.Location() != time.UTC || !store.now.Equal(now) {
		t.Fatalf("now = %s (%s), want same instant in UTC", store.now, store.now.Location())
	}
}

func TestBanAdapterPropagatesStoreError(t *testing.T) {
	dependencyErr := errors.New("postgres unavailable")
	adapter, err := NewBanAdapter(&recordingBanStore{err: dependencyErr}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if banned, err := adapter.Check(context.Background(), netip.MustParseAddr("192.0.2.9")); banned || !errors.Is(err, dependencyErr) {
		t.Fatalf("Check() = (%v, %v), want (false, dependency error)", banned, err)
	}
}

func TestAdapterConstructorsRejectNilDependencies(t *testing.T) {
	if adapter, err := NewRateAdapter(nil); err == nil || adapter != nil {
		t.Fatalf("NewRateAdapter(nil) = (%v, %v), want nil and error", adapter, err)
	}
	var typedNilRate *recordingRateStore
	if adapter, err := NewRateAdapter(typedNilRate); err == nil || adapter != nil {
		t.Fatalf("NewRateAdapter(typed nil) = (%v, %v), want nil and error", adapter, err)
	}
	if adapter, err := NewBanAdapter(nil, time.Now); err == nil || adapter != nil {
		t.Fatalf("NewBanAdapter(nil) = (%v, %v), want nil and error", adapter, err)
	}
	if adapter, err := NewBanAdapter(&recordingBanStore{}, nil); err == nil || adapter != nil {
		t.Fatalf("NewBanAdapter(nil clock) = (%v, %v), want nil and error", adapter, err)
	}
	var typedNilBan *recordingBanStore
	if adapter, err := NewBanAdapter(typedNilBan, time.Now); err == nil || adapter != nil {
		t.Fatalf("NewBanAdapter(typed nil) = (%v, %v), want nil and error", adapter, err)
	}
}
