package controlserver

import (
	"context"
	"errors"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/anonymousreconcile"
	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
	"github.com/Wy2926/nodelane-tunneld/internal/store"
)

type registeredRuns struct {
	*store.ControlPostgres
	observer *frpevidence.Client
	clients  *clientObserver
}

func (r *registeredRuns) Heartbeat(ctx context.Context, proof domain.RunProof) (domain.HeartbeatResult, error) {
	authorized, err := r.AuthorizeRun(ctx, proof)
	if err != nil && !errors.Is(err, domain.ErrRunStopped) {
		return domain.HeartbeatResult{}, err
	}
	if err == nil && authorized.Run.ConnectedAt == nil {
		connectedIP, clientErr := r.clients.connectedIP(ctx, authorized.Run.ID)
		if clientErr == nil {
			expected := frpevidence.Expected{RunID: authorized.Run.ID, ProxyName: authorized.Route.ProxyName, Protocol: authorized.Route.Protocol}
			evidence := r.observer.Observe(ctx, expected)
			if evidence.Availability == frpevidence.Available && evidence.RunID == expected.RunID && evidence.ProxyName == expected.ProxyName && evidence.Protocol == expected.Protocol && evidence.Phase == "online" && evidence.CurrentConnections >= 0 {
				if _, err := r.ConfirmOnline(ctx, domain.RunRegistrationEvidence{RunID: authorized.Run.ID, RouteID: authorized.Route.ID, ProxyName: authorized.Route.ProxyName, ConnectedIP: connectedIP, ObservedOnline: true}); err != nil && !errors.Is(err, domain.ErrRunStopped) {
					return domain.HeartbeatResult{}, err
				}
			}
		}
	}
	return r.ControlPostgres.Heartbeat(ctx, proof)
}

type anonymousRuns struct {
	*anonymous.Store
	coordinator *anonymousreconcile.Coordinator
}

func (r *anonymousRuns) Heartbeat(ctx context.Context, runID, token string) (anonymous.HeartbeatResult, error) {
	authorized, err := r.AuthorizeLogin(ctx, runID, token)
	if err != nil {
		return anonymous.HeartbeatResult{}, err
	}
	if authorized.State == anonymous.StateReserved {
		_, err := r.coordinator.ObserveConnected(ctx, frpevidence.Expected{RunID: authorized.RunID, ProxyName: authorized.ProxyName, Protocol: string(authorized.Protocol)})
		if err != nil && !errors.Is(err, anonymousreconcile.ErrObservationUnconfirmed) {
			return anonymous.HeartbeatResult{}, err
		}
	}
	return r.Store.Heartbeat(ctx, runID, token)
}
