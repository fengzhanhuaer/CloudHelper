package core

import (
	"net/http"
	"strings"
)

func enforceProbeScopeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasProbeHeaders := strings.TrimSpace(r.Header.Get("X-Probe-Node-Id")) != "" ||
			strings.TrimSpace(r.Header.Get("X-Probe-Nonce")) != "" ||
			strings.TrimSpace(r.Header.Get("X-Probe-Signature")) != ""

		if hasProbeHeaders {
			path := strings.TrimSpace(r.URL.Path)
			if path != "/api/probe" && !strings.HasPrefix(path, "/api/probe/") {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error": "probe identity is restricted to /api/probe/* endpoints",
				})
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, CF-Connecting-IP, X-Forwarded-For, X-Real-IP")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

func authRequiredMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := extractBearerToken(r)
		if err != nil || !IsTokenValid(token) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "invalid or expired session token",
			})
			return
		}
		next(w, r)
	}
}

func requireHTTPSMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isHTTPSRequest(r) {
			writeJSON(w, http.StatusUpgradeRequired, map[string]string{
				"error": "https is required",
			})
			return
		}
		next(w, r)
	}
}

func isHTTPSRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}

	xfp := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if xfp != "" {
		parts := strings.Split(xfp, ",")
		if len(parts) > 0 && strings.EqualFold(strings.TrimSpace(parts[0]), "https") {
			return true
		}
	}

	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Ssl")), "on") {
		return true
	}

	forwarded := strings.ToLower(strings.TrimSpace(r.Header.Get("Forwarded")))
	return strings.Contains(forwarded, "proto=https")
}
