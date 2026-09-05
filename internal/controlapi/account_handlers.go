package controlapi

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	routepolicy "github.com/Wy2926/nodelane-tunneld/internal/routes"
)

func (s *Server) listRoutes(w http.ResponseWriter, r *http.Request) {
	query, ok := parseRouteQuery(r)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	principal, ok := s.authorizeAccount(w, r, true, "routes:read")
	if !ok {
		return
	}
	views, err := s.routes.ListRouteViews(r.Context(), principal.AccountID, query)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	result := make([]routeDTO, 0, len(views))
	for _, view := range views {
		result = append(result, s.routeProjection(view))
	}
	writeJSON(w, http.StatusOK, map[string]any{"routes": result})
}

func (s *Server) getRoute(w http.ResponseWriter, r *http.Request, routeID string) {
	if r.URL.RawQuery != "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	principal, ok := s.authorizeAccount(w, r, true, "routes:read")
	if !ok {
		return
	}
	view, err := s.routes.GetRouteView(r.Context(), principal.AccountID, routeID)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.routeProjection(view))
}

func (s *Server) getRouteStats(w http.ResponseWriter, r *http.Request, routeID string) {
	if r.URL.RawQuery != "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	principal, ok := s.authorizeAccount(w, r, true, "routes:read")
	if !ok {
		return
	}
	view, err := s.routes.GetRouteView(r.Context(), principal.AccountID, routeID)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	snapshot := normalizeStatsSnapshot(s.stats.Snapshot(r.Context(), view.Route.ProxyName))
	writeJSON(w, http.StatusOK, makeStatsDTO(routeID, snapshot))
}

type createRouteRequest struct {
	Protocol  string `json:"protocol"`
	Subdomain string `json:"subdomain"`
}

func (s *Server) createRoute(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	principal, ok := s.authorizeAccount(w, r, true, "")
	if !ok || !s.validateWrite(w, r, principal, false) {
		return
	}
	key, ok := idempotencyKey(r)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var request createRouteRequest
	if err := decodeStrictJSON(r, &request); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	request.Protocol = strings.ToLower(strings.TrimSpace(request.Protocol))
	request.Subdomain = strings.ToLower(strings.TrimSpace(request.Subdomain))
	if request.Protocol != "http" {
		s.writeDomainError(w, domain.ErrProtocolNotAllowed)
		return
	}
	if err := routepolicy.ValidateSubdomain(request.Subdomain); err != nil {
		s.writeDomainError(w, err)
		return
	}
	if _, ok := s.guard(w, r, operationCreateRoute, "account:"+principal.AccountID, defaultControlRateLimit, 10*time.Minute); !ok {
		return
	}
	result, err := s.routes.CreateRoute(r.Context(), domain.CreateRouteCommand{
		AccountID:      principal.AccountID,
		Protocol:       request.Protocol,
		Subdomain:      request.Subdomain,
		IdempotencyKey: key,
	})
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"route": s.routeDTO(result.Route), "replayed": result.Replayed})
}

func (s *Server) deleteRoute(w http.ResponseWriter, r *http.Request, routeID string) {
	if r.URL.RawQuery != "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	principal, ok := s.authorizeAccount(w, r, true, "")
	if !ok || !s.validateWrite(w, r, principal, true) {
		return
	}
	route, err := s.routes.DeleteRoute(r.Context(), principal.AccountID, routeID)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.routeDTO(route))
}

func (s *Server) restoreRoute(w http.ResponseWriter, r *http.Request, routeID string) {
	if r.URL.RawQuery != "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	principal, ok := s.authorizeAccount(w, r, true, "")
	if !ok || !s.validateWrite(w, r, principal, true) {
		return
	}
	if _, ok := s.guard(w, r, operationRestoreRoute, "account:"+principal.AccountID, defaultControlRateLimit, 10*time.Minute); !ok {
		return
	}
	route, err := s.routes.RestoreRoute(r.Context(), principal.AccountID, routeID)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.routeDTO(route))
}

func (s *Server) issueLaunchCode(w http.ResponseWriter, r *http.Request, routeID string) {
	if r.URL.RawQuery != "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	principal, ok := s.authorizeAccount(w, r, true, "")
	if !ok || !s.validateWrite(w, r, principal, true) {
		return
	}
	if _, ok := s.guard(w, r, operationIssueLaunch, "account:"+principal.AccountID, launchIssueRateLimit, 10*time.Minute); !ok {
		return
	}
	issued, err := s.routes.IssueLaunchCode(r.Context(), domain.IssueLaunchCodeCommand{AccountID: principal.AccountID, RouteID: routeID})
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"launch_code": issued.Token,
		"route_id":    routeID,
		"expires_at":  issued.Code.ExpiresAt,
	})
}

func (s *Server) startAccountRun(w http.ResponseWriter, r *http.Request, routeID string) {
	if r.URL.RawQuery != "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	principal, ok := s.authorizeAccount(w, r, false, "runs:start")
	if !ok || !s.validateWrite(w, r, principal, true) {
		return
	}
	key, ok := idempotencyKey(r)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	ip, ok := s.guard(w, r, operationStartRun, "account:"+principal.AccountID, defaultControlRateLimit, 10*time.Minute)
	if !ok {
		return
	}
	result, err := s.runs.StartAccountRun(r.Context(), domain.AccountStartCommand{
		AccountID:      principal.AccountID,
		RouteID:        routeID,
		IdempotencyKey: key,
		RequestIP:      ip,
	})
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	s.writeStartResult(w, r, result, routeID)
}

func (s *Server) stopOwnedRun(w http.ResponseWriter, r *http.Request, routeID string) {
	if r.URL.RawQuery != "" {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	principal, ok := s.authorizeAccount(w, r, true, "")
	if !ok || !s.validateWrite(w, r, principal, true) {
		return
	}
	run, err := s.runs.RequestOwnedStop(r.Context(), principal.AccountID, routeID)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": makeRunDTO(run), "stopped": true})
}

func parseRouteQuery(r *http.Request) (domain.RouteQuery, bool) {
	if r.URL.RawQuery == "" {
		return domain.RouteQuery{}, true
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return domain.RouteQuery{}, false
	}
	if len(values) != 1 {
		return domain.RouteQuery{}, false
	}
	deleted, exists := values["deleted"]
	if !exists || len(deleted) != 1 || (deleted[0] != "true" && deleted[0] != "false") {
		return domain.RouteQuery{}, false
	}
	return domain.RouteQuery{Deleted: deleted[0] == "true"}, true
}
