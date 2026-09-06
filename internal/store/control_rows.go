package store

import (
	"database/sql"
	"fmt"
	"net/netip"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

const controlRouteColumns = `id, account_id::text, protocol, subdomain, proxy_name, status, created_at, updated_at, deleted_at, recoverable_until, name_released_at`

const controlRunColumns = `id, route_id, started_via, status, desired_state, host(request_ip), host(connected_ip), created_at, connected_at, last_heartbeat_at, stop_requested_at, stopped_at, connect_deadline_at, lease_expires_at, stop_reason, proxy_registration_granted`

type controlScanner interface {
	Scan(...any) error
}

func scanControlRoute(row controlScanner) (domain.Route, error) {
	var route domain.Route
	if err := row.Scan(&route.ID, &route.AccountID, &route.Protocol, &route.Subdomain, &route.ProxyName, &route.Status,
		&route.CreatedAt, &route.UpdatedAt, &route.DeletedAt, &route.RecoverableUntil, &route.NameReleasedAt); err != nil {
		return domain.Route{}, err
	}
	return route, nil
}

func scanControlRun(row controlScanner) (domain.Run, error) {
	var run domain.Run
	var requestIP string
	var connectedIP, stopReason sql.NullString
	if err := row.Scan(&run.ID, &run.RouteID, &run.StartedVia, &run.Status, &run.DesiredState,
		&requestIP, &connectedIP, &run.CreatedAt, &run.ConnectedAt, &run.LastHeartbeatAt,
		&run.StopRequestedAt, &run.StoppedAt, &run.ConnectDeadlineAt, &run.LeaseExpiresAt, &stopReason, &run.ProxyRegistrationGranted); err != nil {
		return domain.Run{}, err
	}
	address, err := netip.ParseAddr(requestIP)
	if err != nil {
		return domain.Run{}, fmt.Errorf("parse request IP: %w", err)
	}
	run.RequestIP = address
	if connectedIP.Valid {
		address, err := netip.ParseAddr(connectedIP.String)
		if err != nil {
			return domain.Run{}, fmt.Errorf("parse connected IP: %w", err)
		}
		run.ConnectedIP = address
	}
	run.StopReason = stopReason.String
	return run, nil
}
