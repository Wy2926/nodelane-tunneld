package controlapi

import (
	"fmt"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/runtimestats"
)

type routeDTO struct {
	ID               string             `json:"id"`
	Protocol         string             `json:"protocol"`
	Subdomain        string             `json:"subdomain"`
	ProxyName        string             `json:"proxy_name"`
	PublicURL        string             `json:"public_url"`
	Status           domain.RouteStatus `json:"status"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
	DeletedAt        *time.Time         `json:"deleted_at,omitempty"`
	RecoverableUntil *time.Time         `json:"recoverable_until,omitempty"`
	NameReleasedAt   *time.Time         `json:"name_released_at,omitempty"`
	CurrentRun       *runDTO            `json:"current_run,omitempty"`
}

type runDTO struct {
	ID                string              `json:"id"`
	RouteID           string              `json:"route_id"`
	StartedVia        domain.StartedVia   `json:"started_via"`
	Status            domain.RunStatus    `json:"status"`
	DesiredState      domain.DesiredState `json:"desired_state"`
	CreatedAt         time.Time           `json:"created_at"`
	ConnectedAt       *time.Time          `json:"connected_at,omitempty"`
	LastHeartbeatAt   *time.Time          `json:"last_heartbeat_at,omitempty"`
	StopRequestedAt   *time.Time          `json:"stop_requested_at,omitempty"`
	StoppedAt         *time.Time          `json:"stopped_at,omitempty"`
	ConnectDeadlineAt time.Time           `json:"connect_deadline_at"`
	LeaseExpiresAt    *time.Time          `json:"lease_expires_at,omitempty"`
	StopReason        string              `json:"stop_reason,omitempty"`
}

type startRouteDTO struct {
	ID        string `json:"id"`
	Protocol  string `json:"protocol"`
	Subdomain string `json:"subdomain"`
	ProxyName string `json:"proxy_name"`
	PublicURL string `json:"public_url"`
}

type statsDTO struct {
	RouteID            string                    `json:"route_id"`
	CurrentConnections *int64                    `json:"current_connections"`
	UploadBytesToday   *int64                    `json:"upload_bytes_today"`
	DownloadBytesToday *int64                    `json:"download_bytes_today"`
	ProxyState         *string                   `json:"proxy_state"`
	ObservedAt         time.Time                 `json:"observed_at"`
	Availability       runtimestats.Availability `json:"availability"`
	TimeZone           string                    `json:"time_zone"`
}

func makeStatsDTO(routeID string, snapshot runtimestats.Snapshot) statsDTO {
	return statsDTO{
		RouteID: routeID, CurrentConnections: snapshot.CurrentConnections,
		UploadBytesToday: snapshot.UploadBytesToday, DownloadBytesToday: snapshot.DownloadBytesToday,
		ProxyState: snapshot.ProxyState, ObservedAt: snapshot.ObservedAt.UTC(),
		Availability: snapshot.Availability, TimeZone: "UTC",
	}
}

func normalizeStatsSnapshot(snapshot runtimestats.Snapshot) runtimestats.Snapshot {
	validState := snapshot.ProxyState != nil && (*snapshot.ProxyState == "online" || *snapshot.ProxyState == "offline")
	validNumbers := snapshot.CurrentConnections != nil && *snapshot.CurrentConnections >= 0 &&
		snapshot.UploadBytesToday != nil && *snapshot.UploadBytesToday >= 0 &&
		snapshot.DownloadBytesToday != nil && *snapshot.DownloadBytesToday >= 0
	if snapshot.Availability == runtimestats.Available && validState && validNumbers && !snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = snapshot.ObservedAt.UTC()
		return snapshot
	}
	availability := snapshot.Availability
	if availability != runtimestats.NotObserved && availability != runtimestats.Unavailable {
		availability = runtimestats.Unavailable
	}
	return runtimestats.Snapshot{ObservedAt: snapshot.ObservedAt.UTC(), Availability: availability}
}

func (s *Server) routeProjection(view domain.RouteView) routeDTO {
	result := s.routeDTO(view.Route)
	if view.CurrentRun != nil {
		run := makeRunDTO(*view.CurrentRun)
		result.CurrentRun = &run
	}
	return result
}

func (s *Server) routeDTO(route domain.Route) routeDTO {
	return routeDTO{
		ID:               route.ID,
		Protocol:         route.Protocol,
		Subdomain:        route.Subdomain,
		ProxyName:        route.ProxyName,
		PublicURL:        s.publicURL(route.Subdomain),
		Status:           route.Status,
		CreatedAt:        route.CreatedAt,
		UpdatedAt:        route.UpdatedAt,
		DeletedAt:        route.DeletedAt,
		RecoverableUntil: route.RecoverableUntil,
		NameReleasedAt:   route.NameReleasedAt,
	}
}

func (s *Server) startRouteDTO(route domain.Route) startRouteDTO {
	return startRouteDTO{
		ID:        route.ID,
		Protocol:  route.Protocol,
		Subdomain: route.Subdomain,
		ProxyName: route.ProxyName,
		PublicURL: s.publicURL(route.Subdomain),
	}
}

func makeRunDTO(run domain.Run) runDTO {
	return runDTO{
		ID:                run.ID,
		RouteID:           run.RouteID,
		StartedVia:        run.StartedVia,
		Status:            run.Status,
		DesiredState:      run.DesiredState,
		CreatedAt:         run.CreatedAt,
		ConnectedAt:       run.ConnectedAt,
		LastHeartbeatAt:   run.LastHeartbeatAt,
		StopRequestedAt:   run.StopRequestedAt,
		StoppedAt:         run.StoppedAt,
		ConnectDeadlineAt: run.ConnectDeadlineAt,
		LeaseExpiresAt:    run.LeaseExpiresAt,
		StopReason:        run.StopReason,
	}
}

func (s *Server) publicURL(subdomain string) string {
	return fmt.Sprintf("https://%s.%s", subdomain, s.publicDomain)
}
