// Package middleware provides HTTP middleware for manager_service.
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/cloudhelper/manager_service/internal/auth"
	"github.com/cloudhelper/manager_service/internal/logging"
)

const requestIDHeader = "X-Request-ID"
const tokenHeader = "X-Session-Token"

// RequestID injects a unique X-Request-ID into every request context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get(requestIDHeader)
		if rid == "" {
			rid = newRequestID()
		}
		w.Header().Set(requestIDHeader, rid)
		r.Header.Set(requestIDHeader, rid) // propagate to handlers via r.Header
		next.ServeHTTP(w, r)
	})
}

// Logger logs each request with method, path, status, and duration.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		logging.Infof("http %s %s => %d (%s) rid=%s",
			r.Method, r.URL.Path, rw.status,
			time.Since(start).Round(time.Millisecond),
			r.Header.Get(requestIDHeader),
		)
	})
}

// Auth validates the session token from X-Session-Token header.
// Returns 401 if absent or invalid.
func Auth(svc *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimSpace(r.Header.Get(tokenHeader))
			rid := r.Header.Get(requestIDHeader)
			if token == "" {
				response.Unauthorized(w, rid)
				return
			}
			if err := svc.ValidateToken(token); err != nil {
				response.Unauthorized(w, rid)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// LocalhostOnly rejects requests not originating from 127.0.0.1.
// Used for the password reset endpoint.
func LocalhostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get(requestIDHeader)
		host := r.RemoteAddr
		// RemoteAddr is "ip:port"
		idx := strings.LastIndex(host, ":")
		if idx >= 0 {
			host = host[:idx]
		}
		host = strings.Trim(host, "[]") // strip IPv6 brackets
		if host != "127.0.0.1" && host != "::1" {
			response.BadRequest(w, rid, "this endpoint is only accessible from localhost")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- helpers ----

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
