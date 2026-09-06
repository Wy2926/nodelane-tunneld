// Package runclient implements the native run-credential control protocol.
package runclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DefaultBaseURL = "https://tunnel.nodelane.net/api/v1"
const maxResponseBytes = 1 << 20
const requestTimeout = 10 * time.Second

var ErrInvalidConfiguration = errors.New("invalid_client_configuration")
var ErrInvalidRequest = errors.New("invalid_request")

type Options struct {
	BaseURL    string
	HTTPClient *http.Client
}

type Client struct {
	baseURL string
	http    *http.Client
}

type BootstrapConfig struct {
	FRP  FRPConfig  `json:"frp"`
	OIDC OIDCConfig `json:"oidc"`
}

type FRPConfig struct {
	ServerAddr    string `json:"server_addr"`
	ServerPort    int    `json:"server_port"`
	TLSServerName string `json:"tls_server_name"`
	TrustedCAPEM  string `json:"trusted_ca_pem"`
}

type OIDCConfig struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"client_id"`
	Resource string `json:"resource"`
}

type Route struct {
	ID         string    `json:"id"`
	Protocol   string    `json:"protocol"`
	Subdomain  string    `json:"subdomain"`
	ProxyName  string    `json:"proxy_name"`
	PublicURL  string    `json:"public_url"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	CurrentRun *Run      `json:"current_run,omitempty"`
}

type Run struct {
	ID                string    `json:"id"`
	RouteID           string    `json:"route_id,omitempty"`
	ProxyName         string    `json:"proxy_name,omitempty"`
	Protocol          string    `json:"protocol,omitempty"`
	Subdomain         string    `json:"subdomain,omitempty"`
	PublicEndpoint    string    `json:"public_endpoint,omitempty"`
	CredentialToken   string    `json:"-"`
	Status            string    `json:"status,omitempty"`
	DesiredState      string    `json:"desired_state,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	ConnectedAt       time.Time `json:"connected_at,omitempty"`
	ConnectDeadlineAt time.Time `json:"connect_deadline_at"`
	LeaseExpiresAt    time.Time `json:"lease_expires_at,omitempty"`
	HardExpiresAt     time.Time `json:"hard_expires_at,omitempty"`
	Replayed          bool      `json:"replayed"`
}

func (r Run) String() string   { return "run(" + r.ID + ")" }
func (r Run) GoString() string { return r.String() }

type HeartbeatResult struct {
	Run     Run  `json:"run"`
	Stopped bool `json:"stopped"`
}

type APIError struct {
	StatusCode int           `json:"status"`
	Code       string        `json:"code"`
	RequestID  string        `json:"request_id,omitempty"`
	RetryAfter time.Duration `json:"-"`
}

func (e *APIError) Error() string { return safeErrorCode(e.Code) }

func New(options Options) (*Client, error) {
	base := options.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" ||
		u.RawPath != "" || strings.TrimRight(u.Path, "/") != "/api/v1" || !validHost(u.Hostname()) {
		return nil, ErrInvalidConfiguration
	}
	if u.Scheme != "https" {
		ip, parseErr := netip.ParseAddr(u.Hostname())
		if u.Scheme != "http" || parseErr != nil || !ip.IsLoopback() {
			return nil, ErrInvalidConfiguration
		}
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, ErrInvalidConfiguration
		}
	}
	var client http.Client
	if options.HTTPClient != nil {
		client = *options.HTTPClient
	}
	if client.Transport == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = requestTimeout
		transport.MaxResponseHeaderBytes = 32 << 10
		client.Transport = transport
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout <= 0 || client.Timeout > requestTimeout {
		client.Timeout = requestTimeout
	}
	u.Path = "/api/v1"
	return &Client{baseURL: u.String(), http: &client}, nil
}

func (c *Client) Bootstrap(ctx context.Context) (BootstrapConfig, error) {
	var result BootstrapConfig
	if err := c.request(ctx, http.MethodGet, "/client-config", "", "", nil, false, &result); err != nil {
		return BootstrapConfig{}, err
	}
	if validateBootstrapMetadata(result) != nil || len(result.FRP.TrustedCAPEM) > maxCABytes {
		return BootstrapConfig{}, invalidResponse()
	}
	return result, nil
}

func (c *Client) Routes(ctx context.Context, accessToken string) ([]Route, error) {
	if !validOpaque(accessToken, 16384) {
		return nil, ErrInvalidRequest
	}
	var result struct {
		Routes []Route `json:"routes"`
	}
	if err := c.request(ctx, http.MethodGet, "/routes", accessToken, "", nil, false, &result); err != nil {
		return nil, err
	}
	if result.Routes == nil {
		return nil, invalidResponse()
	}
	for _, route := range result.Routes {
		if !validID(route.ID, "rte_") || route.Protocol != "http" || !validID(route.ProxyName, "rte_") || !validPublicEndpoint(route.Protocol, route.PublicURL) {
			return nil, invalidResponse()
		}
	}
	return result.Routes, nil
}

func (c *Client) Start(ctx context.Context, routeID, accessToken, idempotencyKey string) (Run, error) {
	if !validID(routeID, "rte_") || !validOpaque(accessToken, 16384) || !validOpaque(idempotencyKey, 256) {
		return Run{}, ErrInvalidRequest
	}
	return c.start(ctx, "/routes/"+routeID+"/runs", accessToken, idempotencyKey, struct{}{}, routeID)
}

func (c *Client) Redeem(ctx context.Context, launchCode, nonce string) (Run, error) {
	if !validOpaque(launchCode, 4096) || !validOpaque(nonce, 256) {
		return Run{}, ErrInvalidRequest
	}
	request := struct {
		LaunchCode string `json:"launch_code"`
		Nonce      string `json:"nonce"`
	}{launchCode, nonce}
	return c.start(ctx, "/launch/redeem", "", "", request, "")
}

func (c *Client) Allocate(ctx context.Context, installationID, protocol, localHost string, localPort int, key string) (Run, error) {
	if !validOpaque(installationID, 256) || !validOpaque(key, 256) || !validTarget(Target{protocol, localHost, localPort}) {
		return Run{}, ErrInvalidRequest
	}
	request := struct {
		InstallationID string `json:"installation_id"`
		Protocol       string `json:"protocol"`
		LocalHost      string `json:"local_host"`
		LocalPort      int    `json:"local_port"`
	}{installationID, protocol, localHost, localPort}
	return c.start(ctx, "/anonymous/runs", "", key, request, "")
}

func (c *Client) start(ctx context.Context, path, token, key string, body any, routeID string) (Run, error) {
	var response struct {
		Run             Run    `json:"run"`
		Route           Route  `json:"route"`
		CredentialToken string `json:"credential_token"`
		Replayed        bool   `json:"replayed"`
	}
	if err := c.request(ctx, http.MethodPost, path, token, key, body, true, &response); err != nil {
		return Run{}, err
	}
	run := response.Run
	if path == "/anonymous/runs" && !strings.HasPrefix(run.ID, "anr_") {
		return Run{}, invalidResponse()
	}
	if strings.HasPrefix(run.ID, "run_") {
		if !validID(response.Route.ID, "rte_") || response.Route.ID != run.RouteID || routeID != "" && routeID != run.RouteID {
			return Run{}, invalidResponse()
		}
		run.ProxyName, run.Protocol, run.Subdomain, run.PublicEndpoint = response.Route.ProxyName, response.Route.Protocol, response.Route.Subdomain, response.Route.PublicURL
	} else if !strings.HasPrefix(run.ID, "anr_") || path != "/anonymous/runs" {
		return Run{}, invalidResponse()
	}
	run.CredentialToken, run.Replayed = response.CredentialToken, response.Replayed
	if !validAllocatedRun(run) {
		return Run{}, invalidResponse()
	}
	return run, nil
}

func (c *Client) Heartbeat(ctx context.Context, runID, runToken string) (HeartbeatResult, error) {
	return c.runOperation(ctx, "heartbeat", runID, runToken)
}

func (c *Client) Stop(ctx context.Context, runID, runToken string) (Run, error) {
	result, err := c.runOperation(ctx, "stop", runID, runToken)
	if err != nil {
		return Run{}, err
	}
	if !result.Stopped || result.Run.DesiredState != "stopped" {
		return Run{}, invalidResponse()
	}
	return result.Run, nil
}

func (c *Client) runOperation(ctx context.Context, operation, runID, token string) (HeartbeatResult, error) {
	if !validRunID(runID) || !validOpaque(token, 4096) {
		return HeartbeatResult{}, ErrInvalidRequest
	}
	var response struct {
		Run struct {
			Run
			State string `json:"state"`
		} `json:"run"`
		Stopped bool `json:"stopped"`
	}
	if err := c.request(ctx, http.MethodPost, "/runs/"+runID+"/"+operation, token, "", struct{}{}, false, &response); err != nil {
		return HeartbeatResult{}, err
	}
	run := response.Run.Run
	if run.Status == "" {
		run.Status = response.Run.State
	}
	if run.ID != runID || run.DesiredState != "running" && run.DesiredState != "stopped" || response.Stopped != (run.DesiredState == "stopped") {
		return HeartbeatResult{}, invalidResponse()
	}
	return HeartbeatResult{Run: run, Stopped: response.Stopped}, nil
}

func (c *Client) request(ctx context.Context, method, path, token, key string, body any, retry bool, out any) error {
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return ErrInvalidRequest
		}
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	attempts := 1
	if retry {
		attempts = 2
	}
	for attempt := 0; attempt < attempts; attempt++ {
		request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(encoded))
		if err != nil {
			return ErrInvalidRequest
		}
		// Disable implicit transport POST replay; this method owns the retry bound.
		request.GetBody = nil
		request.Header.Set("Accept", "application/json")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		response, err := c.http.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return contextError(ctx)
			}
			if attempt+1 < attempts {
				continue
			}
			return &APIError{Code: "network_unavailable"}
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		_ = response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return responseError(response, data)
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return contextError(ctx)
			}
			if attempt+1 < attempts {
				continue
			}
			return &APIError{Code: "network_unavailable"}
		}
		if len(data) > maxResponseBytes {
			return invalidResponse()
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(out); err != nil {
			return invalidResponse()
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return invalidResponse()
		}
		return nil
	}
	return &APIError{Code: "network_unavailable"}
}

func responseError(response *http.Response, data []byte) error {
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if len(data) <= maxResponseBytes {
		_ = json.Unmarshal(data, &envelope)
	}
	requestID := response.Header.Get("X-Request-ID")
	if !validRequestID(requestID) {
		requestID = envelope.Error.RequestID
	}
	if !validRequestID(requestID) {
		requestID = ""
	}
	return &APIError{StatusCode: response.StatusCode, Code: safeErrorCode(envelope.Error.Code), RequestID: requestID, RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now())}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		deadline, err := http.ParseTime(value)
		if err != nil {
			return 0
		}
		seconds = deadline.Sub(now).Seconds()
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0
	}
	if seconds > 86400 {
		seconds = 86400
	}
	return time.Duration(math.Ceil(seconds)) * time.Second
}

func safeErrorCode(code string) string {
	switch code {
	case "invalid_request", "unauthorized", "insufficient_scope", "route_not_found", "subdomain_invalid", "subdomain_reserved", "subdomain_conflict", "route_limit_reached", "route_deleted", "run_already_active", "run_stopped", "idempotency_conflict", "launch_code_expired", "launch_code_used", "launch_code_revoked", "rate_limited", "ip_banned", "dependency_unavailable", "anonymous_installation_concurrency_limited", "anonymous_network_concurrency_limited", "anonymous_installation_rate_limited", "anonymous_network_rate_limited", "network_unavailable", "request_timeout", "invalid_response":
		return code
	default:
		return "request_failed"
	}
}

func contextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	return &APIError{Code: "request_timeout"}
}

func invalidResponse() error { return &APIError{Code: "invalid_response"} }

func validRequestID(value string) bool {
	if len(value) != 32 && len(value) != 36 {
		return false
	}
	for _, ch := range value {
		if !(ch >= 'a' && ch <= 'f' || ch >= '0' && ch <= '9' || ch == '-') {
			return false
		}
	}
	return true
}

func validID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) <= len(prefix) || len(value) > 256 {
		return false
	}
	for _, ch := range value {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

func validRunID(value string) bool { return validID(value, "run_") || validID(value, "anr_") }

func validOpaque(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, ch := range value {
		if ch < 33 || ch > 126 {
			return false
		}
	}
	return true
}

func validHost(value string) bool {
	if value == "" || len(value) > 253 || strings.TrimSpace(value) != value {
		return false
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return true
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func validPublicEndpoint(protocol, value string) bool {
	if protocol == "http" {
		if validHost(value) {
			return true
		}
		u, err := url.Parse(value)
		return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.User == nil && validHost(u.Hostname()) && u.Port() == "" && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
	}
	if protocol != "tcp" && protocol != "udp" {
		return false
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || !validHost(host) {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}

func validAllocatedRun(run Run) bool {
	if !validRunID(run.ID) || !validOpaque(run.ProxyName, 256) || !validOpaque(run.CredentialToken, 4096) || !validPublicEndpoint(run.Protocol, run.PublicEndpoint) || run.CreatedAt.IsZero() || !run.ConnectDeadlineAt.After(run.CreatedAt) {
		return false
	}
	if strings.HasPrefix(run.ID, "anr_") {
		return run.HardExpiresAt.After(run.ConnectDeadlineAt)
	}
	return run.Protocol == "http" && validHost(run.Subdomain) && !strings.Contains(run.Subdomain, ".") && run.ProxyName == run.RouteID
}
