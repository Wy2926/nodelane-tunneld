package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
)

type pluginRun struct {
	clientID string
	tunnelID string
	ip       netip.Addr
}

type pluginPrincipal struct {
	claims identity.TunnelClaims
	tunnel domain.Tunnel
	client domain.Client
}

type pluginAuthorizationError struct {
	public string
	detail error
}

func (e *pluginAuthorizationError) Error() string { return e.detail.Error() }
func (e *pluginAuthorizationError) Unwrap() error { return e.detail }

func pluginPublicReason(err error) string {
	var authorizationError *pluginAuthorizationError
	if errors.As(err, &authorizationError) {
		return authorizationError.public
	}
	return err.Error()
}

func (s *Server) frpPlugin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	frpRequestID := r.Header.Get("X-Frp-Reqid")
	request, err := frpplugin.DecodeRequest(
		http.MaxBytesReader(w, r.Body, 64<<10),
		r.URL.Query().Get("version"),
		r.URL.Query().Get("op"),
	)
	if err != nil {
		s.rejectPlugin(ctx, w, "invalid plugin request", "decode callback", err, frpRequestID, r.URL.Query().Get("op"), "", "")
		return
	}

	s.log.DebugContext(ctx, "frp callback received",
		"request_id", requestIDFromContext(ctx),
		"frp_request_id", frpRequestID,
		"op", request.Op,
		"api_version", request.Version,
	)

	switch request.Op {
	case frpplugin.OpLogin:
		var content frpplugin.LoginContent
		if err := request.DecodeContent(&content); err != nil {
			s.rejectPlugin(ctx, w, "invalid Login content", "decode content", err, frpRequestID, string(request.Op), "", "")
			return
		}
		s.handleFRPLogin(w, r, content, frpRequestID)

	case frpplugin.OpNewProxy:
		var content frpplugin.NewProxyContent
		if err := request.DecodeContent(&content); err != nil {
			s.rejectPlugin(ctx, w, "invalid NewProxy content", "decode content", err, frpRequestID, string(request.Op), "", "")
			return
		}
		s.handleFRPNewProxy(w, r, content, frpRequestID)

	case frpplugin.OpPing:
		var content frpplugin.PingContent
		if err := request.DecodeContent(&content); err != nil {
			s.rejectPlugin(ctx, w, "invalid Ping content", "decode content", err, frpRequestID, string(request.Op), "", "")
			return
		}
		s.handleFRPPing(w, r, content, frpRequestID)

	case frpplugin.OpCloseProxy:
		var content frpplugin.CloseProxyContent
		if err := request.DecodeContent(&content); err != nil {
			s.rejectPlugin(ctx, w, "invalid CloseProxy content", "decode content", err, frpRequestID, string(request.Op), "", "")
			return
		}
		s.handleFRPCloseProxy(w, r, content, frpRequestID)

	case frpplugin.OpNewWorkConn:
		var content frpplugin.NewWorkConnContent
		if err := request.DecodeContent(&content); err != nil {
			s.rejectPlugin(ctx, w, "invalid NewWorkConn content", "decode content", err, frpRequestID, string(request.Op), "", "")
			return
		}
		s.handleFRPNewWorkConn(w, r, content, frpRequestID)

	case frpplugin.OpNewUserConn:
		var content frpplugin.NewUserConnContent
		if err := request.DecodeContent(&content); err != nil {
			s.rejectPlugin(ctx, w, "invalid NewUserConn content", "decode content", err, frpRequestID, string(request.Op), "", "")
			return
		}
		s.handleFRPNewUserConn(w, r, content, frpRequestID)
	}
}

func (s *Server) handleFRPLogin(w http.ResponseWriter, r *http.Request, content frpplugin.LoginContent, frpRequestID string) {
	ctx := r.Context()
	if content.ClientID == "" || content.User != "" {
		err := errors.New("login client_id is required and user must be empty")
		s.rejectPlugin(ctx, w, "client identity does not match tunnel credential", "validate content", err, frpRequestID, string(frpplugin.OpLogin), "", content.RunID)
		return
	}
	principal, err := s.pluginIdentity(ctx, content.Metas["tunnel_token"], "", false)
	if err != nil {
		s.rejectPlugin(ctx, w, pluginPublicReason(err), "authorize callback", err, frpRequestID, string(frpplugin.OpLogin), "", content.RunID)
		return
	}
	if content.ClientID != principal.claims.Subject {
		err := fmt.Errorf("client_id %q does not match credential subject %q", content.ClientID, principal.claims.Subject)
		s.rejectPlugin(ctx, w, "client identity does not match tunnel credential", "validate content", err, frpRequestID, string(frpplugin.OpLogin), "", content.RunID)
		return
	}
	address, err := parseAddress(content.ClientAddress)
	if err != nil {
		s.rejectPlugin(ctx, w, "invalid client address", "parse client address", err, frpRequestID, string(frpplugin.OpLogin), principal.tunnel.ID, content.RunID)
		return
	}
	address = address.Unmap()
	if s.ipBanned(ctx, address, "tunnel_client") {
		s.rejectPlugin(ctx, w, "client network is banned", "network policy", domain.ErrBanned, frpRequestID, string(frpplugin.OpLogin), principal.tunnel.ID, content.RunID)
		return
	}
	run := pluginRun{clientID: principal.client.ID, tunnelID: principal.tunnel.ID, ip: address}
	// On a client's first connection, frps assigns run_id only after the Login
	// plugin returns. Keep the address by tunnel until NewProxy supplies it.
	s.runIPs.Store(tunnelRunKey(principal.tunnel.ID), run)
	if content.RunID != "" {
		s.runIPs.Store(content.RunID, run)
	}
	if err := s.repo.TouchClient(ctx, principal.client.ID, address, s.now().UTC()); err != nil {
		s.log.WarnContext(ctx, "could not update client after frp login",
			"error", err, "client_id", principal.client.ID, "tunnel_id", principal.tunnel.ID,
			"run_id", content.RunID, "frp_request_id", frpRequestID)
	}
	s.allowPlugin(ctx, w, string(frpplugin.OpLogin), frpRequestID, principal, content.RunID, false, nil)
}

func (s *Server) handleFRPNewProxy(w http.ResponseWriter, r *http.Request, content frpplugin.NewProxyContent, frpRequestID string) {
	ctx := r.Context()
	principal, err := s.pluginIdentity(ctx, content.User.Metas["tunnel_token"], content.User.User, false)
	if err != nil {
		s.rejectPlugin(ctx, w, pluginPublicReason(err), "authorize callback", err, frpRequestID, string(frpplugin.OpNewProxy), "", content.User.RunID)
		return
	}
	if err := validateProxyClaims(principal.claims, content); err != nil {
		s.rejectPlugin(ctx, w, err.Error(), "validate proxy", err, frpRequestID, string(frpplugin.OpNewProxy), principal.tunnel.ID, content.User.RunID)
		return
	}
	connectedIP := s.pluginClientIP(content.User.RunID, principal.tunnel)
	if content.User.RunID != "" {
		s.runIPs.Store(content.User.RunID, pluginRun{
			clientID: principal.client.ID,
			tunnelID: principal.tunnel.ID,
			ip:       connectedIP,
		})
	}
	if s.ipBanned(ctx, connectedIP, "tunnel_client") {
		s.rejectPlugin(ctx, w, "client network is banned", "network policy", domain.ErrBanned, frpRequestID, string(frpplugin.OpNewProxy), principal.tunnel.ID, content.User.RunID)
		return
	}
	content.BandwidthLimit = s.cfg.FRPBandwidth
	content.BandwidthLimitMode = "server"
	if err := s.repo.UpdateTunnelConnected(ctx, principal.tunnel.ID, connectedIP, s.now().UTC()); err != nil {
		s.rejectPlugin(ctx, w, "could not activate tunnel", "update tunnel", err, frpRequestID, string(frpplugin.OpNewProxy), principal.tunnel.ID, content.User.RunID)
		return
	}
	s.allowPlugin(ctx, w, string(frpplugin.OpNewProxy), frpRequestID, principal, content.User.RunID, true, content)
}

func (s *Server) handleFRPPing(w http.ResponseWriter, r *http.Request, content frpplugin.PingContent, frpRequestID string) {
	ctx := r.Context()
	principal, err := s.pluginIdentity(ctx, content.User.Metas["tunnel_token"], content.User.User, false)
	if err != nil {
		s.rejectPlugin(ctx, w, pluginPublicReason(err), "authorize callback", err, frpRequestID, string(frpplugin.OpPing), "", content.User.RunID)
		return
	}
	if s.ipBanned(ctx, s.pluginClientIP(content.User.RunID, principal.tunnel), "tunnel_client") {
		s.rejectPlugin(ctx, w, "client network is banned", "network policy", domain.ErrBanned, frpRequestID, string(frpplugin.OpPing), principal.tunnel.ID, content.User.RunID)
		return
	}
	s.allowPlugin(ctx, w, string(frpplugin.OpPing), frpRequestID, principal, content.User.RunID, false, nil)
}

func (s *Server) handleFRPCloseProxy(w http.ResponseWriter, r *http.Request, content frpplugin.CloseProxyContent, frpRequestID string) {
	ctx := r.Context()
	principal, err := s.pluginIdentity(ctx, content.User.Metas["tunnel_token"], content.User.User, true)
	if err != nil {
		s.rejectPlugin(ctx, w, pluginPublicReason(err), "authorize callback", err, frpRequestID, string(frpplugin.OpCloseProxy), "", content.User.RunID)
		return
	}
	if !proxyNameMatches(principal.claims, content.ProxyName) {
		err := fmt.Errorf("proxy_name %q does not match lease", content.ProxyName)
		s.rejectPlugin(ctx, w, "proxy identity does not match its lease", "validate proxy", err, frpRequestID, string(frpplugin.OpCloseProxy), principal.tunnel.ID, content.User.RunID)
		return
	}
	if err := s.leases.Release(ctx, principal.tunnel.ClientID, limitIPKey(principal.tunnel.RequestIP), principal.tunnel.ID, principal.tunnel.ResourceKey()); err != nil {
		s.log.WarnContext(ctx, "could not release closed frp tunnel lease", "error", err, "tunnel_id", principal.tunnel.ID)
	}
	if err := s.repo.CloseTunnel(ctx, principal.tunnel.ID, domain.TunnelClosed, s.now().UTC()); err != nil {
		s.log.WarnContext(ctx, "could not persist closed frp tunnel", "error", err, "tunnel_id", principal.tunnel.ID)
	}
	s.runIPs.Delete(content.User.RunID)
	s.runIPs.Delete(tunnelRunKey(principal.tunnel.ID))
	s.allowPlugin(ctx, w, string(frpplugin.OpCloseProxy), frpRequestID, principal, content.User.RunID, false, nil)
}

func (s *Server) handleFRPNewWorkConn(w http.ResponseWriter, r *http.Request, content frpplugin.NewWorkConnContent, frpRequestID string) {
	ctx := r.Context()
	principal, err := s.pluginIdentity(ctx, content.User.Metas["tunnel_token"], content.User.User, false)
	if err != nil {
		s.rejectPlugin(ctx, w, pluginPublicReason(err), "authorize callback", err, frpRequestID, string(frpplugin.OpNewWorkConn), "", content.User.RunID)
		return
	}
	if content.RunID != "" && content.User.RunID != "" && content.RunID != content.User.RunID {
		err := errors.New("NewWorkConn run_id does not match user.run_id")
		s.rejectPlugin(ctx, w, "run id does not match its session", "validate content", err, frpRequestID, string(frpplugin.OpNewWorkConn), principal.tunnel.ID, content.User.RunID)
		return
	}
	if s.ipBanned(ctx, s.pluginClientIP(content.User.RunID, principal.tunnel), "tunnel_client") {
		s.rejectPlugin(ctx, w, "client network is banned", "network policy", domain.ErrBanned, frpRequestID, string(frpplugin.OpNewWorkConn), principal.tunnel.ID, content.User.RunID)
		return
	}
	s.allowPlugin(ctx, w, string(frpplugin.OpNewWorkConn), frpRequestID, principal, content.User.RunID, false, nil)
}

func (s *Server) handleFRPNewUserConn(w http.ResponseWriter, r *http.Request, content frpplugin.NewUserConnContent, frpRequestID string) {
	ctx := r.Context()
	principal, err := s.pluginIdentity(ctx, content.User.Metas["tunnel_token"], content.User.User, false)
	if err != nil {
		s.rejectPlugin(ctx, w, pluginPublicReason(err), "authorize callback", err, frpRequestID, string(frpplugin.OpNewUserConn), "", content.User.RunID)
		return
	}
	if !proxyNameMatches(principal.claims, content.ProxyName) || content.ProxyType != principal.claims.Protocol {
		err := fmt.Errorf("proxy name/type %q/%q does not match lease", content.ProxyName, content.ProxyType)
		s.rejectPlugin(ctx, w, "proxy identity does not match its lease", "validate proxy", err, frpRequestID, string(frpplugin.OpNewUserConn), principal.tunnel.ID, content.User.RunID)
		return
	}
	if s.ipBanned(ctx, s.pluginClientIP(content.User.RunID, principal.tunnel), "tunnel_client") {
		s.rejectPlugin(ctx, w, "client network is banned", "network policy", domain.ErrBanned, frpRequestID, string(frpplugin.OpNewUserConn), principal.tunnel.ID, content.User.RunID)
		return
	}
	visitorIP, err := parseAddress(content.RemoteAddr)
	if err == nil && s.ipBanned(ctx, visitorIP.Unmap(), "public_visitor") {
		s.rejectPlugin(ctx, w, "visitor network is banned", "visitor network policy", domain.ErrBanned, frpRequestID, string(frpplugin.OpNewUserConn), principal.tunnel.ID, content.User.RunID)
		return
	}
	s.allowPlugin(ctx, w, string(frpplugin.OpNewUserConn), frpRequestID, principal, content.User.RunID, false, nil)
}

func (s *Server) pluginClientIP(runID string, tunnel domain.Tunnel) netip.Addr {
	for _, key := range []string{runID, tunnelRunKey(tunnel.ID)} {
		if key == "" {
			continue
		}
		value, ok := s.runIPs.Load(key)
		run, valid := value.(pluginRun)
		if ok && valid && run.clientID == tunnel.ClientID && run.tunnelID == tunnel.ID && run.ip.IsValid() {
			return run.ip
		}
	}
	if tunnel.ConnectedIP.IsValid() {
		return tunnel.ConnectedIP
	}
	return tunnel.RequestIP
}

func tunnelRunKey(tunnelID string) string {
	return "tunnel:" + tunnelID
}

func (s *Server) pluginIdentity(ctx context.Context, token, frpUser string, allowClosed bool) (pluginPrincipal, error) {
	claims, err := identity.VerifyTunnelToken(s.cfg.TunnelJWTSecret, token, "nodelane-tunnel", "frp-plugin", s.now().UTC())
	if err != nil {
		return pluginPrincipal{}, &pluginAuthorizationError{
			public: "invalid tunnel credential",
			detail: fmt.Errorf("verify tunnel credential: %w", err),
		}
	}
	if frpUser != "" {
		return pluginPrincipal{}, &pluginAuthorizationError{
			public: "client identity does not match tunnel credential",
			detail: fmt.Errorf("frp user must be empty, got %q", frpUser),
		}
	}
	client, err := s.repo.GetClient(ctx, claims.Subject)
	if err != nil {
		public := "could not validate tunnel credential"
		if errors.Is(err, domain.ErrNotFound) {
			public = "client is suspended"
		}
		return pluginPrincipal{}, &pluginAuthorizationError{public: public, detail: fmt.Errorf("load client %q: %w", claims.Subject, err)}
	}
	if client.IsBanned(s.now()) {
		return pluginPrincipal{}, &pluginAuthorizationError{public: "client is suspended", detail: fmt.Errorf("client %q is banned", claims.Subject)}
	}
	tunnel, err := s.repo.GetTunnel(ctx, claims.SessionID)
	if err != nil {
		public := "could not validate tunnel credential"
		if errors.Is(err, domain.ErrNotFound) {
			public = "tunnel does not exist"
		}
		return pluginPrincipal{}, &pluginAuthorizationError{public: public, detail: fmt.Errorf("load tunnel %q: %w", claims.SessionID, err)}
	}
	if tunnel.ClientID != client.ID || tunnel.TokenID != claims.TokenID || tunnel.NodeID != claims.Node {
		return pluginPrincipal{}, &pluginAuthorizationError{
			public: "tunnel credential does not match its lease",
			detail: fmt.Errorf("credential claims do not match tunnel %q", tunnel.ID),
		}
	}
	if !tunnel.ExpiresAt.After(s.now()) {
		return pluginPrincipal{}, &pluginAuthorizationError{
			public: "tunnel credential does not match its lease",
			detail: fmt.Errorf("tunnel %q expired at %s", tunnel.ID, tunnel.ExpiresAt.UTC().Format(time.RFC3339)),
		}
	}
	if !allowClosed && (tunnel.Status == domain.TunnelClosed || tunnel.Status == domain.TunnelExpired) {
		return pluginPrincipal{}, &pluginAuthorizationError{
			public: "tunnel is closed",
			detail: fmt.Errorf("tunnel %q has status %q", tunnel.ID, tunnel.Status),
		}
	}
	return pluginPrincipal{claims: claims, tunnel: tunnel, client: client}, nil
}

func validateProxyClaims(claims identity.TunnelClaims, content frpplugin.NewProxyContent) error {
	if !proxyNameMatches(claims, content.ProxyName) || content.ProxyType != claims.Protocol {
		return errors.New("proxy identity does not match its lease")
	}
	if content.Metas["session_id"] != claims.SessionID {
		return errors.New("proxy session metadata does not match its lease")
	}
	switch claims.Protocol {
	case "http":
		if content.Subdomain != claims.Subdomain {
			return errors.New("subdomain does not match its lease")
		}
		if len(content.CustomDomains) > 0 {
			return errors.New("custom domains are not allowed")
		}
	case "tcp", "udp":
		if content.RemotePort != claims.RemotePort {
			return errors.New("remote port does not match its lease")
		}
	default:
		return errors.New("proxy protocol is not allowed")
	}
	return nil
}

func proxyNameMatches(claims identity.TunnelClaims, actual string) bool {
	return actual == claims.ProxyName
}

func (s *Server) allowPlugin(ctx context.Context, w http.ResponseWriter, op, frpRequestID string, principal pluginPrincipal, runID string, changed bool, content any) {
	log := s.log.InfoContext
	if op == string(frpplugin.OpPing) || op == string(frpplugin.OpNewWorkConn) {
		log = s.log.DebugContext
	}
	log(ctx, "frp callback allowed",
		"request_id", requestIDFromContext(ctx),
		"frp_request_id", frpRequestID,
		"op", op,
		"client_id", principal.client.ID,
		"tunnel_id", principal.tunnel.ID,
		"run_id", runID,
		"proxy_name", principal.claims.ProxyName,
		"changed", changed,
	)
	writeJSON(w, http.StatusOK, frpplugin.Response{Reject: false, Unchange: !changed, Content: content})
}

func (s *Server) rejectPlugin(ctx context.Context, w http.ResponseWriter, publicReason, stage string, err error, frpRequestID, op, tunnelID, runID string) {
	s.log.WarnContext(ctx, "frp callback rejected",
		"request_id", requestIDFromContext(ctx),
		"frp_request_id", frpRequestID,
		"op", op,
		"stage", stage,
		"error", err,
		"tunnel_id", tunnelID,
		"run_id", runID,
	)
	writeJSON(w, http.StatusOK, frpplugin.Response{Reject: true, RejectReason: publicReason})
}
