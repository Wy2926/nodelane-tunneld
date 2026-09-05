package controlapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
)

const maxRequestBody = 64 << 10

const (
	operationCreateRoute    = "create_route"
	operationRestoreRoute   = "restore_route"
	operationIssueLaunch    = "issue_launch_code"
	operationStartRun       = "start_run"
	operationRedeemLaunch   = "redeem_launch"
	operationHeartbeat      = "heartbeat"
	defaultControlRateLimit = 20
	launchIssueRateLimit    = 5
	heartbeatRateLimit      = 60
)

var fallbackRequestID atomic.Uint64

type Server struct {
	publicOrigin string
	publicDomain string
	auth         Authenticator
	routes       RouteRepository
	runs         RunRepository
	sourceIP     SourceIPFunc
	banned       BanChecker
	rateLimit    RateLimiter
}

func New(options Options) (*Server, error) {
	if options.Authenticator == nil || options.Routes == nil || options.Runs == nil || options.SourceIP == nil || options.Banned == nil || options.RateLimit == nil {
		return nil, errors.New("control API dependencies are required")
	}
	if err := validatePublicOrigin(options.PublicOrigin); err != nil {
		return nil, err
	}
	if err := validatePublicDomain(options.PublicDomain); err != nil {
		return nil, err
	}
	return &Server{
		publicOrigin: options.PublicOrigin,
		publicDomain: options.PublicDomain,
		auth:         options.Authenticator,
		routes:       options.Routes,
		runs:         options.Runs,
		sourceIP:     options.SourceIP,
		banned:       options.Banned,
		rateLimit:    options.RateLimit,
	}, nil
}

func (s *Server) Handler() http.Handler { return s }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := newRequestID()
	w.Header().Set("X-Request-ID", requestID)
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
	case r.URL.Path == "/api/v1/routes":
		s.serveRoutes(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/routes/"):
		s.serveRouteResource(w, r)
	case r.URL.Path == "/api/v1/launch/redeem":
		if r.Method != http.MethodPost {
			s.methodNotAllowed(w, http.MethodPost)
			return
		}
		s.redeemLaunch(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/runs/"):
		s.serveRunResource(w, r)
	default:
		s.writeError(w, http.StatusNotFound, "route_not_found")
	}
}

func (s *Server) serveRoutes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listRoutes(w, r)
	case http.MethodPost:
		s.createRoute(w, r)
	default:
		s.methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) serveRouteResource(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/routes/"), "/")
	if len(parts) == 0 || !validResourceID(parts[0], "rte_") {
		s.writeError(w, http.StatusNotFound, "route_not_found")
		return
	}
	routeID := parts[0]
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		s.getRoute(w, r, routeID)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		s.deleteRoute(w, r, routeID)
	case len(parts) == 2 && parts[1] == "restore" && r.Method == http.MethodPost:
		s.restoreRoute(w, r, routeID)
	case len(parts) == 2 && parts[1] == "launch-codes" && r.Method == http.MethodPost:
		s.issueLaunchCode(w, r, routeID)
	case len(parts) == 2 && parts[1] == "runs" && r.Method == http.MethodPost:
		s.startAccountRun(w, r, routeID)
	case len(parts) == 4 && parts[1] == "runs" && parts[2] == "current" && parts[3] == "stop" && r.Method == http.MethodPost:
		s.stopOwnedRun(w, r, routeID)
	case knownRouteShape(parts):
		s.methodNotAllowed(w, allowedRouteMethods(parts)...)
	default:
		s.writeError(w, http.StatusNotFound, "route_not_found")
	}
}

func (s *Server) serveRunResource(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/runs/"), "/")
	if len(parts) != 2 || !validResourceID(parts[0], "run_") || (parts[1] != "heartbeat" && parts[1] != "stop") {
		s.writeError(w, http.StatusNotFound, "route_not_found")
		return
	}
	if r.Method != http.MethodPost {
		s.methodNotAllowed(w, http.MethodPost)
		return
	}
	if parts[1] == "heartbeat" {
		s.heartbeat(w, r, parts[0])
		return
	}
	s.stopCredentialRun(w, r, parts[0])
}

func knownRouteShape(parts []string) bool {
	return len(parts) == 1 ||
		len(parts) == 2 && (parts[1] == "restore" || parts[1] == "launch-codes" || parts[1] == "runs") ||
		len(parts) == 4 && parts[1] == "runs" && parts[2] == "current" && parts[3] == "stop"
}

func allowedRouteMethods(parts []string) []string {
	if len(parts) == 1 {
		return []string{http.MethodGet, http.MethodDelete}
	}
	return []string{http.MethodPost}
}

func (s *Server) methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	s.writeError(w, http.StatusMethodNotAllowed, "invalid_request")
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	principal, err := s.auth.Authenticate(r.Context(), r)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			s.writeError(w, http.StatusUnauthorized, "unauthorized")
		} else {
			s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		}
		return Principal{}, false
	}
	if principal.AccountID == "" || (principal.Kind != PrincipalKindWeb && principal.Kind != PrincipalKindNative) {
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
		return Principal{}, false
	}
	return principal, true
}

func (s *Server) authorizeAccount(w http.ResponseWriter, r *http.Request, allowWeb bool, nativeScope string) (Principal, bool) {
	principal, ok := s.authenticate(w, r)
	if !ok {
		return Principal{}, false
	}
	allowed := principal.Kind == PrincipalKindWeb && allowWeb || principal.Kind == PrincipalKindNative && nativeScope != "" && hasScope(principal.Scopes, nativeScope)
	if !allowed {
		s.writeError(w, http.StatusForbidden, "insufficient_scope")
		return Principal{}, false
	}
	return principal, true
}

func hasScope(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}

func (s *Server) validateWrite(w http.ResponseWriter, r *http.Request, principal Principal, parameterless bool) bool {
	if principal.Kind == PrincipalKindWeb {
		origins := r.Header.Values("Origin")
		if len(origins) != 1 || origins[0] != s.publicOrigin || !constantTimeEqual(singleHeader(r.Header, "X-CSRF-Token"), principal.CSRFToken) {
			s.writeError(w, http.StatusForbidden, "insufficient_scope")
			return false
		}
	}
	if !requireJSONContentType(r) {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	if parameterless {
		if err := decodeEmptyObject(r); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request")
			return false
		}
	}
	return true
}

func (s *Server) guard(w http.ResponseWriter, r *http.Request, operation, principalKey string, limit int, window time.Duration) (netip.Addr, bool) {
	ip, err := s.sourceIP(r)
	if err != nil || !validSourceIP(ip) {
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return netip.Addr{}, false
	}
	ip = ip.Unmap()
	banned, err := s.banned(r.Context(), ip)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return netip.Addr{}, false
	}
	if banned {
		s.writeError(w, http.StatusForbidden, "ip_banned")
		return netip.Addr{}, false
	}
	key := principalKey + ":" + networkKey(ip)
	retryAfter, err := s.rateLimit(r.Context(), operation, key, limit, window)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return netip.Addr{}, false
	}
	if retryAfter > 0 {
		seconds := int64((retryAfter + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		s.writeError(w, http.StatusTooManyRequests, "rate_limited")
		return netip.Addr{}, false
	}
	return ip, true
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
		return "v4:" + ip.String()
	}
	return "v6:" + netip.PrefixFrom(ip, 64).Masked().String()
}

func requireJSONContentType(r *http.Request) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" {
		return false
	}
	for key, value := range params {
		if key != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func singleHeader(header http.Header, name string) string {
	values := header.Values(name)
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func constantTimeEqual(left, right string) bool {
	if left == "" || right == "" || len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func idempotencyKey(r *http.Request) (string, bool) {
	value := strings.TrimSpace(singleHeader(r.Header, "Idempotency-Key"))
	return value, value != "" && len(value) <= 256 && utf8.ValidString(value)
}

func bearerRunProof(r *http.Request, runID string) (domain.RunProof, bool) {
	value := singleHeader(r.Header, "Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return domain.RunProof{}, false
	}
	token := strings.TrimPrefix(value, "Bearer ")
	if token == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, "\t\r\n ,") {
		return domain.RunProof{}, false
	}
	if _, err := identity.ParseRunCredential(token); err != nil {
		return domain.RunProof{}, false
	}
	return domain.RunProof{RunID: runID, Token: token}, true
}

func decodeStrictJSON(r *http.Request, target any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil || len(data) > maxRequestBody {
		return errors.New("invalid request body")
	}
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return errors.New("JSON object required")
	}
	if err := rejectDuplicateObjectKeys(trimmed); err != nil {
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

func rejectDuplicateObjectKeys(data string) error {
	decoder := json.NewDecoder(strings.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("JSON object required")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("JSON object key required")
		}
		if _, exists := seen[key]; exists {
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

func decodeEmptyObject(r *http.Request) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil || len(data) > maxRequestBody {
		return errors.New("invalid request body")
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	r.Body = io.NopCloser(strings.NewReader(string(data)))
	return decodeStrictJSON(r, &struct{}{})
}

func validResourceID(id, prefix string) bool {
	if len(id) != len(prefix)+26 || !strings.HasPrefix(id, prefix) {
		return false
	}
	for i := len(prefix); i < len(id); i++ {
		if !(id[i] >= 'a' && id[i] <= 'z' || id[i] >= '2' && id[i] <= '7') {
			return false
		}
	}
	return true
}

func validatePublicOrigin(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != raw {
		return errors.New("public origin must be an exact HTTPS origin")
	}
	return nil
}

func validatePublicDomain(domainName string) error {
	if domainName == "" || len(domainName) > 253 || strings.ContainsAny(domainName, "/:*?@[]\\") || strings.HasPrefix(domainName, ".") || strings.HasSuffix(domainName, ".") || domainName != strings.ToLower(domainName) {
		return errors.New("invalid public domain")
	}
	labels := strings.Split(domainName, ".")
	if len(labels) < 2 {
		return errors.New("invalid public domain")
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("invalid public domain")
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
				return errors.New("invalid public domain")
			}
		}
	}
	return nil
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("fallback-%d", fallbackRequestID.Add(1))
}

type errorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func (s *Server) writeError(w http.ResponseWriter, status int, code string) {
	requestID := w.Header().Get("X-Request-ID")
	envelope := errorEnvelope{}
	envelope.Error.Code = code
	envelope.Error.Message = errorMessage(code)
	envelope.Error.RequestID = requestID
	writeJSON(w, status, envelope)
}

func errorMessage(code string) string {
	switch code {
	case "invalid_request":
		return "The request is invalid."
	case "unauthorized":
		return "Authentication is required."
	case "insufficient_scope":
		return "The current credential cannot perform this operation."
	case "route_not_found":
		return "The route was not found."
	case "subdomain_invalid":
		return "The subdomain is invalid."
	case "subdomain_reserved":
		return "The subdomain is reserved."
	case "subdomain_conflict":
		return "The subdomain is already in use."
	case "route_limit_reached":
		return "The route limit has been reached."
	case "route_deleted":
		return "The route is deleted."
	case "run_already_active":
		return "The route already has an active run."
	case "run_stopped":
		return "The run is no longer active."
	case "idempotency_conflict":
		return "The idempotency key was already used for another request."
	case "launch_code_expired":
		return "The launch code has expired."
	case "launch_code_used":
		return "The launch code was already used."
	case "launch_code_revoked":
		return "The launch code was revoked."
	case "rate_limited":
		return "Too many requests."
	case "ip_banned":
		return "This network is not allowed."
	default:
		return "A required dependency is unavailable."
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) writeDomainError(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "dependency_unavailable"
	switch {
	case errors.Is(err, domain.ErrInvalidRequest), errors.Is(err, domain.ErrProtocolNotAllowed):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, domain.ErrSubdomainInvalid):
		status, code = http.StatusBadRequest, "subdomain_invalid"
	case errors.Is(err, domain.ErrSubdomainReserved):
		status, code = http.StatusConflict, "subdomain_reserved"
	case errors.Is(err, domain.ErrSubdomainConflict):
		status, code = http.StatusConflict, "subdomain_conflict"
	case errors.Is(err, domain.ErrRouteLimitReached):
		status, code = http.StatusConflict, "route_limit_reached"
	case errors.Is(err, domain.ErrRouteDeleted):
		status, code = http.StatusConflict, "route_deleted"
	case errors.Is(err, domain.ErrRouteNotFound), errors.Is(err, domain.ErrNotFound):
		status, code = http.StatusNotFound, "route_not_found"
	case errors.Is(err, domain.ErrRunAlreadyActive):
		status, code = http.StatusConflict, "run_already_active"
	case errors.Is(err, domain.ErrRunStopped):
		status, code = http.StatusGone, "run_stopped"
	case errors.Is(err, domain.ErrIdempotencyConflict):
		status, code = http.StatusConflict, "idempotency_conflict"
	case errors.Is(err, domain.ErrLaunchCodeExpired):
		status, code = http.StatusGone, "launch_code_expired"
	case errors.Is(err, domain.ErrLaunchCodeUsed):
		status, code = http.StatusGone, "launch_code_used"
	case errors.Is(err, domain.ErrLaunchCodeRevoked):
		status, code = http.StatusGone, "launch_code_revoked"
	case errors.Is(err, domain.ErrInvalidRunProof), errors.Is(err, identity.ErrInvalidCredential):
		status, code = http.StatusUnauthorized, "unauthorized"
	}
	s.writeError(w, status, code)
}
