package controlserver

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Wy2926/nodelane-tunneld/internal/controlapi"
)

type consoleShellReader func(string) ([]byte, error)

func consoleOrPublic(console, public http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/console" || strings.HasPrefix(r.URL.Path, "/console/") {
			console.ServeHTTP(w, r)
			return
		}
		public.ServeHTTP(w, r)
	})
}

func newConsoleHandler(auth controlapi.Authenticator, readShell consoleShellReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Vary", "Cookie")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.RawPath != "" || strings.Contains(r.URL.EscapedPath(), "%") || !validConsolePath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		locale, ok := consoleLocale(r.URL)
		if !ok {
			http.Error(w, "Invalid console request.", http.StatusBadRequest)
			return
		}
		if len(r.Header.Values("Authorization")) != 0 {
			http.Error(w, "A browser session is required.", http.StatusForbidden)
			return
		}
		principal, err := auth.Authenticate(r.Context(), r)
		if errors.Is(err, controlapi.ErrUnauthorized) {
			query := url.Values{"return_to": {r.URL.RequestURI()}, "locale": {locale}}
			http.Redirect(w, r, "/auth/login?"+query.Encode(), http.StatusSeeOther)
			return
		}
		if err != nil {
			http.Error(w, "Console unavailable.", http.StatusServiceUnavailable)
			return
		}
		if principal.Kind != controlapi.PrincipalKindWeb || principal.AccountID == "" {
			http.Error(w, "A browser session is required.", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/console" || r.URL.Path == "/console/" {
			target := "/console/tunnels"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		shell, err := readShell(locale)
		if err != nil || len(shell) == 0 {
			http.Error(w, "Console unavailable.", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodGet {
			_, _ = w.Write(shell)
		}
	})
}

func validConsolePath(path string) bool {
	path = strings.TrimSuffix(path, "/")
	if path == "/console" || path == "/console/tunnels" || path == "/console/tunnels/new" {
		return true
	}
	const prefix = "/console/tunnels/rte_"
	if !strings.HasPrefix(path, prefix) || len(path) != len(prefix)+26 {
		return false
	}
	for _, char := range path[len(prefix):] {
		if (char < 'a' || char > 'z') && (char < '2' || char > '7') {
			return false
		}
	}
	return true
}

func consoleLocale(target *url.URL) (string, bool) {
	if len(target.RawQuery) > 2048 {
		return "", false
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return "", false
	}
	for key, values := range query {
		if (key != "lang" && key != "view") || len(values) != 1 {
			return "", false
		}
	}
	if view := query.Get("view"); view != "" && view != "deleted" {
		return "", false
	}
	locale := query.Get("lang")
	if locale == "" {
		return "en", true
	}
	for _, supported := range []string{"en", "zh-CN", "zh-TW", "es", "fr", "de", "ja", "ko", "pt-BR", "ru", "ar", "hi"} {
		if locale == supported {
			return supported, true
		}
	}
	return "", false
}
