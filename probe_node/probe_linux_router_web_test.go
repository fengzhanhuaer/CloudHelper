//go:build linux_router

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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
	oldLocalOverride := probeLinuxRouterRuntimeState.localOverride
	probeLinuxRouterRuntimeState.nodeID = "21"
	probeLinuxRouterRuntimeState.desired = nil
	probeLinuxRouterRuntimeState.report = probeLinuxRouterRuntimeReport{}
	probeLinuxRouterRuntimeState.manualFailOpen = false
	probeLinuxRouterRuntimeState.localOverride = false
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
		probeLinuxRouterRuntimeState.localOverride = oldLocalOverride
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
	token, err := ensureProbeLocalSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	register := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/api/auth/register", "192.168.1.150:18080", "192.168.1.20:43210", map[string]any{
		"username": "admin", "password": "secret1234", "confirm_password": "secret1234", "setup_token": token,
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
}

func TestProbeLinuxRouterWebConfigAndFailOpenFlow(t *testing.T) {
	handler := setupProbeLinuxRouterWebTest(t)
	cookie := registerAndLoginProbeLinuxRouterWeb(t, handler)
	config := map[string]any{
		"enabled": true, "dns_enabled": true, "interface": "eth0", "gateway_address": "192.168.1.150/24", "upstream_gateway": "192.168.1.1", "lan_cidrs": []string{"192.168.1.0/24", "192.168.1.0/24"},
	}
	saved := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/config", "192.168.1.150:18080", "192.168.1.20:43210", config, cookie)
	if saved.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", saved.Code, saved.Body.String())
	}
	desired, manualFailOpen, localOverride, _ := currentProbeLinuxRouterLocalState()
	if desired == nil || !desired.GatewayProxy.Enabled || len(desired.GatewayProxy.LANCIDRs) != 1 || manualFailOpen || !localOverride {
		t.Fatalf("unexpected local state: desired=%+v fail_open=%t override=%t", desired, manualFailOpen, localOverride)
	}
	if _, err := os.Stat(resolveProbeLinuxRouterConfigPathForTest(t)); err != nil {
		t.Fatalf("saved config missing: %v", err)
	}

	failOpen := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/fail-open", "192.168.1.150:18080", "192.168.1.20:43210", map[string]any{}, cookie)
	if failOpen.Code != http.StatusOK {
		t.Fatalf("fail-open status=%d body=%s", failOpen.Code, failOpen.Body.String())
	}
	_, manualFailOpen, _, _ = currentProbeLinuxRouterLocalState()
	if !manualFailOpen {
		t.Fatal("manual fail-open was not enabled")
	}
	resume := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/resume", "192.168.1.150:18080", "192.168.1.20:43210", map[string]any{}, cookie)
	if resume.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", resume.Code, resume.Body.String())
	}
	_, manualFailOpen, _, _ = currentProbeLinuxRouterLocalState()
	if manualFailOpen {
		t.Fatal("manual fail-open was not cleared")
	}
}

func TestProbeLinuxRouterWebRejectsInvalidGatewayConfig(t *testing.T) {
	handler := setupProbeLinuxRouterWebTest(t)
	cookie := registerAndLoginProbeLinuxRouterWeb(t, handler)
	config := map[string]any{
		"enabled": true, "dns_enabled": true, "interface": "eth0", "gateway_address": "203.0.113.10/24", "upstream_gateway": "203.0.113.1", "lan_cidrs": []string{"192.168.1.0/24"},
	}
	response := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/config", "192.168.1.150:18080", "192.168.1.20:43210", config, cookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid config status=%d body=%s", response.Code, response.Body.String())
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
