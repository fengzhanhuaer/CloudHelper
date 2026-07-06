package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func setupProbeLocalConsoleTest(t *testing.T) *http.ServeMux {
	t.Helper()
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalAuthManagerForTest()
	resetProbeLocalControlStateForTest()
	resetProbeLocalDNSServiceForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	setprobeLocalRouteRuntimeContext(nodeIdentity{}, "")
	probeLocalTUNLinkFeatureEnabled = func() bool { return true }
	probeLocalFlushSystemDNSCache = func() error { return nil }
	t.Cleanup(func() {
		resetProbeLocalAuthManagerForTest()
		resetProbeLocalControlStateForTest()
		resetProbeLocalDNSServiceForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
		resetprobeLocalRouteHooksForTest()
		resetProbeLocalTUNHooksForTest()
		resetProbeLocalUpgradeHooksForTest()
		setprobeLocalRouteRuntimeContext(nodeIdentity{}, "")
		probeLocalTUNLinkFeatureEnabled = func() bool { return false }
	})
	return buildProbeLocalConsoleMux()
}

func doProbeLocalRequest(t *testing.T, mux *http.ServeMux, method, path string, payload any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload failed: %v", err)
		}
		body = raw
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func decodeProbeLocalJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	payload := map[string]any{}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json failed: %v body=%q", err, rr.Body.String())
	}
	return payload
}

func extractCookieByName(rr *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range rr.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func registerAndLoginProbeLocal(t *testing.T, mux *http.ServeMux, username, password string) *http.Cookie {
	t.Helper()
	registerResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/register", map[string]any{
		"username":         username,
		"password":         password,
		"confirm_password": password,
	})
	if registerResp.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", registerResp.Code, registerResp.Body.String())
	}
	loginResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/login", map[string]any{
		"username": username,
		"password": password,
	})
	if loginResp.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResp.Code, loginResp.Body.String())
	}
	cookie := extractCookieByName(loginResp, probeLocalSessionCookieName)
	if cookie == nil || cookie.Value == "" {
		t.Fatalf("missing session cookie from login response")
	}
	return cookie
}

func TestProbeLocalAuthFlowRegisterOnceAndSession(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)

	bootstrapResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/auth/bootstrap", nil)
	if bootstrapResp.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d", bootstrapResp.Code)
	}
	bootstrapPayload := decodeProbeLocalJSON(t, bootstrapResp)
	registered, ok := bootstrapPayload["registered"].(bool)
	if !ok || registered {
		t.Fatalf("bootstrap registered=%v ok=%v", bootstrapPayload["registered"], ok)
	}

	registerResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/register", map[string]any{
		"username":         "admin",
		"password":         "secret1234",
		"confirm_password": "secret1234",
	})
	if registerResp.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", registerResp.Code, registerResp.Body.String())
	}

	repeatedRegisterResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/register", map[string]any{
		"username":         "admin2",
		"password":         "secret1234",
		"confirm_password": "secret1234",
	})
	if repeatedRegisterResp.Code != http.StatusForbidden {
		t.Fatalf("repeated register status=%d body=%s", repeatedRegisterResp.Code, repeatedRegisterResp.Body.String())
	}

	loginResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/login", map[string]any{
		"username": "admin",
		"password": "secret1234",
	})
	if loginResp.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResp.Code, loginResp.Body.String())
	}
	sessionCookie := extractCookieByName(loginResp, probeLocalSessionCookieName)
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatalf("expected session cookie in login response")
	}

	sessionResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/auth/session", nil, sessionCookie)
	if sessionResp.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s", sessionResp.Code, sessionResp.Body.String())
	}
	sessionPayload := decodeProbeLocalJSON(t, sessionResp)
	authenticated, ok := sessionPayload["authenticated"].(bool)
	if !ok || !authenticated {
		t.Fatalf("session authenticated=%v ok=%v", sessionPayload["authenticated"], ok)
	}
	if sessionPayload["username"] != "admin" {
		t.Fatalf("session username=%v", sessionPayload["username"])
	}
	if sessionPayload["version"] != BuildVersion {
		t.Fatalf("session version=%v want=%s", sessionPayload["version"], BuildVersion)
	}

	logoutResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/logout", map[string]any{}, sessionCookie)
	if logoutResp.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", logoutResp.Code, logoutResp.Body.String())
	}

	afterLogoutSessionResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/auth/session", nil, sessionCookie)
	if afterLogoutSessionResp.Code != http.StatusUnauthorized {
		t.Fatalf("session-after-logout status=%d body=%s", afterLogoutSessionResp.Code, afterLogoutSessionResp.Body.String())
	}
}

func TestProbeLocalProtectedRoutesRequireSession(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)

	tunStatusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/tun/status", nil)
	if tunStatusResp.Code != http.StatusUnauthorized {
		t.Fatalf("tun/status without session status=%d", tunStatusResp.Code)
	}

	removedPaths := []string{
		"/local/api/dns/status",
		"/local/api/dns/records",
		"/local/api/dns/real_ip/list",
		"/local/api/dns/real_ip/lookup?domain=api.example.com",
		"/local/api/dns/fake_ip/list",
		"/local/api/dns/fake_ip/lookup?ip=198.18.0.9",
		"/local/api/proxy/status",
		"/local/api/proxy/monitor",
	}
	for _, path := range removedPaths {
		resp := doProbeLocalRequest(t, mux, http.MethodGet, path, nil)
		if resp.Code != http.StatusNotFound {
			t.Fatalf("%s removed route status=%d", path, resp.Code)
		}
	}

	logsResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/logs", nil)
	if logsResp.Code != http.StatusUnauthorized {
		t.Fatalf("logs without session status=%d", logsResp.Code)
	}

	upgradeStatusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/system/upgrade/status", nil)
	if upgradeStatusResp.Code != http.StatusUnauthorized {
		t.Fatalf("system/upgrade/status without session status=%d", upgradeStatusResp.Code)
	}

	protectedPagePaths := []string{"/local/panel", "/local/logs", "/local/system", "/local/sync"}
	for _, path := range protectedPagePaths {
		pageResp := doProbeLocalRequest(t, mux, http.MethodGet, path, nil)
		if pageResp.Code != http.StatusFound {
			t.Fatalf("%s without session status=%d", path, pageResp.Code)
		}
		if location := pageResp.Header().Get("Location"); location != "/local/login" {
			t.Fatalf("%s redirect location=%q", path, location)
		}
	}
	removedPagePaths := []string{"/local/proxy", "/local/dns", "/local/monitor"}
	for _, path := range removedPagePaths {
		pageResp := doProbeLocalRequest(t, mux, http.MethodGet, path, nil)
		if pageResp.Code != http.StatusNotFound {
			t.Fatalf("%s removed page status=%d", path, pageResp.Code)
		}
	}
}

func TestResolveProbeLocalDNSUpstreamBypassTarget(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	if err := persistProbeLocalHostMappings([]probeLocalHostMapping{
		{DNS: "dns.alidns.com", IP: "223.5.5.5"},
		{DNS: "dns.google", IP: "8.8.4.4"},
	}); err != nil {
		t.Fatalf("persist host mappings failed: %v", err)
	}
	storeProbeLocalDNSCacheRecords("cached.example", []string{"9.9.9.9"})
	oldBootstrap := probeLocalDNSBootstrapLookupIPv4
	probeLocalDNSBootstrapLookupIPv4 = func(string) ([]string, error) {
		return nil, errors.New("unexpected bootstrap lookup")
	}
	t.Cleanup(func() {
		probeLocalDNSBootstrapLookupIPv4 = oldBootstrap
		resetProbeLocalDNSServiceForTest()
	})

	tests := []struct {
		name      string
		kind      string
		address   string
		want      string
		wantFound bool
	}{
		{name: "dns ipv4", kind: "dns", address: "119.29.29.29", want: "119.29.29.29:53", wantFound: true},
		{name: "dns domain static host", kind: "dns", address: "dns.alidns.com:53", want: "223.5.5.5:53", wantFound: true},
		{name: "dot ipv4", kind: "dot", address: "1.1.1.1:853", want: "1.1.1.1:853", wantFound: true},
		{name: "dot domain static host", kind: "dot", address: "dns.alidns.com:853", want: "223.5.5.5:853", wantFound: true},
		{name: "doh ipv4 https", kind: "doh", address: "https://1.1.1.1/dns-query", want: "1.1.1.1:443", wantFound: true},
		{name: "doh ipv4 http", kind: "doh", address: "http://8.8.8.8/dns-query", want: "8.8.8.8:80", wantFound: true},
		{name: "doh domain static host", kind: "doh", address: "https://dns.google/dns-query", want: "8.8.4.4:443", wantFound: true},
		{name: "doh domain cached host", kind: "doh", address: "https://cached.example/dns-query", want: "9.9.9.9:443", wantFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolveProbeLocalDNSUpstreamBypassTarget(tt.kind, tt.address)
			if ok != tt.wantFound {
				t.Fatalf("found=%v want=%v target=%q", ok, tt.wantFound, got)
			}
			if got != tt.want {
				t.Fatalf("target=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestCurrentProbeLocalDNSUpstreamCandidatesAppendsSystemDNSLast(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	oldSystemDNS := probeLocalDNSSystemServers
	probeLocalDNSSystemServers = func() []string { return []string{"192.168.1.1", "8.8.8.8"} }
	t.Cleanup(func() {
		probeLocalDNSSystemServers = oldSystemDNS
		resetProbeLocalDNSServiceForTest()
	})

	candidates := currentProbeLocalDNSUpstreamCandidatesForDecision(probeLocalDNSRouteDecision{})
	got := make([]string, 0, len(candidates))
	for _, item := range candidates {
		got = append(got, item.Kind+"|"+item.Address)
	}
	want := []string{
		"doh|https://dns.alidns.com/dns-query",
		"doh|https://doh.pub/dns-query",
		"dot|dns.alidns.com:853",
		"dot|dot.pub:853",
		"dns|223.5.5.5:53",
		"dns|119.29.29.29:53",
		"dns|192.168.1.1:53",
		"dns|8.8.8.8:53",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("candidates=\n%v\nwant=\n%v", got, want)
	}
}

func TestProbeLocalDNSStartupLoadsCacheThenHostMapping(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	storeProbeLocalDNSCacheRecords("static.example.com", []string{"203.0.113.20"})
	flushProbeLocalDNSCacheToDisk()
	resetProbeLocalDNSServiceForTest()
	if err := persistProbeLocalHostMappings([]probeLocalHostMapping{
		{DNS: "static.example.com", IP: "203.0.113.10"},
	}); err != nil {
		t.Fatalf("persist host mappings failed: %v", err)
	}
	packet, err := buildProbeLocalDNSQueryA("static.example.com")
	if err != nil {
		t.Fatalf("build dns query failed: %v", err)
	}
	response, domain, ips, _, err := resolveProbeLocalDNSResponse(packet)
	if err != nil {
		t.Fatalf("resolveProbeLocalDNSResponse returned error: %v", err)
	}
	if domain != "static.example.com" {
		t.Fatalf("domain=%q", domain)
	}
	if strings.Join(ips, ",") != "203.0.113.10" {
		t.Fatalf("ips=%v", ips)
	}
	if got := strings.Join(extractProbeLocalDNSResponseIPsBestEffort(response), ","); got != "203.0.113.10" {
		t.Fatalf("response ips=%q", got)
	}
}

func TestResolveProbeLocalDNSUpstreamHostIPv4CachesBootstrapResult(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	oldBootstrap := probeLocalDNSBootstrapLookupIPv4
	lookupCalls := 0
	probeLocalDNSBootstrapLookupIPv4 = func(host string) ([]string, error) {
		lookupCalls++
		if host != "bootstrap.example" {
			return nil, fmt.Errorf("unexpected bootstrap host: %s", host)
		}
		return []string{"203.0.113.20"}, nil
	}
	t.Cleanup(func() {
		probeLocalDNSBootstrapLookupIPv4 = oldBootstrap
		resetProbeLocalDNSServiceForTest()
	})

	target1, ok1 := resolveProbeLocalDNSUpstreamBypassTarget("doh", "https://bootstrap.example/dns-query")
	target2, ok2 := resolveProbeLocalDNSUpstreamBypassTarget("doh", "https://bootstrap.example/dns-query")
	if !ok1 || !ok2 {
		t.Fatalf("targets not resolved: ok1=%v ok2=%v target1=%q target2=%q", ok1, ok2, target1, target2)
	}
	if target1 != "203.0.113.20:443" || target2 != "203.0.113.20:443" {
		t.Fatalf("unexpected targets: target1=%q target2=%q", target1, target2)
	}
	if lookupCalls != 1 {
		t.Fatalf("bootstrap lookupCalls=%d want=1", lookupCalls)
	}
	if got := strings.Join(lookupProbeLocalDNSCacheIPv4ByDomain("bootstrap.example"), ","); got != "203.0.113.20" {
		t.Fatalf("cached bootstrap ips=%q", got)
	}
}

func TestProbeLocalDNSCachePersistsToDiskAndReloads(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	storeProbeLocalDNSCacheRecords("persist.example", []string{"203.0.113.30"})
	flushProbeLocalDNSCacheToDisk()

	resetProbeLocalDNSServiceForTest()

	if got := strings.Join(lookupProbeLocalDNSCacheIPv4ByDomain("persist.example"), ","); got != "203.0.113.30" {
		t.Fatalf("reloaded cache ips=%q", got)
	}

	packet, err := buildProbeLocalDNSQueryA("persist.example")
	if err != nil {
		t.Fatalf("build dns query failed: %v", err)
	}
	response, domain, ips, _, err := resolveProbeLocalDNSResponse(packet)
	if err != nil {
		t.Fatalf("resolveProbeLocalDNSResponse returned error: %v", err)
	}
	if domain != "persist.example" {
		t.Fatalf("domain=%q", domain)
	}
	if strings.Join(ips, ",") != "203.0.113.30" {
		t.Fatalf("ips=%v", ips)
	}
	if got := strings.Join(extractProbeLocalDNSResponseIPsBestEffort(response), ","); got != "203.0.113.30" {
		t.Fatalf("response ips=%q", got)
	}
}

func TestProbeLocalTUNResetAndUninstallHandlers(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.tun.Enabled = true
	probeLocalControl.mu.Unlock()

	restoreDNSCalls := 0
	uninstallCalls := 0
	probeLocalRestoreTUNPrimaryDNS = func() error {
		restoreDNSCalls++
		return nil
	}
	probeLocalUninstallTUNDriver = func() error {
		uninstallCalls++
		return nil
	}
	probeLocalDetectTUNInstalled = func() (bool, error) { return true, nil }
	t.Cleanup(func() { resetprobeLocalRouteHooksForTest(); resetProbeLocalTUNHooksForTest() })

	resetResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/tun/reset", map[string]any{}, sessionCookie)
	if resetResp.Code != http.StatusOK {
		t.Fatalf("tun/reset status=%d body=%s", resetResp.Code, resetResp.Body.String())
	}
	resetPayload := decodeProbeLocalJSON(t, resetResp)
	resetTun, ok := resetPayload["tun"].(map[string]any)
	if !ok {
		t.Fatalf("tun/reset tun payload type=%T", resetPayload["tun"])
	}
	if installed, _ := resetTun["installed"].(bool); !installed {
		t.Fatalf("tun/reset should keep detected installed state")
	}
	if enabled, _ := resetTun["enabled"].(bool); enabled {
		t.Fatalf("tun/reset enabled should be false")
	}
	if restoreDNSCalls != 0 || uninstallCalls != 0 {
		t.Fatalf("after reset restoreDNS=%d uninstall=%d", restoreDNSCalls, uninstallCalls)
	}

	uninstallResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/tun/uninstall", map[string]any{}, sessionCookie)
	if uninstallResp.Code != http.StatusOK {
		t.Fatalf("tun/uninstall status=%d body=%s", uninstallResp.Code, uninstallResp.Body.String())
	}
	uninstallPayload := decodeProbeLocalJSON(t, uninstallResp)
	uninstallTun, ok := uninstallPayload["tun"].(map[string]any)
	if !ok {
		t.Fatalf("tun/uninstall tun payload type=%T", uninstallPayload["tun"])
	}
	if installed, _ := uninstallTun["installed"].(bool); installed {
		t.Fatalf("tun/uninstall installed should be false")
	}
	if restoreDNSCalls != 0 || uninstallCalls != 1 {
		t.Fatalf("after uninstall restoreDNS=%d uninstall=%d", restoreDNSCalls, uninstallCalls)
	}
}

func TestProbeLocalSystemUpgradeDirectAccepted(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	upgradeCmdCh := make(chan probeControlMessage, 1)
	identityCh := make(chan nodeIdentity, 1)
	probeLocalRunUpgrade = func(cmd probeControlMessage, identity nodeIdentity) {
		upgradeCmdCh <- cmd
		identityCh <- identity
	}
	t.Cleanup(func() {
		resetProbeLocalUpgradeHooksForTest()
		setprobeLocalRouteRuntimeContext(nodeIdentity{}, "")
	})
	setprobeLocalRouteRuntimeContext(nodeIdentity{NodeID: "node-upgrade-direct", Secret: "secret-direct"}, "")

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/upgrade", map[string]any{
		"mode":         "direct",
		"release_repo": "  fengzhanhuaer/CloudHelper  ",
	}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("system/upgrade direct status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	if payload["mode"] != "direct" {
		t.Fatalf("system/upgrade direct mode=%v", payload["mode"])
	}
	if payload["release_repo"] != "fengzhanhuaer/CloudHelper" {
		t.Fatalf("system/upgrade direct release_repo=%v", payload["release_repo"])
	}

	select {
	case cmd := <-upgradeCmdCh:
		if cmd.Type != "upgrade" {
			t.Fatalf("upgrade cmd type=%q", cmd.Type)
		}
		if cmd.Mode != "direct" {
			t.Fatalf("upgrade cmd mode=%q", cmd.Mode)
		}
		if cmd.ReleaseRepo != "fengzhanhuaer/CloudHelper" {
			t.Fatalf("upgrade cmd release_repo=%q", cmd.ReleaseRepo)
		}
		if strings.TrimSpace(cmd.ControllerBaseURL) != "" {
			t.Fatalf("upgrade cmd controller_base_url should be empty, got=%q", cmd.ControllerBaseURL)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("system/upgrade direct did not trigger upgrade hook")
	}

	select {
	case identity := <-identityCh:
		if identity.NodeID != "node-upgrade-direct" {
			t.Fatalf("upgrade identity node_id=%q", identity.NodeID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("system/upgrade direct did not pass runtime identity")
	}
	upgradeStatusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/system/upgrade/status", nil, sessionCookie)
	if upgradeStatusResp.Code != http.StatusOK {
		t.Fatalf("system/upgrade/status status=%d body=%s", upgradeStatusResp.Code, upgradeStatusResp.Body.String())
	}
	statusPayload := decodeProbeLocalJSON(t, upgradeStatusResp)
	if statusPayload["status"] != "accepted" {
		t.Fatalf("system/upgrade/status status=%v", statusPayload["status"])
	}
	if statusPayload["mode"] != "direct" {
		t.Fatalf("system/upgrade/status mode=%v", statusPayload["mode"])
	}
	if statusPayload["release_repo"] != "fengzhanhuaer/CloudHelper" {
		t.Fatalf("system/upgrade/status release_repo=%v", statusPayload["release_repo"])
	}
}

func TestProbeLocalSystemUpgradeProxyRequiresController(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	t.Cleanup(func() { setprobeLocalRouteRuntimeContext(nodeIdentity{}, "") })
	setprobeLocalRouteRuntimeContext(nodeIdentity{NodeID: "node-upgrade-proxy-empty"}, "")

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/upgrade", map[string]any{
		"mode": "proxy",
	}, sessionCookie)
	if resp.Code != http.StatusConflict {
		t.Fatalf("system/upgrade proxy without controller status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "controller") {
		t.Fatalf("system/upgrade proxy without controller error=%q", errText)
	}
}

func TestProbeLocalSystemUpgradeCheckDirect(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalFetchRelease = func(_ context.Context, mode, repo, controllerBase string, identity nodeIdentity) (releaseInfo, error) {
		if mode != "direct" {
			t.Fatalf("mode=%q", mode)
		}
		if repo != "fengzhanhuaer/CloudHelper" {
			t.Fatalf("repo=%q", repo)
		}
		if strings.TrimSpace(controllerBase) != "" {
			t.Fatalf("controllerBase=%q", controllerBase)
		}
		return releaseInfo{
			Repo:    repo,
			TagName: "v9.9.9",
			Assets:  []releaseAsset{{Name: "cloudhelper-probe-node-" + runtime.GOOS + "-" + runtime.GOARCH + ".zip", DownloadURL: "https://example.com/probe.zip"}},
		}, nil
	}
	t.Cleanup(resetProbeLocalUpgradeHooksForTest)

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/upgrade/check", map[string]any{
		"mode":         "direct",
		"release_repo": "fengzhanhuaer/CloudHelper",
	}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("system/upgrade/check status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	if payload["current_version"] != BuildVersion {
		t.Fatalf("current_version=%v want=%s", payload["current_version"], BuildVersion)
	}
	if payload["latest_version"] != "v9.9.9" {
		t.Fatalf("latest_version=%v", payload["latest_version"])
	}
	if upgradable, _ := payload["upgradeable"].(bool); !upgradable {
		t.Fatalf("upgradeable=%v", payload["upgradeable"])
	}
	if payload["asset_name"] == "" {
		t.Fatalf("asset_name empty payload=%v", payload)
	}
}

func TestProbeLocalSystemUpgradeCheckProxyRequiresController(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	t.Cleanup(func() { setprobeLocalRouteRuntimeContext(nodeIdentity{}, "") })
	setprobeLocalRouteRuntimeContext(nodeIdentity{NodeID: "node-upgrade-check-proxy"}, "")

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/upgrade/check", map[string]any{
		"mode": "proxy",
	}, sessionCookie)
	if resp.Code != http.StatusConflict {
		t.Fatalf("system/upgrade/check proxy without controller status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestProbeLocalSystemRestartAccepted(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	restartCalled := make(chan struct{}, 1)
	probeLocalRestartProcess = func(_ string) error {
		restartCalled <- struct{}{}
		return nil
	}
	t.Cleanup(func() { resetProbeLocalUpgradeHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/restart", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("system/restart status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	accepted, _ := payload["accepted"].(bool)
	if !accepted {
		t.Fatalf("system/restart accepted=%v", payload["accepted"])
	}
	select {
	case <-restartCalled:
	case <-time.After(2 * time.Second):
		t.Fatalf("system/restart did not trigger restart hook")
	}
}

func TestProbeLocalSystemRestartClosesLocalConsoleBeforeRestart(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	addr := reserveProbeLocalListenAddrForTest(t)
	if err := startProbeLocalConsoleServer(http.NewServeMux(), addr); err != nil {
		t.Fatalf("start local console failed: %v", err)
	}
	t.Cleanup(func() { cleanupProbeLocalConsoleServerForTest(t) })

	restartCalled := make(chan struct{}, 1)
	probeLocalRestartProcess = func(_ string) error {
		if got := currentProbeLocalConsoleListen(); got != "" {
			t.Errorf("local console listen still active before restart: %q", got)
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Errorf("local console addr not released before restart: %v", err)
		} else {
			_ = ln.Close()
		}
		restartCalled <- struct{}{}
		return nil
	}
	t.Cleanup(func() { resetProbeLocalUpgradeHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/restart", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("system/restart status=%d body=%s", resp.Code, resp.Body.String())
	}
	select {
	case <-restartCalled:
	case <-time.After(3 * time.Second):
		t.Fatalf("system/restart did not trigger restart hook")
	}
}

func TestProbeLocalSystemUpgradeProxyAccepted(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	upgradeCmdCh := make(chan probeControlMessage, 1)
	probeLocalRunUpgrade = func(cmd probeControlMessage, identity nodeIdentity) {
		upgradeCmdCh <- cmd
	}
	t.Cleanup(func() {
		resetProbeLocalUpgradeHooksForTest()
		setprobeLocalRouteRuntimeContext(nodeIdentity{}, "")
	})
	setprobeLocalRouteRuntimeContext(nodeIdentity{NodeID: "node-upgrade-proxy"}, "  https://controller.example.com/base  ")

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/upgrade", map[string]any{
		"mode": "proxy",
	}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("system/upgrade proxy status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	if payload["mode"] != "proxy" {
		t.Fatalf("system/upgrade proxy mode=%v", payload["mode"])
	}

	select {
	case cmd := <-upgradeCmdCh:
		if cmd.Mode != "proxy" {
			t.Fatalf("upgrade cmd mode=%q", cmd.Mode)
		}
		if cmd.ControllerBaseURL != "https://controller.example.com/base" {
			t.Fatalf("upgrade cmd controller_base_url=%q", cmd.ControllerBaseURL)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("system/upgrade proxy did not trigger upgrade hook")
	}
}

func TestProbeLocalSystemUpgradeRejectsInvalidMode(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/upgrade", map[string]any{
		"mode": "invalid-mode",
	}, sessionCookie)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("system/upgrade invalid mode status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "mode") {
		t.Fatalf("system/upgrade invalid mode error=%q", errText)
	}
}

func TestProbeLocalSystemChainAuthBlacklistSaveAndGet(t *testing.T) {
	resetProbeChainAuthIPStateForTest()
	defer resetProbeChainAuthIPStateForTest()

	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	saveResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/chain_auth_blacklist", map[string]any{
		"content": "203.0.113.10\n\n# comment\n203.0.113.11 extra",
	}, sessionCookie)
	if saveResp.Code != http.StatusOK {
		t.Fatalf("chain_auth_blacklist save status=%d body=%s", saveResp.Code, saveResp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, saveResp)
	if content, _ := payload["content"].(string); content != "203.0.113.10\n203.0.113.11" {
		t.Fatalf("chain_auth_blacklist content=%q", content)
	}
	if blocked, _ := isProbeChainAuthIPBlacklisted("203.0.113.10"); !blocked {
		t.Fatalf("expected saved ip to be blacklisted")
	}

	path, err := resolveProbeChainAuthBlacklistPath()
	if err != nil {
		t.Fatalf("resolve blacklist path: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read blacklist file: %v", err)
	}
	if !strings.Contains(string(raw), "203.0.113.11") {
		t.Fatalf("blacklist file missing saved ip: %s", string(raw))
	}

	getResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/system/chain_auth_blacklist", nil, sessionCookie)
	if getResp.Code != http.StatusOK {
		t.Fatalf("chain_auth_blacklist get status=%d body=%s", getResp.Code, getResp.Body.String())
	}
	getPayload := decodeProbeLocalJSON(t, getResp)
	items, ok := getPayload["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("chain_auth_blacklist items=%T %v", getPayload["items"], getPayload["items"])
	}

	invalidResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/chain_auth_blacklist", map[string]any{
		"content": "not-an-ip",
	}, sessionCookie)
	if invalidResp.Code != http.StatusBadRequest {
		t.Fatalf("invalid blacklist status=%d body=%s", invalidResp.Code, invalidResp.Body.String())
	}
}

func TestProbeLocalTUNInstallReturnsInternalErrorOnFailure(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalInstallTUNDriver = func() error {
		obs := newProbeLocalTUNInstallObservation()
		obs.Driver.PackageExists = true
		obs.Driver.PackagePath = `C:\\temp\\wintun.dll`
		obs.Create.Called = true
		obs.Create.HandleNonZero = false
		obs.Create.RawError = "create/open wintun adapter: access denied"
		obs.Visibility.DetectVisible = false
		obs.Final.Success = false
		obs.Final.ReasonCode = probeLocalTUNInstallCodeAdapterCreateFailed
		obs.Final.Reason = "Wintun 适配器创建失败"
		obs.Diagnostic.Code = probeLocalTUNInstallCodeAdapterCreateFailed
		obs.Diagnostic.RawError = "create/open wintun adapter: access denied"
		return newProbeLocalTUNInstallError(
			probeLocalTUNInstallCodeAdapterCreateFailed,
			"create_or_open_adapter",
			"Wintun 适配器创建失败，请检查管理员权限与驱动状态",
			errors.New("tun install failed for test"),
			[]string{"create_or_open_adapter: failed"},
			obs,
		)
	}
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/tun/install", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("tun/install status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(errText, "tun install failed for test") {
		t.Fatalf("tun/install error=%q", errText)
	}
	codeText, _ := payload["code"].(string)
	if codeText != probeLocalTUNInstallCodeAdapterCreateFailed {
		t.Fatalf("tun/install payload code=%q", codeText)
	}
	stageText, _ := payload["stage"].(string)
	if stageText != "create_or_open_adapter" {
		t.Fatalf("tun/install payload stage=%q", stageText)
	}
	hintText, _ := payload["hint"].(string)
	if !strings.Contains(hintText, "Wintun") {
		t.Fatalf("tun/install payload hint=%q", hintText)
	}
	observation, ok := payload["install_observation"].(map[string]any)
	if !ok {
		t.Fatalf("tun/install failure observation type=%T", payload["install_observation"])
	}
	finalObj, _ := observation["final"].(map[string]any)
	if finalObj["reason_code"] != probeLocalTUNInstallCodeAdapterCreateFailed {
		t.Fatalf("failure observation reason_code=%v", finalObj["reason_code"])
	}
	diagnosticObj, _ := observation["diagnostic"].(map[string]any)
	rawErr, _ := diagnosticObj["raw_error"].(string)
	if !strings.Contains(strings.ToLower(rawErr), "access denied") {
		t.Fatalf("failure observation diagnostic.raw_error=%q", rawErr)
	}
}

func TestProbeLocalTUNInstallReturnsSuccessNotReadyOnJointVisibilityMissing(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalInstallTUNDriver = func() error {
		obs := newProbeLocalTUNInstallObservation()
		obs.Driver.PackageExists = true
		obs.Driver.PackagePath = `C:\\temp\\wintun.dll`
		obs.Create.Called = true
		obs.Create.HandleNonZero = true
		obs.Visibility.DetectVisible = false
		obs.Visibility.IfIndexResolved = true
		obs.Visibility.IfIndexValue = 9
		obs.Final.Success = true
		obs.Final.ReasonCode = probeLocalTUNInstallCodeAdapterJointVisibilityMiss
		obs.Final.Reason = "LUID 路径冲突后重建仍未满足 present PnP + NetAdapter 联合可见"
		obs.Diagnostic.Code = probeLocalTUNInstallCodeAdapterJointVisibilityMiss
		obs.Diagnostic.Stage = "verify_adapter"
		obs.Diagnostic.Hint = "LUID 路径冲突后重建仍未满足 present PnP + NetAdapter 联合可见"
		obs.Diagnostic.RawError = "fallback fresh create still joint visibility missing: joint visibility still missing"
		obs.Diagnostic.Details = obs.Diagnostic.RawError
		setProbeLocalTUNInstallObservation(obs)
		return nil
	}
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/tun/install", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("tun/install status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	observation, ok := payload["install_observation"].(map[string]any)
	if !ok {
		t.Fatalf("tun/install success-not-ready observation type=%T", payload["install_observation"])
	}
	finalObj, _ := observation["final"].(map[string]any)
	if success, _ := finalObj["success"].(bool); !success {
		t.Fatalf("success-not-ready final.success=%v", finalObj["success"])
	}
	if reasonCode, _ := finalObj["reason_code"].(string); reasonCode != probeLocalTUNInstallCodeAdapterJointVisibilityMiss {
		t.Fatalf("success-not-ready final.reason_code=%q", reasonCode)
	}
	diagnosticObj, _ := observation["diagnostic"].(map[string]any)
	if stage, _ := diagnosticObj["stage"].(string); stage != "verify_adapter" {
		t.Fatalf("success-not-ready diagnostic.stage=%q", stage)
	}
	if hint, _ := diagnosticObj["hint"].(string); !strings.Contains(hint, "联合可见") {
		t.Fatalf("success-not-ready diagnostic.hint=%q", hint)
	}
}

func TestProbeLocalTUNInstallReturnsNotImplementedOnUnsupported(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalInstallTUNDriver = func() error {
		return errProbeLocalTUNUnsupported
	}
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/tun/install", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("tun/install unsupported status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "not supported") {
		t.Fatalf("tun/install unsupported error=%q", errText)
	}
}

func TestProbeLocalLogsEndpointWithFilters(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	_, _ = probeLogStore.Write([]byte("2026/04/26 15:21:02 [normal] panel logs smoke info\n"))
	_, _ = probeLogStore.Write([]byte("2026/04/26 15:21:02 [warning] panel logs smoke warning\n"))
	_, _ = probeLogStore.Write([]byte("2026/04/26 15:21:02 [error] panel logs smoke error\n"))

	resp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/logs?lines=50&min_level=warning&keyword=smoke", nil, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("logs endpoint status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	if okValue, ok := payload["ok"].(bool); !ok || !okValue {
		t.Fatalf("logs endpoint ok=%v raw=%v", okValue, payload["ok"])
	}
	if payload["source"] != probeLogSourceName {
		t.Fatalf("logs source=%v", payload["source"])
	}
	if payload["keyword"] != "smoke" {
		t.Fatalf("logs keyword=%v", payload["keyword"])
	}
	entries, ok := payload["entries"].([]any)
	if !ok {
		t.Fatalf("logs entries type=%T", payload["entries"])
	}
	if len(entries) < 2 {
		t.Fatalf("logs entries should include warning/error, got=%d payload=%v", len(entries), payload)
	}
	content, _ := payload["content"].(string)
	if !strings.Contains(strings.ToLower(content), "warning") || !strings.Contains(strings.ToLower(content), "error") {
		t.Fatalf("logs content should contain warning and error lines: %q", content)
	}
}

func TestProbeLocalTUNInstallSuccessUpdatesState(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalInstallTUNDriver = func() error {
		obs := newProbeLocalTUNInstallObservation()
		obs.Driver.PackageExists = true
		obs.Driver.PackagePath = `C:\\temp\\wintun.dll`
		obs.Create.Called = true
		obs.Create.HandleNonZero = true
		obs.Create.RawError = ""
		obs.Visibility.DetectVisible = true
		obs.Visibility.IfIndexResolved = true
		obs.Visibility.IfIndexValue = 7
		obs.Final.Success = true
		obs.Final.ReasonCode = "TUN_INSTALL_SUCCEEDED"
		obs.Final.Reason = "创建后检测到 TUN 适配器可见"
		setProbeLocalTUNInstallObservation(obs)
		return nil
	}
	probeLocalCheckTUNReadyAfterInstall = func() error { return nil }
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/tun/install", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("tun/install success status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	tunObj, ok := payload["tun"].(map[string]any)
	if !ok {
		t.Fatalf("tun/install payload type=%T", payload["tun"])
	}
	if installed, _ := tunObj["installed"].(bool); !installed {
		t.Fatalf("tun/install installed should be true")
	}
	observation, ok := payload["install_observation"].(map[string]any)
	if !ok {
		t.Fatalf("tun/install success observation type=%T", payload["install_observation"])
	}
	driverObj, _ := observation["driver"].(map[string]any)
	if pkgExists, _ := driverObj["package_exists"].(bool); !pkgExists {
		t.Fatalf("success observation driver.package_exists=%v", driverObj["package_exists"])
	}
	createObj, _ := observation["create"].(map[string]any)
	if called, _ := createObj["called"].(bool); !called {
		t.Fatalf("success observation create.called=%v", createObj["called"])
	}
	visibilityObj, _ := observation["visibility"].(map[string]any)
	if visible, _ := visibilityObj["detect_visible"].(bool); !visible {
		t.Fatalf("success observation visibility.detect_visible=%v", visibilityObj["detect_visible"])
	}
	finalObj, _ := observation["final"].(map[string]any)
	if success, _ := finalObj["success"].(bool); !success {
		t.Fatalf("success observation final.success=%v", finalObj["success"])
	}

	state, err := loadProbeLocalTUNStateFile()
	if err != nil {
		t.Fatalf("load tun state failed: %v", err)
	}
	if !state.TUN.Installed {
		t.Fatalf("persisted tun installed=%v, want true", state.TUN.Installed)
	}

	statusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/tun/status", nil, sessionCookie)
	if statusResp.Code != http.StatusOK {
		t.Fatalf("tun/status status=%d body=%s", statusResp.Code, statusResp.Body.String())
	}
	statusPayload := decodeProbeLocalJSON(t, statusResp)
	if _, exists := statusPayload["install_observation"]; exists {
		t.Fatalf("tun/status should not expose install_observation")
	}
	lastObs, ok := statusPayload["last_install_observation"].(map[string]any)
	if !ok {
		t.Fatalf("tun/status last_install_observation type=%T", statusPayload["last_install_observation"])
	}
	lastFinal, _ := lastObs["final"].(map[string]any)
	if success, _ := lastFinal["success"].(bool); !success {
		t.Fatalf("tun/status last_install_observation.final.success=%v", lastFinal["success"])
	}
}

func TestProbeLocalTUNStatusDoesNotBlockWhenControlBusy(t *testing.T) {
	_ = setupProbeLocalConsoleTest(t)
	if err := persistProbeLocalTUNPersistentState(true, true); err != nil {
		t.Fatalf("persist tun state failed: %v", err)
	}

	probeLocalControl.mu.Lock()
	defer probeLocalControl.mu.Unlock()

	done := make(chan probeLocalTunRuntimeState, 1)
	go func() {
		done <- probeLocalControl.tunStatus()
	}()

	select {
	case status := <-done:
		if status.RecoveryStatus != "running" {
			t.Fatalf("recovery_status=%q, want running", status.RecoveryStatus)
		}
		if status.RecoveryLastError != "tun control is busy" {
			t.Fatalf("recovery_last_error=%q, want busy", status.RecoveryLastError)
		}
		if status.LastError != "" {
			t.Fatalf("last_error=%q, want empty", status.LastError)
		}
		if !status.Installed || !status.Enabled {
			t.Fatalf("status installed/enabled=%v/%v, want true/true", status.Installed, status.Enabled)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("tunStatus blocked while control lock was held")
	}
}

func TestProbeLocalTUNInstallDoesNotStartDataPlaneWhenProxyDirect(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "22")

	probeLocalInstallTUNDriver = func() error {
		obs := newProbeLocalTUNInstallObservation()
		obs.Final.Success = true
		obs.Final.ReasonCode = "TUN_INSTALL_SUCCEEDED"
		obs.Final.Reason = "driver-ready"
		setProbeLocalTUNInstallObservation(obs)
		return nil
	}
	probeLocalCheckTUNReadyAfterInstall = func() error { return nil }
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/tun/install", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("tun/install status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	tunObj, ok := payload["tun"].(map[string]any)
	if !ok {
		t.Fatalf("tun/install tun payload type=%T", payload["tun"])
	}
	if enabled, _ := tunObj["enabled"].(bool); !enabled {
		t.Fatalf("tun/install enabled=%v, want true for adapter switch", enabled)
	}
	if dataPlane, _ := tunObj["data_plane"].(bool); dataPlane {
		t.Fatalf("tun/install data_plane=%v, want false without proxy enable", dataPlane)
	}
	state, err := loadProbeLocalTUNStateFile()
	if err != nil {
		t.Fatalf("load tun state failed: %v", err)
	}
	if !state.TUN.Installed || !state.TUN.Enabled {
		t.Fatalf("persisted tun state=%+v, want installed=true enabled=true", state.TUN)
	}
}

func TestProbeLocalTUNStartupRecoveryDetectsInstalledAdapter(t *testing.T) {
	_ = setupProbeLocalConsoleTest(t)

	detectCalls := 0
	probeLocalDetectTUNInstalled = func() (bool, error) {
		detectCalls++
		return true, nil
	}
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest() })

	if err := recoverProbeLocalTUNRuntimeOnStartup(); err != nil {
		t.Fatalf("recoverProbeLocalTUNRuntimeOnStartup returned error: %v", err)
	}
	if detectCalls != 1 {
		t.Fatalf("detect calls=%d, want 1", detectCalls)
	}
	status := probeLocalControl.tunStatus()
	if !status.Installed {
		t.Fatalf("startup recovery installed=%v, want true", status.Installed)
	}
	if status.Enabled {
		t.Fatalf("startup recovery enabled=%v, want false without persisted enabled", status.Enabled)
	}
	state, err := loadProbeLocalTUNStateFile()
	if err != nil {
		t.Fatalf("load tun state failed: %v", err)
	}
	if !state.TUN.Installed || state.TUN.Enabled {
		t.Fatalf("persisted tun state=%+v, want installed=true enabled=false", state.TUN)
	}
}

func TestProbeLocalTUNStartupRecoveryUsesTUNEnabledIntent(t *testing.T) {
	_ = setupProbeLocalConsoleTest(t)

	state := defaultProbeLocalTUNStateFile()
	state.TUN.Installed = true
	state.TUN.Enabled = true
	if err := persistProbeLocalTUNStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}
	if !probeLocalControl.shouldRecoverTUNOnStartup() {
		t.Fatal("startup recovery should run when tun.enabled is true")
	}
}

func TestProbeLocalTUNStartupRecoveryRepairsTUNOnlyStateWithoutDataPlane(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("TUN data plane hook path is Windows-only")
	}
	_ = setupProbeLocalConsoleTest(t)
	if err := persistProbeLocalTUNPersistentState(true, true); err != nil {
		t.Fatalf("persist tun state failed: %v", err)
	}
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	probeLocalDetectTUNInstalled = func() (bool, error) { return true, nil }
	probeLocalInstallTUNDriver = func() error { return nil }
	probeLocalCheckTUNReadyAfterInstall = func() error { return nil }
	dataPlaneCalls := 0
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) { return uintptr(1), nil }
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error { return nil }
	probeLocalNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeLocalTUNDataPlane, error) {
		dataPlaneCalls++
		return &fakeProbeLocalTUNDataPlane{stats: probeLocalTUNDataPlaneStats{Running: true}}, nil
	}
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest(); resetprobeLocalRouteHooksForTest() })

	if err := recoverProbeLocalTUNRuntimeOnStartup(); err != nil {
		t.Fatalf("recoverProbeLocalTUNRuntimeOnStartup returned error: %v", err)
	}
	if dataPlaneCalls != 0 {
		t.Fatalf("data plane calls=%d, want 0 for startup adapter-only recovery", dataPlaneCalls)
	}
	status := probeLocalControl.tunStatus()
	if !status.Installed || !status.Enabled || status.DataPlane {
		t.Fatalf("startup recovery tun status=%+v, want installed enabled without data plane", status)
	}
	state, err := loadProbeLocalTUNStateFile()
	if err != nil {
		t.Fatalf("load tun state failed: %v", err)
	}
	if !state.TUN.Installed || !state.TUN.Enabled {
		t.Fatalf("persisted tun state=%+v, want installed=true enabled=true", state.TUN)
	}
}

func TestProbeLocalTUNStartupRecoveryRepairsPersistedEnabledState(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("TUN data plane hook path is Windows-only")
	}
	_ = setupProbeLocalConsoleTest(t)
	if err := persistProbeLocalTUNPersistentState(true, true); err != nil {
		t.Fatalf("persist tun state failed: %v", err)
	}

	detectCalls := 0
	probeLocalDetectTUNInstalled = func() (bool, error) {
		detectCalls++
		return detectCalls >= 2, nil
	}
	installCalls := 0
	probeLocalInstallTUNDriver = func() error {
		installCalls++
		obs := newProbeLocalTUNInstallObservation()
		obs.Final.Success = true
		obs.Final.ReasonCode = "TUN_INSTALL_SUCCEEDED"
		obs.Final.Reason = "startup repair"
		setProbeLocalTUNInstallObservation(obs)
		return nil
	}
	probeLocalCheckTUNReadyAfterInstall = func() error { return nil }
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")
	dataPlaneCalls := 0
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) { return uintptr(1), nil }
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error { return nil }
	probeLocalNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeLocalTUNDataPlane, error) {
		dataPlaneCalls++
		return &fakeProbeLocalTUNDataPlane{stats: probeLocalTUNDataPlaneStats{Running: true}}, nil
	}
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest(); resetprobeLocalRouteHooksForTest() })

	if err := recoverProbeLocalTUNRuntimeOnStartup(); err != nil {
		t.Fatalf("recoverProbeLocalTUNRuntimeOnStartup returned error: %v", err)
	}
	if detectCalls != 2 {
		t.Fatalf("detect calls=%d, want 2", detectCalls)
	}
	if installCalls != 1 {
		t.Fatalf("install calls=%d, want 1", installCalls)
	}
	if dataPlaneCalls != 0 {
		t.Fatalf("data plane calls=%d, want 0 for adapter-only recovery", dataPlaneCalls)
	}
	status := probeLocalControl.tunStatus()
	if !status.Installed || !status.Enabled || status.DataPlane {
		t.Fatalf("startup recovery tun status=%+v, want installed enabled without data plane", status)
	}
	state, err := loadProbeLocalTUNStateFile()
	if err != nil {
		t.Fatalf("load tun state failed: %v", err)
	}
	if !state.TUN.Installed || !state.TUN.Enabled {
		t.Fatalf("persisted tun state=%+v, want installed=true enabled=true", state.TUN)
	}
}

func TestProbeLocalTUNStartupRecoveryRestoresPersistedEnabledState(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("TUN data plane hook path is Windows-only")
	}
	_ = setupProbeLocalConsoleTest(t)
	if err := persistProbeLocalTUNPersistentState(true, true); err != nil {
		t.Fatalf("persist tun state failed: %v", err)
	}

	probeLocalDetectTUNInstalled = func() (bool, error) { return true, nil }
	probeLocalInstallTUNDriver = func() error { return nil }
	probeLocalCheckTUNReadyAfterInstall = func() error { return nil }
	probeLocalApplyTUNPrimaryDNS = func() error { return nil }
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")
	dataPlaneCalls := 0
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) { return uintptr(1), nil }
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error { return nil }
	probeLocalNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeLocalTUNDataPlane, error) {
		dataPlaneCalls++
		return &fakeProbeLocalTUNDataPlane{stats: probeLocalTUNDataPlaneStats{Running: true}}, nil
	}
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest(); resetprobeLocalRouteHooksForTest() })

	if err := recoverProbeLocalTUNRuntimeOnStartup(); err != nil {
		t.Fatalf("recoverProbeLocalTUNRuntimeOnStartup returned error: %v", err)
	}
	if dataPlaneCalls != 0 {
		t.Fatalf("data plane calls=%d, want 0 for adapter-only recovery", dataPlaneCalls)
	}
	status := probeLocalControl.tunStatus()
	if !status.Installed || !status.Enabled || status.DataPlane {
		t.Fatalf("startup recovery tun status=%+v, want installed enabled without data plane", status)
	}
	state, err := loadProbeLocalTUNStateFile()
	if err != nil {
		t.Fatalf("load tun state failed: %v", err)
	}
	if !state.TUN.Installed || !state.TUN.Enabled {
		t.Fatalf("persisted tun state=%+v, want installed=true enabled=true", state.TUN)
	}
}

func TestProbeLocalTUNStartupRecoveryFailureRecordsError(t *testing.T) {
	_ = setupProbeLocalConsoleTest(t)
	if err := persistProbeLocalTUNPersistentState(true, true); err != nil {
		t.Fatalf("persist tun state failed: %v", err)
	}

	probeLocalDetectTUNInstalled = func() (bool, error) { return false, nil }
	probeLocalInstallTUNDriver = func() error { return errors.New("device stack not ready") }
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest() })

	err := recoverProbeLocalTUNRuntimeOnStartup()
	if err == nil {
		t.Fatal("expected startup recovery error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "device stack") {
		t.Fatalf("startup recovery error=%q", err.Error())
	}
	status := probeLocalControl.tunStatus()
	if status.RecoveryStatus != "failed" {
		t.Fatalf("recovery status=%q, want failed", status.RecoveryStatus)
	}
	if status.RecoveryAttempts != 1 {
		t.Fatalf("recovery attempts=%d, want 1", status.RecoveryAttempts)
	}
	if !strings.Contains(strings.ToLower(status.RecoveryLastError), "device stack") {
		t.Fatalf("recovery last error=%q", status.RecoveryLastError)
	}
	if !strings.Contains(strings.ToLower(status.LastError), "device stack") {
		t.Fatalf("last error=%q", status.LastError)
	}
}

func TestProbeLocalTUNStatusReturnsLastInstallObservation(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalInstallTUNDriver = func() error {
		obs := newProbeLocalTUNInstallObservation()
		obs.Driver.PackageExists = true
		obs.Driver.PackagePath = `C:\\temp\\wintun.dll`
		obs.Create.Called = true
		obs.Create.HandleNonZero = true
		obs.Visibility.DetectVisible = true
		obs.Visibility.IfIndexResolved = true
		obs.Visibility.IfIndexValue = 11
		obs.Final.Success = true
		obs.Final.ReasonCode = "TUN_INSTALL_SUCCEEDED"
		obs.Final.Reason = "status-check"
		setProbeLocalTUNInstallObservation(obs)
		return nil
	}
	probeLocalCheckTUNReadyAfterInstall = func() error { return nil }
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest() })

	installResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/tun/install", map[string]any{}, sessionCookie)
	if installResp.Code != http.StatusOK {
		t.Fatalf("tun/install status=%d body=%s", installResp.Code, installResp.Body.String())
	}

	statusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/tun/status", nil, sessionCookie)
	if statusResp.Code != http.StatusOK {
		t.Fatalf("tun/status status=%d body=%s", statusResp.Code, statusResp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, statusResp)
	if _, exists := payload["install_observation"]; exists {
		t.Fatalf("tun/status should not include install_observation")
	}
	lastObs, ok := payload["last_install_observation"].(map[string]any)
	if !ok {
		t.Fatalf("tun/status last_install_observation type=%T", payload["last_install_observation"])
	}
	finalObj, _ := lastObs["final"].(map[string]any)
	if success, _ := finalObj["success"].(bool); !success {
		t.Fatalf("tun/status last_install_observation.final.success=%v", finalObj["success"])
	}
	if reasonCode, _ := finalObj["reason_code"].(string); reasonCode != "TUN_INSTALL_SUCCEEDED" {
		t.Fatalf("tun/status last_install_observation.final.reason_code=%q", reasonCode)
	}
	if reason, _ := finalObj["reason"].(string); strings.TrimSpace(reason) == "" {
		t.Fatalf("tun/status last_install_observation.final.reason should not be empty")
	}
}

func TestProbeLocalTUNInstallReturnsInternalErrorWhenPostCheckFails(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalInstallTUNDriver = func() error {
		obs := newProbeLocalTUNInstallObservation()
		obs.Driver.PackageExists = true
		obs.Driver.PackagePath = `C:\\temp\\wintun.dll`
		obs.Create.Called = true
		obs.Create.HandleNonZero = true
		obs.Visibility.DetectVisible = true
		obs.Visibility.IfIndexResolved = true
		obs.Visibility.IfIndexValue = 17
		obs.Final.Success = true
		obs.Final.ReasonCode = "TUN_INSTALL_SUCCEEDED"
		obs.Final.Reason = "driver-ready"
		setProbeLocalTUNInstallObservation(obs)
		return nil
	}
	probeLocalCheckTUNReadyAfterInstall = func() error {
		return errors.New("ipv4 address not bindable in time")
	}
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/tun/install", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("tun/install post-check-fail status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "bindable") {
		t.Fatalf("tun/install post-check-fail error=%q", errText)
	}
	if code, _ := payload["code"].(string); code != probeLocalTUNInstallCodeRouteTargetFailed {
		t.Fatalf("tun/install post-check-fail code=%q", code)
	}
	if stage, _ := payload["stage"].(string); stage != "post_install_route_target_check" {
		t.Fatalf("tun/install post-check-fail stage=%q", stage)
	}
	observation, ok := payload["install_observation"].(map[string]any)
	if !ok {
		t.Fatalf("tun/install post-check-fail observation type=%T", payload["install_observation"])
	}
	finalObj, _ := observation["final"].(map[string]any)
	if success, _ := finalObj["success"].(bool); success {
		t.Fatalf("post-check-fail final.success=%v", finalObj["success"])
	}
	if reasonCode, _ := finalObj["reason_code"].(string); reasonCode != probeLocalTUNInstallCodeRouteTargetFailed {
		t.Fatalf("post-check-fail final.reason_code=%q", reasonCode)
	}
}

func TestEnsureProbeLocalDNSHostDefaultsInitializedCreatesFile(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	hostPath, err := resolveProbeLocalDNSHostPath()
	if err != nil {
		t.Fatalf("resolve host path failed: %v", err)
	}

	if _, err := os.Stat(hostPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host file should not exist before init, err=%v", err)
	}

	if err := ensureprobeLocalRouteDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults failed: %v", err)
	}
	if err := ensureprobeLocalRouteDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults second call failed: %v", err)
	}

	if _, err := os.Stat(hostPath); err != nil {
		t.Fatalf("host file should exist after init: %v", err)
	}

	hostRaw, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatalf("read host file failed: %v", err)
	}
	if strings.TrimSpace(string(hostRaw)) != "# dns,ip" {
		t.Fatalf("unexpected host default content: %q", string(hostRaw))
	}
}
