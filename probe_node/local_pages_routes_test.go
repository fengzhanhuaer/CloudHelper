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
	if strings.Contains(body, "id=\"tabProxy\"") || strings.Contains(body, "id=\"panelProxy\"") {
		t.Fatalf("panel tile home should not contain old tab proxy DOM")
	}
	if !strings.Contains(body, "id=\"tile-proxy\"") || !strings.Contains(body, "href=\"/local/proxy\"") {
		t.Fatalf("panel should contain proxy tile")
	}
	if !strings.Contains(body, "id=\"tile-dns\"") || !strings.Contains(body, "href=\"/local/dns\"") {
		t.Fatalf("panel should contain dns tile")
	}
	if !strings.Contains(body, "id=\"tile-logs\"") || !strings.Contains(body, "href=\"/local/logs\"") {
		t.Fatalf("panel should contain logs tile")
	}
	if !strings.Contains(body, "id=\"tile-system\"") || !strings.Contains(body, "href=\"/local/system\"") {
		t.Fatalf("panel should contain system tile")
	}
	if !strings.Contains(body, "id=\"tile-monitor\"") || !strings.Contains(body, "href=\"/local/monitor\"") {
		t.Fatalf("panel should contain proxy monitor tile")
	}
	if strings.Contains(body, "id=\"monitor-panel-details\"") || strings.Contains(body, "/local/api/proxy/monitor") {
		t.Fatalf("panel should not inline monitor page details")
	}
	if !strings.Contains(body, "当前账号") || !strings.Contains(body, "/local/api/auth/session") {
		t.Fatalf("panel should contain local session summary")
	}
	if !strings.Contains(body, "当前版本") || !strings.Contains(body, "id=\"version\"") {
		t.Fatalf("panel should contain current version tile")
	}
}

func TestProbeLocalStandalonePagesServedAfterLogin(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	cases := []struct {
		path      string
		contains  []string
		notExists []string
	}{
		{
			path: "/local/proxy",
			contains: []string{
				"<title>Probe Node 代理状态</title>",
				"id=\"panelProxy\"",
				"id=\"proxyRuleGroups\"",
				"/local/api/proxy/select",
				"刷新代理组",
				"刷新链路",
				"备份代理规则组",
				"恢复代理规则组",
				"/local/api/proxy/groups/backup",
				"/local/api/proxy/groups/restore",
				"最近测试延迟: 不可达",
			},
			notExists: []string{"id=\"panelDNS\"", "id=\"panelLogs\"", "id=\"panelSystem\""},
		},
		{
			path: "/local/dns",
			contains: []string{
				"<title>Probe Node DNS 状态</title>",
				"id=\"panelDNS\"",
				"id=\"dnsRefreshBtn\"",
				"id=\"dnsMapTableBody\"",
				"fake IP",
				"5353",
			},
			notExists: []string{"id=\"panelProxy\"", "id=\"panelLogs\"", "id=\"panelSystem\""},
		},
		{
			path: "/local/logs",
			contains: []string{
				"<title>Probe Node 运行日志</title>",
				"id=\"panelLogs\"",
				"id=\"logsRefreshBtn\"",
				"id=\"logsAutoRefresh\"",
				"id=\"logsKeyword\"",
			},
			notExists: []string{"id=\"panelProxy\"", "id=\"panelDNS\"", "id=\"panelSystem\""},
		},
		{
			path: "/local/monitor",
			contains: []string{
				"<title>Probe Node 状态监视</title>",
				"id=\"panelMonitor\"",
				"id=\"monitor-panel-details\"",
				"/local/api/proxy/monitor",
				"监视数据",
				"TCP relay 拆分",
				"UDP bridge / association",
				"UDP bridge 明细",
			},
			notExists: []string{"id=\"panelProxy\"", "id=\"panelDNS\"", "id=\"panelLogs\"", "id=\"panelSystem\""},
		},
		{
			path: "/local/system",
			contains: []string{
				"<title>Probe Node 系统设置</title>",
				"id=\"panelSystem\"",
				"TUN 状态",
				"id=\"upgradeDirectBtn\"",
				"id=\"upgradeStatusText\"",
				"id=\"restartServiceBtn\"",
			},
			notExists: []string{"id=\"panelProxy\"", "id=\"panelDNS\"", "id=\"panelLogs\""},
		},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp := doProbeLocalRequest(t, mux, http.MethodGet, tc.path, nil, sessionCookie)
			if resp.Code != http.StatusOK {
				t.Fatalf("GET %s status=%d body=%s", tc.path, resp.Code, resp.Body.String())
			}
			if got := resp.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Fatalf("GET %s content-type=%q", tc.path, got)
			}
			body := resp.Body.String()
			for _, want := range tc.contains {
				if !strings.Contains(body, want) {
					t.Fatalf("GET %s should contain %q", tc.path, want)
				}
			}
			for _, unwanted := range tc.notExists {
				if strings.Contains(body, unwanted) {
					t.Fatalf("GET %s should not contain %q", tc.path, unwanted)
				}
			}
		})
	}
}

func TestProbeLocalPanelMethodNotAllowed(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	paths := []string{"/local/panel", "/local/proxy", "/local/dns", "/local/logs", "/local/monitor", "/local/system"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			resp := doProbeLocalRequest(t, mux, http.MethodPost, path, map[string]any{})
			if resp.Code != http.StatusMethodNotAllowed {
				t.Fatalf("POST %s status=%d body=%s", path, resp.Code, resp.Body.String())
			}
		})
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
