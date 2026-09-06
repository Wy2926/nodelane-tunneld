package server

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Console shells live under _shells and must be embedded for authenticated reads.
//
//go:embed all:assets/*
var publicAssets embed.FS

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/run.sh" || r.URL.Path == "/run.ps1" || r.URL.Path == "/run.cmd" ||
			r.URL.Path == "/install.cmd" || strings.HasPrefix(r.URL.Path, "/releases/") {
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		}
		next.ServeHTTP(w, r)
	})
}

func frontend() http.Handler {
	root, err := fs.Sub(publicAssets, "assets/web")
	if err != nil {
		panic(fmt.Sprintf("open embedded frontend: %v", err))
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath := path.Clean(r.URL.Path)
		if requestedPath == "/console" || strings.HasPrefix(requestedPath, "/console/") {
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
			http.NotFound(w, r)
			return
		}
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

func runScript(w http.ResponseWriter, r *http.Request) {
	name := "assets" + r.URL.Path
	data, err := publicAssets.ReadFile(name)
	if err != nil {
		http.Error(w, "the service could not complete this request", http.StatusInternalServerError)
		return
	}
	contentType := "text/plain; charset=utf-8"
	if r.URL.Path == "/run.sh" {
		contentType = "text/x-shellscript; charset=utf-8"
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		data = bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "inline")
	_, _ = w.Write(data)
}
