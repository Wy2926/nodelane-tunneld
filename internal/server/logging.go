package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

type requestIDContextKey struct{}

var fallbackRequestID atomic.Uint64

func newRequestID() string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("fallback-%d", fallbackRequestID.Add(1))
}

func requestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

type responseLogWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *responseLogWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseLogWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

func (w *responseLogWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" || len(requestID) > 128 {
			requestID = newRequestID()
		}
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", requestID)
		recorder := &responseLogWriter{ResponseWriter: w}

		next.ServeHTTP(recorder, r)
		if recorder.status == 0 {
			recorder.status = http.StatusOK
		}

		args := []any{
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"response_bytes", recorder.bytes,
			"duration_ms", time.Since(started).Milliseconds(),
			"remote_addr", r.RemoteAddr,
			"user_agent", r.UserAgent(),
		}
		switch {
		case recorder.status >= 500:
			s.log.ErrorContext(ctx, "http request completed", args...)
		case recorder.status >= 400:
			s.log.WarnContext(ctx, "http request completed", args...)
		case r.URL.Path == "/healthz":
			s.log.DebugContext(ctx, "http request completed", args...)
		default:
			s.log.InfoContext(ctx, "http request completed", args...)
		}
	})
}
