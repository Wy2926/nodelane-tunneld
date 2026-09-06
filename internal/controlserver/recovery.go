package controlserver

import (
	"context"
	"errors"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
)

var errRecoveryUnavailable = errors.New("run recovery unavailable")

type entryRelease uint8

const (
	entryHeld entryRelease = iota
	entryOffline
	entryAbsent
)

type entryObserver interface {
	ObserveClient(context.Context, string) frpevidence.ClientEvidence
	Observe(context.Context, frpevidence.Expected) frpevidence.Evidence
}

// Native client termination follows its synchronous registration handlers.
// Independent session IDs prevent a delayed old termination from naming a new
// control. A subsequent proxy sample describes entry ownership, not all old I/O.
func observeReleasedEntry(ctx context.Context, observer entryObserver, expected frpevidence.Expected) entryRelease {
	client := observer.ObserveClient(ctx, expected.RunID)
	if ctx.Err() != nil || client.Availability != frpevidence.Available || client.ClientID != expected.RunID ||
		client.Online || client.NativeSessionID != "" || client.DisconnectedAt <= 0 {
		return entryHeld
	}
	proxy := observer.Observe(ctx, expected)
	if ctx.Err() != nil {
		return entryHeld
	}
	if proxy.Availability == frpevidence.NotObserved {
		return entryAbsent
	}
	if proxy.Availability == frpevidence.Available && proxy.RunID == expected.RunID && proxy.ProxyName == expected.ProxyName &&
		proxy.Protocol == expected.Protocol && proxy.Phase == "offline" && proxy.CurrentConnections == 0 {
		return entryOffline
	}
	return entryHeld
}

type registeredRecoveryStore interface {
	PendingRunReconciliation(context.Context, int) ([]domain.RunAuthorization, error)
	ReleaseNeverGranted(context.Context, string) (domain.Run, error)
	ConfirmOffline(context.Context, domain.RunDisconnectEvidence) (domain.Run, error)
}

type registeredReconciler struct {
	store    registeredRecoveryStore
	observer entryObserver
}

func (r *registeredReconciler) reconcile(ctx context.Context, limit int) error {
	seen := make(map[string]struct{}, limit)
	var result error
	for range limit {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		items, err := r.store.PendingRunReconciliation(ctx, 1)
		if err != nil {
			return errRecoveryUnavailable
		}
		if len(items) == 0 {
			break
		}
		if len(items) != 1 {
			return errRecoveryUnavailable
		}
		item := items[0]
		if _, duplicate := seen[item.Run.ID]; duplicate {
			break
		}
		seen[item.Run.ID] = struct{}{}
		if !item.Run.ProxyRegistrationGranted {
			_, err = r.store.ReleaseNeverGranted(ctx, item.Run.ID)
		} else {
			expected := frpevidence.Expected{RunID: item.Run.ID, ProxyName: item.Route.ProxyName, Protocol: item.Route.Protocol}
			release := observeReleasedEntry(ctx, r.observer, expected)
			if release == entryHeld {
				continue
			}
			_, err = r.store.ConfirmOffline(ctx, domain.RunDisconnectEvidence{
				RunID: item.Run.ID, RouteID: item.Route.ID, ProxyName: item.Route.ProxyName,
				ObservedOffline: release == entryOffline, ProxyNotObserved: release == entryAbsent, ConfirmedClientDisconnected: release == entryAbsent,
			})
		}
		if err != nil {
			result = errRecoveryUnavailable
		}
	}
	return result
}

type anonymousRecoveryStore interface {
	PendingVerification(context.Context, int64) ([]anonymous.VerificationItem, error)
	ReleaseNeverGranted(context.Context, string) error
	ConfirmReleased(context.Context, anonymous.ReleaseEvidence) error
}

type anonymousReconciler struct {
	store    anonymousRecoveryStore
	observer entryObserver
}

func (r *anonymousReconciler) reconcile(ctx context.Context, limit int) error {
	seen := make(map[string]struct{}, limit)
	var result error
	for range limit {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		items, err := r.store.PendingVerification(ctx, 1)
		if err != nil {
			result = errRecoveryUnavailable
			if !errors.Is(err, anonymous.ErrVerificationCorrupt) {
				return result
			}
		}
		if len(items) == 0 {
			break
		}
		if len(items) != 1 {
			return errRecoveryUnavailable
		}
		item := items[0]
		if _, duplicate := seen[item.RunID]; duplicate {
			break
		}
		seen[item.RunID] = struct{}{}
		if !item.ProxyRegistrationGranted {
			err = r.store.ReleaseNeverGranted(ctx, item.RunID)
		} else {
			expected := frpevidence.Expected{RunID: item.RunID, ProxyName: item.ProxyName, Protocol: string(item.Protocol)}
			release := observeReleasedEntry(ctx, r.observer, expected)
			if release == entryHeld {
				continue
			}
			evidence := anonymous.ReleaseEvidence{Kind: anonymous.ReleaseEvidenceOfflineSample,
				RunID: item.RunID, ProxyName: item.ProxyName, ObservedOffline: true, SampleAvailable: true}
			if release == entryAbsent {
				evidence.Kind = anonymous.ReleaseEvidenceDrainedAbsentProxy
				evidence.ObservedOffline, evidence.SampleAvailable = false, false
				evidence.ProxyNotObserved, evidence.ConfirmedClientDisconnected = true, true
			}
			err = r.store.ConfirmReleased(ctx, evidence)
		}
		if err != nil {
			result = errRecoveryUnavailable
		}
	}
	return result
}
