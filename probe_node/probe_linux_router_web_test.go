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
	oldApplied := cloneProbeLinuxRouterSnapshot(probeLinuxRouterRuntimeState.applied)
	oldManualFailOpen := probeLinuxRouterRuntimeState.manualFailOpen
	probeLinuxRouterRuntimeState.nodeID = "21"
	probeLinuxRouterRuntimeState.desired = nil
	probeLinuxRouterRuntimeState.report = probeLinuxRouterRuntimeReport{}
	probeLinuxRouterRuntimeState.applied = nil
	probeLinuxRouterRuntimeState.manualFailOpen = false
	probeLinuxRouterRuntimeState.mu.Unlock()
	oldApply := probeLinuxRouterPlatformApply
	oldResolve := probeLinuxRouterPlatformResolve
	oldFailOpen := probeLinuxRouterPlatformFailOpen
	oldCleanup := probeLinuxRouterPlatformCleanup
	oldRestoreInterfaceAuto := probeLinuxRouterPlatformRestoreInterfaceAuto
	probeLinuxRouterPlatformApply = func(probeLinuxRouterSnapshot) (string, error) { return "eth0", nil }
	probeLinuxRouterPlatformResolve = func(snapshot probeLinuxRouterSnapshot) (probeLinuxRouterSnapshot, error) {
		snapshot.GatewayProxy.Interface = "eth0"
		snapshot.GatewayProxy.GatewayAddress = "192.168.1.150/24"
		if snapshot.GatewayProxy.UpstreamGateway == "" {
			snapshot.GatewayProxy.UpstreamGateway = "192.168.1.1"
		}
		if len(snapshot.GatewayProxy.LANCIDRs) == 0 {
			snapshot.GatewayProxy.LANCIDRs = []string{"192.168.1.0/24"}
		}
		if len(snapshot.LocalIPProxy.PublishedCIDRs) == 0 {
			snapshot.LocalIPProxy.PublishedCIDRs = []string{"192.168.1.0/24"}
		}
		return snapshot, nil
	}
	probeLinuxRouterPlatformFailOpen = func(probeLinuxRouterSnapshot) error { return nil }
	probeLinuxRouterPlatformCleanup = func(*probeLinuxRouterSnapshot) error { return nil }
	probeLinuxRouterPlatformRestoreInterfaceAuto = func(interfaceName string) (string, bool, error) {
		if interfaceName == "auto" {
			interfaceName = "eth0"
		}
		return interfaceName, true, nil
	}
	t.Cleanup(func() {
		resetProbeLocalAuthManagerForTest()
		resetProbeLocalSetupTokenForTest()
		probeLinuxRouterRuntimeState.mu.Lock()
		probeLinuxRouterRuntimeState.nodeID = oldNodeID
		probeLinuxRouterRuntimeState.desired = oldDesired
		probeLinuxRouterRuntimeState.report = oldReport
		probeLinuxRouterRuntimeState.applied = oldApplied
		probeLinuxRouterRuntimeState.manualFailOpen = oldManualFailOpen
		probeLinuxRouterRuntimeState.mu.Unlock()
		probeLinuxRouterPlatformApply = oldApply
		probeLinuxRouterPlatformResolve = oldResolve
		probeLinuxRouterPlatformFailOpen = oldFailOpen
		probeLinuxRouterPlatformCleanup = oldCleanup
		probeLinuxRouterPlatformRestoreInterfaceAuto = oldRestoreInterfaceAuto
	})
	return buildProbeLocalConsoleHandler()
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
	register := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/api/auth/register", "192.168.1.150:16032", "192.168.1.20:43210", map[string]any{
		"username": "admin", "password": "secret1234", "confirm_password": "secret1234",
	})
	if register.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", register.Code, register.Body.String())
	}
	login := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/api/auth/login", "192.168.1.150:16032", "192.168.1.20:43210", map[string]any{
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
	bootstrap := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/api/auth/bootstrap", "192.168.1.150:16032", "192.168.1.20:43210", nil)
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
	register := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/api/auth/register", "192.168.1.150:16032", "192.168.1.20:43210", map[string]any{
		"username": "admin", "password": "secret1234", "confirm_password": "secret1234",
	})
	if register.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", register.Code, register.Body.String())
	}
	repeated := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/api/auth/register", "192.168.1.150:16032", "192.168.1.20:43210", map[string]any{
		"username": "other", "password": "otherpass123", "confirm_password": "otherpass123",
	})
	if repeated.Code != http.StatusForbidden {
		t.Fatalf("repeated register status=%d body=%s", repeated.Code, repeated.Body.String())
	}
	if strings.Contains(probeLinuxRouterWebPageHTML, "setupToken") || strings.Contains(probeLinuxRouterWebPageHTML, "初始化令牌") {
		t.Fatal("router page still exposes the setup token field")
	}
}

func TestProbeLinuxRouterUsesSharedLocalConsoleDefaults(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	t.Setenv("PROBE_LOCAL_LISTEN", "")
	if got := resolveProbeLocalListenAddr(""); got != "0.0.0.0:16032" {
		t.Fatalf("listen=%q want=%q", got, "0.0.0.0:16032")
	}
	profile := buildProbeProductProfile()
	if !profile.EnableLocalConsole || !profile.EnableLocalConsoleByDefault {
		t.Fatalf("router must use the shared local console: %+v", profile)
	}
}

func TestProbeLinuxRouterWebOnlyAllowsLANAndVirtualIPClientsAndHosts(t *testing.T) {
	handler := setupProbeLinuxRouterWebTest(t)
	cookie := registerAndLoginProbeLinuxRouterWeb(t, handler)
	allowed := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/router", "192.168.1.150:16032", "192.168.1.20:43210", nil, cookie)
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), "CloudHelper 旁路由") {
		t.Fatalf("private request status=%d body=%s", allowed.Code, allowed.Body.String())
	}
	virtual := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/router", "198.18.0.15:16032", "198.18.0.7:43210", nil, cookie)
	if virtual.Code != http.StatusOK || !strings.Contains(virtual.Body.String(), "CloudHelper 旁路由") {
		t.Fatalf("virtual request status=%d body=%s", virtual.Code, virtual.Body.String())
	}
	publicClient := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/router", "192.168.1.150:16032", "8.8.8.8:43210", nil)
	if publicClient.Code != http.StatusForbidden {
		t.Fatalf("public client status=%d", publicClient.Code)
	}
	publicHost := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/router", "8.8.8.8:16032", "192.168.1.20:43210", nil)
	if publicHost.Code != http.StatusForbidden {
		t.Fatalf("public host status=%d", publicHost.Code)
	}
	hostname := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/router", "router.local:16032", "192.168.1.20:43210", nil)
	if hostname.Code != http.StatusForbidden {
		t.Fatalf("hostname status=%d", hostname.Code)
	}
}

func TestProbeLinuxRouterWebIsIntegratedIntoGenericConsole(t *testing.T) {
	handler := setupProbeLinuxRouterWebTest(t)
	cookie := registerAndLoginProbeLinuxRouterWeb(t, handler)
	response := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/shell", "192.168.1.150:16032", "192.168.1.20:43210", nil, cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Shell") {
		t.Fatalf("generic console route status=%d body=%s", response.Code, response.Body.String())
	}
	routerPage := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/router", "192.168.1.150:16032", "192.168.1.20:43210", nil, cookie)
	if routerPage.Code != http.StatusOK || !strings.Contains(routerPage.Body.String(), `class="subtab active"`) {
		t.Fatalf("router tab status=%d body=%s", routerPage.Code, routerPage.Body.String())
	}
	for _, marker := range []string{
		`href="/local/virtual-router#status"`,
		`href="/local/virtual-router#routeStatus"`,
		`href="/local/virtual-router#connections"`,
		`href="/local/virtual-router#dnsRecords"`,
		`>DNS 查询记录</a>`,
		`href="/local/virtual-router#packets"`,
		`href="/local/virtual-router#routeTest"`,
	} {
		if !strings.Contains(routerPage.Body.String(), marker) {
			t.Fatalf("router page missing virtual-router subtab %q", marker)
		}
	}
	virtualRouterPage := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/virtual-router", "192.168.1.150:16032", "192.168.1.20:43210", nil, cookie)
	if virtualRouterPage.Code != http.StatusOK || !strings.Contains(virtualRouterPage.Body.String(), `href="/local/router"`) {
		t.Fatalf("virtual router page missing router subtab: status=%d", virtualRouterPage.Code)
	}
	status := doProbeLinuxRouterWebRequest(t, handler, http.MethodGet, "/local/router/api/status", "192.168.1.150:16032", "192.168.1.20:43210", nil)
	if status.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status endpoint=%d", status.Code)
	}
	upgrade := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/upgrade/check", "192.168.1.150:16032", "192.168.1.20:43210", map[string]any{"mode": "proxy"})
	if upgrade.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated upgrade endpoint=%d", upgrade.Code)
	}
	networkAuto := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/network/auto", "192.168.1.150:16032", "192.168.1.20:43210", map[string]any{"interface": "auto"})
	if networkAuto.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated network auto endpoint=%d", networkAuto.Code)
	}
}

func TestProbeLinuxRouterWebRestoresInterfaceAutomaticAddressing(t *testing.T) {
	handler := setupProbeLinuxRouterWebTest(t)
	cookie := registerAndLoginProbeLinuxRouterWeb(t, handler)
	var restoredInterface string
	probeLinuxRouterPlatformRestoreInterfaceAuto = func(interfaceName string) (string, bool, error) {
		restoredInterface = interfaceName
		return "eth0", true, nil
	}

	response := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/network/auto", "192.168.1.150:16032", "192.168.1.20:43210", map[string]any{"interface": "auto"}, cookie)
	if response.Code != http.StatusOK || restoredInterface != "auto" || !strings.Contains(response.Body.String(), `"interface":"eth0"`) || !strings.Contains(response.Body.String(), `"reconnect_scheduled":true`) {
		t.Fatalf("network auto status=%d restored=%q body=%s", response.Code, restoredInterface, response.Body.String())
	}

	restoredInterface = ""
	invalid := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/network/auto", "192.168.1.150:16032", "192.168.1.20:43210", map[string]any{"interface": "eth0;reboot"}, cookie)
	if invalid.Code != http.StatusBadRequest || restoredInterface != "" {
		t.Fatalf("invalid network auto status=%d restored=%q body=%s", invalid.Code, restoredInterface, invalid.Body.String())
	}
}

func TestProbeLinuxRouterWebConfigAndFailOpenFlow(t *testing.T) {
	handler := setupProbeLinuxRouterWebTest(t)
	cookie := registerAndLoginProbeLinuxRouterWeb(t, handler)
	config := map[string]any{
		"gateway_proxy": map[string]any{
			"enabled": true, "dns_enabled": true, "interface": "eth0", "gateway_address": "192.168.1.150", "upstream_gateway": "192.168.1.1",
			"dns_whitelist_enabled": true, "dns_whitelist_ips": []string{"8.8.8.8", "8.8.8.8"}, "dns_whitelist_domains": []string{"DNS.Google."},
		},
		"local_ip_proxy": map[string]any{
			"enabled": true, "allowed_node_ids": []string{"1", "1"},
		},
	}
	saved := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/config", "192.168.1.150:16032", "192.168.1.20:43210", config, cookie)
	if saved.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", saved.Code, saved.Body.String())
	}
	desired, manualFailOpen, _ := currentProbeLinuxRouterLocalState()
	if desired == nil || desired.GatewayProxy.GatewayAddress != "" || len(desired.GatewayProxy.LANCIDRs) != 0 || !desired.GatewayProxy.DNSWhitelistEnabled || len(desired.GatewayProxy.DNSWhitelistIPs) != 1 || desired.GatewayProxy.DNSWhitelistIPs[0] != "8.8.8.8" || len(desired.GatewayProxy.DNSWhitelistDomains) != 1 || desired.GatewayProxy.DNSWhitelistDomains[0] != "dns.google" || !desired.LocalIPProxy.Enabled || len(desired.LocalIPProxy.PublishedCIDRs) != 0 || len(desired.LocalIPProxy.AllowedNodeIDs) != 1 || manualFailOpen {
		t.Fatalf("unexpected local state: desired=%+v fail_open=%t", desired, manualFailOpen)
	}
	if _, err := os.Stat(resolveProbeLinuxRouterConfigPathForTest(t)); err != nil {
		t.Fatalf("saved config missing: %v", err)
	}

	failOpen := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/fail-open", "192.168.1.150:16032", "192.168.1.20:43210", map[string]any{}, cookie)
	if failOpen.Code != http.StatusOK {
		t.Fatalf("fail-open status=%d body=%s", failOpen.Code, failOpen.Body.String())
	}
	_, manualFailOpen, _ = currentProbeLinuxRouterLocalState()
	if !manualFailOpen {
		t.Fatal("manual fail-open was not enabled")
	}
	resume := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/resume", "192.168.1.150:16032", "192.168.1.20:43210", map[string]any{}, cookie)
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
	response := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/config", "192.168.1.150:16032", "192.168.1.20:43210", config, cookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid config status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProbeLinuxRouterWebUsesLocalConfigAndShowsConnections(t *testing.T) {
	for _, marker := range []string{"本地配置", `<select id="interfaceName">`, `id="restoreAutoIPBtn"`, "恢复自动获取 IP", "/local/router/api/network/auto", "IP 地址也可能变化", `id="gatewayAddress" type="text" readonly`, "LAN IP（自动）", "上游网关（可选）", "默认使用上级下发网关", "syncGatewayAddressPreview", `id="runtimeUpstreamGateway"`, `id="dnsWhitelistEnabled"`, `id="dnsWhitelistIPs"`, `id="dnsWhitelistDomains"`, "dns_whitelist_enabled", "dns_whitelist_ips", "dns_whitelist_domains", `id="availableNodeID"`, `id="addAllowedNodeBtn"`, `id="allowedNodeList"`, `id="connectionRows"`, `id="upgradeBtn"`, "/local/router/api/upgrade/check", `href="/local/virtual-router"`, `class="subtab active"`, "gateway_proxy", "local_ip_proxy", "let configDirty = false", "statusRequestSequence", "allowedNodeSelection", "有未保存的更改", "配置已保存并应用", "beforeunload"} {
		if !strings.Contains(probeLinuxRouterWebPageHTML, marker) {
			t.Fatalf("router page missing %q", marker)
		}
	}
	for _, forbidden := range []string{"本地临时配置", "主控配置", "local_override", `id="authView"`, `id="registerForm"`, `id="loginForm"`, "0.0.0.0:18080", `id="lanCIDRs"`, `id="publishedCIDRs"`, `id="allowedNodeIDs"`} {
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

	check := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/upgrade/check", "192.168.1.150:16032", "192.168.1.20:43210", map[string]any{
		"mode": "proxy", "release_repo": "fengzhanhuaer/CloudHelper",
	}, cookie)
	if check.Code != http.StatusOK || !strings.Contains(check.Body.String(), `"upgradeable":true`) || !strings.Contains(check.Body.String(), "cloudhelper-probe-router-") {
		t.Fatalf("upgrade check status=%d body=%s", check.Code, check.Body.String())
	}
	upgrade := doProbeLinuxRouterWebRequest(t, handler, http.MethodPost, "/local/router/api/upgrade", "192.168.1.150:16032", "192.168.1.20:43210", map[string]any{
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
