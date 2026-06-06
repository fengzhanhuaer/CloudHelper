package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMngProbeConsoleTokenRoundTrip(t *testing.T) {
	token := mintMngProbeConsoleToken("3", "alpha-node")
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	node, ok := resolveMngProbeConsoleToken(token)
	if !ok || node != "3" {
		t.Fatalf("resolve failed: node=%q ok=%v", node, ok)
	}
	rec, ok := resolveMngProbeConsoleTokenRecord(token)
	if !ok || rec.DisplayName != "alpha-node" {
		t.Fatalf("expected display name to round-trip, rec=%+v ok=%v", rec, ok)
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

func TestMngProbeConsoleTokenSlidingRenewal(t *testing.T) {
	token := mintMngProbeConsoleToken("7")
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	// Force the token close to expiry, then an active resolve should renew it.
	mngProbeConsoleTokens.mu.Lock()
	rec := mngProbeConsoleTokens.data[token]
	rec.ExpiresAt = time.Now().Add(2 * time.Second)
	mngProbeConsoleTokens.data[token] = rec
	mngProbeConsoleTokens.mu.Unlock()

	if node, ok := resolveMngProbeConsoleToken(token); !ok || node != "7" {
		t.Fatalf("resolve failed: node=%q ok=%v", node, ok)
	}

	mngProbeConsoleTokens.mu.Lock()
	got := mngProbeConsoleTokens.data[token].ExpiresAt
	mngProbeConsoleTokens.mu.Unlock()
	if time.Until(got) < time.Hour {
		t.Fatalf("expected sliding renewal to extend expiry, remaining=%v", time.Until(got))
	}
}

func TestMngProbeConsoleProxyDeniedRemintsWithNodeCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/local/panel", nil)
	req.Header.Set("Accept", "text/html")
	req.AddCookie(&http.Cookie{Name: mngProbeConsoleNodeCookieName, Value: "5"})
	rec := httptest.NewRecorder()
	mngProbeConsoleProxyHandler(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("expected redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/mng/probe-console?node=5" {
		t.Fatalf("expected transparent re-mint redirect, got %q", loc)
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

func TestApplyMngProbeConsoleTitle(t *testing.T) {
	body := []byte("<!doctype html><html><head><title>Probe Node 本地控制台</title></head><body></body></html>")
	headers := map[string][]string{"Content-Type": {"text/html; charset=utf-8"}}
	got := string(applyMngProbeConsoleTitle(body, "alpha-node", headers))
	if want := "<title>alpha-node - Probe Node 本地控制台</title>"; !strings.Contains(got, want) {
		t.Fatalf("expected injected title %q, got %q", want, got)
	}
}

func TestApplyMngProbeConsoleTitleSkipsNonHTML(t *testing.T) {
	body := []byte(`{"ok":true}`)
	headers := map[string][]string{"Content-Type": {"application/json"}}
	got := string(applyMngProbeConsoleTitle(body, "alpha-node", headers))
	if got != string(body) {
		t.Fatalf("expected non-html body unchanged, got %q", got)
	}
}
