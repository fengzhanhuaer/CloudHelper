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
	probeLocalTUNRouteFeatureEnabled = func() bool { return true }
	t.Cleanup(func() {
		waitProbeVirtualRouterLocalInterfaceIPEnsure()
		resetProbeLocalAuthManagerForTest()
		resetProbeLocalControlStateForTest()
		resetProbeLocalDNSServiceForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
		resetprobeLocalRouteHooksForTest()
		resetProbeLocalTUNHooksForTest()
		resetProbeLocalUpgradeHooksForTest()
		setprobeLocalRouteRuntimeContext(nodeIdentity{}, "")
		probeLocalTUNRouteFeatureEnabled = func() bool { return false }
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
	setupToken, err := ensureProbeLocalSetupToken()
	if err != nil {
		t.Fatalf("ensure setup token failed: %v", err)
	}
	registerResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/register", map[string]any{
		"username":         username,
		"password":         password,
		"confirm_password": password,
		"setup_token":      setupToken,
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

	setupToken, err := ensureProbeLocalSetupToken()
	if err != nil {
		t.Fatalf("ensure setup token failed: %v", err)
	}
	registerResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/register", map[string]any{
		"username":         "admin",
		"password":         "secret1234",
		"confirm_password": "secret1234",
		"setup_token":      setupToken,
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

func TestProbeLocalVirtualRouterPacketsHandlerReturnsRecentPackets(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "198.18.0.21", 49152, 443)
	recordProbeVirtualRouterRecentPacket("tun_rx", "forward", nil, packet, []string{"16", "19"}, false, nil)

	resp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/virtual_router/packets", nil, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("packets status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items=%T %v", payload["items"], payload["items"])
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("first item=%T %v", items[0], items[0])
	}
	if first["source"] != "tun_rx" || first["action"] != "forward" || first["protocol"] != "TCP" {
		t.Fatalf("unexpected packet item=%+v", first)
	}
	if first["tcp_flags"] != "SYN" {
		t.Fatalf("unexpected packet tcp flags=%+v", first)
	}
	if first["source_ip"] != "198.18.0.18" || first["destination_ip"] != "198.18.0.21" {
		t.Fatalf("unexpected packet tuple=%+v", first)
	}
}

func TestProbeLocalVirtualRouterConnectionsHandlerReturnsRecentConnections(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	forward := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.18", "198.18.4.9", 49152, 443)
	reply := buildProbeVirtualRouterTestTCPPacket(t, "198.18.4.9", "198.18.0.18", 443, 49152)
	recordProbeVirtualRouterRecentPacket("tun_rx", "forward", nil, forward, []string{"16", "19"}, false, nil)
	recordProbeVirtualRouterRecentPacket("frame_rx", "deliver", nil, reply, []string{"19", "16"}, true, nil)

	resp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/virtual_router/connections", nil, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("connections status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items=%T %v", payload["items"], payload["items"])
	}
	connection, ok := items[0].(map[string]any)
	if !ok || connection["events"] != float64(2) || connection["status"] != "active" || connection["traffic_type"] != "proxy" || connection["endpoint_a"] != "198.18.0.18:49152" || connection["endpoint_b"] != "198.18.4.9:443" || payload["retention_seconds"] != float64(300) {
		t.Fatalf("unexpected connection item=%+v", connection)
	}
}

func TestProbeLocalVirtualRouterDomainObservationsHandler(t *testing.T) {
	resetProbeDomainObservations()
	t.Cleanup(resetProbeDomainObservations)
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	recordProbeDomainObservation("ads.example", "dns", "192.168.51.20:53001", "reject", nil, nil)
	recordProbeDomainObservation("ads.example", "sni", "192.168.51.20", "reject", nil, nil)
	recordProbeDomainObservation("ads.example", "quic", "192.168.51.20", "reject", nil, nil)

	resp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/virtual_router/domain_observations", nil, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("domain observations status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items=%T %v", payload["items"], payload["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok || item["domain"] != "ads.example" || item["events"] != float64(3) || item["dns_queries"] != float64(1) || item["sni_observations"] != float64(1) || item["quic_observations"] != float64(1) {
		t.Fatalf("unexpected domain observation=%+v", item)
	}
	sources, ok := payload["sources"].([]any)
	if !ok || len(sources) != 1 || sources[0] != "192.168.51.20" {
		t.Fatalf("unexpected sources=%v", payload["sources"])
	}

	resp = doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/virtual_router/domain_observations", map[string]any{"domain": "ads.example", "status": "allowed"}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("allow domain status=%d body=%s", resp.Code, resp.Body.String())
	}
	observations, _, err := snapshotProbeDomainObservations()
	if err != nil || len(observations) != 1 || observations[0].Status != "allowed" {
		t.Fatalf("allowed items=%+v err=%v", observations, err)
	}
}

func TestProbeLocalVirtualRouterStatusHandlerReturnsRuntimeDebugState(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeVirtualRouterLocalSettingsForTest)
	t.Cleanup(func() { closeProbeVirtualRouterFrameLinks("test cleanup") })
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", DisplayName: "本机", IP: "198.18.0.16"},
			{NodeID: "19", DisplayName: "东京出口", IP: "198.18.0.19"},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "api.example.com",
				FakeIP:     "198.18.4.9",
				Action:     "probe_exit",
				ExitNodeID: "19",
			}},
		},
	}
	probeVirtualRouterState.localNodeID = "16"
	probeVirtualRouterState.localIP = "198.18.0.16"
	probeVirtualRouterState.nodeToIP = map[string]string{"16": "198.18.0.16", "19": "198.18.0.19"}
	probeVirtualRouterState.ipToNode = map[string]string{"198.18.0.16": "16", "198.18.0.19": "19"}
	probeVirtualRouterState.mu.Unlock()
	enableProbeVirtualRouterLocalSettingsForTest(true, true)
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	oldStats := probeVirtualRouterRuntimeStatsState.items
	probeVirtualRouterRuntimeStatsState.items = make(map[string]*probeVirtualRouterRuntimeStats)
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		probeVirtualRouterRuntimeStatsState.items = oldStats
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
	})

	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{
		routeID:     "vrouter-16-19",
		name:        "edge",
		localNodeID: "16",
		peerNodeID:  "19",
		peerName:    "东京出口",
		fromNodeID:  "16",
		toNodeID:    "19",
		localIP:     "198.18.0.16",
		peerIP:      "198.18.0.19",
		peerHost:    "edge.example.com",
		peerPort:    12040,
		routeLayer:  "auto",
		dialer:      true,
	}}
	probeRouteRelayProtocolStateStore.mu.Lock()
	oldProtocolItems := probeRouteRelayProtocolStateStore.items
	probeRouteRelayProtocolStateStore.items = map[string]*probeRouteRelayProtocolState{
		probeRouteRelayProtocolEndpointKey("edge.example.com", 12040): &probeRouteRelayProtocolState{
			SelectedProtocol: "websocket",
			SelectionReason:  "test",
			UpdatedAt:        time.Now(),
			Qualities:        map[string]probeRouteRelayProtocolQuality{},
		},
	}
	probeRouteRelayProtocolStateStore.mu.Unlock()
	t.Cleanup(func() {
		probeRouteRelayProtocolStateStore.mu.Lock()
		probeRouteRelayProtocolStateStore.items = oldProtocolItems
		probeRouteRelayProtocolStateStore.mu.Unlock()
	})
	left, right := net.Pipe()
	defer right.Close()
	defer left.Close()
	link := newProbeVirtualRouterFrameLink(probeVirtualRouterFrameLinkKey(rt, "", "", nil), rt, nil, []string{"16", "19"})
	link.AttachCarrier(left, "status-carrier", "198.51.100.19:12040")
	probeVirtualRouterRuntimeState.mu.Lock()
	oldRuntimes := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{rt.cfg.routeID: rt}
	probeVirtualRouterRuntimeState.mu.Unlock()
	probeVirtualRouterFrameLinkState.mu.Lock()
	oldLinks := probeVirtualRouterFrameLinkState.links
	probeVirtualRouterFrameLinkState.links = map[string]*probeVirtualRouterFrameLink{link.key: link}
	probeVirtualRouterFrameLinkState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = oldRuntimes
		probeVirtualRouterRuntimeState.mu.Unlock()
		probeVirtualRouterFrameLinkState.mu.Lock()
		probeVirtualRouterFrameLinkState.links = oldLinks
		probeVirtualRouterFrameLinkState.mu.Unlock()
	})
	recordProbeVirtualRouterRuntimeFrameSent(rt, 128)
	recordProbeVirtualRouterRuntimePacketForwarded(rt, 128)
	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.16", "198.18.4.9", 49152, 443)
	recordProbeVirtualRouterRecentPacket("tun_rx", "forward", rt, packet, []string{"16", "19"}, false, nil)

	resp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/virtual_router/status", nil, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("status status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	if payload["local_node_id"] != "16" || payload["local_ip"] != "198.18.0.16" {
		t.Fatalf("unexpected status identity: %+v", payload)
	}
	summary, ok := payload["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary=%T %v", payload["summary"], payload["summary"])
	}
	if summary["runtime_count"] != float64(1) || summary["carrier_count"] != float64(1) {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	runtimes, ok := payload["runtimes"].([]any)
	if !ok || len(runtimes) != 1 {
		t.Fatalf("runtimes=%T %v", payload["runtimes"], payload["runtimes"])
	}
	firstRuntime, ok := runtimes[0].(map[string]any)
	if !ok || firstRuntime["peer_node_name"] != "东京出口" || firstRuntime["route_layer"] != "auto" || firstRuntime["selected_protocol"] != "websocket" || firstRuntime["protocol_text"] != "auto -> websocket" {
		t.Fatalf("unexpected runtime protocol state: %+v", firstRuntime)
	}
	links, ok := payload["frame_links"].([]any)
	if !ok || len(links) != 1 {
		t.Fatalf("frame_links=%T %v", payload["frame_links"], payload["frame_links"])
	}
	firstLink, ok := links[0].(map[string]any)
	if !ok || firstLink["carrier"] != true || firstLink["carrier_session_id"] != "status-carrier" {
		t.Fatalf("unexpected frame link: %+v", firstLink)
	}
}

func TestProbeLocalVirtualRouterBuffersHandlerReturnsAndResetsStats(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	queue := newProbeAdaptiveQueue[int](probeAdaptiveQueueOptions{
		ID:              "local-console-buffer-test",
		Stage:           "test_stage",
		Direction:       "tx",
		RouteID:         "test-route",
		InitialCapacity: 1,
		MaxCapacity:     4,
	})
	t.Cleanup(queue.Close)
	if !queue.TryPush(1) || !queue.TryPush(2) {
		t.Fatal("failed to seed adaptive queue")
	}

	resp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/virtual_router/buffers", nil, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("buffers status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	item := findProbeLocalBufferItem(t, payload, "local-console-buffer-test")
	if item["depth"] != float64(2) || item["allocated_capacity"] != float64(2) || item["peak_depth"] != float64(2) {
		t.Fatalf("unexpected buffer snapshot: %+v", item)
	}

	resetResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/virtual_router/buffers", map[string]any{"action": "reset"}, sessionCookie)
	if resetResp.Code != http.StatusOK {
		t.Fatalf("buffer reset status=%d body=%s", resetResp.Code, resetResp.Body.String())
	}
	resetPayload := decodeProbeLocalJSON(t, resetResp)
	resetItem := findProbeLocalBufferItem(t, resetPayload, "local-console-buffer-test")
	if resetItem["depth"] != float64(2) || resetItem["peak_depth"] != float64(2) || resetItem["enqueued"] != float64(0) || resetItem["grow_events"] != float64(0) {
		t.Fatalf("unexpected reset buffer snapshot: %+v", resetItem)
	}

	badResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/virtual_router/buffers", map[string]any{"action": "clear"}, sessionCookie)
	if badResp.Code != http.StatusBadRequest {
		t.Fatalf("invalid buffer action status=%d body=%s", badResp.Code, badResp.Body.String())
	}
}

func findProbeLocalBufferItem(t *testing.T, payload map[string]any, id string) map[string]any {
	t.Helper()
	items, ok := payload["items"].([]any)
	if !ok {
		t.Fatalf("buffer items=%T %v", payload["items"], payload["items"])
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if ok && item["id"] == id {
			return item
		}
	}
	t.Fatalf("buffer %q not found in %+v", id, items)
	return nil
}

func TestProbeLocalVirtualRouterSettingsHandlerConfiguresProxy(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	oldSet := probeVRouteSystemProxySet
	oldRestore := probeVRouteSystemProxyRestore
	oldDNSRestore := probeVirtualRouterRestoreSystemDNS
	t.Cleanup(func() {
		probeVRouteSystemProxySet = oldSet
		probeVRouteSystemProxyRestore = oldRestore
		probeVirtualRouterRestoreSystemDNS = oldDNSRestore
	})
	applied := make(chan [2]string, 1)
	probeVRouteSystemProxySet = func(httpAddress string, socks5Address string) error {
		applied <- [2]string{httpAddress, socks5Address}
		return nil
	}
	probeVRouteSystemProxyRestore = func() error { return nil }
	probeVirtualRouterRestoreSystemDNS = func() error {
		t.Fatal("proxy-only settings must not change system DNS")
		return nil
	}
	t.Cleanup(stopProbeVRouteProxyRuntime)
	httpListen := reserveProbeVRouteProxyTCPAddress(t)
	socks5Listen := reserveProbeVRouteProxyTCPUDPAddress(t)

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/virtual_router/settings", map[string]any{
		"proxy_enabled":       true,
		"http_proxy_listen":   httpListen,
		"socks5_proxy_listen": socks5Listen,
		"proxy_username":      "proxy-user",
		"proxy_password":      "proxy-secret",
	}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("proxy settings status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	if payload["proxy_enabled"] != true || payload["http_proxy_listen"] != httpListen || payload["socks5_proxy_listen"] != socks5Listen {
		t.Fatalf("unexpected proxy settings payload=%+v", payload)
	}
	if payload["proxy_password_configured"] != true {
		t.Fatalf("proxy password configured=%v", payload["proxy_password_configured"])
	}
	if _, exposed := payload["proxy_password"]; exposed {
		t.Fatal("proxy settings response exposed password")
	}
	select {
	case addresses := <-applied:
		if addresses != [2]string{httpListen, socks5Listen} {
			t.Fatalf("applied system proxy addresses=%v", addresses)
		}
	default:
		t.Fatal("proxy settings did not apply system proxy")
	}

	getResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/virtual_router/settings", nil, sessionCookie)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get proxy settings status=%d body=%s", getResp.Code, getResp.Body.String())
	}
	getPayload := decodeProbeLocalJSON(t, getResp)
	if getPayload["proxy_username"] != "proxy-user" || getPayload["proxy_password_configured"] != true {
		t.Fatalf("unexpected saved proxy auth payload=%+v", getPayload)
	}
	if _, exposed := getPayload["proxy_password"]; exposed {
		t.Fatal("saved proxy settings response exposed password")
	}
}

func TestProbeLocalVirtualRouterRouteTestHandlerReturnsExitReachability(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port

	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.16", ServicePort: 12040},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Version: 1,
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "localhost",
				FakeIP:     "198.18.2.1",
				Action:     "probe_exit",
				ExitNodeID: "16",
			}},
		},
	}
	probeVirtualRouterState.localNodeID = "16"
	probeVirtualRouterState.localIP = "198.18.0.16"
	probeVirtualRouterState.nodeToIP = map[string]string{"16": "198.18.0.16"}
	probeVirtualRouterState.ipToNode = map[string]string{"198.18.0.16": "16"}
	probeVirtualRouterState.mu.Unlock()

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/virtual_router/route_test", map[string]any{
		"target": "198.18.2.1",
		"port":   port,
	}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("route test status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	if payload["ok"] != true {
		t.Fatalf("route test not ok: %+v", payload)
	}
	if payload["exit_node_id"] != "16" || payload["fake_ip"] != "198.18.2.1" {
		t.Fatalf("unexpected route test identity: %+v", payload)
	}
	if _, exists := payload["samples"]; exists {
		t.Fatalf("ordinary route test exposed detailed diagnostic fields: %+v", payload)
	}
	items, ok := payload["results"].([]any)
	if !ok || len(items) < 2 {
		t.Fatalf("route test results=%T %v", payload["results"], payload["results"])
	}
	last, ok := items[len(items)-1].(map[string]any)
	if !ok || last["stage"] != "exit" || last["ok"] != true {
		t.Fatalf("unexpected final route test result: %+v", last)
	}
}

func TestProbeLocalVirtualRouterRouteTestHandlerReturnsCurlResult(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port

	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.16", ServicePort: 12040},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Version: 1,
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "localhost",
				FakeIP:     "198.18.2.1",
				Action:     "probe_exit",
				ExitNodeID: "16",
			}},
		},
	}
	probeVirtualRouterState.localNodeID = "16"
	probeVirtualRouterState.localIP = "198.18.0.16"
	probeVirtualRouterState.nodeToIP = map[string]string{"16": "198.18.0.16"}
	probeVirtualRouterState.ipToNode = map[string]string{"198.18.0.16": "16"}
	probeVirtualRouterState.mu.Unlock()

	oldLookPath := probeVirtualRouterRouteTestLookPath
	oldRunCurl := probeVirtualRouterRouteTestRunCurlCommand
	var capturedArgs []string
	probeVirtualRouterRouteTestLookPath = func(name string) (string, error) {
		if name != "curl" {
			t.Fatalf("unexpected curl binary lookup: %q", name)
		}
		return "curl", nil
	}
	probeVirtualRouterRouteTestRunCurlCommand = func(ctx context.Context, curlPath string, args []string) ([]byte, error) {
		if curlPath != "curl" {
			t.Fatalf("unexpected curl path: %q", curlPath)
		}
		capturedArgs = append([]string(nil), args...)
		return []byte("\nhttp_code=200\nremote_ip=127.0.0.1\nremote_port=443\nurl_effective=https://www.localhost/\nnum_redirects=1\ntime_total=0.123\n"), nil
	}
	t.Cleanup(func() {
		probeVirtualRouterRouteTestLookPath = oldLookPath
		probeVirtualRouterRouteTestRunCurlCommand = oldRunCurl
	})

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/virtual_router/route_test/curl", map[string]any{
		"target": "198.18.2.1",
		"port":   port,
	}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("route test curl status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	if payload["ok"] != true {
		t.Fatalf("route test curl not ok: %+v", payload)
	}
	items, ok := payload["results"].([]any)
	if !ok || len(items) < 3 {
		t.Fatalf("route test curl results=%T %v", payload["results"], payload["results"])
	}
	last, ok := items[len(items)-1].(map[string]any)
	if !ok || last["stage"] != "curl" || last["ok"] != true || last["http_status"] != float64(200) {
		t.Fatalf("unexpected final curl route test result: %+v", last)
	}
	if !strings.Contains(fmt.Sprint(last["curl_url"]), "https://localhost:") {
		t.Fatalf("unexpected curl url: %+v", last)
	}
	argsText := strings.Join(capturedArgs, " ")
	if !strings.Contains(argsText, "--location") || !strings.Contains(argsText, "--noproxy *") || !strings.Contains(argsText, "https://localhost:") {
		t.Fatalf("unexpected curl args: %v", capturedArgs)
	}
}

func TestProbeLocalVirtualRouterDiagnosticHandlerReturnsRemoteTimingsAndState(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.16", ServicePort: 12040},
		},
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:         "rr-google",
			Name:       "Google",
			Action:     "probe_exit",
			ExitNodeID: "16",
			Entries:    []string{"domain_suffix:google.com"},
		}},
	}
	probeVirtualRouterState.localNodeID = "16"
	probeVirtualRouterState.localIP = "198.18.0.16"
	probeVirtualRouterState.nodeToIP = map[string]string{"16": "198.18.0.16"}
	probeVirtualRouterState.ipToNode = map[string]string{"198.18.0.16": "16"}
	probeVirtualRouterState.mu.Unlock()

	oldRunner := probeVirtualRouterRouteDiagnosticHTTPRun
	probeVirtualRouterRouteDiagnosticHTTPRun = func(msg probeVirtualRouterRouteTestPayload, index int, timeout time.Duration) probeVirtualRouterRouteDiagnosticSample {
		if msg.Domain != "mail.google.com" || msg.ExitNodeID != "16" {
			t.Fatalf("unexpected diagnostic message: %+v", msg)
		}
		if timeout != 2*time.Second {
			t.Fatalf("sample timeout=%s, want 2s", timeout)
		}
		return probeVirtualRouterRouteDiagnosticSample{
			Index:               index,
			OK:                  true,
			ResolvedIPs:         []string{"142.250.72.69"},
			CheckedAddress:      "142.250.72.69:443",
			HTTPStatus:          http.StatusMovedPermanently,
			HTTPProtocol:        "HTTP/2.0",
			DNSMS:               12,
			TCPConnectMS:        145,
			TLSHandshakeMS:      180,
			FirstResponseByteMS: 410,
			TotalMS:             425,
			BodyBytes:           1024,
		}
	}
	t.Cleanup(func() { probeVirtualRouterRouteDiagnosticHTTPRun = oldRunner })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/virtual_router/diagnostic", map[string]any{
		"target":     "mail.google.com",
		"port":       443,
		"timeout_ms": 5000,
		"samples":    2,
	}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("diagnostic status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	if payload["ok"] != true || payload["detailed"] != true || payload["samples"] != float64(2) {
		t.Fatalf("unexpected diagnostic payload: %+v", payload)
	}
	items, ok := payload["results"].([]any)
	if !ok || len(items) < 2 {
		t.Fatalf("diagnostic results=%T %v", payload["results"], payload["results"])
	}
	last, ok := items[len(items)-1].(map[string]any)
	if !ok || last["stage"] != "exit_diagnostic" || last["ok"] != true {
		t.Fatalf("unexpected diagnostic final result: %+v", last)
	}
	samples, ok := last["diagnostic_samples"].([]any)
	if !ok || len(samples) != 2 {
		t.Fatalf("unexpected diagnostic samples: %+v", last["diagnostic_samples"])
	}
	first, _ := samples[0].(map[string]any)
	if first["dns_ms"] != float64(12) || first["tcp_connect_ms"] != float64(145) || first["tls_handshake_ms"] != float64(180) || first["first_response_byte_ms"] != float64(410) {
		t.Fatalf("unexpected timing sample: %+v", first)
	}
	remoteState, ok := last["remote_state"].(map[string]any)
	if !ok || remoteState["node_id"] != "16" {
		t.Fatalf("unexpected remote state: %+v", last["remote_state"])
	}
}

func TestProbeLocalVirtualRouterRouteTestHandlerStreamsAsyncProgress(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port

	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "16", IP: "198.18.0.16", ServicePort: 12040},
		},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Version: 1,
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "localhost",
				FakeIP:     "198.18.2.1",
				Action:     "probe_exit",
				ExitNodeID: "16",
			}},
		},
	}
	probeVirtualRouterState.localNodeID = "16"
	probeVirtualRouterState.localIP = "198.18.0.16"
	probeVirtualRouterState.nodeToIP = map[string]string{"16": "198.18.0.16"}
	probeVirtualRouterState.ipToNode = map[string]string{"198.18.0.16": "16"}
	probeVirtualRouterState.mu.Unlock()

	startResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/virtual_router/route_test", map[string]any{
		"target": "198.18.2.1",
		"port":   port,
		"async":  true,
	}, sessionCookie)
	if startResp.Code != http.StatusOK {
		t.Fatalf("async route test start status=%d body=%s", startResp.Code, startResp.Body.String())
	}
	startPayload := decodeProbeLocalJSON(t, startResp)
	requestID, _ := startPayload["request_id"].(string)
	if strings.TrimSpace(requestID) == "" {
		t.Fatalf("async route test missing request_id: %+v", startPayload)
	}

	var payload map[string]any
	for attempt := 0; attempt < 20; attempt++ {
		getResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/virtual_router/route_test?request_id="+requestID, nil, sessionCookie)
		if getResp.Code != http.StatusOK {
			t.Fatalf("async route test get status=%d body=%s", getResp.Code, getResp.Body.String())
		}
		payload = decodeProbeLocalJSON(t, getResp)
		if payload["final"] == true {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if payload["final"] != true || payload["ok"] != true {
		t.Fatalf("async route test did not finish ok: %+v", payload)
	}
	items, ok := payload["results"].([]any)
	if !ok || len(items) < 2 {
		t.Fatalf("async route test results=%T %v", payload["results"], payload["results"])
	}
}

func TestProbeLocalVirtualRouterRouteSpeedHandlerDownloadsFromTargetNode(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.localNodeID = "9"
	probeVirtualRouterState.localIP = "198.18.0.7"
	probeVirtualRouterState.neighbors = map[string]map[string]struct{}{
		"9":  {"18": {}},
		"18": {"9": {}},
	}
	probeVirtualRouterState.mu.Unlock()

	oldRunner := probeVirtualRouterRouteSpeedDownloadRun
	probeVirtualRouterRouteSpeedDownloadRun = func(path []string, sourceNodeID string, targetNodeID string, maxBytes int64, maxDuration time.Duration) (probeVirtualRouterSpeedTestResult, error) {
		if got := strings.Join(path, ">"); got != "9>18" {
			t.Fatalf("path=%q, want 9>18", got)
		}
		if sourceNodeID != "9" || targetNodeID != "18" {
			t.Fatalf("source=%q target=%q", sourceNodeID, targetNodeID)
		}
		if maxBytes != 4*1024*1024 {
			t.Fatalf("maxBytes=%d", maxBytes)
		}
		if maxDuration != 3*time.Second {
			t.Fatalf("maxDuration=%s", maxDuration)
		}
		return probeVirtualRouterSpeedTestResult{
			OK:         true,
			Bytes:      4 * 1024 * 1024,
			Frames:     86,
			DurationMS: 1000,
			Mbps:       33.55,
		}, nil
	}
	t.Cleanup(func() {
		probeVirtualRouterRouteSpeedDownloadRun = oldRunner
	})

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/virtual_router/route_test/speed", map[string]any{
		"target_node_id": "18",
		"max_bytes":      4 * 1024 * 1024,
		"max_seconds":    3,
	}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("route speed status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	if payload["ok"] != true || payload["final"] != true {
		t.Fatalf("route speed not final ok: %+v", payload)
	}
	if payload["source_node_id"] != "9" || payload["target_node_id"] != "18" {
		t.Fatalf("unexpected source/target: %+v", payload)
	}
	if got := fmt.Sprint(payload["path"]); !strings.Contains(got, "9") || !strings.Contains(got, "18") {
		t.Fatalf("unexpected path: %+v", payload["path"])
	}
	download, ok := payload["download"].(map[string]any)
	if !ok || download["bytes"] != float64(4*1024*1024) || download["mbps"] != 33.55 {
		t.Fatalf("unexpected download result: %+v", payload["download"])
	}
	if _, ok := payload["queues_before"].(map[string]any); !ok {
		t.Fatalf("missing queues_before: %+v", payload)
	}
	if _, ok := payload["queues_after"].(map[string]any); !ok {
		t.Fatalf("missing queues_after: %+v", payload)
	}
}

func TestProbeLocalVirtualRouterDebugLogsHandlerFetchesLocalNodeLogs(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "debuglogadmin", "debuglogpass")

	oldStore := probeLogStore
	probeLogStore = newProbeInMemoryLogStore(probeLogMaxBytes)
	t.Cleanup(func() { probeLogStore = oldStore })
	_, _ = probeLogStore.Write([]byte("2026/07/08 20:30:45.000001 probe virtual router debug-log smoke route=vr-1\n"))
	_, _ = probeLogStore.Write([]byte("2026/07/08 20:30:46.000001 unrelated line\n"))

	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.localNodeID = "9"
	probeVirtualRouterState.localIP = "198.18.0.7"
	probeVirtualRouterState.mu.Unlock()
	t.Cleanup(resetProbeVirtualRouterStateForTest)

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/virtual_router/debug/logs", map[string]any{
		"target_node_id": "9",
		"lines":          50,
		"min_level":      "realtime",
		"keyword":        "debug-log smoke",
	}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("debug logs status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	if payload["ok"] != true {
		t.Fatalf("debug logs not ok: %+v", payload)
	}
	if payload["responder"] != "9" || payload["target_node_id"] != "9" {
		t.Fatalf("unexpected responder/target: %+v", payload)
	}
	if count := int(payload["count"].(float64)); count != 1 {
		t.Fatalf("count=%d payload=%+v", count, payload)
	}
	content, _ := payload["content"].(string)
	if !strings.Contains(content, "debug-log smoke") || strings.Contains(content, "unrelated line") {
		t.Fatalf("unexpected content=%q", content)
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
		{DNS: "cached.example", IP: "9.9.9.9"},
	}); err != nil {
		t.Fatalf("persist host mappings failed: %v", err)
	}
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
		{name: "doh domain static host", kind: "doh", address: "https://cached.example/dns-query", want: "9.9.9.9:443", wantFound: true},
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

func TestProbeLocalDNSStartupLoadsStaticHostMappingWithoutDNSCache(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	if err := persistProbeLocalHostMappings([]probeLocalHostMapping{
		{DNS: "static.example.com", IP: "203.0.113.10"},
	}); err != nil {
		t.Fatalf("persist host mappings failed: %v", err)
	}
	ips := lookupProbeLocalDNSStaticHostIPv4ByDomain("static.example.com")
	if strings.Join(ips, ",") != "203.0.113.10" {
		t.Fatalf("ips=%v", ips)
	}
}

func TestResolveProbeLocalDNSUpstreamHostIPv4DoesNotCacheBootstrapResult(t *testing.T) {
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
	if lookupCalls != 2 {
		t.Fatalf("bootstrap lookupCalls=%d want=2", lookupCalls)
	}
}

func TestProbeLocalDNSRealIPResultsDoNotPersistToDisk(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	oldBootstrap := probeLocalDNSBootstrapLookupIPv4
	probeLocalDNSBootstrapLookupIPv4 = func(host string) ([]string, error) {
		if host != "persist.example" {
			return nil, fmt.Errorf("unexpected bootstrap host: %s", host)
		}
		return []string{"203.0.113.30"}, nil
	}
	t.Cleanup(func() {
		probeLocalDNSBootstrapLookupIPv4 = oldBootstrap
		resetProbeLocalDNSServiceForTest()
	})
	if _, ok := resolveProbeLocalDNSUpstreamBypassTarget("doh", "https://persist.example/dns-query"); !ok {
		t.Fatal("bootstrap target was not resolved")
	}
	flushProbeLocalDNSCacheToDisk()
	cachePath, err := resolveProbeLocalDNSCachePath()
	if err != nil {
		t.Fatalf("resolve cache path failed: %v", err)
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("real dns result should not create cache db, stat err=%v", err)
	}
}

func TestProbeLocalTUNResetAndUninstallHandlers(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	settings := defaultProbeVirtualRouterLocalSettings()
	settings.VirtualRouterEnabled = true
	settings.VirtualDNSEnabled = true
	settings.ProxyEnabled = true
	probeVirtualRouterLocalSettingsState.mu.Lock()
	probeVirtualRouterLocalSettingsState.loaded = true
	probeVirtualRouterLocalSettingsState.settings = settings
	probeVirtualRouterLocalSettingsState.mu.Unlock()

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.tun.Enabled = true
	probeLocalControl.mu.Unlock()

	uninstallCalls := 0
	probeLocalUninstallTUNDriver = func() error {
		uninstallCalls++
		return nil
	}
	probeLocalDetectTUNInstalled = func() (bool, error) { return true, nil }
	restoreDNSCalls := 0
	probeVirtualRouterRestoreSystemDNS = func() error {
		restoreDNSCalls++
		return nil
	}
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
	if uninstallCalls != 0 {
		t.Fatalf("after reset uninstall=%d", uninstallCalls)
	}
	settings = loadProbeVirtualRouterLocalSettings()
	if settings.VirtualRouterEnabled || settings.VirtualDNSEnabled {
		t.Fatalf("tun/reset should disable virtual router and dns: %+v", settings)
	}
	if !settings.ProxyEnabled {
		t.Fatalf("tun/reset should preserve the independent proxy switch: %+v", settings)
	}
	if restoreDNSCalls < 1 {
		t.Fatal("tun/reset should restore system DNS")
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
	if uninstallCalls != 1 {
		t.Fatalf("after uninstall uninstall=%d", uninstallCalls)
	}
	if restoreDNSCalls < 2 {
		t.Fatal("tun/uninstall should restore system DNS even when settings are already disabled")
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

func TestProbeLocalConsoleRejectsNonLoopbackPlainHTTPByDefault(t *testing.T) {
	t.Setenv("PROBE_LOCAL_ALLOW_INSECURE_HTTP", "")
	allowInsecure := activeProbeProductProfile.AllowInsecureLocalConsoleHTTP
	activeProbeProductProfile.AllowInsecureLocalConsoleHTTP = false
	t.Cleanup(func() {
		activeProbeProductProfile.AllowInsecureLocalConsoleHTTP = allowInsecure
	})
	if err := startProbeLocalConsoleServer(http.NewServeMux(), "0.0.0.0:16032"); err == nil {
		cleanupProbeLocalConsoleServerForTest(t)
		t.Fatal("non-loopback plain HTTP listener should be rejected")
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

func TestProbeLocalSystemRouteAuthBlacklistSaveAndGet(t *testing.T) {
	resetProbeRouteAuthIPStateForTest()
	defer resetProbeRouteAuthIPStateForTest()

	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	saveResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/route_auth_blacklist", map[string]any{
		"content": "203.0.113.10\n\n# comment\n203.0.113.11 extra",
	}, sessionCookie)
	if saveResp.Code != http.StatusOK {
		t.Fatalf("route_auth_blacklist save status=%d body=%s", saveResp.Code, saveResp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, saveResp)
	if content, _ := payload["content"].(string); content != "203.0.113.10\n203.0.113.11" {
		t.Fatalf("route_auth_blacklist content=%q", content)
	}
	if blocked, _ := isProbeRouteAuthIPBlacklisted("203.0.113.10"); !blocked {
		t.Fatalf("expected saved ip to be blacklisted")
	}

	path, err := resolveProbeRouteAuthBlacklistPath()
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

	getResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/system/route_auth_blacklist", nil, sessionCookie)
	if getResp.Code != http.StatusOK {
		t.Fatalf("route_auth_blacklist get status=%d body=%s", getResp.Code, getResp.Body.String())
	}
	getPayload := decodeProbeLocalJSON(t, getResp)
	items, ok := getPayload["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("route_auth_blacklist items=%T %v", getPayload["items"], getPayload["items"])
	}

	invalidResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/route_auth_blacklist", map[string]any{
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
	t.Skip("disabled: environment-sensitive Wintun data-plane startup path is covered outside the default regression suite")
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
	t.Skip("disabled: environment-sensitive Wintun data-plane startup path is covered outside the default regression suite")
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
	t.Skip("disabled: environment-sensitive Wintun data-plane startup path is covered outside the default regression suite")
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
	probeVirtualRouterNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeVirtualRouterTUNDataPlane, error) {
		dataPlaneCalls++
		return &fakeProbeVirtualRouterTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true}}, nil
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
	probeVirtualRouterNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeVirtualRouterTUNDataPlane, error) {
		dataPlaneCalls++
		return &fakeProbeVirtualRouterTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true}}, nil
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
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")
	dataPlaneCalls := 0
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) { return uintptr(1), nil }
	probeLocalCloseWintunAdapterForDataPlane = func(_ string, _ uintptr) error { return nil }
	probeVirtualRouterNewTUNDataPlaneRunner = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeVirtualRouterTUNDataPlane, error) {
		dataPlaneCalls++
		return &fakeProbeVirtualRouterTUNDataPlane{stats: probeVirtualRouterTUNDataPlaneStats{Running: true}}, nil
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
	t.Skip("disabled: environment-sensitive Wintun data-plane startup path is covered outside the default regression suite")
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
