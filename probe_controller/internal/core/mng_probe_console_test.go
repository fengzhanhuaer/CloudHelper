package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMngProbeConsoleTokenRoundTrip(t *testing.T) {
	token := mintMngProbeConsoleToken("3")
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	node, ok := resolveMngProbeConsoleToken(token)
	if !ok || node != "3" {
		t.Fatalf("resolve failed: node=%q ok=%v", node, ok)
	}
	if _, ok := resolveMngProbeConsoleToken("does-not-exist"); ok {
		t.Fatal("unexpected resolve for unknown token")
	}
}

func TestMngProbeConsoleProxyDeniedWithoutCookie(t *testing.T) {
	// API-style request -> 401 JSON.
	req := httptest.NewRequest(http.MethodGet, "/local/api/anything", nil)
	rec := httptest.NewRecorder()
	mngProbeConsoleProxyHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without cookie, got %d", rec.Code)
	}

	// Top-level HTML navigation -> redirect back to the panel.
	nav := httptest.NewRequest(http.MethodGet, "/local/panel", nil)
	nav.Header.Set("Accept", "text/html")
	navRec := httptest.NewRecorder()
	mngProbeConsoleProxyHandler(navRec, nav)
	if navRec.Code != http.StatusFound {
		t.Fatalf("expected redirect for html navigation, got %d", navRec.Code)
	}
}

func TestMngProbeConsoleHeaderFilters(t *testing.T) {
	if !mngProbeConsoleSkipRequestHeader("Cookie") || !mngProbeConsoleSkipRequestHeader("Host") {
		t.Fatal("Cookie/Host must not be forwarded to the probe")
	}
	if mngProbeConsoleSkipRequestHeader("Content-Type") {
		t.Fatal("Content-Type must be forwarded")
	}
	if !mngProbeConsoleSkipResponseHeader("Set-Cookie") {
		t.Fatal("Set-Cookie must be stripped from proxied responses")
	}
	if mngProbeConsoleSkipResponseHeader("Content-Type") {
		t.Fatal("Content-Type must be returned to the browser")
	}
}
