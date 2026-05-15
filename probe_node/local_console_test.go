package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	resetProbeLocalTUNGroupRuntimeRegistryForTest()
	setProbeLocalProxyRuntimeContext(nodeIdentity{}, "")
	t.Cleanup(func() {
		resetProbeLocalAuthManagerForTest()
		resetProbeLocalControlStateForTest()
		resetProbeLocalDNSServiceForTest()
		resetProbeLocalTUNGroupRuntimeRegistryForTest()
		resetProbeLocalProxyHooksForTest()
		resetProbeLocalTUNHooksForTest()
		resetProbeLocalUpgradeHooksForTest()
		setProbeLocalProxyRuntimeContext(nodeIdentity{}, "")
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

	dnsStatusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/dns/status", nil)
	if dnsStatusResp.Code != http.StatusUnauthorized {
		t.Fatalf("dns/status without session status=%d", dnsStatusResp.Code)
	}

	dnsRealIPListResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/dns/real_ip/list", nil)
	if dnsRealIPListResp.Code != http.StatusUnauthorized {
		t.Fatalf("dns/real_ip/list without session status=%d", dnsRealIPListResp.Code)
	}

	dnsRealIPLookupResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/dns/real_ip/lookup?domain=api.example.com", nil)
	if dnsRealIPLookupResp.Code != http.StatusUnauthorized {
		t.Fatalf("dns/real_ip/lookup without session status=%d", dnsRealIPLookupResp.Code)
	}

	dnsFakeIPListResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/dns/fake_ip/list", nil)
	if dnsFakeIPListResp.Code != http.StatusUnauthorized {
		t.Fatalf("dns/fake_ip/list without session status=%d", dnsFakeIPListResp.Code)
	}

	dnsFakeIPLookupResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/dns/fake_ip/lookup?ip=198.18.0.9", nil)
	if dnsFakeIPLookupResp.Code != http.StatusUnauthorized {
		t.Fatalf("dns/fake_ip/lookup without session status=%d", dnsFakeIPLookupResp.Code)
	}

	proxyStatusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/status", nil)
	if proxyStatusResp.Code != http.StatusUnauthorized {
		t.Fatalf("proxy/status without session status=%d", proxyStatusResp.Code)
	}

	logsResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/logs", nil)
	if logsResp.Code != http.StatusUnauthorized {
		t.Fatalf("logs without session status=%d", logsResp.Code)
	}

	upgradeStatusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/system/upgrade/status", nil)
	if upgradeStatusResp.Code != http.StatusUnauthorized {
		t.Fatalf("system/upgrade/status without session status=%d", upgradeStatusResp.Code)
	}

	protectedPagePaths := []string{"/local/panel", "/local/proxy", "/local/dns", "/local/logs", "/local/system"}
	for _, path := range protectedPagePaths {
		pageResp := doProbeLocalRequest(t, mux, http.MethodGet, path, nil)
		if pageResp.Code != http.StatusFound {
			t.Fatalf("%s without session status=%d", path, pageResp.Code)
		}
		if location := pageResp.Header().Get("Location"); location != "/local/login" {
			t.Fatalf("%s redirect location=%q", path, location)
		}
	}
}

func TestProbeLocalProxyFlowWithSession(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	tunStatusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/tun/status", nil, sessionCookie)
	if tunStatusResp.Code != http.StatusOK {
		t.Fatalf("tun/status status=%d body=%s", tunStatusResp.Code, tunStatusResp.Body.String())
	}
	tunPayload := decodeProbeLocalJSON(t, tunStatusResp)
	if tunPayload["platform"] != runtime.GOOS {
		t.Fatalf("tun platform=%v want=%v", tunPayload["platform"], runtime.GOOS)
	}
	if installed, _ := tunPayload["installed"].(bool); installed {
		t.Fatalf("tun installed should be false at init")
	}

	proxyStatusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/status", nil, sessionCookie)
	if proxyStatusResp.Code != http.StatusOK {
		t.Fatalf("proxy/status status=%d body=%s", proxyStatusResp.Code, proxyStatusResp.Body.String())
	}
	proxyPayload := decodeProbeLocalJSON(t, proxyStatusResp)
	if proxyPayload["mode"] != probeLocalProxyModeDirect {
		t.Fatalf("proxy mode=%v", proxyPayload["mode"])
	}
	if enabled, _ := proxyPayload["enabled"].(bool); enabled {
		t.Fatalf("proxy enabled should be false at init")
	}
	if latencyStatus, _ := proxyPayload["selected_chain_latency_status"].(string); latencyStatus != "" && latencyStatus != "unreachable" {
		t.Fatalf("proxy latency status=%q", latencyStatus)
	}

	enableResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/enable", map[string]any{}, sessionCookie)
	if enableResp.Code != http.StatusConflict {
		t.Fatalf("proxy/enable status=%d body=%s", enableResp.Code, enableResp.Body.String())
	}
	enablePayload := decodeProbeLocalJSON(t, enableResp)
	if enablePayload["error"] == nil || enablePayload["error"] == "" {
		t.Fatalf("proxy/enable should return error message")
	}

	directResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/direct", map[string]any{}, sessionCookie)
	if directResp.Code != http.StatusOK {
		t.Fatalf("proxy/direct status=%d body=%s", directResp.Code, directResp.Body.String())
	}
	directPayload := decodeProbeLocalJSON(t, directResp)
	proxyObj, ok := directPayload["proxy"].(map[string]any)
	if !ok {
		t.Fatalf("proxy/direct proxy payload type=%T", directPayload["proxy"])
	}
	if proxyObj["mode"] != probeLocalProxyModeDirect {
		t.Fatalf("proxy/direct mode=%v", proxyObj["mode"])
	}
	if enabled, _ := proxyObj["enabled"].(bool); enabled {
		t.Fatalf("proxy/direct enabled should be false")
	}
}

func TestProbeLocalDNSStatusWithSession(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	resp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/dns/status", nil, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("dns/status status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	if _, ok := payload["enabled"].(bool); !ok {
		t.Fatalf("dns/status enabled type=%T", payload["enabled"])
	}
	tunListener, ok := payload["tun_listener"].(map[string]any)
	if !ok {
		t.Fatalf("dns/status tun_listener type=%T", payload["tun_listener"])
	}
	if _, ok := tunListener["enabled"].(bool); !ok {
		t.Fatalf("dns/status tun_listener.enabled type=%T", tunListener["enabled"])
	}
	if _, ok := payload["fake_ip_cidr"].(string); !ok {
		t.Fatalf("dns/status fake_ip_cidr type=%T", payload["fake_ip_cidr"])
	}
	if _, ok := payload["fake_ip_entries"].([]any); !ok {
		t.Fatalf("dns/status fake_ip_entries type=%T", payload["fake_ip_entries"])
	}
	if _, ok := payload["route_hint_count"].(float64); !ok {
		t.Fatalf("dns/status route_hint_count type=%T", payload["route_hint_count"])
	}
	if ttl, ok := payload["cache_ttl_seconds"].(float64); !ok || int64(ttl) != int64(probeLocalDNSCacheTTL/time.Second) {
		t.Fatalf("dns/status cache_ttl_seconds=%v", payload["cache_ttl_seconds"])
	}
	if records, ok := payload["cache_records"].([]any); !ok || len(records) != 0 {
		t.Fatalf("dns/status cache_records=%v", payload["cache_records"])
	}
}

func TestProbeLocalDNSDebugAPIs(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	realListResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/dns/real_ip/list", nil, sessionCookie)
	if realListResp.Code != http.StatusOK {
		t.Fatalf("dns/real_ip/list status=%d body=%s", realListResp.Code, realListResp.Body.String())
	}
	realListPayload := decodeProbeLocalJSON(t, realListResp)
	if _, ok := realListPayload["items"].([]any); !ok {
		t.Fatalf("dns/real_ip/list items type=%T", realListPayload["items"])
	}

	missingDomainResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/dns/real_ip/lookup", nil, sessionCookie)
	if missingDomainResp.Code != http.StatusBadRequest {
		t.Fatalf("dns/real_ip/lookup missing domain status=%d body=%s", missingDomainResp.Code, missingDomainResp.Body.String())
	}

	realNotFoundResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/dns/real_ip/lookup?domain=api.example.com", nil, sessionCookie)
	if realNotFoundResp.Code != http.StatusNotFound {
		t.Fatalf("dns/real_ip/lookup not found status=%d body=%s", realNotFoundResp.Code, realNotFoundResp.Body.String())
	}

	fakeListResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/dns/fake_ip/list", nil, sessionCookie)
	if fakeListResp.Code != http.StatusOK {
		t.Fatalf("dns/fake_ip/list status=%d body=%s", fakeListResp.Code, fakeListResp.Body.String())
	}
	fakeListPayload := decodeProbeLocalJSON(t, fakeListResp)
	if _, ok := fakeListPayload["items"].([]any); !ok {
		t.Fatalf("dns/fake_ip/list items type=%T", fakeListPayload["items"])
	}

	missingIPResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/dns/fake_ip/lookup", nil, sessionCookie)
	if missingIPResp.Code != http.StatusBadRequest {
		t.Fatalf("dns/fake_ip/lookup missing ip status=%d body=%s", missingIPResp.Code, missingIPResp.Body.String())
	}

	fakeNotFoundResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/dns/fake_ip/lookup?ip=198.18.0.10", nil, sessionCookie)
	if fakeNotFoundResp.Code != http.StatusNotFound {
		t.Fatalf("dns/fake_ip/lookup not found status=%d body=%s", fakeNotFoundResp.Code, fakeNotFoundResp.Body.String())
	}
}

func TestResolveProbeLocalDNSUpstreamBypassTarget(t *testing.T) {
	tests := []struct {
		name      string
		kind      string
		address   string
		want      string
		wantFound bool
	}{
		{name: "dns ipv4", kind: "dns", address: "119.29.29.29", want: "119.29.29.29:53", wantFound: true},
		{name: "dns domain", kind: "dns", address: "dns.alidns.com:53", want: "", wantFound: false},
		{name: "dot ipv4", kind: "dot", address: "1.1.1.1:853", want: "1.1.1.1:853", wantFound: true},
		{name: "doh ipv4 https", kind: "doh", address: "https://1.1.1.1/dns-query", want: "1.1.1.1:443", wantFound: true},
		{name: "doh ipv4 http", kind: "doh", address: "http://8.8.8.8/dns-query", want: "8.8.8.8:80", wantFound: true},
		{name: "doh domain", kind: "doh", address: "https://dns.google/dns-query", want: "", wantFound: false},
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

func TestEnsureProbeLocalDNSUpstreamDirectBypass(t *testing.T) {
	oldEnsure := probeLocalDNSEnsureDirectBypassForTarget
	t.Cleanup(func() {
		probeLocalDNSEnsureDirectBypassForTarget = oldEnsure
	})
	calls := make([]string, 0, 4)
	probeLocalDNSEnsureDirectBypassForTarget = func(target string) error {
		calls = append(calls, strings.TrimSpace(target))
		return nil
	}

	ensureProbeLocalDNSUpstreamDirectBypass("dns", "119.29.29.29")
	ensureProbeLocalDNSUpstreamDirectBypass("dns", "dns.alidns.com")
	ensureProbeLocalDNSUpstreamDirectBypass("doh", "https://1.1.1.1/dns-query")

	if len(calls) != 0 {
		t.Fatalf("bypass calls len=%d want=0 calls=%v", len(calls), calls)
	}
}

func TestCurrentProbeLocalDNSUpstreamCandidatesAppendsSystemDNSLast(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	cfg := defaultProbeLocalProxyGroupFile()
	cfg.DoHProxyServers = []string{"https://proxy.example/dns-query"}
	cfg.DoHServers = []string{"https://doh.example/dns-query"}
	cfg.DoTServers = []string{"1.1.1.1:853"}
	cfg.DNSServers = []string{"8.8.8.8:53"}
	if err := persistProbeLocalProxyGroupFile(cfg); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
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
		"doh|https://proxy.example/dns-query",
		"doh|https://doh.example/dns-query",
		"dot|1.1.1.1:853",
		"dns|8.8.8.8:53",
		"dns|192.168.1.1:53",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("candidates=\n%v\nwant=\n%v", got, want)
	}
}

func TestProbeLocalProxyEnableReturnsInternalErrorOnTakeoverFailure(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.mu.Unlock()

	probeLocalApplyProxyTakeover = func() error {
		return errors.New("takeover failed for test")
	}
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/enable", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("proxy/enable status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(errText, "takeover failed for test") {
		t.Fatalf("proxy/enable error=%q", errText)
	}
}

func TestProbeLocalProxyEnableReturnsNotImplementedOnUnsupported(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.mu.Unlock()

	probeLocalApplyProxyTakeover = func() error {
		return errProbeLocalProxyUnsupported
	}
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/enable", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("proxy/enable unsupported status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "not supported") {
		t.Fatalf("proxy/enable unsupported error=%q", errText)
	}
}

func TestProbeLocalProxyEnableAndDirectSuccessWithHooks(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.mu.Unlock()

	probeLocalApplyProxyTakeover = func() error { return nil }
	probeLocalRestoreProxyDirect = func() error { return nil }
	applyDNSCalls := 0
	restoreDNSCalls := 0
	probeLocalApplyTUNPrimaryDNS = func() error {
		applyDNSCalls++
		return nil
	}
	probeLocalRestoreTUNPrimaryDNS = func() error {
		restoreDNSCalls++
		return nil
	}
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) { return uintptr(1), nil }
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error { return nil }
	probeLocalNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeLocalTUNDataPlane, error) {
		return &fakeProbeLocalTUNDataPlane{stats: probeLocalTUNDataPlaneStats{Running: true}}, nil
	}
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest(); resetProbeLocalTUNHooksForTest() })

	enableResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/enable", map[string]any{}, sessionCookie)
	if enableResp.Code != http.StatusOK {
		t.Fatalf("proxy/enable success status=%d body=%s", enableResp.Code, enableResp.Body.String())
	}
	enablePayload := decodeProbeLocalJSON(t, enableResp)
	proxyObj, ok := enablePayload["proxy"].(map[string]any)
	if !ok {
		t.Fatalf("proxy/enable proxy payload type=%T", enablePayload["proxy"])
	}
	if proxyObj["mode"] != probeLocalProxyModeTUN {
		t.Fatalf("proxy/enable mode=%v", proxyObj["mode"])
	}
	if enabled, _ := proxyObj["enabled"].(bool); !enabled {
		t.Fatalf("proxy/enable enabled should be true")
	}
	tunObj, ok := enablePayload["tun"].(map[string]any)
	if !ok {
		t.Fatalf("proxy/enable tun payload type=%T", enablePayload["tun"])
	}
	if tunEnabled, _ := tunObj["enabled"].(bool); !tunEnabled {
		t.Fatalf("proxy/enable tun.enabled should be true")
	}
	if applyDNSCalls != 0 {
		t.Fatalf("apply dns calls=%d, want 0", applyDNSCalls)
	}

	directResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/direct", map[string]any{}, sessionCookie)
	if directResp.Code != http.StatusOK {
		t.Fatalf("proxy/direct success status=%d body=%s", directResp.Code, directResp.Body.String())
	}
	directPayload := decodeProbeLocalJSON(t, directResp)
	directProxyObj, ok := directPayload["proxy"].(map[string]any)
	if !ok {
		t.Fatalf("proxy/direct proxy payload type=%T", directPayload["proxy"])
	}
	if directProxyObj["mode"] != probeLocalProxyModeDirect {
		t.Fatalf("proxy/direct mode=%v", directProxyObj["mode"])
	}
	if enabled, _ := directProxyObj["enabled"].(bool); enabled {
		t.Fatalf("proxy/direct enabled should be false")
	}
	if restoreDNSCalls != 0 {
		t.Fatalf("restore dns calls=%d, want 0", restoreDNSCalls)
	}
}

func TestProbeLocalTUNResetAndUninstallHandlers(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.tun.Enabled = true
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	restoreProxyCalls := 0
	restoreDNSCalls := 0
	uninstallCalls := 0
	probeLocalRestoreProxyDirect = func() error {
		restoreProxyCalls++
		return nil
	}
	probeLocalRestoreTUNPrimaryDNS = func() error {
		restoreDNSCalls++
		return nil
	}
	probeLocalUninstallTUNDriver = func() error {
		uninstallCalls++
		return nil
	}
	probeLocalDetectTUNInstalled = func() (bool, error) { return true, nil }
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest(); resetProbeLocalTUNHooksForTest() })

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
	if restoreProxyCalls != 1 || restoreDNSCalls != 0 || uninstallCalls != 0 {
		t.Fatalf("after reset restoreProxy=%d restoreDNS=%d uninstall=%d", restoreProxyCalls, restoreDNSCalls, uninstallCalls)
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
	if restoreProxyCalls != 2 || restoreDNSCalls != 0 || uninstallCalls != 1 {
		t.Fatalf("after uninstall restoreProxy=%d restoreDNS=%d uninstall=%d", restoreProxyCalls, restoreDNSCalls, uninstallCalls)
	}
}

func TestProbeLocalProxyEnableSelectionWritesRuntimeState(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	proxyChainPath, err := resolveProbeLocalProxyChainPath()
	if err != nil {
		t.Fatalf("resolve proxy_chain path failed: %v", err)
	}
	proxyChainPayload := `{
  "updated_at": "2026-04-24T00:00:00Z",
  "items": [
    {
      "chain_id":"chain-proxy-1",
      "chain_type":"proxy_chain",
      "name":"Proxy 1",
      "entry_node_id":"node-10",
      "exit_node_id":"node-35",
      "cascade_node_ids":["node-21"],
      "link_layer":"http2",
      "secret":"secret-1",
      "hop_configs":[
        {"node_no":10,"relay_host":"entry.example.com","external_port":11110,"listen_port":11010,"link_layer":"http2"},
        {"node_no":21,"relay_host":"relay.example.com","external_port":12121,"listen_port":12021,"link_layer":"http2"},
        {"node_no":35,"relay_host":"exit.example.com","external_port":13131,"listen_port":13031,"link_layer":"http2"}
      ]
    }
  ]
}`
	if err := os.WriteFile(proxyChainPath, []byte(proxyChainPayload), 0o644); err != nil {
		t.Fatalf("write proxy_chain file failed: %v", err)
	}

	saveGroupsResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/groups/save", map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "default", "rules": []string{"domain_suffix:example.com"}},
			{"group": "media", "rules": []string{"domain_keyword:stream"}},
		},
	}, sessionCookie)
	if saveGroupsResp.Code != http.StatusOK {
		t.Fatalf("groups save status=%d body=%s", saveGroupsResp.Code, saveGroupsResp.Body.String())
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.mu.Unlock()

	setProbeLocalProxyRuntimeContext(nodeIdentity{NodeID: "node-enable-test"}, "https://controller.example.com/base")
	bypassTargets := make([]string, 0, 8)
	probeLocalLookupIPv4ForBypass = func(host string) ([]string, error) {
		switch strings.TrimSpace(host) {
		case "controller.example.com":
			return []string{"203.0.113.10"}, nil
		case "entry.example.com":
			return []string{"203.0.113.11"}, nil
		case "relay.example.com":
			return []string{"203.0.113.12"}, nil
		case "exit.example.com":
			return []string{"203.0.113.13"}, nil
		default:
			return nil, fmt.Errorf("unexpected host lookup: %s", host)
		}
	}
	probeLocalEnsureExplicitDirectBypass = func(target string) error {
		bypassTargets = append(bypassTargets, strings.TrimSpace(target))
		return nil
	}
	probeLocalApplyProxyTakeover = func() error { return nil }
	probeLocalApplyTUNPrimaryDNS = func() error { return nil }
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) { return uintptr(1), nil }
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error { return nil }
	probeLocalNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeLocalTUNDataPlane, error) {
		return &fakeProbeLocalTUNDataPlane{stats: probeLocalTUNDataPlaneStats{Running: true}}, nil
	}
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest(); resetProbeLocalTUNHooksForTest() })

	enableResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/enable", map[string]any{
		"group":             "media",
		"selected_chain_id": "chain-proxy-1",
	}, sessionCookie)
	if enableResp.Code != http.StatusOK {
		t.Fatalf("proxy/enable with selection status=%d body=%s", enableResp.Code, enableResp.Body.String())
	}
	enablePayload := decodeProbeLocalJSON(t, enableResp)
	selectionObj, ok := enablePayload["selection"].(map[string]any)
	if !ok {
		t.Fatalf("proxy/enable selection payload type=%T", enablePayload["selection"])
	}
	if selectionObj["group"] != "media" {
		t.Fatalf("proxy/enable selection group=%v", selectionObj["group"])
	}
	if selectionObj["selected_chain_id"] != "chain-proxy-1" {
		t.Fatalf("proxy/enable selection selected_chain_id=%v", selectionObj["selected_chain_id"])
	}
	if selectionObj["tunnel_node_id"] != "chain:chain-proxy-1" {
		t.Fatalf("proxy/enable selection tunnel_node_id=%v", selectionObj["tunnel_node_id"])
	}

	stateResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/state", nil, sessionCookie)
	if stateResp.Code != http.StatusOK {
		t.Fatalf("state get status=%d body=%s", stateResp.Code, stateResp.Body.String())
	}
	statePayload := decodeProbeLocalJSON(t, stateResp)
	groups, ok := statePayload["groups"].([]any)
	if !ok {
		t.Fatalf("state groups payload type=%T", statePayload["groups"])
	}
	found := false
	for _, item := range groups {
		entry, _ := item.(map[string]any)
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(entry["group"])), "media") {
			found = true
			if entry["action"] != "tunnel" {
				t.Fatalf("media action=%v", entry["action"])
			}
			if entry["selected_chain_id"] != "chain-proxy-1" {
				t.Fatalf("media selected_chain_id=%v", entry["selected_chain_id"])
			}
			if entry["tunnel_node_id"] != "chain:chain-proxy-1" {
				t.Fatalf("media tunnel_node_id=%v", entry["tunnel_node_id"])
			}
			if entry["runtime_status"] != "online" {
				t.Fatalf("media runtime_status=%v", entry["runtime_status"])
			}
			break
		}
	}
	if !found {
		t.Fatalf("state groups missing media entry: %v", groups)
	}
	expectedBypass := map[string]struct{}{
		"203.0.113.10:443":   {},
		"203.0.113.11:11110": {},
		"203.0.113.12:12121": {},
		"203.0.113.13:13131": {},
	}
	if len(bypassTargets) != len(expectedBypass) {
		t.Fatalf("bootstrap direct bypass targets=%v want=%v", bypassTargets, expectedBypass)
	}
	for _, item := range bypassTargets {
		if _, ok := expectedBypass[item]; !ok {
			t.Fatalf("unexpected bootstrap bypass target=%s all=%v", item, bypassTargets)
		}
	}
}

func TestProbeLocalProxySelectSelectionWritesRuntimeStateWithoutEnablingProxy(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	proxyChainPath, err := resolveProbeLocalProxyChainPath()
	if err != nil {
		t.Fatalf("resolve proxy_chain path failed: %v", err)
	}
	proxyChainPayload := `{
  "updated_at": "2026-04-24T00:00:00Z",
  "items": [
    {"chain_id":"chain-proxy-1","chain_type":"proxy_chain","name":"Proxy 1"}
  ]
}`
	if err := os.WriteFile(proxyChainPath, []byte(proxyChainPayload), 0o644); err != nil {
		t.Fatalf("write proxy_chain file failed: %v", err)
	}

	saveGroupsResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/groups/save", map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "default", "rules": []string{"domain_suffix:example.com"}},
			{"group": "media", "rules": []string{"domain_keyword:stream"}},
		},
	}, sessionCookie)
	if saveGroupsResp.Code != http.StatusOK {
		t.Fatalf("groups save status=%d body=%s", saveGroupsResp.Code, saveGroupsResp.Body.String())
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.mu.Unlock()

	probeLocalApplyProxyTakeover = func() error {
		t.Fatalf("proxy takeover should not be called for /proxy/select")
		return nil
	}
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) { return uintptr(1), nil }
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error { return nil }
	probeLocalNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeLocalTUNDataPlane, error) {
		return &fakeProbeLocalTUNDataPlane{stats: probeLocalTUNDataPlaneStats{Running: true}}, nil
	}
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest(); resetProbeLocalTUNDataPlaneHooksForTest() })

	selectResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/select", map[string]any{
		"group":             "media",
		"selected_chain_id": "chain-proxy-1",
	}, sessionCookie)
	if selectResp.Code != http.StatusOK {
		t.Fatalf("proxy/select status=%d body=%s", selectResp.Code, selectResp.Body.String())
	}
	selectPayload := decodeProbeLocalJSON(t, selectResp)
	selectionObj, ok := selectPayload["selection"].(map[string]any)
	if !ok {
		t.Fatalf("proxy/select selection payload type=%T", selectPayload["selection"])
	}
	if selectionObj["group"] != "media" {
		t.Fatalf("proxy/select selection group=%v", selectionObj["group"])
	}
	if selectionObj["selected_chain_id"] != "chain-proxy-1" {
		t.Fatalf("proxy/select selection selected_chain_id=%v", selectionObj["selected_chain_id"])
	}
	if selectionObj["tunnel_node_id"] != "chain:chain-proxy-1" {
		t.Fatalf("proxy/select selection tunnel_node_id=%v", selectionObj["tunnel_node_id"])
	}
	proxyObj, ok := selectPayload["proxy"].(map[string]any)
	if !ok {
		t.Fatalf("proxy/select proxy payload type=%T", selectPayload["proxy"])
	}
	if proxyObj["enabled"] != false {
		t.Fatalf("proxy/select enabled=%v", proxyObj["enabled"])
	}
	if proxyObj["mode"] != probeLocalProxyModeDirect {
		t.Fatalf("proxy/select mode=%v", proxyObj["mode"])
	}

	stateResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/state", nil, sessionCookie)
	if stateResp.Code != http.StatusOK {
		t.Fatalf("state get status=%d body=%s", stateResp.Code, stateResp.Body.String())
	}
	statePayload := decodeProbeLocalJSON(t, stateResp)
	groups, ok := statePayload["groups"].([]any)
	if !ok {
		t.Fatalf("state groups payload type=%T", statePayload["groups"])
	}
	found := false
	for _, item := range groups {
		entry, _ := item.(map[string]any)
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(entry["group"])), "media") {
			found = true
			if entry["action"] != "tunnel" {
				t.Fatalf("media action=%v", entry["action"])
			}
			if entry["selected_chain_id"] != "chain-proxy-1" {
				t.Fatalf("media selected_chain_id=%v", entry["selected_chain_id"])
			}
			if entry["tunnel_node_id"] != "chain:chain-proxy-1" {
				t.Fatalf("media tunnel_node_id=%v", entry["tunnel_node_id"])
			}
			if entry["runtime_status"] != "online" {
				t.Fatalf("media runtime_status=%v", entry["runtime_status"])
			}
			break
		}
	}
	if !found {
		t.Fatalf("state groups missing media entry: %v", groups)
	}
}

func TestProbeLocalProxyEnableRejectsUnknownGroup(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.mu.Unlock()

	probeLocalApplyProxyTakeover = func() error { return nil }
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/enable", map[string]any{
		"group": "unknown-group",
	}, sessionCookie)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("proxy/enable unknown group status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "not found") {
		t.Fatalf("proxy/enable unknown group error=%q", errText)
	}
}

func TestProbeLocalProxyEnableRejectsUnknownTunnelNodeID(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	proxyChainPath, err := resolveProbeLocalProxyChainPath()
	if err != nil {
		t.Fatalf("resolve proxy_chain path failed: %v", err)
	}
	proxyChainPayload := `{
  "updated_at": "2026-04-24T00:00:00Z",
  "items": [
    {"chain_id":"chain-proxy-1","chain_type":"proxy_chain","name":"Proxy 1"}
  ]
}`
	if err := os.WriteFile(proxyChainPath, []byte(proxyChainPayload), 0o644); err != nil {
		t.Fatalf("write proxy_chain file failed: %v", err)
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.mu.Unlock()

	probeLocalApplyProxyTakeover = func() error { return nil }
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/enable", map[string]any{
		"group":             "default",
		"selected_chain_id": "chain-not-exists",
	}, sessionCookie)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("proxy/enable unknown tunnel status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "not found") {
		t.Fatalf("proxy/enable unknown tunnel error=%q", errText)
	}
}

func TestProbeLocalProxyDirectSelectionWritesRuntimeStateGroup(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	saveGroupsResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/groups/save", map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "default", "rules": []string{"domain_suffix:example.com"}},
			{"group": "media", "rules": []string{"domain_keyword:stream"}},
		},
	}, sessionCookie)
	if saveGroupsResp.Code != http.StatusOK {
		t.Fatalf("groups save status=%d body=%s", saveGroupsResp.Code, saveGroupsResp.Body.String())
	}

	probeLocalRestoreProxyDirect = func() error { return nil }
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	directResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/direct", map[string]any{
		"group": "media",
	}, sessionCookie)
	if directResp.Code != http.StatusOK {
		t.Fatalf("proxy/direct with group status=%d body=%s", directResp.Code, directResp.Body.String())
	}
	directPayload := decodeProbeLocalJSON(t, directResp)
	selectionObj, ok := directPayload["selection"].(map[string]any)
	if !ok {
		t.Fatalf("proxy/direct selection payload type=%T", directPayload["selection"])
	}
	if selectionObj["group"] != "media" {
		t.Fatalf("proxy/direct selection group=%v", selectionObj["group"])
	}

	stateResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/state", nil, sessionCookie)
	if stateResp.Code != http.StatusOK {
		t.Fatalf("state get status=%d body=%s", stateResp.Code, stateResp.Body.String())
	}
	statePayload := decodeProbeLocalJSON(t, stateResp)
	groups, ok := statePayload["groups"].([]any)
	if !ok {
		t.Fatalf("state groups payload type=%T", statePayload["groups"])
	}
	found := false
	for _, item := range groups {
		entry, _ := item.(map[string]any)
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(entry["group"])), "media") {
			found = true
			if entry["action"] != "direct" {
				t.Fatalf("media action=%v", entry["action"])
			}
			if tunnelNodeID, _ := entry["tunnel_node_id"].(string); strings.TrimSpace(tunnelNodeID) != "" {
				t.Fatalf("media tunnel_node_id should be empty on direct, got=%v", entry["tunnel_node_id"])
			}
			break
		}
	}
	if !found {
		t.Fatalf("state groups missing media entry: %v", groups)
	}
}

func TestProbeLocalProxyDirectRejectsUnknownGroup(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalRestoreProxyDirect = func() error { return nil }
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/direct", map[string]any{
		"group": "unknown-group",
	}, sessionCookie)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("proxy/direct unknown group status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "not found") {
		t.Fatalf("proxy/direct unknown group error=%q", errText)
	}
}

func TestProbeLocalProxyRejectSelectionWritesRuntimeStateGroup(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	saveGroupsResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/groups/save", map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "default", "rules": []string{"domain_suffix:example.com"}},
			{"group": "media", "rules": []string{"domain_keyword:stream"}},
		},
	}, sessionCookie)
	if saveGroupsResp.Code != http.StatusOK {
		t.Fatalf("groups save status=%d body=%s", saveGroupsResp.Code, saveGroupsResp.Body.String())
	}

	rejectResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/reject", map[string]any{
		"group": "media",
	}, sessionCookie)
	if rejectResp.Code != http.StatusOK {
		t.Fatalf("proxy/reject with group status=%d body=%s", rejectResp.Code, rejectResp.Body.String())
	}
	rejectPayload := decodeProbeLocalJSON(t, rejectResp)
	selectionObj, ok := rejectPayload["selection"].(map[string]any)
	if !ok {
		t.Fatalf("proxy/reject selection payload type=%T", rejectPayload["selection"])
	}
	if selectionObj["group"] != "media" {
		t.Fatalf("proxy/reject selection group=%v", selectionObj["group"])
	}
	if selectionObj["action"] != "reject" {
		t.Fatalf("proxy/reject selection action=%v", selectionObj["action"])
	}

	stateResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/state", nil, sessionCookie)
	if stateResp.Code != http.StatusOK {
		t.Fatalf("state get status=%d body=%s", stateResp.Code, stateResp.Body.String())
	}
	statePayload := decodeProbeLocalJSON(t, stateResp)
	groups, ok := statePayload["groups"].([]any)
	if !ok {
		t.Fatalf("state groups payload type=%T", statePayload["groups"])
	}
	found := false
	for _, item := range groups {
		entry, _ := item.(map[string]any)
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(entry["group"])), "media") {
			found = true
			if entry["action"] != "reject" {
				t.Fatalf("media action=%v", entry["action"])
			}
			if entry["runtime_status"] != "blocked" {
				t.Fatalf("media runtime_status=%v", entry["runtime_status"])
			}
			if tunnelNodeID, _ := entry["tunnel_node_id"].(string); strings.TrimSpace(tunnelNodeID) != "" {
				t.Fatalf("media tunnel_node_id should be empty on reject, got=%v", entry["tunnel_node_id"])
			}
			break
		}
	}
	if !found {
		t.Fatalf("state groups missing media entry: %v", groups)
	}
}

func TestProbeLocalProxyRejectRejectsUnknownGroup(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/reject", map[string]any{
		"group": "unknown-group",
	}, sessionCookie)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("proxy/reject unknown group status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "not found") {
		t.Fatalf("proxy/reject unknown group error=%q", errText)
	}
}

func TestProbeLocalProxyDirectKeepsSelectedTunnelWhenForwardingDisabled(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	proxyChainPath, err := resolveProbeLocalProxyChainPath()
	if err != nil {
		t.Fatalf("resolve proxy_chain path failed: %v", err)
	}
	proxyChainPayload := `{
  "updated_at": "2026-04-24T00:00:00Z",
  "items": [
    {"chain_id":"chain-proxy-1","chain_type":"proxy_chain","name":"Proxy 1"}
  ]
}`
	if err := os.WriteFile(proxyChainPath, []byte(proxyChainPayload), 0o644); err != nil {
		t.Fatalf("write proxy_chain file failed: %v", err)
	}

	saveGroupsResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/groups/save", map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "default", "rules": []string{"domain_suffix:example.com"}},
			{"group": "media", "rules": []string{"domain_keyword:stream"}},
		},
	}, sessionCookie)
	if saveGroupsResp.Code != http.StatusOK {
		t.Fatalf("groups save status=%d body=%s", saveGroupsResp.Code, saveGroupsResp.Body.String())
	}

	selectResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/select", map[string]any{
		"group":             "media",
		"selected_chain_id": "chain-proxy-1",
	}, sessionCookie)
	if selectResp.Code != http.StatusOK {
		t.Fatalf("proxy/select status=%d body=%s", selectResp.Code, selectResp.Body.String())
	}

	probeLocalRestoreProxyDirect = func() error { return nil }
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	directResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/direct", map[string]any{
		"group": "media",
	}, sessionCookie)
	if directResp.Code != http.StatusOK {
		t.Fatalf("proxy/direct status=%d body=%s", directResp.Code, directResp.Body.String())
	}

	statusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/status", nil, sessionCookie)
	if statusResp.Code != http.StatusOK {
		t.Fatalf("proxy/status status=%d body=%s", statusResp.Code, statusResp.Body.String())
	}
	statusPayload := decodeProbeLocalJSON(t, statusResp)
	if enabled, _ := statusPayload["enabled"].(bool); enabled {
		t.Fatalf("proxy/status enabled should be false")
	}
	if mode, _ := statusPayload["mode"].(string); mode != probeLocalProxyModeDirect {
		t.Fatalf("proxy/status mode=%q", mode)
	}
	if selectedTunnel, _ := statusPayload["selected_tunnel_node_id"].(string); selectedTunnel != "chain:chain-proxy-1" {
		t.Fatalf("proxy/status selected_tunnel_node_id=%q", selectedTunnel)
	}
	if selectedChainID, _ := statusPayload["selected_chain_id"].(string); selectedChainID != "chain-proxy-1" {
		t.Fatalf("proxy/status selected_chain_id=%q", selectedChainID)
	}
}

func TestProbeLocalProxyRejectKeepsSelectedTunnelWhenForwardingBlocked(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	proxyChainPath, err := resolveProbeLocalProxyChainPath()
	if err != nil {
		t.Fatalf("resolve proxy_chain path failed: %v", err)
	}
	proxyChainPayload := `{
  "updated_at": "2026-04-24T00:00:00Z",
  "items": [
    {"chain_id":"chain-proxy-1","chain_type":"proxy_chain","name":"Proxy 1"}
  ]
}`
	if err := os.WriteFile(proxyChainPath, []byte(proxyChainPayload), 0o644); err != nil {
		t.Fatalf("write proxy_chain file failed: %v", err)
	}

	saveGroupsResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/groups/save", map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "default", "rules": []string{"domain_suffix:example.com"}},
			{"group": "media", "rules": []string{"domain_keyword:stream"}},
		},
	}, sessionCookie)
	if saveGroupsResp.Code != http.StatusOK {
		t.Fatalf("groups save status=%d body=%s", saveGroupsResp.Code, saveGroupsResp.Body.String())
	}

	selectResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/select", map[string]any{
		"group":             "media",
		"selected_chain_id": "chain-proxy-1",
	}, sessionCookie)
	if selectResp.Code != http.StatusOK {
		t.Fatalf("proxy/select status=%d body=%s", selectResp.Code, selectResp.Body.String())
	}

	rejectResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/reject", map[string]any{
		"group": "media",
	}, sessionCookie)
	if rejectResp.Code != http.StatusOK {
		t.Fatalf("proxy/reject status=%d body=%s", rejectResp.Code, rejectResp.Body.String())
	}

	statusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/status", nil, sessionCookie)
	if statusResp.Code != http.StatusOK {
		t.Fatalf("proxy/status status=%d body=%s", statusResp.Code, statusResp.Body.String())
	}
	statusPayload := decodeProbeLocalJSON(t, statusResp)
	if enabled, _ := statusPayload["enabled"].(bool); enabled {
		t.Fatalf("proxy/status enabled should be false")
	}
	if mode, _ := statusPayload["mode"].(string); mode != probeLocalProxyModeDirect {
		t.Fatalf("proxy/status mode=%q", mode)
	}
	if selectedTunnel, _ := statusPayload["selected_tunnel_node_id"].(string); selectedTunnel != "chain:chain-proxy-1" {
		t.Fatalf("proxy/status selected_tunnel_node_id=%q", selectedTunnel)
	}
	if selectedChainID, _ := statusPayload["selected_chain_id"].(string); selectedChainID != "chain-proxy-1" {
		t.Fatalf("proxy/status selected_chain_id=%q", selectedChainID)
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
		setProbeLocalProxyRuntimeContext(nodeIdentity{}, "")
	})
	setProbeLocalProxyRuntimeContext(nodeIdentity{NodeID: "node-upgrade-direct", Secret: "secret-direct"}, "")

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

	t.Cleanup(func() { setProbeLocalProxyRuntimeContext(nodeIdentity{}, "") })
	setProbeLocalProxyRuntimeContext(nodeIdentity{NodeID: "node-upgrade-proxy-empty"}, "")

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

func TestProbeLocalSystemUpgradeProxyAccepted(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	upgradeCmdCh := make(chan probeControlMessage, 1)
	probeLocalRunUpgrade = func(cmd probeControlMessage, identity nodeIdentity) {
		upgradeCmdCh <- cmd
	}
	t.Cleanup(func() {
		resetProbeLocalUpgradeHooksForTest()
		setProbeLocalProxyRuntimeContext(nodeIdentity{}, "")
	})
	setProbeLocalProxyRuntimeContext(nodeIdentity{NodeID: "node-upgrade-proxy"}, "  https://controller.example.com/base  ")

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

func TestProbeLocalProxyDirectReturnsNotImplementedOnUnsupported(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalRestoreProxyDirect = func() error {
		return errProbeLocalProxyUnsupported
	}
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/direct", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("proxy/direct unsupported status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "not supported") {
		t.Fatalf("proxy/direct unsupported error=%q", errText)
	}
}

func TestProbeLocalProxyDirectReturnsInternalErrorOnRollbackFailure(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalRestoreProxyDirect = func() error {
		return errors.New("rollback failed for test")
	}
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/direct", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("proxy/direct status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(errText, "rollback failed for test") {
		t.Fatalf("proxy/direct error=%q", errText)
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

func TestProbeLocalProxyGroupsAndStateAndHostsLifecycle(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	groupsResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/groups", nil, sessionCookie)
	if groupsResp.Code != http.StatusOK {
		t.Fatalf("groups get status=%d body=%s", groupsResp.Code, groupsResp.Body.String())
	}
	groupsPayload := decodeProbeLocalJSON(t, groupsResp)
	if int(groupsPayload["version"].(float64)) != 1 {
		t.Fatalf("groups version=%v", groupsPayload["version"])
	}
	groupsArr, ok := groupsPayload["groups"].([]any)
	if !ok || len(groupsArr) == 0 {
		t.Fatalf("groups payload invalid: %v", groupsPayload["groups"])
	}

	saveGroupsResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/groups/save", map[string]any{
		"version": 1,
		"groups": []map[string]any{
			{"group": "default", "rules": []string{"domain_suffix:example.com", "domain_prefix:api."}},
			{"group": "media", "rules": []string{"domain_keyword:stream"}},
		},
	}, sessionCookie)
	if saveGroupsResp.Code != http.StatusOK {
		t.Fatalf("groups save status=%d body=%s", saveGroupsResp.Code, saveGroupsResp.Body.String())
	}

	invalidGroupsResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/groups/save", map[string]any{
		"version": 1,
		"groups":  []map[string]any{{"group": "fallback", "rules": []string{"domain_suffix:x"}}},
	}, sessionCookie)
	if invalidGroupsResp.Code != http.StatusBadRequest {
		t.Fatalf("invalid groups save status=%d body=%s", invalidGroupsResp.Code, invalidGroupsResp.Body.String())
	}

	stateResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/state", nil, sessionCookie)
	if stateResp.Code != http.StatusOK {
		t.Fatalf("state get status=%d body=%s", stateResp.Code, stateResp.Body.String())
	}
	statePayload := decodeProbeLocalJSON(t, stateResp)
	backupObj, ok := statePayload["backup"].(map[string]any)
	if !ok {
		t.Fatalf("state backup payload type=%T", statePayload["backup"])
	}
	if backupObj["last_upload_status"] != "idle" {
		t.Fatalf("state backup status=%v", backupObj["last_upload_status"])
	}

	hostsGetResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/hosts", nil, sessionCookie)
	if hostsGetResp.Code != http.StatusOK {
		t.Fatalf("hosts get status=%d body=%s", hostsGetResp.Code, hostsGetResp.Body.String())
	}
	hostsGetPayload := decodeProbeLocalJSON(t, hostsGetResp)
	if content, _ := hostsGetPayload["content"].(string); !strings.Contains(content, "# dns,ip") {
		t.Fatalf("hosts default content=%q", content)
	}

	hostsSaveResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/hosts/save", map[string]any{
		"content": "# dns,ip\napi.internal.example,10.20.30.40\napi.internal.example,10.20.30.41\ncdn.edge.example,203.0.113.20\n",
	}, sessionCookie)
	if hostsSaveResp.Code != http.StatusOK {
		t.Fatalf("hosts save status=%d body=%s", hostsSaveResp.Code, hostsSaveResp.Body.String())
	}
	hostsSavePayload := decodeProbeLocalJSON(t, hostsSaveResp)
	hostsArr, ok := hostsSavePayload["hosts"].([]any)
	if !ok || len(hostsArr) != 2 {
		t.Fatalf("hosts save hosts payload invalid: %v", hostsSavePayload["hosts"])
	}
	firstHost, _ := hostsArr[0].(map[string]any)
	if firstHost["dns"] != "api.internal.example" || firstHost["ip"] != "10.20.30.41" {
		t.Fatalf("hosts duplicate replacement failed: %v", firstHost)
	}

	invalidHostResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/hosts/save", map[string]any{
		"content": "bad.example,not_an_ip\n",
	}, sessionCookie)
	if invalidHostResp.Code != http.StatusBadRequest {
		t.Fatalf("invalid hosts save status=%d body=%s", invalidHostResp.Code, invalidHostResp.Body.String())
	}
}

func TestProbeLocalProxyGroupsRefreshClearsDNSRuntimeCaches(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{{Group: "media", Rules: []string{"domain_suffix:example.com"}}}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	decision := probeLocalDNSRouteDecision{Group: "media", Action: "tunnel", SelectedChainID: "chain-1"}
	fakeIP, ok := allocateProbeLocalDNSFakeIP("video.example.com", decision)
	if !ok || strings.TrimSpace(fakeIP) == "" {
		t.Fatalf("allocate fake ip failed: ip=%q ok=%v", fakeIP, ok)
	}
	storeProbeLocalDNSCacheRecords("video.example.com", []string{"203.0.113.10"})
	if got := len(queryProbeLocalDNSCacheRecords()); got == 0 {
		t.Fatalf("dns cache should be populated before refresh")
	}
	if got := len(queryProbeLocalDNSFakeIPEntries()); got == 0 {
		t.Fatalf("fake ip cache should be populated before refresh")
	}
	if got := probeLocalDNSRouteHintCount(); got == 0 {
		t.Fatalf("route hint should be populated before refresh")
	}

	refreshResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/groups/refresh", map[string]any{}, sessionCookie)
	if refreshResp.Code != http.StatusOK {
		t.Fatalf("groups refresh status=%d body=%s", refreshResp.Code, refreshResp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, refreshResp)
	if okValue, _ := payload["ok"].(bool); !okValue {
		t.Fatalf("groups refresh ok=%v payload=%v", payload["ok"], payload)
	}
	if got := len(queryProbeLocalDNSCacheRecords()); got != 0 {
		t.Fatalf("dns cache records after refresh=%d, want 0", got)
	}
	if got := len(queryProbeLocalDNSFakeIPEntries()); got != 0 {
		t.Fatalf("fake ip entries after refresh=%d, want 0", got)
	}
	if got := probeLocalDNSRouteHintCount(); got != 0 {
		t.Fatalf("route hint count after refresh=%d, want 0", got)
	}
	dnsObj, ok := payload["dns"].(map[string]any)
	if !ok {
		t.Fatalf("groups refresh dns payload type=%T", payload["dns"])
	}
	if routeHintCount, _ := dnsObj["route_hint_count"].(float64); routeHintCount != 0 {
		t.Fatalf("groups refresh dns route_hint_count=%v", dnsObj["route_hint_count"])
	}
	fakeEntries, ok := dnsObj["fake_ip_entries"].([]any)
	if !ok || len(fakeEntries) != 0 {
		t.Fatalf("groups refresh fake_ip_entries=%v", dnsObj["fake_ip_entries"])
	}
}

func TestProbeLocalProxyChainsRefreshEndpointUsesHook(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	setProbeLocalProxyRuntimeContext(nodeIdentity{NodeID: "node-refresh"}, "https://controller.example")
	probeLocalRefreshProxyChainCache = func(ctx context.Context, identity nodeIdentity, controllerBaseURL string) ([]probeLinkChainServerItem, error) {
		if identity.NodeID != "node-refresh" {
			t.Fatalf("refresh identity node id=%q", identity.NodeID)
		}
		if controllerBaseURL != "https://controller.example" {
			t.Fatalf("refresh controller url=%q", controllerBaseURL)
		}
		return []probeLinkChainServerItem{{ChainID: "chain-refresh", ChainType: "proxy_chain", Name: "Refreshed Chain"}}, nil
	}
	defer resetProbeLocalProxyHooksForTest()

	refreshResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/chains/refresh", map[string]any{}, sessionCookie)
	if refreshResp.Code != http.StatusOK {
		t.Fatalf("chains refresh status=%d body=%s", refreshResp.Code, refreshResp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, refreshResp)
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("chains refresh items invalid: %v", payload["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["chain_id"] != "chain-refresh" {
		t.Fatalf("chains refresh first item=%v", first)
	}
}

func TestProbeLocalProxyPageGetEndpointsDoNotTriggerRemoteRefresh(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	refreshCalls := 0
	latencyCalls := 0
	probeLocalRefreshProxyChainCache = func(ctx context.Context, identity nodeIdentity, controllerBaseURL string) ([]probeLinkChainServerItem, error) {
		refreshCalls++
		return []probeLinkChainServerItem{{ChainID: "chain-remote", ChainType: "proxy_chain", Name: "Remote"}}, nil
	}
	probeLocalResolveGroupRuntimeLatency = func(rt *probeLocalTUNGroupRuntime) (string, *int64, string, string) {
		latencyCalls++
		value := int64(123)
		return "connected", &value, "2026-05-15T03:00:00Z", ""
	}
	defer resetProbeLocalProxyHooksForTest()

	if err := persistProbeLocalProxyGroupFile(probeLocalProxyGroupFile{
		Version: 1,
		Groups: []probeLocalProxyGroupEntry{
			{Group: "default", Rules: []string{"domain_suffix:example.com"}},
		},
	}); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	if err := persistProbeLocalProxyStateFile(probeLocalProxyStateFile{
		Version:   1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Groups: []probeLocalProxyStateGroupEntry{
			{Group: "default", Action: "tunnel", SelectedChainID: "chain-local", TunnelNodeID: "chain:chain-local", RuntimeStatus: "online"},
		},
		Backup: probeLocalProxyBackupState{LastUploadStatus: "idle"},
		TUN:    probeLocalTUNPersistentState{UpdatedAt: time.Now().UTC().Format(time.RFC3339)},
	}); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}
	proxyChainPath, err := resolveProbeLocalProxyChainPath()
	if err != nil {
		t.Fatalf("resolve proxy chain path failed: %v", err)
	}
	if err := os.WriteFile(proxyChainPath, []byte(`{"updated_at":"2026-05-15T00:00:00Z","items":[{"chain_id":"chain-local","chain_type":"proxy_chain","name":"Local"}]}`), 0o644); err != nil {
		t.Fatalf("write proxy chain file failed: %v", err)
	}
	if err := ensureProbeLocalProxyDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults failed: %v", err)
	}

	resp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/groups", nil, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("groups status=%d body=%s", resp.Code, resp.Body.String())
	}
	resp = doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/chains", nil, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("chains status=%d body=%s", resp.Code, resp.Body.String())
	}
	resp = doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/state", nil, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("state status=%d body=%s", resp.Code, resp.Body.String())
	}
	resp = doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/status", nil, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("status status=%d body=%s", resp.Code, resp.Body.String())
	}

	if refreshCalls != 0 {
		t.Fatalf("refreshCalls=%d, want 0", refreshCalls)
	}
	if latencyCalls != 0 {
		t.Fatalf("latencyCalls=%d, want 0", latencyCalls)
	}
}

func TestProbeLocalProxyStatusRefreshUpdatesGroupLatencySnapshots(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	if err := persistProbeLocalProxyStateFile(probeLocalProxyStateFile{
		Version:   1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Groups: []probeLocalProxyStateGroupEntry{
			{Group: "default", Action: "tunnel", SelectedChainID: "chain-local", TunnelNodeID: "chain:chain-local", RuntimeStatus: "online"},
		},
		Backup: probeLocalProxyBackupState{LastUploadStatus: "idle"},
		TUN:    probeLocalTUNPersistentState{UpdatedAt: time.Now().UTC().Format(time.RFC3339)},
	}); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	proxyChainPath, err := resolveProbeLocalProxyChainPath()
	if err != nil {
		t.Fatalf("resolve proxy chain path failed: %v", err)
	}
	if err := os.WriteFile(proxyChainPath, []byte(`{"updated_at":"2026-05-15T00:00:00Z","items":[{"chain_id":"chain-local","chain_type":"proxy_chain","name":"Local"}]}`), 0o644); err != nil {
		t.Fatalf("write proxy chain file failed: %v", err)
	}
	if err := ensureProbeLocalProxyDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults failed: %v", err)
	}

	latencyCalls := 0
	probeLocalResolveGroupRuntimeLatency = func(rt *probeLocalTUNGroupRuntime) (string, *int64, string, string) {
		latencyCalls++
		value := int64(523)
		return "connected", &value, "2026-05-15T02:55:58Z", ""
	}
	defer resetProbeLocalProxyHooksForTest()

	stateResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/state", nil, sessionCookie)
	if stateResp.Code != http.StatusOK {
		t.Fatalf("state before refresh status=%d body=%s", stateResp.Code, stateResp.Body.String())
	}
	statePayload := decodeProbeLocalJSON(t, stateResp)
	groups, ok := statePayload["groups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("state before refresh groups=%v", statePayload["groups"])
	}
	entry, _ := groups[0].(map[string]any)
	if _, exists := entry["selected_chain_latency_ms"]; exists {
		t.Fatalf("latency should not exist before manual refresh: %v", entry)
	}
	if latencyCalls != 0 {
		t.Fatalf("latencyCalls before refresh=%d, want 0", latencyCalls)
	}

	refreshResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/status/refresh", map[string]any{}, sessionCookie)
	if refreshResp.Code != http.StatusOK {
		t.Fatalf("status refresh status=%d body=%s", refreshResp.Code, refreshResp.Body.String())
	}
	if latencyCalls != 1 {
		t.Fatalf("latencyCalls after refresh=%d, want 1", latencyCalls)
	}

	stateResp = doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/state", nil, sessionCookie)
	if stateResp.Code != http.StatusOK {
		t.Fatalf("state after refresh status=%d body=%s", stateResp.Code, stateResp.Body.String())
	}
	statePayload = decodeProbeLocalJSON(t, stateResp)
	groups, ok = statePayload["groups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("state after refresh groups=%v", statePayload["groups"])
	}
	entry, _ = groups[0].(map[string]any)
	if entry["selected_chain_latency_status"] != "reachable" {
		t.Fatalf("latency status=%v", entry["selected_chain_latency_status"])
	}
	if latencyMS, ok := entry["selected_chain_latency_ms"].(float64); !ok || int64(latencyMS) != 523 {
		t.Fatalf("latency ms=%v", entry["selected_chain_latency_ms"])
	}
	if entry["selected_chain_latency_updated_at"] != "2026-05-15T02:55:58Z" {
		t.Fatalf("latency updated_at=%v", entry["selected_chain_latency_updated_at"])
	}
}

func TestProbeLocalProxyChainsAndBackupEndpoints(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	proxyChainPath, err := resolveProbeLocalProxyChainPath()
	if err != nil {
		t.Fatalf("resolve proxy_chain path failed: %v", err)
	}
	proxyChainPayload := `{
	  "updated_at": "2026-04-24T00:00:00Z",
	  "items": [
	    {"chain_id":"chain-proxy-1","chain_type":"proxy_chain","name":"Proxy 1"},
	    {"chain_id":"chain-forward-1","chain_type":"port_forward","name":"Forward 1"}
	  ]
	}`
	if err := os.WriteFile(proxyChainPath, []byte(proxyChainPayload), 0o644); err != nil {
		t.Fatalf("write proxy_chain file failed: %v", err)
	}

	chainsResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/chains", nil, sessionCookie)
	if chainsResp.Code != http.StatusOK {
		t.Fatalf("proxy chains status=%d body=%s", chainsResp.Code, chainsResp.Body.String())
	}
	chainsPayload := decodeProbeLocalJSON(t, chainsResp)
	items, ok := chainsPayload["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("proxy chains payload invalid: %v", chainsPayload["items"])
	}
	chainObj, _ := items[0].(map[string]any)
	if chainObj["chain_type"] != "proxy_chain" {
		t.Fatalf("proxy chains filter failed: %v", chainObj)
	}

	backupNoControllerResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/groups/backup", map[string]any{}, sessionCookie)
	if backupNoControllerResp.Code != http.StatusConflict {
		t.Fatalf("backup without controller status=%d body=%s", backupNoControllerResp.Code, backupNoControllerResp.Body.String())
	}
}

func TestProbeLocalProxyGroupsBackupEndpointSuccess(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	setProbeLocalProxyRuntimeContext(nodeIdentity{}, "")
	t.Cleanup(func() { setProbeLocalProxyRuntimeContext(nodeIdentity{}, "") })

	if _, err := loadProbeLocalProxyGroupFile(); err != nil {
		t.Fatalf("prepare proxy_group file failed: %v", err)
	}

	reqBodyCh := make(chan map[string]any, 1)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != probeLocalProxyBackupAPIPath {
			t.Fatalf("backup path=%q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("backup method=%q", r.Method)
		}
		payload := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode controller request body failed: %v", err)
		}
		reqBodyCh <- payload
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer controller.Close()

	setProbeLocalProxyRuntimeContext(nodeIdentity{NodeID: "node-backup-success"}, "  "+controller.URL+"  ")
	ctx := currentProbeLocalProxyRuntimeContext()
	if ctx.Identity.NodeID != "node-backup-success" {
		t.Fatalf("runtime context node id=%q", ctx.Identity.NodeID)
	}
	if ctx.ControllerBaseURL != controller.URL {
		t.Fatalf("runtime context controller base url=%q want=%q", ctx.ControllerBaseURL, controller.URL)
	}

	backupResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/groups/backup", map[string]any{}, sessionCookie)
	if backupResp.Code != http.StatusOK {
		t.Fatalf("backup status=%d body=%s", backupResp.Code, backupResp.Body.String())
	}

	var controllerPayload map[string]any
	select {
	case controllerPayload = <-reqBodyCh:
	default:
		t.Fatalf("controller did not receive backup request")
	}
	if controllerPayload["file_name"] != probeLocalProxyGroupFileName {
		t.Fatalf("backup file_name=%v", controllerPayload["file_name"])
	}
	if controllerPayload["node_id"] != "node-backup-success" {
		t.Fatalf("backup node_id=%v", controllerPayload["node_id"])
	}
	contentBase64, _ := controllerPayload["content_base64"].(string)
	if strings.TrimSpace(contentBase64) == "" {
		t.Fatalf("backup content_base64 is empty")
	}
	decoded, err := base64.StdEncoding.DecodeString(contentBase64)
	if err != nil {
		t.Fatalf("decode content_base64 failed: %v", err)
	}
	if !strings.Contains(string(decoded), `"groups"`) {
		t.Fatalf("backup payload content mismatch: %q", string(decoded))
	}

	stateResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/state", nil, sessionCookie)
	if stateResp.Code != http.StatusOK {
		t.Fatalf("state status=%d body=%s", stateResp.Code, stateResp.Body.String())
	}
	statePayload := decodeProbeLocalJSON(t, stateResp)
	backupObj, ok := statePayload["backup"].(map[string]any)
	if !ok {
		t.Fatalf("state backup payload type=%T", statePayload["backup"])
	}
	if backupObj["last_upload_status"] != "ok" {
		t.Fatalf("backup status=%v", backupObj["last_upload_status"])
	}
	if uploadedAt, _ := backupObj["last_uploaded_at"].(string); strings.TrimSpace(uploadedAt) == "" {
		t.Fatalf("backup uploaded_at should not be empty")
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

	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		t.Fatalf("load proxy state failed: %v", err)
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
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		t.Fatalf("load proxy state failed: %v", err)
	}
	if !state.TUN.Installed || state.TUN.Enabled {
		t.Fatalf("persisted tun state=%+v, want installed=true enabled=false", state.TUN)
	}
}

func TestProbeLocalTUNStartupRecoveryClearsStaleInstalledState(t *testing.T) {
	_ = setupProbeLocalConsoleTest(t)
	if err := persistProbeLocalTUNPersistentState(true, true); err != nil {
		t.Fatalf("persist tun state failed: %v", err)
	}

	detectCalls := 0
	probeLocalDetectTUNInstalled = func() (bool, error) {
		detectCalls++
		return false, nil
	}
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest() })

	if err := recoverProbeLocalTUNRuntimeOnStartup(); err != nil {
		t.Fatalf("recoverProbeLocalTUNRuntimeOnStartup returned error: %v", err)
	}
	if detectCalls != 1 {
		t.Fatalf("detect calls=%d, want 1", detectCalls)
	}
	status := probeLocalControl.tunStatus()
	if status.Installed || status.Enabled {
		t.Fatalf("startup recovery tun status=%+v, want installed=false enabled=false", status)
	}
	if !strings.Contains(strings.ToLower(status.LastError), "not available") {
		t.Fatalf("startup recovery last error=%q, want not available", status.LastError)
	}
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		t.Fatalf("load proxy state failed: %v", err)
	}
	if state.TUN.Installed || state.TUN.Enabled {
		t.Fatalf("persisted tun state=%+v, want installed=false enabled=false", state.TUN)
	}
}

func TestProbeLocalTUNStartupRecoveryRestoresPersistedEnabledState(t *testing.T) {
	_ = setupProbeLocalConsoleTest(t)
	if err := persistProbeLocalTUNPersistentState(true, true); err != nil {
		t.Fatalf("persist tun state failed: %v", err)
	}

	probeLocalDetectTUNInstalled = func() (bool, error) { return true, nil }
	takeoverCalls := 0
	probeLocalApplyProxyTakeover = func() error {
		takeoverCalls++
		return nil
	}
	probeLocalApplyTUNPrimaryDNS = func() error { return nil }
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest(); resetProbeLocalProxyHooksForTest() })

	if err := recoverProbeLocalTUNRuntimeOnStartup(); err != nil {
		t.Fatalf("recoverProbeLocalTUNRuntimeOnStartup returned error: %v", err)
	}
	if takeoverCalls != 1 {
		t.Fatalf("takeover calls=%d, want 1", takeoverCalls)
	}
	status := probeLocalControl.tunStatus()
	if !status.Installed || !status.Enabled {
		t.Fatalf("startup recovery tun status=%+v, want installed=true enabled=true", status)
	}
	proxyStatus := probeLocalControl.proxyStatus()
	if !proxyStatus.Enabled || proxyStatus.Mode != probeLocalProxyModeTUN {
		t.Fatalf("startup recovery proxy status=%+v, want enabled tunnel", proxyStatus)
	}
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		t.Fatalf("load proxy state failed: %v", err)
	}
	if !state.TUN.Installed || !state.TUN.Enabled {
		t.Fatalf("persisted tun state=%+v, want installed=true enabled=true", state.TUN)
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

func TestEnsureProbeLocalProxyDefaultsInitializedCreatesFiles(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	groupPath, err := resolveProbeLocalProxyGroupPath()
	if err != nil {
		t.Fatalf("resolve group path failed: %v", err)
	}
	statePath, err := resolveProbeLocalProxyStatePath()
	if err != nil {
		t.Fatalf("resolve state path failed: %v", err)
	}
	hostPath, err := resolveProbeLocalProxyHostPath()
	if err != nil {
		t.Fatalf("resolve host path failed: %v", err)
	}

	if _, err := os.Stat(groupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("group file should not exist before init, err=%v", err)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file should not exist before init, err=%v", err)
	}
	if _, err := os.Stat(hostPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host file should not exist before init, err=%v", err)
	}

	if err := ensureProbeLocalProxyDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults failed: %v", err)
	}
	if err := ensureProbeLocalProxyDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults second call failed: %v", err)
	}

	if _, err := os.Stat(groupPath); err != nil {
		t.Fatalf("group file should exist after init: %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state file should exist after init: %v", err)
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
