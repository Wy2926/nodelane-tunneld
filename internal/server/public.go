package server

import (
	"errors"
	"net/http"
	"strings"
)

// PublicHandler serves only public website, installer, and release assets.
func PublicHandler(releaseDir string) http.Handler {
	mux := http.NewServeMux()
	for _, path := range []string{"/run.sh", "/run.ps1", "/run.cmd", "/install.cmd"} {
		mux.HandleFunc("GET "+path, runScript)
	}
	if releaseDir != "" {
		mux.Handle("GET /releases/", http.StripPrefix("/releases/", http.FileServer(http.Dir(releaseDir))))
	}
	mux.Handle("GET /", frontend())
	return securityHeaders(mux)
}

// ReadConsoleShell is deliberately separate from routing. The control server
// must authenticate the request before using any of these static shells.
func ReadConsoleShell(locale string) ([]byte, error) {
	for _, supported := range []string{"en", "zh-CN", "zh-TW", "es", "fr", "de", "ja", "ko", "pt-BR", "ru", "ar", "hi"} {
		if locale == supported {
			return publicAssets.ReadFile("assets/web/console/_shells/" + strings.ToLower(locale) + "/index.html")
		}
	}
	return nil, errors.New("unsupported console locale")
}
