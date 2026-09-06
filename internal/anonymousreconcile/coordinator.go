// Package anonymousreconcile confirms anonymous lifecycle observations without
// treating absent frps samples or expired authorization as resource release.
package anonymousreconcile

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
)

var (
	ErrInvalidConfiguration   = errors.New("invalid anonymous reconciliation configuration")
	ErrInvalidRequest         = errors.New("invalid anonymous reconciliation request")
	ErrObservationUnconfirmed = errors.New("anonymous connection observation is unconfirmed")
	ErrStoreUnavailable       = errors.New("anonymous reconciliation store unavailable")
	ErrGuardUnavailable       = errors.New("anonymous release guard unavailable")
)

type Store interface {
	MarkConnected(context.Context, string, string) (anonymous.Run, error)
	PendingVerification(context.Context, int64) ([]anonymous.VerificationItem, error)
	ConfirmReleased(context.Context, anonymous.ReleaseEvidence) error
}

// Observer must perform an uncached, authenticated stock-frps observation.
type Observer interface {
	Observe(context.Context, frpevidence.Expected) frpevidence.Evidence
}

// ReleaseGuard supplies trusted per-run registration-drain evidence. A true
// result must establish that new registration is permanently forbidden for
// this run and every previously approved registration has finished. A timeout,
// CloseProxy alone, or an offline/404 proxy snapshot cannot establish this.
// The guarantee must remain valid through the subsequent observation and CAS.
type ReleaseGuard interface {
	CanConfirmRelease(context.Context, anonymous.VerificationItem) (bool, error)
}

type Report struct {
	Inspected int
	Released  int
	Held      int
	Failed    int
}

type Coordinator struct {
	store    Store
	observer Observer
	guard    ReleaseGuard
}

var _ Store = (*anonymous.Store)(nil)
var _ Observer = (*frpevidence.Client)(nil)

// New permits a missing guard for observation-only use; all resources then stay held.
func New(store Store, observer Observer, guard ReleaseGuard) (*Coordinator, error) {
	if nilDependency(store) || nilDependency(observer) {
		return nil, ErrInvalidConfiguration
	}
	if nilDependency(guard) {
		guard = nil
	}
	return &Coordinator{store: store, observer: observer, guard: guard}, nil
}

// ObserveConnected accepts an expectation from current server-side authorization,
// not request input. Invoke it after the NewProxy response, never inside that
// callback: stock frps registers the proxy only after the callback returns.
// The Store enforces the original deadline and never renews an online lease here.
func (c *Coordinator) ObserveConnected(ctx context.Context, expected frpevidence.Expected) (anonymous.Run, error) {
	if !validExpected(expected) {
		return anonymous.Run{}, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return anonymous.Run{}, err
	}
	evidence := c.observer.Observe(ctx, expected)
	if err := ctx.Err(); err != nil {
		return anonymous.Run{}, err
	}
	if !matches(evidence, expected) || evidence.Phase != "online" {
		return anonymous.Run{}, ErrObservationUnconfirmed
	}
	run, err := c.store.MarkConnected(ctx, expected.RunID, expected.ProxyName)
	if err != nil {
		return anonymous.Run{}, storeError(err)
	}
	return run, nil
}

// Reconcile processes one bounded batch. It never reopens the resource fence,
// infers never-registered evidence, or treats inventory absence as release.
func (c *Coordinator) Reconcile(ctx context.Context, limit int64) (Report, error) {
	var report Report
	if limit < 1 || limit > 1000 {
		return report, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	items, err := c.store.PendingVerification(ctx, limit)
	var batchErr error
	if err != nil {
		if !errors.Is(err, anonymous.ErrVerificationCorrupt) {
			return report, storeError(err)
		}
		batchErr = anonymous.ErrVerificationCorrupt
	}
	if err := ctx.Err(); err != nil {
		return report, combine(batchErr, err)
	}
	if int64(len(items)) > limit {
		return report, combine(batchErr, anonymous.ErrInvalidState)
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return report, combine(batchErr, err)
		}
		report.Inspected++
		expected := frpevidence.Expected{RunID: item.RunID, ProxyName: item.ProxyName, Protocol: string(item.Protocol)}
		if !validExpected(expected) {
			report.Held++
			report.Failed++
			batchErr = combine(batchErr, anonymous.ErrInvalidState)
			continue
		}
		if c.guard == nil {
			report.Held++
			continue
		}
		confirmed, err := c.guard.CanConfirmRelease(ctx, item)
		if canceled := ctx.Err(); canceled != nil {
			report.Held++
			return report, combine(batchErr, canceled)
		}
		if err != nil {
			report.Held++
			report.Failed++
			batchErr = combine(batchErr, ErrGuardUnavailable)
			continue
		}
		if !confirmed {
			report.Held++
			continue
		}

		// Sample only after registration has drained; otherwise an approved
		// reconnect could still register after an older offline sample.
		evidence := c.observer.Observe(ctx, expected)
		if canceled := ctx.Err(); canceled != nil {
			report.Held++
			return report, combine(batchErr, canceled)
		}
		if !matches(evidence, expected) || evidence.Phase != "offline" || evidence.CurrentConnections != 0 {
			report.Held++
			continue
		}
		err = c.store.ConfirmReleased(ctx, anonymous.ReleaseEvidence{
			Kind: anonymous.ReleaseEvidenceOfflineSample, RunID: item.RunID, ProxyName: item.ProxyName,
			ObservedOffline: true, SampleAvailable: true, CurrentConnections: 0,
		})
		if err != nil {
			report.Held++
			report.Failed++
			batchErr = combine(batchErr, storeError(err))
		} else {
			report.Released++
		}
	}
	return report, combine(batchErr, ctx.Err())
}

func matches(evidence frpevidence.Evidence, expected frpevidence.Expected) bool {
	return evidence.Availability == frpevidence.Available && evidence.RunID == expected.RunID &&
		evidence.ProxyName == expected.ProxyName && evidence.Protocol == expected.Protocol && evidence.CurrentConnections >= 0
}

func validExpected(expected frpevidence.Expected) bool {
	return validIdentifier(expected.RunID, "anr_") && validIdentifier(expected.ProxyName, "anon_") &&
		(expected.Protocol == "http" || expected.Protocol == "tcp" || expected.Protocol == "udp")
}

func validIdentifier(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+26 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < 'a' || character > 'z') && (character < '2' || character > '7') {
			return false
		}
	}
	return true
}

func storeError(err error) error {
	for _, known := range []error{
		context.Canceled, context.DeadlineExceeded, anonymous.ErrRunNotFound,
		anonymous.ErrInvalidState, anonymous.ErrRunExpired, anonymous.ErrRunStopped,
		anonymous.ErrReleaseUnconfirmed, anonymous.ErrResourcesUnverified, anonymous.ErrVerificationCorrupt,
	} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrStoreUnavailable
}

func combine(current, next error) error {
	if next == nil || errors.Is(current, next) {
		return current
	}
	if current == nil {
		return next
	}
	return errors.Join(current, next)
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
