package controladapters

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/session"
)

const TunnelClientBanScope = "tunnel_client"

var (
	ErrInvalidDependency = errors.New("control adapter dependency is required")
	ErrInvalidRateResult = errors.New("rate limiter returned an inconsistent decision")
)

type RateStore interface {
	Allow(context.Context, string, string, int64, time.Duration) (session.RateLimit, error)
}

type BanStore interface {
	IsIPBanned(context.Context, netip.Addr, string, time.Time) (bool, error)
}

type RateAdapter struct {
	store RateStore
}

func NewRateAdapter(store RateStore) (*RateAdapter, error) {
	if dependencyIsNil(store) {
		return nil, ErrInvalidDependency
	}
	return &RateAdapter{store: store}, nil
}

func (a *RateAdapter) Limit(ctx context.Context, operation, key string, limit int, window time.Duration) (time.Duration, error) {
	if a == nil || dependencyIsNil(a.store) {
		return 0, ErrInvalidDependency
	}
	decision, err := a.store.Allow(ctx, operation, key, int64(limit), window)
	if err != nil {
		return 0, err
	}
	if decision.Allowed {
		if decision.RetryAfter != 0 {
			return 0, ErrInvalidRateResult
		}
		return 0, nil
	}
	if decision.RetryAfter <= 0 {
		return 0, ErrInvalidRateResult
	}
	return decision.RetryAfter, nil
}

type BanAdapter struct {
	store BanStore
	now   func() time.Time
}

func NewBanAdapter(store BanStore, now func() time.Time) (*BanAdapter, error) {
	if dependencyIsNil(store) || now == nil {
		return nil, ErrInvalidDependency
	}
	return &BanAdapter{store: store, now: now}, nil
}

func (a *BanAdapter) Check(ctx context.Context, ip netip.Addr) (bool, error) {
	if a == nil || dependencyIsNil(a.store) || a.now == nil {
		return false, ErrInvalidDependency
	}
	return a.store.IsIPBanned(ctx, ip.Unmap(), TunnelClientBanScope, a.now().UTC())
}

func dependencyIsNil(value any) bool {
	if value == nil {
		return true
	}
	kind := reflect.ValueOf(value).Kind()
	switch kind {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	default:
		return false
	}
}
