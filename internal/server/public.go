package server

import (
	"log/slog"
	"net/http"
)

// PublicHandler reuses embedded files without constructing the legacy API,
// repository, lease manager, admin endpoints, or plugin transport.
func PublicHandler(releaseDir string) http.Handler {
	assets := &Server{cfg: Config{ReleaseDir: releaseDir}, log: slog.Default()}
	mux := http.NewServeMux()
	for _, path := range []string{"/run.sh", "/run.ps1", "/run.cmd", "/install.cmd"} {
		mux.HandleFunc("GET "+path, assets.runScript)
	}
	if releaseDir != "" {
		mux.Handle("GET /releases/", http.StripPrefix("/releases/", http.FileServer(http.Dir(releaseDir))))
	}
	mux.Handle("GET /", assets.frontend())
	return mux
}
