package bff

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/session"
)

const (
	SessionCookieName = "__Host-nodelane-tunnel-session"
	loginCookiePrefix = "__Host-nodelane-tunnel-login-"
	loginLifetime     = 5 * time.Minute
	sessionLifetime   = 24 * time.Hour
	maxLogoutBody     = 4096
	maxLoginQuery     = 8 << 10
	maxCallbackQuery  = 16 << 10
)

var (
	ErrInvalidConfiguration = errors.New("invalid BFF configuration")
	errQueryTooLarge        = errors.New("query too large")
)

type OIDCProvider interface {
	AuthorizationURL(state, nonce, verifier, locale string) (string, error)
	ValidateAuthorizationResponseIssuer(ctx context.Context, issuer string) error
	Exchange(ctx context.Context, code, verifier, nonce string) (identity.OIDCTokens, error)
	Revoke(ctx context.Context, refreshToken string) error
	EndSessionURL(locale string) (string, error)
}

type SessionStore interface {
	PutLogin(ctx context.Context, login session.LoginTransaction) error
	ConsumeLogin(ctx context.Context, state, binding string) (session.LoginTransaction, error)
	CreateSession(ctx context.Context, record session.Record) error
	ReadSession(ctx context.Context, id string) (session.Record, error)
	DeleteSession(ctx context.Context, id string) error
}

type AccountStore interface {
	ResolveAccount(ctx context.Context, issuer, subject string) (domain.Account, error)
}

type Options struct {
	PublicOrigin string
	Provider     OIDCProvider
	Sessions     SessionStore
	LogoutReader SessionReader
	Accounts     AccountStore
	Now          func() time.Time
	Random       io.Reader
}

type Server struct {
	publicOrigin string
	provider     OIDCProvider
	sessions     SessionStore
	logoutReader SessionReader
	accounts     AccountStore
	now          func() time.Time
	random       io.Reader
}

func New(options Options) (*Server, error) {
	origin, err := url.Parse(options.PublicOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawPath != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" ||
		options.Provider == nil || options.Sessions == nil || options.Accounts == nil || options.Random == nil {
		return nil, ErrInvalidConfiguration
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	logoutReader := options.LogoutReader
	if logoutReader == nil {
		logoutReader = options.Sessions
	}
	return &Server{
		publicOrigin: strings.TrimSuffix(options.PublicOrigin, "/"),
		provider:     options.Provider, sessions: options.Sessions, logoutReader: logoutReader, accounts: options.Accounts,
		now: now, random: options.Random,
	}, nil
}

func (s *Server) Handler() http.Handler { return s }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	requestID, err := s.randomToken(16)
	if err != nil {
		requestID = "request-unavailable"
	}
	w.Header().Set("X-Request-ID", requestID)

	switch {
	case r.URL.Path == "/auth/login" && r.Method == http.MethodGet:
		s.login(w, r)
	case r.URL.Path == "/auth/callback" && r.Method == http.MethodGet:
		s.callback(w, r)
	case r.URL.Path == "/auth/logout" && r.Method == http.MethodPost:
		s.logout(w, r)
	case r.URL.Path == "/api/v1/session" && r.Method == http.MethodGet:
		s.getSession(w, r)
	case r.URL.Path == "/auth/login" || r.URL.Path == "/auth/callback" || r.URL.Path == "/auth/logout" || r.URL.Path == "/api/v1/session":
		s.writeError(w, http.StatusMethodNotAllowed, "invalid_request")
	default:
		s.writeError(w, http.StatusNotFound, "route_not_found")
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	query, err := parseBoundedQuery(r.URL.RawQuery, maxLoginQuery)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	returnTo, locale, ok := parseLoginQuery(query)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	state, err := s.randomToken(32)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	nonce, err := s.randomToken(32)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	verifier, err := s.randomToken(32)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	binding, err := s.randomToken(32)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	now := s.now().UTC()
	login := session.LoginTransaction{
		State: state, Nonce: nonce, Verifier: verifier, Binding: binding,
		ReturnTo: returnTo, Locale: locale, ExpiresAt: now.Add(loginLifetime),
	}
	if err := s.sessions.PutLogin(r.Context(), login); err != nil {
		s.writeDependencyError(w, err)
		return
	}
	authorizationURL, err := s.provider.AuthorizationURL(state, nonce, verifier, locale)
	if err != nil {
		// The short-lived record cannot be removed through the intentionally
		// narrow store interface; without its binding cookie it is unusable.
		s.writeDependencyError(w, err)
		return
	}
	s.setLoginCookie(w, state, binding, int(loginLifetime/time.Second))
	http.Redirect(w, r, authorizationURL, http.StatusFound)
}

func parseLoginQuery(query url.Values) (string, string, bool) {
	for key := range query {
		if key != "return_to" && key != "locale" {
			return "", "", false
		}
	}
	returnTo := "/console/tunnels"
	if values, exists := query["return_to"]; exists {
		if len(values) != 1 || !validReturnTo(values[0]) {
			return "", "", false
		}
		returnTo = values[0]
	}
	locale := ""
	if values, exists := query["locale"]; exists {
		if len(values) != 1 {
			return "", "", false
		}
		var ok bool
		locale, ok = canonicalLocale(values[0])
		if !ok {
			return "", "", false
		}
	}
	return returnTo, locale, true
}

func validReturnTo(raw string) bool {
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\\") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" || !strings.HasPrefix(parsed.Path, "/console") {
		return false
	}
	if parsed.Path != "/console" && !strings.HasPrefix(parsed.Path, "/console/") {
		return false
	}
	cleaned := path.Clean(parsed.Path)
	return cleaned == parsed.Path && !strings.Contains(strings.ToLower(parsed.EscapedPath()), "%2f") && !strings.Contains(strings.ToLower(parsed.EscapedPath()), "%5c")
}

func canonicalLocale(raw string) (string, bool) {
	locales := map[string]string{
		"en": "en", "zh-cn": "zh-CN", "zh-tw": "zh-TW", "fr": "fr", "de": "de", "es": "es",
		"pt-br": "pt-BR", "ru": "ru", "ja": "ja", "ko": "ko", "ar": "ar", "hi": "hi",
	}
	locale, ok := locales[strings.ToLower(raw)]
	return locale, ok
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	query, err := parseBoundedQuery(r.URL.RawQuery, maxCallbackQuery)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	code, state, issuer, ok := authorizationResponse(query)
	if !ok || !validAuthorizationCode(code) || !validRandomToken(state) {
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	cookie, err := r.Cookie(loginCookieName(state))
	if err != nil || !validRandomToken(cookie.Value) {
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := s.provider.ValidateAuthorizationResponseIssuer(r.Context(), issuer); err != nil {
		s.writeDependencyError(w, err)
		return
	}
	s.clearLoginCookie(w, state)
	login, err := s.sessions.ConsumeLogin(r.Context(), state, cookie.Value)
	if err != nil {
		s.writeDependencyError(w, err)
		return
	}
	tokens, err := s.provider.Exchange(r.Context(), code, login.Verifier, login.Nonce)
	if err != nil {
		s.writeDependencyError(w, err)
		return
	}
	if tokens.RefreshToken == "" {
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	if tokens.Identity.Issuer == "" || tokens.Identity.Subject == "" {
		_ = s.provider.Revoke(r.Context(), tokens.RefreshToken)
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	account, err := s.accounts.ResolveAccount(r.Context(), tokens.Identity.Issuer, tokens.Identity.Subject)
	if err != nil {
		_ = s.provider.Revoke(r.Context(), tokens.RefreshToken)
		s.writeDependencyError(w, err)
		return
	}
	if account.ID == "" || account.IdentityIssuer != tokens.Identity.Issuer || account.IdentitySubject != tokens.Identity.Subject {
		_ = s.provider.Revoke(r.Context(), tokens.RefreshToken)
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	sessionID, err := s.randomToken(32)
	if err != nil {
		_ = s.provider.Revoke(r.Context(), tokens.RefreshToken)
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	csrfToken, err := s.randomToken(32)
	if err != nil {
		_ = s.provider.Revoke(r.Context(), tokens.RefreshToken)
		s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
		return
	}
	now := s.now().UTC()
	record := session.Record{
		ID: sessionID, AccountID: account.ID, Tokens: tokens, CSRFToken: csrfToken,
		CreatedAt: now, ExpiresAt: now.Add(sessionLifetime),
	}
	if err := s.sessions.CreateSession(r.Context(), record); err != nil {
		_ = s.provider.Revoke(r.Context(), tokens.RefreshToken)
		s.writeDependencyError(w, err)
		return
	}
	s.setSessionCookie(w, sessionID, int(sessionLifetime/time.Second))
	http.Redirect(w, r, login.ReturnTo, http.StatusSeeOther)
}

func parseBoundedQuery(raw string, limit int) (url.Values, error) {
	if len(raw) > limit {
		return nil, errQueryTooLarge
	}
	return url.ParseQuery(raw)
}

func authorizationResponse(query url.Values) (string, string, string, bool) {
	for key := range query {
		if key != "code" && key != "state" && key != "iss" {
			return "", "", "", false
		}
	}
	codes, states := query["code"], query["state"]
	if len(codes) != 1 || len(states) != 1 || codes[0] == "" || states[0] == "" {
		return "", "", "", false
	}
	issuer := ""
	if values, exists := query["iss"]; exists {
		if len(values) != 1 || values[0] == "" {
			return "", "", "", false
		}
		issuer = values[0]
	}
	return codes[0], states[0], issuer, true
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		s.writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	if !validRandomToken(cookie.Value) {
		s.clearSessionCookie(w)
		s.writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	record, err := s.sessions.ReadSession(r.Context(), cookie.Value)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrExpired) {
			s.clearSessionCookie(w)
			s.writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
			return
		}
		s.writeDependencyError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, struct {
		Authenticated bool      `json:"authenticated"`
		AccountID     string    `json:"account_id"`
		Name          string    `json:"name,omitempty"`
		Email         string    `json:"email,omitempty"`
		CSRFToken     string    `json:"csrf_token"`
		ExpiresAt     time.Time `json:"expires_at"`
	}{
		Authenticated: true, AccountID: record.AccountID, Name: record.Tokens.Identity.Name,
		Email: record.Tokens.Identity.Email, CSRFToken: record.CSRFToken, ExpiresAt: record.ExpiresAt,
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || !validRandomToken(cookie.Value) {
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	record, err := s.logoutReader.ReadSession(r.Context(), cookie.Value)
	if err != nil {
		s.writeDependencyError(w, err)
		return
	}
	if !s.validateBrowserWrite(w, r, record.CSRFToken) {
		return
	}
	if !decodeEmptyObject(r.Body) {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	if err := s.sessions.DeleteSession(r.Context(), cookie.Value); err != nil && !errors.Is(err, session.ErrNotFound) {
		s.writeDependencyError(w, err)
		return
	}
	s.clearSessionCookie(w)
	if err := s.provider.Revoke(r.Context(), record.Tokens.RefreshToken); err != nil {
		s.writeDependencyError(w, err)
		return
	}
	endSessionURL, err := s.provider.EndSessionURL("")
	if err != nil {
		s.writeDependencyError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"logged_out": true, "end_session_url": endSessionURL})
}

func (s *Server) validateBrowserWrite(w http.ResponseWriter, r *http.Request, csrfToken string) bool {
	origins := r.Header.Values("Origin")
	csrfValues := r.Header.Values("X-CSRF-Token")
	if len(origins) != 1 || origins[0] != s.publicOrigin || len(csrfValues) != 1 || csrfValues[0] == "" || csrfValues[0] != csrfToken {
		s.writeError(w, http.StatusForbidden, "insufficient_scope")
		return false
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

func decodeEmptyObject(body io.Reader) bool {
	limited := io.LimitReader(body, maxLogoutBody+1)
	data, err := io.ReadAll(limited)
	if err != nil || len(data) > maxLogoutBody {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil || len(object) != 0 {
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	var value any
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return false
	}
	return true
}

func (s *Server) randomToken(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := io.ReadFull(s.random, buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func validRandomToken(value string) bool {
	if len(value) != 43 || strings.TrimSpace(value) != value {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func validAuthorizationCode(code string) bool {
	return code != "" && len(code) <= 4096 && strings.TrimSpace(code) == code && !strings.ContainsAny(code, "\x00\r\n")
}

func loginCookieName(state string) string {
	digest := sha256.Sum256([]byte(state))
	return loginCookiePrefix + hex.EncodeToString(digest[:8])
}

func (s *Server) setLoginCookie(w http.ResponseWriter, state, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: loginCookieName(state), Value: value, Path: "/", MaxAge: maxAge, Expires: s.now().Add(time.Duration(maxAge) * time.Second), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func (s *Server) clearLoginCookie(w http.ResponseWriter, state string) {
	s.clearCookie(w, loginCookieName(state))
}

func (s *Server) setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: value, Path: "/", MaxAge: maxAge, Expires: s.now().Add(time.Duration(maxAge) * time.Second), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) { s.clearCookie(w, SessionCookieName) }

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0).UTC(), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func (s *Server) writeDependencyError(w http.ResponseWriter, err error) {
	if errors.Is(err, identity.ErrOIDCUnauthorized) || errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrExpired) {
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	s.writeError(w, http.StatusServiceUnavailable, "dependency_unavailable")
}

func (s *Server) writeError(w http.ResponseWriter, status int, code string) {
	message := map[string]string{
		"invalid_request": "request is invalid", "unauthorized": "authentication is required",
		"insufficient_scope": "request origin or CSRF token was rejected",
		"route_not_found":    "route not found", "dependency_unavailable": "a required service is unavailable",
	}[code]
	s.writeJSON(w, status, map[string]any{"error": map[string]string{
		"code": code, "message": message, "request_id": w.Header().Get("X-Request-ID"),
	}})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
