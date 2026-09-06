// Package anonymousapi exposes the short-lived anonymous run HTTP protocol.
package anonymousapi

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
)

const maxRequestBody = 64 << 10

var (
	fallbackRequestID atomic.Uint64
	rawBase32         = base32.StdEncoding.WithPadding(base32.NoPadding)
)

type Store interface {
	Allocate(context.Context, anonymous.AllocateRequest) (anonymous.Allocation, error)
	Heartbeat(context.Context, string, string) (anonymous.HeartbeatResult, error)
	RequestStop(context.Context, string, string) (anonymous.Run, error)
}

type SourceIPFunc func(*http.Request) (netip.Addr, error)
type BanChecker func(context.Context, netip.Addr) (bool, error)

type Options struct {
	Store    Store
	SourceIP SourceIPFunc
	Banned   BanChecker
}

type Server struct {
	store    Store
	sourceIP SourceIPFunc
	banned   BanChecker
}

func New(options Options) (*Server, error) {
	if options.Store == nil || options.SourceIP == nil || options.Banned == nil {
		return nil, errors.New("anonymous API dependencies are required")
	}
	return &Server{store: options.Store, sourceIP: options.SourceIP, banned: options.Banned}, nil
}

func (s *Server) Handler() http.Handler { return s }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Request-ID", newRequestID())
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	defer func() {
		if recover() != nil {
			s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		}
	}()

	if r.URL.RawPath != "" || strings.Contains(r.URL.EscapedPath(), "%") {
		s.writeError(w, http.StatusNotFound, "route_not_found")
		return
	}
	switch {
	case r.URL.Path == "/api/v1/anonymous/runs":
		if r.Method != http.MethodPost {
			s.methodNotAllowed(w)
			return
		}
		s.allocate(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/runs/"):
		s.runOperation(w, r)
	default:
		s.writeError(w, http.StatusNotFound, "route_not_found")
	}
}

type allocateRequest struct {
	InstallationID string             `json:"installation_id"`
	Protocol       anonymous.Protocol `json:"protocol"`
	LocalHost      string             `json:"local_host"`
	LocalPort      uint16             `json:"local_port"`
}

var allocateKeys = map[string]struct{}{
	"installation_id": {}, "protocol": {}, "local_host": {}, "local_port": {},
}

func (s *Server) allocate(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || len(r.Header.Values("Authorization")) != 0 || !requireJSONContentType(r) {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	key, ok := idempotencyKey(r)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var input allocateRequest
	if err := decodeStrictObject(r, &input, allocateKeys); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !validOpaque(input.InstallationID, 256) || !validOpaque(input.LocalHost, 255) || input.LocalPort == 0 ||
		input.Protocol != anonymous.ProtocolHTTP && input.Protocol != anonymous.ProtocolTCP && input.Protocol != anonymous.ProtocolUDP {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	ip, err := s.sourceIP(r)
	if err != nil || !validSourceIP(ip) {
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	ip = ip.Unmap()
	banned, err := s.banned(r.Context(), ip)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	if banned {
		s.writeError(w, http.StatusForbidden, "ip_banned")
		return
	}
	result, err := s.store.Allocate(r.Context(), anonymous.AllocateRequest{
		InstallationID: input.InstallationID,
		NetworkKey:     networkKey(ip),
		IdempotencyKey: key,
		Protocol:       input.Protocol,
		LocalHost:      input.LocalHost,
		LocalPort:      input.LocalPort,
	})
	if err != nil {
		s.writeAnonymousError(w, err)
		return
	}
	if !validAllocation(result) {
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"run": map[string]any{
			"id":                  result.RunID,
			"proxy_name":          result.ProxyName,
			"public_endpoint":     result.PublicEndpoint,
			"protocol":            result.Protocol,
			"state":               anonymous.StateReserved,
			"desired_state":       anonymous.DesiredRunning,
			"created_at":          result.CreatedAt.UTC(),
			"connect_deadline_at": result.ConnectDeadlineAt.UTC(),
			"hard_expires_at":     result.HardExpiresAt.UTC(),
		},
		"credential_token": result.CredentialToken,
		"replayed":         result.Replayed,
	})
}

func (s *Server) runOperation(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/runs/"), "/")
	if len(parts) != 2 || !validRandomID(parts[0], "anr_") || (parts[1] != "heartbeat" && parts[1] != "stop") {
		s.writeError(w, http.StatusNotFound, "route_not_found")
		return
	}
	if r.Method != http.MethodPost {
		s.methodNotAllowed(w)
		return
	}
	if r.URL.RawQuery != "" || !requireJSONContentType(r) {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	token, ok := anonymousBearer(r)
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := decodeStrictObject(r, &struct{}{}, map[string]struct{}{}); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if parts[1] == "heartbeat" {
		result, err := s.store.Heartbeat(r.Context(), parts[0], token)
		if err != nil {
			s.writeAnonymousError(w, err)
			return
		}
		if result.RunID != parts[0] || result.DesiredState != anonymous.DesiredRunning && result.DesiredState != anonymous.DesiredStopped || result.HardExpiresAt.IsZero() {
			s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
			return
		}
		run := map[string]any{
			"id": result.RunID, "desired_state": result.DesiredState,
			"hard_expires_at": result.HardExpiresAt.UTC(),
		}
		if !result.LeaseExpiresAt.IsZero() {
			run["lease_expires_at"] = result.LeaseExpiresAt.UTC()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"run":     run,
			"stopped": result.DesiredState == anonymous.DesiredStopped,
		})
		return
	}
	run, err := s.store.RequestStop(r.Context(), parts[0], token)
	if err != nil {
		s.writeAnonymousError(w, err)
		return
	}
	if run.RunID != parts[0] || !validRun(run) {
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": runDTO(run), "stopped": true})
}

func validAllocation(result anonymous.Allocation) bool {
	return validRandomID(result.RunID, "anr_") && validRandomID(result.ProxyName, "anon_") &&
		validCredential(result.CredentialToken) && result.PublicEndpoint != "" &&
		(result.Protocol == anonymous.ProtocolHTTP || result.Protocol == anonymous.ProtocolTCP || result.Protocol == anonymous.ProtocolUDP) &&
		!result.CreatedAt.IsZero() && result.CreatedAt.Before(result.ConnectDeadlineAt) && result.ConnectDeadlineAt.Before(result.HardExpiresAt)
}

func validRun(run anonymous.Run) bool {
	return validRandomID(run.RunID, "anr_") && validRandomID(run.ProxyName, "anon_") && run.PublicEndpoint != "" &&
		(run.Protocol == anonymous.ProtocolHTTP || run.Protocol == anonymous.ProtocolTCP || run.Protocol == anonymous.ProtocolUDP) &&
		(run.State == anonymous.StateReserved || run.State == anonymous.StateOnline || run.State == anonymous.StateStopping || run.State == anonymous.StateVerifying || run.State == anonymous.StateReleased) &&
		(run.DesiredState == anonymous.DesiredRunning || run.DesiredState == anonymous.DesiredStopped) && !run.CreatedAt.IsZero() && !run.HardExpiresAt.IsZero()
}

func runDTO(run anonymous.Run) map[string]any {
	result := map[string]any{
		"id": run.RunID, "proxy_name": run.ProxyName, "public_endpoint": run.PublicEndpoint,
		"protocol": run.Protocol, "state": run.State, "desired_state": run.DesiredState,
		"created_at": run.CreatedAt.UTC(), "connect_deadline_at": run.ConnectDeadlineAt.UTC(),
		"hard_expires_at": run.HardExpiresAt.UTC(),
	}
	if !run.LeaseExpiresAt.IsZero() {
		result["lease_expires_at"] = run.LeaseExpiresAt.UTC()
	}
	return result
}

func (s *Server) methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", http.MethodPost)
	s.writeError(w, http.StatusMethodNotAllowed, "invalid_request")
}

func (s *Server) writeAnonymousError(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "dependency_unavailable"
	switch {
	case errors.Is(err, anonymous.ErrInvalidRequest):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, anonymous.ErrInvalidCredential), errors.Is(err, anonymous.ErrRunNotFound):
		status, code = http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, anonymous.ErrRunExpired), errors.Is(err, anonymous.ErrRunStopped):
		status, code = http.StatusGone, "run_stopped"
	case errors.Is(err, anonymous.ErrIdempotencyConflict):
		status, code = http.StatusConflict, "idempotency_conflict"
	case errors.Is(err, anonymous.ErrInstallationLimit), errors.Is(err, anonymous.ErrNetworkLimit):
		status, code = http.StatusConflict, "anonymous_run_limit_reached"
	case errors.Is(err, anonymous.ErrRateLimited):
		var limited *anonymous.RateLimitError
		if errors.As(err, &limited) && limited.RetryAfter > 0 {
			status, code = http.StatusTooManyRequests, "rate_limited"
			seconds := int64((limited.RetryAfter + time.Second - 1) / time.Second)
			w.Header().Set("Retry-After", strconv.FormatInt(max(seconds, 1), 10))
		}
	}
	s.writeError(w, status, code)
}

type errorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func (s *Server) writeError(w http.ResponseWriter, status int, code string) {
	envelope := errorEnvelope{}
	envelope.Error.Code = code
	envelope.Error.Message = errorMessage(code)
	envelope.Error.RequestID = w.Header().Get("X-Request-ID")
	writeJSON(w, status, envelope)
}

func errorMessage(code string) string {
	switch code {
	case "invalid_request":
		return "The request is invalid."
	case "unauthorized":
		return "Authentication is required."
	case "route_not_found":
		return "The route was not found."
	case "ip_banned":
		return "This network is not allowed."
	case "idempotency_conflict":
		return "The idempotency key was already used for another request."
	case "anonymous_run_limit_reached":
		return "The anonymous run limit has been reached."
	case "run_stopped":
		return "The run is no longer active."
	case "rate_limited":
		return "Too many requests."
	default:
		return "A required dependency is unavailable."
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func requireJSONContentType(r *http.Request) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" {
		return false
	}
	for key, value := range parameters {
		if key != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func idempotencyKey(r *http.Request) (string, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	return value, validOpaque(value, 256)
}

func validOpaque(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func anonymousBearer(r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	return token, validCredential(token)
}

func validCredential(token string) bool {
	id, encoded, ok := strings.Cut(token, ".")
	if !ok || !validRandomID(id, "nac_") || len(encoded) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == encoded
}

func validRandomID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	encoded := strings.TrimPrefix(value, prefix)
	if len(encoded) != 26 || encoded != strings.ToLower(encoded) {
		return false
	}
	decoded, err := rawBase32.DecodeString(strings.ToUpper(encoded))
	return err == nil && len(decoded) == 16 && strings.ToLower(rawBase32.EncodeToString(decoded)) == encoded
}

func validSourceIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	return !ip.IsUnspecified() && !ip.IsMulticast()
}

func networkKey(ip netip.Addr) string {
	ip = ip.Unmap()
	if ip.Is4() {
		return ip.String()
	}
	return netip.PrefixFrom(ip, 64).Masked().String()
}

func decodeStrictObject(r *http.Request, target any, allowed map[string]struct{}) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil || len(data) > maxRequestBody {
		return errors.New("invalid request body")
	}
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return errors.New("JSON object required")
	}
	if err := validateObjectKeys(trimmed, allowed); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}

func validateObjectKeys(data string, allowed map[string]struct{}) error {
	decoder := json.NewDecoder(strings.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("JSON object required")
	}
	seen := make(map[string]struct{}, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("JSON object key required")
		}
		if _, ok := allowed[key]; !ok {
			return errors.New("unknown JSON object key")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("duplicate JSON object key")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func newRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("fallback-%d", fallbackRequestID.Add(1))
}
