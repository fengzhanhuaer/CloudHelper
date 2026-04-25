package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestProbeLocalLoginPageServedAsHTML(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)

	resp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/login", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /local/login status=%d body=%s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("GET /local/login content-type=%q", got)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "id=\"registerForm\"") {
		t.Fatalf("login page should contain register form")
	}
	if !strings.Contains(body, "id=\"loginForm\"") {
		t.Fatalf("login page should contain login form")
	}
}

func TestProbeLocalLoginPageMethodNotAllowed(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/login", map[string]any{})
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /local/login status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestProbeLocalPanelServedAfterLogin(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	resp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/panel", nil, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /local/panel status=%d body=%s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("GET /local/panel content-type=%q", got)
	}
	body := resp.Body.String()
	if strings.Contains(body, "当前会话") {
		t.Fatalf("panel should not contain session section")
	}
	if !strings.Contains(body, "代理状态") {
		t.Fatalf("panel should contain proxy status section")
	}
	if !strings.Contains(body, "TUN 状态") {
		t.Fatalf("panel should contain tun status section")
	}
	if !strings.Contains(body, "id=\"tabProxy\"") {
		t.Fatalf("panel should contain proxy tab button")
	}
	if !strings.Contains(body, "id=\"tabTun\"") {
		t.Fatalf("panel should contain tun tab button")
	}
	if !strings.Contains(body, "id=\"tabSystem\"") {
		t.Fatalf("panel should contain system tab button")
	}
	if strings.Contains(body, "id=\"refreshAllBtn\"") {
		t.Fatalf("panel should not contain top refresh all button")
	}
	if !strings.Contains(body, "id=\"proxyRuleGroups\"") {
		t.Fatalf("panel should contain proxy rule group list")
	}
	if !strings.Contains(body, "rule-option-flat") {
		t.Fatalf("panel should contain flat rule option style")
	}
	if !strings.Contains(body, "刷新组与链路") {
		t.Fatalf("panel should contain proxy selection refresh button text")
	}
	if !strings.Contains(body, "直连 + 拒绝 + 可用链路") {
		t.Fatalf("panel should contain reject option hint text")
	}
	if !strings.Contains(body, "id=\"upgradeDirectBtn\"") || !strings.Contains(body, "id=\"upgradeProxyBtn\"") {
		t.Fatalf("panel should contain system upgrade buttons")
	}
}

func TestProbeLocalPanelMethodNotAllowed(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/panel", map[string]any{})
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /local/panel status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestProbeLocalRootRedirectsToLoginWithoutSession(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)

	resp := doProbeLocalRequest(t, mux, http.MethodGet, "/", nil)
	if resp.Code != http.StatusFound {
		t.Fatalf("GET / status=%d body=%s", resp.Code, resp.Body.String())
	}
	if location := resp.Header().Get("Location"); location != "/local/login" {
		t.Fatalf("GET / redirect location=%q", location)
	}
}

func TestProbeLocalRootRedirectsToPanelAfterLogin(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	resp := doProbeLocalRequest(t, mux, http.MethodGet, "/", nil, sessionCookie)
	if resp.Code != http.StatusFound {
		t.Fatalf("GET / (with session) status=%d body=%s", resp.Code, resp.Body.String())
	}
	if location := resp.Header().Get("Location"); location != "/local/panel" {
		t.Fatalf("GET / (with session) redirect location=%q", location)
	}
}
