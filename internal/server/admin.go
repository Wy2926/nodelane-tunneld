package server

import (
	"crypto/subtle"
	"net/http"
	"net/netip"
	"strings"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
)

func (s *Server) createIPBan(w http.ResponseWriter, r *http.Request) {
	if !s.authenticateAdmin(w, r) {
		return
	}
	var request struct {
		Network          string `json:"network"`
		Scope            string `json:"scope"`
		Reason           string `json:"reason"`
		ExpiresInSeconds int64  `json:"expires_in_seconds"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	prefix, err := netip.ParsePrefix(request.Network)
	if err != nil {
		if address, addressErr := netip.ParseAddr(request.Network); addressErr == nil {
			bits := 128
			if address.Is4() {
				bits = 32
			}
			prefix = netip.PrefixFrom(address, bits)
		} else {
			writeError(w, http.StatusBadRequest, "invalid_network", "network must be an IP address or CIDR")
			return
		}
	}
	if request.Scope == "" {
		request.Scope = "tunnel_client"
	}
	if request.Scope != "tunnel_client" && request.Scope != "public_visitor" && request.Scope != "both" {
		writeError(w, http.StatusBadRequest, "invalid_scope", "scope must be tunnel_client, public_visitor, or both")
		return
	}
	if strings.TrimSpace(request.Reason) == "" {
		writeError(w, http.StatusBadRequest, "missing_reason", "a ban reason is required")
		return
	}
	id, err := identity.NewID("ban_", 10)
	if err != nil {
		s.internalError(w, "generate ban id", err)
		return
	}
	now := s.now().UTC()
	ban := domain.NetworkBan{
		ID: id, Network: prefix.Masked(), Scope: request.Scope, Reason: request.Reason,
		ExpiresAt: parseExpiresIn(request.ExpiresInSeconds, now), CreatedAt: now,
	}
	if err := s.repo.CreateNetworkBan(r.Context(), ban); err != nil {
		s.internalError(w, "create network ban", err)
		return
	}
	s.log.WarnContext(r.Context(), "network ban created",
		"request_id", requestIDFromContext(r.Context()),
		"ban_id", ban.ID,
		"network", ban.Network,
		"scope", ban.Scope,
		"expires_at", ban.ExpiresAt,
	)
	writeJSON(w, http.StatusCreated, ban)
}

func (s *Server) banClient(w http.ResponseWriter, r *http.Request) {
	if !s.authenticateAdmin(w, r) {
		return
	}
	var request struct {
		Reason           string `json:"reason"`
		ExpiresInSeconds int64  `json:"expires_in_seconds"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(request.Reason) == "" {
		writeError(w, http.StatusBadRequest, "missing_reason", "a ban reason is required")
		return
	}
	now := s.now().UTC()
	if err := s.repo.BanClient(r.Context(), r.PathValue("id"), request.Reason, parseExpiresIn(request.ExpiresInSeconds, now)); err != nil {
		if err == domain.ErrNotFound {
			writeError(w, http.StatusNotFound, "client_not_found", "client not found")
			return
		}
		s.internalError(w, "ban client", err)
		return
	}
	s.log.WarnContext(r.Context(), "client banned",
		"request_id", requestIDFromContext(r.Context()),
		"client_id", r.PathValue("id"),
		"expires_in_seconds", request.ExpiresInSeconds,
	)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) authenticateAdmin(w http.ResponseWriter, r *http.Request) bool {
	want := "Bearer " + s.cfg.AdminToken
	got := r.Header.Get("Authorization")
	if s.cfg.AdminToken == "" || len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "admin authorization required")
		return false
	}
	return true
}
