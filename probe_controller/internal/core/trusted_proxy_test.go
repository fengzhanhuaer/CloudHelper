package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestForwardedHeadersIgnoredFromUntrustedPeer(t *testing.T) {
	t.Setenv("PROBE_TRUSTED_PROXY_CIDRS", "")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.20:12345"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if isHTTPSRequest(req) {
		t.Fatal("untrusted peer must not assert HTTPS")
	}
	if ip, _ := getClientIP(req); ip != "198.51.100.20" {
		t.Fatalf("client ip=%q, want socket peer", ip)
	}
}

func TestForwardedHeadersAcceptedFromConfiguredTrustedProxy(t *testing.T) {
	t.Setenv("PROBE_TRUSTED_PROXY_CIDRS", "198.51.100.0/24")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.20:12345"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if !isHTTPSRequest(req) {
		t.Fatal("configured trusted proxy should be able to assert HTTPS")
	}
	if ip, _ := getClientIP(req); ip != "203.0.113.9" {
		t.Fatalf("client ip=%q, want forwarded client", ip)
	}
}

func TestSensitiveTransportRejectsRemotePlainHTTP(t *testing.T) {
	handler := enforceSensitiveTransportMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/mng", nil)
	req.RemoteAddr = "198.51.100.20:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("status=%d, want %d", rr.Code, http.StatusUpgradeRequired)
	}
}

func TestCORSMiddlewareRejectsUnconfiguredCrossOrigin(t *testing.T) {
	t.Setenv("PROBE_ALLOWED_ORIGINS", "")
	handler := corsMiddleware(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodGet, "https://controller.example/api/ping", nil)
	req.Header.Set("Origin", "https://attacker.example")
	rr := httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want forbidden", rr.Code)
	}
}
