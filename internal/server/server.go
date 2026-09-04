package server

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/big"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
)

//go:embed assets/*
var publicAssets embed.FS

type Server struct {
	cfg    Config
	repo   domain.Repository
	leases domain.LeaseManager
	log    *slog.Logger
	now    func() time.Time
	runIPs sync.Map
}

func New(cfg Config, repo domain.Repository, leases domain.LeaseManager, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{cfg: cfg, repo: repo, leases: leases, log: logger, now: time.Now}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /api/v1/clients", s.createClient)
	mux.HandleFunc("GET /api/v1/me", s.getMe)
	mux.HandleFunc("POST /api/v1/tunnels", s.createTunnel)
	mux.HandleFunc("GET /api/v1/tunnels/{id}", s.getTunnel)
	mux.HandleFunc("DELETE /api/v1/tunnels/{id}", s.deleteTunnel)
	mux.Handle("POST /internal/frp", s.internalOnly(http.HandlerFunc(s.frpPlugin)))
	mux.Handle("POST /internal/admin/ip-bans", s.internalOnly(http.HandlerFunc(s.createIPBan)))
	mux.Handle("POST /internal/admin/clients/{id}/ban", s.internalOnly(http.HandlerFunc(s.banClient)))
	mux.HandleFunc("GET /run.sh", s.runScript)
	mux.HandleFunc("GET /run.ps1", s.runScript)
	if s.cfg.ReleaseDir != "" {
		files := http.StripPrefix("/releases/", http.FileServer(http.Dir(s.cfg.ReleaseDir)))
		mux.Handle("GET /releases/", files)
	}
	mux.Handle("GET /", s.frontend())
	return s.accessLog(s.recoverer(s.securityHeaders(mux)))
}

func (s *Server) internalOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote, err := parseAddress(r.RemoteAddr)
		forwarded := r.Header.Get("X-Real-IP") != "" || r.Header.Get("X-Forwarded-For") != ""
		if err != nil || !remote.IsLoopback() || forwarded {
			s.log.WarnContext(r.Context(), "internal endpoint access rejected",
				"request_id", requestIDFromContext(r.Context()),
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
				"forwarded_headers", forwarded,
				"address_error", err,
			)
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "tunneld"})
}

func (s *Server) createClient(w http.ResponseWriter, r *http.Request) {
	ip, err := s.requestIP(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_source_ip", err.Error())
		return
	}
	if s.ipBanned(r.Context(), ip, "tunnel_client") {
		writeError(w, http.StatusForbidden, "ip_banned", "this network is not allowed")
		return
	}
	clientID, tokenID, token, err := identity.NewClientCredential()
	if err != nil {
		s.internalError(w, "generate client identity", err)
		return
	}
	now := s.now().UTC()
	client := domain.Client{
		ID: clientID, Status: domain.ClientActive,
		RegistrationIP: ip, LastIP: ip, CreatedAt: now, LastSeenAt: now,
	}
	clientToken := domain.ClientToken{
		ID: tokenID, ClientID: clientID, TokenHash: identity.HashToken(s.cfg.TokenPepper, token), CreatedAt: now,
	}
	if err := s.repo.CreateClient(r.Context(), client, clientToken); err != nil {
		s.internalError(w, "create client", err)
		return
	}
	s.log.InfoContext(r.Context(), "client registered",
		"request_id", requestIDFromContext(r.Context()),
		"client_id", clientID,
		"source_ip", ip,
	)
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":    clientID,
		"client_token": token,
	})
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	client, ok := s.authenticateClient(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"client_id":    client.ID,
		"status":       client.Status,
		"created_at":   client.CreatedAt,
		"last_seen_at": client.LastSeenAt,
	})
}

type createTunnelRequest struct {
	Protocol      string `json:"protocol"`
	LocalPort     int    `json:"local_port"`
	ClientVersion string `json:"client_version"`
}

type frpConnection struct {
	ServerAddr    string `json:"server_addr"`
	ServerPort    int    `json:"server_port"`
	AuthToken     string `json:"auth_token"`
	TLSServerName string `json:"tls_server_name"`
}

type tunnelResponse struct {
	ID          string        `json:"id"`
	ClientID    string        `json:"client_id"`
	Protocol    string        `json:"protocol"`
	Status      string        `json:"status"`
	ProxyName   string        `json:"proxy_name"`
	Subdomain   string        `json:"subdomain,omitempty"`
	RemotePort  int           `json:"remote_port,omitempty"`
	PublicURL   string        `json:"public_url"`
	TunnelToken string        `json:"tunnel_token,omitempty"`
	ExpiresAt   time.Time     `json:"expires_at"`
	Bandwidth   string        `json:"bandwidth_limit,omitempty"`
	FRP         frpConnection `json:"frp,omitempty"`
}

func (s *Server) createTunnel(w http.ResponseWriter, r *http.Request) {
	client, ok := s.authenticateClient(w, r)
	if !ok {
		return
	}
	var request createTunnelRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request.Protocol = strings.ToLower(strings.TrimSpace(request.Protocol))
	if request.Protocol != "http" && request.Protocol != "tcp" && request.Protocol != "udp" {
		writeError(w, http.StatusBadRequest, "invalid_protocol", "protocol must be http, tcp, or udp")
		return
	}
	if request.LocalPort < 1 || request.LocalPort > 65535 {
		writeError(w, http.StatusBadRequest, "invalid_port", "local_port must be between 1 and 65535")
		return
	}
	ip, err := s.requestIP(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_source_ip", err.Error())
		return
	}
	if s.ipBanned(r.Context(), ip, "tunnel_client") {
		writeError(w, http.StatusForbidden, "ip_banned", "this network is not allowed")
		return
	}

	now := s.now().UTC()
	tunnelID, err := identity.NewID("tun_", 10)
	if err != nil {
		s.internalError(w, "generate tunnel id", err)
		return
	}
	tokenID, err := identity.NewID("tok_", 10)
	if err != nil {
		s.internalError(w, "generate tunnel token id", err)
		return
	}
	expiresAt := now.Add(s.cfg.TunnelTTL)
	ipKey := limitIPKey(ip)

	var tunnel domain.Tunnel
	for attempt := 0; attempt < 24; attempt++ {
		tunnel = domain.Tunnel{
			ID: tunnelID, ClientID: client.ID, TokenID: tokenID, Protocol: request.Protocol,
			NodeID: s.cfg.NodeID, ProxyName: tunnelID, RequestIP: ip,
			Status: domain.TunnelReserved, CreatedAt: now, ExpiresAt: expiresAt,
		}
		switch request.Protocol {
		case "http":
			tunnel.Subdomain, err = identity.NewSlug()
		case "tcp":
			tunnel.RemotePort, err = randomPort(s.cfg.TCPPortStart, s.cfg.TCPPortEnd)
		case "udp":
			tunnel.RemotePort, err = randomPort(s.cfg.UDPPortStart, s.cfg.UDPPortEnd)
		}
		if err != nil {
			s.internalError(w, "allocate tunnel resource", err)
			return
		}
		err = s.leases.Reserve(r.Context(), client.ID, ipKey, tunnel.ID, tunnel.ResourceKey(), expiresAt, s.cfg.MaxPerClient, s.cfg.MaxPerIP)
		if errors.Is(err, domain.ErrConflict) {
			s.log.DebugContext(r.Context(), "tunnel resource collision",
				"request_id", requestIDFromContext(r.Context()),
				"client_id", client.ID,
				"protocol", request.Protocol,
				"attempt", attempt+1,
			)
			continue
		}
		if errors.Is(err, domain.ErrLimitReached) {
			writeError(w, http.StatusTooManyRequests, "tunnel_limit_reached", "active tunnel limit reached for this client or network")
			return
		}
		if err != nil {
			s.internalError(w, "reserve tunnel resource", err)
			return
		}
		if err = s.repo.CreateTunnel(r.Context(), tunnel); err == nil {
			break
		}
		_ = s.leases.Release(r.Context(), client.ID, ipKey, tunnel.ID, tunnel.ResourceKey())
		if !errors.Is(err, domain.ErrConflict) {
			s.internalError(w, "persist tunnel", err)
			return
		}
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "resource_exhausted", "could not allocate a tunnel address")
		return
	}

	claims := identity.TunnelClaims{
		Issuer: "nodelane-tunnel", Audience: "frp-plugin", Subject: client.ID,
		SessionID: tunnel.ID, TokenID: tunnel.TokenID, Node: tunnel.NodeID,
		Protocol: tunnel.Protocol, ProxyName: tunnel.ProxyName, Subdomain: tunnel.Subdomain,
		RemotePort: tunnel.RemotePort, IssuedAt: now.Unix(), ExpiresAt: expiresAt.Unix(),
	}
	tunnelToken, err := identity.SignTunnelToken(s.cfg.TunnelJWTSecret, claims)
	if err != nil {
		_ = s.leases.Release(r.Context(), client.ID, ipKey, tunnel.ID, tunnel.ResourceKey())
		_ = s.repo.CloseTunnel(r.Context(), tunnel.ID, domain.TunnelClosed, now)
		s.internalError(w, "sign tunnel token", err)
		return
	}
	_ = s.repo.TouchClient(r.Context(), client.ID, ip, now)
	s.log.InfoContext(r.Context(), "tunnel reserved",
		"request_id", requestIDFromContext(r.Context()),
		"client_id", client.ID,
		"tunnel_id", tunnel.ID,
		"proxy_name", tunnel.ProxyName,
		"protocol", tunnel.Protocol,
		"subdomain", tunnel.Subdomain,
		"remote_port", tunnel.RemotePort,
		"source_ip", ip,
		"client_version", truncateLogValue(request.ClientVersion, 64),
		"expires_at", tunnel.ExpiresAt,
	)
	writeJSON(w, http.StatusCreated, s.responseForTunnel(tunnel, tunnelToken, true))
}

func (s *Server) getTunnel(w http.ResponseWriter, r *http.Request) {
	client, ok := s.authenticateClient(w, r)
	if !ok {
		return
	}
	tunnel, err := s.ownedTunnel(r.Context(), r.PathValue("id"), client.ID)
	if errors.Is(err, domain.ErrNotFound) {
		writeError(w, http.StatusNotFound, "tunnel_not_found", "tunnel not found")
		return
	}
	if err != nil {
		s.internalError(w, "get tunnel", err)
		return
	}
	writeJSON(w, http.StatusOK, s.responseForTunnel(tunnel, "", false))
}

func (s *Server) deleteTunnel(w http.ResponseWriter, r *http.Request) {
	client, ok := s.authenticateClient(w, r)
	if !ok {
		return
	}
	tunnel, err := s.ownedTunnel(r.Context(), r.PathValue("id"), client.ID)
	if errors.Is(err, domain.ErrNotFound) {
		writeError(w, http.StatusNotFound, "tunnel_not_found", "tunnel not found")
		return
	}
	if err != nil {
		s.internalError(w, "get tunnel", err)
		return
	}
	now := s.now().UTC()
	_ = s.leases.Release(r.Context(), client.ID, limitIPKey(tunnel.RequestIP), tunnel.ID, tunnel.ResourceKey())
	if err := s.repo.CloseTunnel(r.Context(), tunnel.ID, domain.TunnelClosed, now); err != nil {
		s.internalError(w, "close tunnel", err)
		return
	}
	s.log.InfoContext(r.Context(), "tunnel closed by client",
		"request_id", requestIDFromContext(r.Context()),
		"client_id", client.ID,
		"tunnel_id", tunnel.ID,
		"protocol", tunnel.Protocol,
	)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) responseForTunnel(tunnel domain.Tunnel, token string, includeSecrets bool) tunnelResponse {
	publicURL := s.cfg.PublicScheme + "://" + tunnel.Subdomain + "." + s.cfg.PublicDomain
	if tunnel.Protocol != "http" {
		publicURL = fmt.Sprintf("%s://%s:%d", tunnel.Protocol, s.cfg.FRPServerAddr, tunnel.RemotePort)
	}
	response := tunnelResponse{
		ID: tunnel.ID, ClientID: tunnel.ClientID, Protocol: tunnel.Protocol, Status: string(tunnel.Status),
		ProxyName: tunnel.ProxyName, Subdomain: tunnel.Subdomain, RemotePort: tunnel.RemotePort,
		PublicURL: publicURL, ExpiresAt: tunnel.ExpiresAt, Bandwidth: s.cfg.FRPBandwidth,
	}
	if includeSecrets {
		response.TunnelToken = token
		response.FRP = frpConnection{
			ServerAddr: s.cfg.FRPServerAddr, ServerPort: s.cfg.FRPServerPort,
			AuthToken: s.cfg.FRPAuthToken, TLSServerName: s.cfg.FRPTLSServerName,
		}
	}
	return response
}

func (s *Server) ownedTunnel(ctx context.Context, tunnelID, clientID string) (domain.Tunnel, error) {
	tunnel, err := s.repo.GetTunnel(ctx, tunnelID)
	if err == nil && tunnel.ClientID != clientID {
		return domain.Tunnel{}, domain.ErrNotFound
	}
	return tunnel, err
}

func (s *Server) authenticateClient(w http.ResponseWriter, r *http.Request) (domain.Client, bool) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	tokenID, err := identity.ParseClientToken(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_client_token", "a valid client token is required")
		return domain.Client{}, false
	}
	clientToken, err := s.repo.GetClientToken(r.Context(), tokenID)
	if err != nil || !clientToken.IsValid(s.now()) || !identity.TokenHashEqual(clientToken.TokenHash, identity.HashToken(s.cfg.TokenPepper, token)) {
		writeError(w, http.StatusUnauthorized, "invalid_client_token", "a valid client token is required")
		return domain.Client{}, false
	}
	client, err := s.repo.GetClient(r.Context(), clientToken.ClientID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_client_token", "a valid client token is required")
		return domain.Client{}, false
	}
	if client.IsBanned(s.now()) {
		writeError(w, http.StatusForbidden, "client_banned", "this client has been suspended")
		return domain.Client{}, false
	}
	_ = s.repo.TouchClientToken(r.Context(), tokenID, s.now().UTC())
	return client, true
}

func (s *Server) requestIP(r *http.Request) (netip.Addr, error) {
	remote, err := parseAddress(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, err
	}
	remote = remote.Unmap()
	if !s.isTrustedProxy(remote) {
		return remote, nil
	}

	if raw := strings.TrimSpace(r.Header.Get("X-Real-IP")); raw != "" {
		clientIP, err := parseAddress(raw)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("parse X-Real-IP: %w", err)
		}
		return clientIP.Unmap(), nil
	}

	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(forwarded) - 1; i >= 0; i-- {
		raw := strings.TrimSpace(forwarded[i])
		if raw == "" {
			continue
		}
		candidate, err := parseAddress(raw)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("parse X-Forwarded-For: %w", err)
		}
		candidate = candidate.Unmap()
		if !s.isTrustedProxy(candidate) {
			return candidate, nil
		}
	}
	return remote, nil
}

func (s *Server) isTrustedProxy(ip netip.Addr) bool {
	for _, prefix := range s.cfg.TrustedProxyRanges {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) ipBanned(ctx context.Context, ip netip.Addr, scope string) bool {
	banned, err := s.repo.IsIPBanned(ctx, ip, scope, s.now().UTC())
	if err != nil {
		s.log.Error("check ip ban", "error", err, "ip", ip, "scope", scope)
		return true
	}
	return banned
}

func limitIPKey(ip netip.Addr) string {
	ip = ip.Unmap()
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().String()
	}
	return ip.String()
}

func randomPort(start, end int) (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(end-start+1)))
	if err != nil {
		return 0, err
	}
	return start + int(n.Int64()), nil
}

func parseAddress(value string) (netip.Addr, error) {
	value = strings.TrimSpace(value)
	if addressPort, err := netip.ParseAddrPort(value); err == nil {
		return addressPort.Addr(), nil
	}
	return netip.ParseAddr(strings.Trim(value, "[]"))
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (s *Server) internalError(w http.ResponseWriter, operation string, err error) {
	s.log.Error(operation, "error", err, "request_id", w.Header().Get("X-Request-ID"))
	writeError(w, http.StatusInternalServerError, "internal_error", "the service could not complete this request")
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/healthz" || r.URL.Path == "/run.sh" || r.URL.Path == "/run.ps1" ||
			strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/internal/") ||
			strings.HasPrefix(r.URL.Path, "/releases/") {
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.ErrorContext(r.Context(), "request panic",
					"request_id", requestIDFromContext(r.Context()),
					"panic", recovered,
					"path", r.URL.Path,
				)
				writeError(w, http.StatusInternalServerError, "internal_error", "the service could not complete this request")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) frontend() http.Handler {
	root, err := fs.Sub(publicAssets, "assets/web")
	if err != nil {
		panic(fmt.Sprintf("open embedded frontend: %v", err))
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		switch {
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case r.URL.Path == "/nodelane-mark.png" || r.URL.Path == "/nodelane-mark-96.png" ||
			r.URL.Path == "/nodelane-mark-192.png" || r.URL.Path == "/nodelane-tunnel-og.png":
			w.Header().Set("Cache-Control", "public, max-age=604800")
		case r.URL.Path == "/robots.txt" || r.URL.Path == "/sitemap.xml":
			w.Header().Set("Cache-Control", "public, max-age=3600")
		default:
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) runScript(w http.ResponseWriter, r *http.Request) {
	name := "assets/run.sh"
	contentType := "text/x-shellscript; charset=utf-8"
	if r.URL.Path == "/run.ps1" {
		name = "assets/run.ps1"
		contentType = "text/plain; charset=utf-8"
	}
	data, err := publicAssets.ReadFile(name)
	if err != nil {
		s.internalError(w, "read bootstrap script", err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "inline")
	_, _ = w.Write(data)
}

func parseExpiresIn(value int64, now time.Time) *time.Time {
	if value <= 0 {
		return nil
	}
	result := now.Add(time.Duration(value) * time.Second)
	return &result
}

func truncateLogValue(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}
