package controlapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
)

type redeemLaunchRequest struct {
	LaunchCode string `json:"launch_code"`
	Nonce      string `json:"nonce"`
}

func (s *Server) redeemLaunch(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || len(r.Header.Values("Authorization")) != 0 || !requireJSONContentType(r) {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var request redeemLaunchRequest
	if err := decodeStrictJSON(r, &request); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	codeID, err := identity.ParseLaunchCredential(request.LaunchCode)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	request.Nonce = strings.TrimSpace(request.Nonce)
	if request.Nonce == "" || len(request.Nonce) > 256 {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	ip, ok := s.guard(w, r, operationRedeemLaunch, "launch:"+codeID, defaultControlRateLimit, 10*time.Minute)
	if !ok {
		return
	}
	result, err := s.runs.RedeemLaunchCode(r.Context(), domain.LaunchRedeemCommand{
		Token:     request.LaunchCode,
		Nonce:     request.Nonce,
		RequestIP: ip,
	})
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	s.writeStartResult(w, r, result, result.Run.RouteID)
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request, runID string) {
	if r.URL.RawQuery != "" || !requireJSONContentType(r) {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	proof, ok := bearerRunProof(r, runID)
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := decodeEmptyObject(r); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if _, ok := s.guard(w, r, operationHeartbeat, "run:"+runID, heartbeatRateLimit, time.Minute); !ok {
		return
	}
	result, err := s.runs.Heartbeat(r.Context(), proof)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": makeRunDTO(result.Run), "stopped": result.Stopped})
}

func (s *Server) stopCredentialRun(w http.ResponseWriter, r *http.Request, runID string) {
	if r.URL.RawQuery != "" || !requireJSONContentType(r) {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	proof, ok := bearerRunProof(r, runID)
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := decodeEmptyObject(r); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	run, err := s.runs.RequestCredentialStop(r.Context(), proof)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": makeRunDTO(run), "stopped": true})
}

func (s *Server) writeStartResult(w http.ResponseWriter, r *http.Request, result domain.StartResult, expectedRouteID string) {
	proof := domain.RunProof{RunID: result.Run.ID, Token: result.CredentialToken}
	authorized, err := s.runs.AuthorizeRun(r.Context(), proof)
	if err != nil || authorized.Run.ID != result.Run.ID || authorized.Route.ID != result.Run.RouteID || authorized.Route.ID != expectedRouteID {
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"run":              makeRunDTO(result.Run),
		"route":            s.startRouteDTO(authorized.Route),
		"credential_token": result.CredentialToken,
		"replayed":         result.Replayed,
	})
}
