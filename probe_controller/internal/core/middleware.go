package core

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
)

func enforceProbeScopeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasProbeHeaders := strings.TrimSpace(r.Header.Get("X-Probe-Node-Id")) != "" ||
			strings.TrimSpace(r.Header.Get("X-Probe-Rand")) != "" ||
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

func enforceSensitiveTransportMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSpace(r.URL.Path)
		sensitive := path == "/mng" || strings.HasPrefix(path, "/mng/") ||
			path == "/api/probe" || strings.HasPrefix(path, "/api/probe/") ||
			path == "/api/controller/migration/script"
		if sensitive && !isHTTPSRequest(r) && !isLoopbackSocketPeer(r) {
			writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" {
			if !isAllowedCORSOrigin(r, origin) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin is not allowed"})
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, Accept, Cache-Control, X-Requested-With")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

func isAllowedCORSOrigin(r *http.Request, origin string) bool {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return true
	}
	scheme := "http"
	if isHTTPSRequest(r) {
		scheme = "https"
	}
	if strings.EqualFold(origin, scheme+"://"+strings.TrimSpace(r.Host)) {
		return true
	}
	for _, configured := range strings.Split(os.Getenv("PROBE_ALLOWED_ORIGINS"), ",") {
		if strings.EqualFold(origin, strings.TrimSpace(configured)) {
			return true
		}
	}
	return false
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

func mngAuthRequiredMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, err := currentMngSessionFromRequest(r)
		if err != nil {
			if strings.HasPrefix(strings.TrimSpace(r.URL.Path), "/mng/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired mng session"})
				return
			}
			http.Redirect(w, r, "/mng", http.StatusFound)
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
	if !isTrustedProxyRequest(r) {
		return false
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

func isTrustedProxyRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	remote, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return false
	}
	if remote.IsLoopback() {
		return true
	}
	for _, raw := range strings.Split(os.Getenv("PROBE_TRUSTED_PROXY_CIDRS"), ",") {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if addr, err := netip.ParseAddr(value); err == nil && addr == remote {
			return true
		}
		if prefix, err := netip.ParsePrefix(value); err == nil && prefix.Contains(remote) {
			return true
		}
	}
	return false
}

func isLoopbackSocketPeer(r *http.Request) bool {
	if r == nil {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	remote, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && remote.IsLoopback()
}
