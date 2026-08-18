//go:build linux_router

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func setupProbeLinuxRouterWebTest(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalAuthManagerForTest()
	resetProbeLocalSetupTokenForTest()
	probeLinuxRouterRuntimeState.mu.Lock()
	oldNodeID := probeLinuxRouterRuntimeState.nodeID
	oldDesired := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.desired)
	oldReport := probeLinuxRouterRuntimeState.report
	oldManualFailOpen := probeLinuxRouterRuntimeState.manualFailOpen
	probeLinuxRouterRuntimeState.nodeID = "21"
	probeLinuxRouterRuntimeState.desired = nil
	probeLinuxRouterRuntimeState.report = probeLinuxRouterRuntimeReport{}
	probeLinuxRouterRuntimeState.manualFailOpen = false
	probeLinuxRouterRuntimeState.mu.Unlock()
	oldApply := probeLinuxRouterPlatformApply
	oldFailOpen := probeLinuxRouterPlatformFailOpen
	oldCleanup := probeLinuxRouterPlatformCleanup
	probeLinuxRouterPlatformApply = func(probeLinuxRouterSnapshot) (string, error) { return "eth0", nil }
	probeLinuxRouterPlatformFailOpen = func(probeLinuxRouterSnapshot) error { return nil }
	probeLinuxRouterPlatformCleanup = func(*probeLinuxRouterSnapshot) error { return nil }
	t.Cleanup(func() {
		resetProbeLocalAuthManagerForTest()
		resetProbeLocalSetupTokenForTest()
		probeLinuxRouterRuntimeState.mu.Lock()
		probeLinuxRouterRuntimeState.nodeID = oldNodeID
		probeLinuxRouterRuntimeState.desired = oldDesired
		probeLinuxRouterRuntimeState.report = oldReport
		probeLinuxRouterRuntimeState.manualFailOpen = oldManualFailOpen
		probeLinuxRouterRuntimeState.mu.Unlock()
		probeLinuxRouterPlatformApply = oldApply
		probeLinuxRouterPlatformFailOpen = oldFailOpen
		probeLinuxRouterPlatformCleanup = oldCleanup
	})
	return buildProbeLinuxRouterWebHandler()
}

func doProbeLinuxRouterWebRequest(t *testing.T, handler http.Handler, method, path, host, remote string, payload any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
	}
	request := httptest.NewRequest(method, "http://"+host+path, bytes.NewReader(body))
	request.Host = host
	request.RemoteAddr = remote
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func registerAndLoginProbeLinuxRouterWeb(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	register := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/api/auth/register", "192.168.1.150:18080", "192.168.1.20:43210", map[string]any{
		"username": "admin", "password": "secret1234", "confirm_password": "secret1234",
	})
	if register.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", register.Code, register.Body.String())
	}
	login := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/api/auth/login", "192.168.1.150:18080", "192.168.1.20:43210", map[string]any{
		"username": "admin", "password": "secret1234",
	})
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	cookie := extractCookieByName(login, probeLocalSessionCookieName)
	if cookie == nil {
		t.Fatal("login did not return a session cookie")
	}
	return cookie
}

func TestProbeLinuxRouterWebRegistrationDoesNotRequireSetupToken(t *testing.T) {
	handler := setupProbeLinuxRouterWebTest(t)
	bootstrap := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/api/auth/bootstrap", "192.168.1.150:18080", "192.168.1.20:43210", nil)
	if bootstrap.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", bootstrap.Code, bootstrap.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(bootstrap.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["registered"] != false || payload["setup_token_required"] != false {
		t.Fatalf("unexpected bootstrap payload: %#v", payload)
	}
	dataDir, err := resolveDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataDir + string(os.PathSeparator) + probeLocalSetupTokenFile); !os.IsNotExist(err) {
		t.Fatalf("router bootstrap created a setup token: %v", err)
	}
	register := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/api/auth/register", "192.168.1.150:18080", "192.168.1.20:43210", map[string]any{
		"username": "admin", "password": "secret1234", "confirm_password": "secret1234",
	})
	if register.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", register.Code, register.Body.String())
	}
	repeated := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/api/auth/register", "192.168.1.150:18080", "192.168.1.20:43210", map[string]any{
		"username": "other", "password": "otherpass123", "confirm_password": "otherpass123",
	})
	if repeated.Code != http.StatusForbidden {
		t.Fatalf("repeated register status=%d body=%s", repeated.Code, repeated.Body.String())
	}
	if strings.Contains(probeLinuxRouterWebPageHTML, "setupToken") || strings.Contains(probeLinuxRouterWebPageHTML, "初始化令牌") {
		t.Fatal("router page still exposes the setup token field")
	}
}

func TestProbeLinuxRouterWebListenDefaults(t *testing.T) {
	t.Setenv(probeLinuxRouterWebListenEnv, "")
	addr, err := resolveProbeLinuxRouterWebListenAddr()
	if err != nil {
		t.Fatal(err)
	}
	if addr != probeLinuxRouterWebListenDefault {
		t.Fatalf("listen=%q want=%q", addr, probeLinuxRouterWebListenDefault)
	}
	t.Setenv(probeLinuxRouterWebListenEnv, "8.8.8.8:18080")
	if _, err := resolveProbeLinuxRouterWebListenAddr(); err == nil {
		t.Fatal("public listen address unexpectedly accepted")
	}
}

func TestProbeLinuxRouterWebOnlyAllowsPrivateIPClientsAndHosts(t *testing.T) {
	handler := setupProbeLinuxRouterWebTest(t)
	allowed := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/router", "192.168.1.150:18080", "192.168.1.20:43210", nil)
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), "CloudHelper 旁路由") {
		t.Fatalf("private request status=%d body=%s", allowed.Code, allowed.Body.String())
	}
	publicClient := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/router", "192.168.1.150:18080", "8.8.8.8:43210", nil)
	if publicClient.Code != http.StatusForbidden {
		t.Fatalf("public client status=%d", publicClient.Code)
	}
	publicHost := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/router", "8.8.8.8:18080", "192.168.1.20:43210", nil)
	if publicHost.Code != http.StatusForbidden {
		t.Fatalf("public host status=%d", publicHost.Code)
	}
	hostname := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/router", "router.local:18080", "192.168.1.20:43210", nil)
	if hostname.Code != http.StatusForbidden {
		t.Fatalf("hostname status=%d", hostname.Code)
	}
}

func TestProbeLinuxRouterWebDoesNotExposeGenericConsole(t *testing.T) {
	handler := setupProbeLinuxRouterWebTest(t)
	response := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/shell", "192.168.1.150:18080", "192.168.1.20:43210", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("generic console route status=%d", response.Code)
	}
	status := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/router/api/status", "192.168.1.150:18080", "192.168.1.20:43210", nil)
	if status.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status endpoint=%d", status.Code)
	}
	upgrade := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/upgrade/check", "192.168.1.150:18080", "192.168.1.20:43210", map[string]any{"mode": "proxy"})
	if upgrade.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated upgrade endpoint=%d", upgrade.Code)
	}
}

func TestProbeLinuxRouterWebConfigAndFailOpenFlow(t *testing.T) {
	handler := setupProbeLinuxRouterWebTest(t)
	cookie := registerAndLoginProbeLinuxRouterWeb(t, handler)
	config := map[string]any{
		"gateway_proxy": map[string]any{
			"enabled": true, "dns_enabled": true, "interface": "eth0", "gateway_address": "192.168.1.150/24", "upstream_gateway": "192.168.1.1", "lan_cidrs": []string{"192.168.1.0/24", "192.168.1.0/24"},
		},
		"local_ip_proxy": map[string]any{
			"enabled": true, "published_cidrs": []string{"192.168.50.0/24"}, "allowed_node_ids": []string{"1", "1"},
		},
	}
	saved := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/config", "192.168.1.150:18080", "192.168.1.20:43210", config, cookie)
	if saved.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", saved.Code, saved.Body.String())
	}
	desired, manualFailOpen, _ := currentProbeLinuxRouterLocalState()
	if desired == nil || !desired.GatewayProxy.Enabled || len(desired.GatewayProxy.LANCIDRs) != 1 || !desired.LocalIPProxy.Enabled || len(desired.LocalIPProxy.AllowedNodeIDs) != 1 || manualFailOpen {
		t.Fatalf("unexpected local state: desired=%+v fail_open=%t", desired, manualFailOpen)
	}
	if _, err := os.Stat(resolveProbeLinuxRouterConfigPathForTest(t)); err != nil {
		t.Fatalf("saved config missing: %v", err)
	}

	failOpen := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/fail-open", "192.168.1.150:18080", "192.168.1.20:43210", map[string]any{}, cookie)
	if failOpen.Code != http.StatusOK {
		t.Fatalf("fail-open status=%d body=%s", failOpen.Code, failOpen.Body.String())
	}
	_, manualFailOpen, _ = currentProbeLinuxRouterLocalState()
	if !manualFailOpen {
		t.Fatal("manual fail-open was not enabled")
	}
	resume := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/resume", "192.168.1.150:18080", "192.168.1.20:43210", map[string]any{}, cookie)
	if resume.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", resume.Code, resume.Body.String())
	}
	_, manualFailOpen, _ = currentProbeLinuxRouterLocalState()
	if manualFailOpen {
		t.Fatal("manual fail-open was not cleared")
	}
}

func TestProbeLinuxRouterWebRejectsInvalidGatewayConfig(t *testing.T) {
	handler := setupProbeLinuxRouterWebTest(t)
	cookie := registerAndLoginProbeLinuxRouterWeb(t, handler)
	config := map[string]any{
		"gateway_proxy": map[string]any{
			"enabled": true, "dns_enabled": true, "interface": "eth0", "gateway_address": "203.0.113.10/24", "upstream_gateway": "203.0.113.1", "lan_cidrs": []string{"192.168.1.0/24"},
		},
		"local_ip_proxy": map[string]any{"enabled": false, "published_cidrs": []string{"192.168.50.0/24"}},
	}
	response := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/config", "192.168.1.150:18080", "192.168.1.20:43210", config, cookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid config status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProbeLinuxRouterWebUsesLocalConfigAndShowsConnections(t *testing.T) {
	for _, marker := range []string{"本地配置", `<select id="interfaceName">`, `id="allowedNodeIDs"`, `id="connectionRows"`, `id="upgradeBtn"`, "/local/router/api/upgrade/check", "gateway_proxy", "local_ip_proxy"} {
		if !strings.Contains(probeLinuxRouterWebPageHTML, marker) {
			t.Fatalf("router page missing %q", marker)
		}
	}
	for _, forbidden := range []string{"本地临时配置", "主控配置", "local_override"} {
		if strings.Contains(probeLinuxRouterWebPageHTML, forbidden) {
			t.Fatalf("router page still contains %q", forbidden)
		}
	}
}

func TestProbeLinuxRouterWebUpgradeUsesControllerProxy(t *testing.T) {
	handler := setupProbeLinuxRouterWebTest(t)
	cookie := registerAndLoginProbeLinuxRouterWeb(t, handler)
	setprobeLocalRouteRuntimeContext(nodeIdentity{NodeID: "21", Secret: "router-secret"}, "https://controller.example")
	probeLocalFetchRelease = func(_ context.Context, mode, repo, controllerBase string, identity nodeIdentity) (releaseInfo, error) {
		if mode != "proxy" || repo != "fengzhanhuaer/CloudHelper" || controllerBase != "https://controller.example" || identity.NodeID != "21" {
			t.Fatalf("unexpected upgrade check: mode=%q repo=%q controller=%q identity=%+v", mode, repo, controllerBase, identity)
		}
		return releaseInfo{TagName: "v9.9.9", Assets: []releaseAsset{{
			Name: "cloudhelper-probe-router-" + runtime.GOOS + "-" + runtime.GOARCH + ".zip", DownloadURL: "https://example.com/router.zip",
		}}}, nil
	}
	upgradeCommands := make(chan probeControlMessage, 1)
	probeLocalRunUpgrade = func(command probeControlMessage, _ nodeIdentity) { upgradeCommands <- command }
	t.Cleanup(func() {
		resetProbeLocalUpgradeHooksForTest()
		setprobeLocalRouteRuntimeContext(nodeIdentity{}, "")
	})

	check := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/upgrade/check", "192.168.1.150:18080", "192.168.1.20:43210", map[string]any{
		"mode": "proxy", "release_repo": "fengzhanhuaer/CloudHelper",
	}, cookie)
	if check.Code != http.StatusOK || !strings.Contains(check.Body.String(), `"upgradeable":true`) || !strings.Contains(check.Body.String(), "cloudhelper-probe-router-") {
		t.Fatalf("upgrade check status=%d body=%s", check.Code, check.Body.String())
	}
	upgrade := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/upgrade", "192.168.1.150:18080", "192.168.1.20:43210", map[string]any{
		"mode": "proxy", "release_repo": "fengzhanhuaer/CloudHelper",
	}, cookie)
	if upgrade.Code != http.StatusOK {
		t.Fatalf("upgrade status=%d body=%s", upgrade.Code, upgrade.Body.String())
	}
	select {
	case command := <-upgradeCommands:
		if command.Mode != "proxy" || command.ControllerBaseURL != "https://controller.example" {
			t.Fatalf("unexpected upgrade command: %+v", command)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("router upgrade command was not started")
	}
}

func resolveProbeLinuxRouterConfigPathForTest(t *testing.T) string {
	t.Helper()
	dataDir, err := resolveDataDir()
	if err != nil {
		t.Fatal(err)
	}
	return dataDir + string(os.PathSeparator) + probeLinuxRouterConfigFileName
}
