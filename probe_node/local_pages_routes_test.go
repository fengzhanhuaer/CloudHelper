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
	if !strings.Contains(body, "selected_chain_latency_status") {
		t.Fatalf("panel should contain selected_chain_latency_status handling")
	}
	if !strings.Contains(body, "最近测试延迟: 不可达") {
		t.Fatalf("panel should contain unreachable latency text")
	}
	if !strings.Contains(body, "60000") {
		t.Fatalf("panel should contain 60s proxy status polling interval")
	}
	if !strings.Contains(body, "TUN 状态") {
		t.Fatalf("panel should contain tun status section")
	}
	if !strings.Contains(body, "id=\"tabProxy\"") {
		t.Fatalf("panel should contain proxy tab button")
	}
	if strings.Contains(body, "id=\"tabTun\"") || strings.Contains(body, "id=\"panelTun\"") {
		t.Fatalf("panel should not contain standalone tun tab or panel")
	}
	if !strings.Contains(body, "id=\"tabDNS\"") {
		t.Fatalf("panel should contain dns tab button")
	}
	if !strings.Contains(body, "id=\"tabLogs\"") {
		t.Fatalf("panel should contain logs tab button")
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
	if !strings.Contains(body, "/local/api/proxy/select") {
		t.Fatalf("panel should contain proxy select endpoint")
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
	if !strings.Contains(body, "id=\"upgradeStatusText\"") || !strings.Contains(body, "id=\"upgradeProgressFill\"") {
		t.Fatalf("panel should contain upgrade status and progress elements")
	}
	if !strings.Contains(body, "id=\"proxyGlobalToggleBtn\"") {
		t.Fatalf("panel should contain proxy global toggle button")
	}
	if !strings.Contains(body, "id=\"restartServiceBtn\"") {
		t.Fatalf("panel should contain restart service button")
	}
	if !strings.Contains(body, "DNS 状态") {
		t.Fatalf("panel should contain dns status section")
	}
	if !strings.Contains(body, "id=\"dnsRefreshBtn\"") {
		t.Fatalf("panel should contain dns refresh button")
	}
	if !strings.Contains(body, "53") || !strings.Contains(body, "5353") {
		t.Fatalf("panel should contain dns port fallback hint")
	}
	if !strings.Contains(body, "id=\"dnsMapTableBody\"") {
		t.Fatalf("panel should contain dns map table body")
	}
	if !strings.Contains(body, "域名") || !strings.Contains(body, "IP") || !strings.Contains(body, "fake IP") {
		t.Fatalf("panel should contain dns map columns")
	}
	if !strings.Contains(body, "id=\"logsRefreshBtn\"") {
		t.Fatalf("panel should contain logs refresh button")
	}
	if !strings.Contains(body, "id=\"logsAutoRefresh\"") {
		t.Fatalf("panel should contain logs auto refresh switch")
	}
	if !strings.Contains(body, "id=\"logsKeyword\"") {
		t.Fatalf("panel should contain logs keyword filter")
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
