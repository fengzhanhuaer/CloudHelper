package core

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildProbeVirtualRouterConfigForNodeReturnsFullVirtualTopology(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore })
	setProbeVirtualRouterTestProbeStore(t, probeConfigData{
		ProbeNodes: []probeNodeRecord{
			{NodeNo: 1, NodeName: "node-1"},
			{NodeNo: 2, NodeName: "node-2"},
			{NodeNo: 3, NodeName: "node-3"},
			{NodeNo: 4, NodeName: "node-4"},
		},
		ProbeSecrets: map[string]string{},
	})

	ProbeRouteConfigStore = &probeRouteConfigStore{
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: "198.18.0.0/15",
				ProbeIPs: []probeVirtualRouterProbeIP{
					{NodeID: "1", IP: "198.18.0.11"},
					{NodeID: "2", IP: "198.18.0.12"},
					{NodeID: "3", IP: "198.18.0.13"},
					{NodeID: "4", IP: "198.18.0.14"},
				},
				TopologyRules: []probeVirtualRouterTopologyRule{
					{FromNodeID: "1", ToNodeID: "2", Direction: "bidirectional", Enabled: true},
					{FromNodeID: "2", ToNodeID: "3", Direction: "bidirectional", Enabled: true},
					{FromNodeID: "3", ToNodeID: "4", Direction: "bidirectional", Enabled: true},
				},
			},
		},
	}

	config := buildProbeVirtualRouterConfigForNodeLocked("1")
	if len(config.ProbeIPs) != 4 {
		t.Fatalf("probe ips=%+v, want full virtual ip map", config.ProbeIPs)
	}
	if len(config.TopologyRules) != 3 {
		t.Fatalf("topology rules=%+v, want full virtual topology", config.TopologyRules)
	}
	if config.TopologyRules[0].FromServicePort != 0 || config.TopologyRules[0].ToServicePort != probeVirtualRouterDefaultServicePort {
		t.Fatalf("default service ports=%d/%d, want from=0 to=%d", config.TopologyRules[0].FromServicePort, config.TopologyRules[0].ToServicePort, probeVirtualRouterDefaultServicePort)
	}
}

func TestProbeVirtualRouterProbeIPPoolUsesFirst1024FakeIPs(t *testing.T) {
	pool := probeVirtualRouterProbeIPPool("198.18.0.0/15")
	if len(pool) != 1022 {
		t.Fatalf("pool size=%d, want 1022", len(pool))
	}
	if pool[0] != "198.18.0.3" {
		t.Fatalf("first pool ip=%q", pool[0])
	}
	if pool[len(pool)-1] != "198.18.4.0" {
		t.Fatalf("last pool ip=%q", pool[len(pool)-1])
	}
	for _, ip := range pool {
		if ip == probeVirtualRouterReservedGatewayIP || ip == probeVirtualRouterReservedTUNIP {
			t.Fatalf("pool contains reserved ip %s", ip)
		}
	}
}

func TestEnsureProbeVirtualRouterProbeIPsAllocatesFreedAddress(t *testing.T) {
	setProbeVirtualRouterTestProbeStore(t, probeConfigData{
		ProbeNodes: []probeNodeRecord{
			{NodeNo: 1, NodeName: "node-1"},
			{NodeNo: 3, NodeName: "node-3"},
			{NodeNo: 4, NodeName: "node-4"},
		},
		DeletedProbeNodes:   []probeNodeRecord{{NodeNo: 2, NodeName: "node-2"}},
		DeletedProbeNodeNos: []int{2},
		ProbeSecrets:        map[string]string{},
	})
	clearProbeVirtualRouterTestRuntimes(t)

	config := ensureProbeVirtualRouterProbeIPsForKnownNodes(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.3"},
			{NodeID: "2", IP: "198.18.0.4"},
			{NodeID: "3", IP: "198.18.0.5"},
		},
	})
	ipByNode := probeVirtualRouterTestIPByNode(config.ProbeIPs)
	if _, ok := ipByNode["2"]; ok {
		t.Fatalf("deleted node should be released: %+v", config.ProbeIPs)
	}
	if ipByNode["1"] != "198.18.0.3" || ipByNode["3"] != "198.18.0.5" {
		t.Fatalf("existing active ips should be preserved: %+v", config.ProbeIPs)
	}
	if ipByNode["4"] != "198.18.0.4" {
		t.Fatalf("node 4 ip=%q, want freed 198.18.0.4; ips=%+v", ipByNode["4"], config.ProbeIPs)
	}
}

func TestEnsureProbeVirtualRouterProbeIPsAllocatesHighNodeIDFromFreePool(t *testing.T) {
	setProbeVirtualRouterTestProbeStore(t, probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 2000, NodeName: "node-2000"}},
		ProbeSecrets: map[string]string{},
	})
	clearProbeVirtualRouterTestRuntimes(t)

	config := ensureProbeVirtualRouterProbeIPsForKnownNodes(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
	})
	ipByNode := probeVirtualRouterTestIPByNode(config.ProbeIPs)
	if ipByNode["2000"] != "198.18.0.3" {
		t.Fatalf("node 2000 ip=%q, ips=%+v", ipByNode["2000"], config.ProbeIPs)
	}
}

func TestProbeVirtualRouterFakeIPLibraryAllocatesRenewsAndResetsIndependentStore(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore })
	setProbeVirtualRouterTestProbeStore(t, probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 9, NodeName: "exit-9"}},
		ProbeSecrets: map[string]string{"9": "secret-9"},
	})

	tmpDir := t.TempDir()
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
				ProbeIPs:   []probeVirtualRouterProbeIP{{NodeID: "9", IP: "198.18.0.9"}},
			},
			VirtualRouterFakeIP: defaultProbeVirtualRouterFakeIPLibrary(),
		},
	}
	rule := probeVirtualRouterRouteRule{ID: "rr-1", Action: probeVirtualRouterRouteRuleActionExit, ExitNodeID: "9"}

	first, firstLibrary, err := allocateProbeVirtualRouterFakeIPForDomain("WWW.Reddit.COM.", rule)
	if err != nil {
		t.Fatalf("allocate fake ip failed: %v", err)
	}
	if first.Domain != "www.reddit.com" || first.FakeIP != "198.18.4.1" || first.ExitNodeID != "9" {
		t.Fatalf("first fake ip entry=%+v", first)
	}
	if firstLibrary.Version != 2 || len(firstLibrary.Items) != 1 {
		t.Fatalf("first library=%+v", firstLibrary)
	}
	expiresAt, err := time.Parse(time.RFC3339, first.ExpiresAt)
	if err != nil || time.Until(expiresAt) < 29*24*time.Hour {
		t.Fatalf("expires_at=%q err=%v", first.ExpiresAt, err)
	}

	second, secondLibrary, err := allocateProbeVirtualRouterFakeIPForDomain("www.reddit.com", rule)
	if err != nil {
		t.Fatalf("renew fake ip failed: %v", err)
	}
	if second.FakeIP != first.FakeIP || secondLibrary.Version != firstLibrary.Version+1 {
		t.Fatalf("renew entry=%+v library=%+v first_version=%d", second, secondLibrary, firstLibrary.Version)
	}

	resetLibrary, err := resetProbeVirtualRouterFakeIPLibrary()
	if err != nil {
		t.Fatalf("reset fake ip library failed: %v", err)
	}
	if len(resetLibrary.Items) != 0 || resetLibrary.Version != secondLibrary.Version+1 {
		t.Fatalf("reset library=%+v second_version=%d", resetLibrary, secondLibrary.Version)
	}
}

func TestProbeRouteFakeIPResolveHandlerPersistsLibrary(t *testing.T) {
	oldRouteStore := ProbeRouteConfigStore
	oldProbeStore := ProbeStore
	t.Cleanup(func() {
		ProbeRouteConfigStore = oldRouteStore
		ProbeStore = oldProbeStore
	})
	tmpDir := t.TempDir()
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 1, NodeName: "node-1"}, {NodeNo: 9, NodeName: "exit-9"}},
		ProbeSecrets: map[string]string{"1": "secret-1", "9": "secret-9"},
	}}
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
			},
			VirtualRouterFakeIP: defaultProbeVirtualRouterFakeIPLibrary(),
		},
	}
	commandCh, cleanupSession := attachProbeVirtualRouterTestSession(t, "1")
	defer cleanupSession()
	body := bytes.NewBufferString(`{"domain":"api.reddit.com","rule_id":"rr-1","action":"probe_exit","exit_node_id":"9"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/probe/route/fake_ip/resolve?node_id=1&secret=secret-1", body)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()

	ProbeRouteFakeIPResolveHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("resolve status=%d body=%s", rr.Code, rr.Body.String())
	}
	var payload probeRouteFakeIPResolveResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response failed: %v body=%s", err, rr.Body.String())
	}
	if payload.Item.Domain != "api.reddit.com" || payload.Item.FakeIP == "" || len(payload.FakeIPLibrary.Items) != 1 {
		t.Fatalf("payload=%+v", payload)
	}
	if payload.Sync.Total != 2 || payload.Sync.Dispatched != 1 || payload.Sync.Offline != 1 || payload.Sync.Failed != 0 {
		t.Fatalf("sync result=%+v", payload.Sync)
	}
	assertProbeVirtualRouterRouteConfigSyncCommand(t, commandCh)
	raw, err := os.ReadFile(filepath.Join(tmpDir, "probe_route_config.json"))
	if err != nil {
		t.Fatalf("read persisted route config failed: %v", err)
	}
	if !strings.Contains(string(raw), `"virtual_router_fake_ip"`) || !strings.Contains(string(raw), `"api.reddit.com"`) {
		t.Fatalf("persisted config missing fake ip library: %s", string(raw))
	}
}

func TestMngLinkVirtualRouterFakeIPResetHandlerDispatchesRouteConfigSync(t *testing.T) {
	oldRouteStore := ProbeRouteConfigStore
	oldProbeStore := ProbeStore
	t.Cleanup(func() {
		ProbeRouteConfigStore = oldRouteStore
		ProbeStore = oldProbeStore
	})
	tmpDir := t.TempDir()
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 1, NodeName: "node-1"}},
		ProbeSecrets: map[string]string{"1": "secret-1"},
	}}
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
			},
			VirtualRouterFakeIP: probeVirtualRouterFakeIPLibrary{
				Version:   3,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
				Items: []probeVirtualRouterFakeIPEntry{{
					Domain:    "api.reddit.com",
					FakeIP:    "198.18.4.1",
					ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
				}},
			},
		},
	}
	commandCh, cleanupSession := attachProbeVirtualRouterTestSession(t, "1")
	defer cleanupSession()

	req := httptest.NewRequest(http.MethodPost, "/mng/api/route/virtual_router/fake_ip/reset", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mngLinkVirtualRouterFakeIPResetHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", rr.Code, rr.Body.String())
	}
	var payload struct {
		FakeIPLibrary probeVirtualRouterFakeIPLibrary   `json:"fake_ip_library"`
		Sync          probeLinkConfigSyncDispatchResult `json:"sync"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode reset payload failed: %v body=%s", err, rr.Body.String())
	}
	if len(payload.FakeIPLibrary.Items) != 0 || payload.FakeIPLibrary.Version != 4 {
		t.Fatalf("library after reset=%+v", payload.FakeIPLibrary)
	}
	if payload.Sync.Total != 1 || payload.Sync.Dispatched != 1 || payload.Sync.Failed != 0 {
		t.Fatalf("sync result=%+v", payload.Sync)
	}
	assertProbeVirtualRouterRouteConfigSyncCommand(t, commandCh)
}

func attachProbeVirtualRouterTestSession(t *testing.T, nodeID string) (<-chan map[string]any, func()) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	session := &probeSession{nodeID: nodeID, stream: serverConn, enc: json.NewEncoder(serverConn)}
	probeSessions.mu.Lock()
	oldSession := probeSessions.data[nodeID]
	probeSessions.data[nodeID] = session
	probeSessions.mu.Unlock()
	commandCh := make(chan map[string]any, 1)
	go func() {
		var msg map[string]any
		_ = json.NewDecoder(clientConn).Decode(&msg)
		commandCh <- msg
	}()
	return commandCh, func() {
		probeSessions.mu.Lock()
		if oldSession != nil {
			probeSessions.data[nodeID] = oldSession
		} else {
			delete(probeSessions.data, nodeID)
		}
		probeSessions.mu.Unlock()
		_ = clientConn.Close()
		_ = serverConn.Close()
	}
}

func assertProbeVirtualRouterRouteConfigSyncCommand(t *testing.T, commandCh <-chan map[string]any) {
	t.Helper()
	select {
	case msg := <-commandCh:
		if msg["type"] != "route_config_sync" {
			t.Fatalf("sync command=%v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting route_config_sync command")
	}
}

func setProbeVirtualRouterTestProbeStore(t *testing.T, data probeConfigData) {
	t.Helper()
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{data: data}
	t.Cleanup(func() { ProbeStore = oldStore })
}

func clearProbeVirtualRouterTestRuntimes(t *testing.T) {
	t.Helper()
	probeRuntimeStore.mu.Lock()
	oldRuntimeData := probeRuntimeStore.data
	probeRuntimeStore.data = make(map[string]probeRuntimeStatus)
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		probeRuntimeStore.mu.Lock()
		probeRuntimeStore.data = oldRuntimeData
		probeRuntimeStore.mu.Unlock()
	})
}

func probeVirtualRouterTestIPByNode(items []probeVirtualRouterProbeIP) map[string]string {
	out := make(map[string]string, len(items))
	for _, item := range items {
		out[item.NodeID] = item.IP
	}
	return out
}

func TestProbeVirtualRouterRuntimeChainIDIsStableAcrossServiceEndpointChanges(t *testing.T) {
	base := probeVirtualRouterTopologyRule{
		ID:                "edge-a-b",
		FromNodeID:        "1",
		ToNodeID:          "2",
		FromServiceDomain: "old-a.internal",
		FromServicePort:   12040,
		ToServiceDomain:   "old-b.internal",
		ToServicePort:     12040,
		Enabled:           true,
	}
	changedEndpoint := base
	changedEndpoint.FromServiceDomain = "new-a.internal"
	changedEndpoint.FromServicePort = 13040
	changedEndpoint.ToServiceDomain = "new-b.internal"
	changedEndpoint.ToServicePort = 13041
	changedEndpoint.FromNodeID = "3"
	changedEndpoint.ToNodeID = "4"

	if left, right := probeVirtualRouterRuntimeChainID(base), probeVirtualRouterRuntimeChainID(changedEndpoint); left != right {
		t.Fatalf("same rule should keep chain id across topology endpoint changes: %s != %s", left, right)
	}

	changedRule := base
	changedRule.ID = "edge-a-b-other"
	if left, right := probeVirtualRouterRuntimeChainID(base), probeVirtualRouterRuntimeChainID(changedRule); left == right {
		t.Fatalf("different rule ids should produce different chain ids: %s", left)
	}
}

func TestNormalizeProbeVirtualRouterTopologyRulesInitializesRuleIDsBySequence(t *testing.T) {
	config := normalizeProbeVirtualRouterConfig(probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Enabled: true},
			{ID: "vr-1", FromNodeID: "2", ToNodeID: "3", Enabled: true},
			{FromNodeID: "3", ToNodeID: "4", Enabled: true},
		},
	})
	if len(config.TopologyRules) != 3 {
		t.Fatalf("topology rules=%+v", config.TopologyRules)
	}
	idsByFrom := map[string]string{}
	for _, rule := range config.TopologyRules {
		idsByFrom[rule.FromNodeID] = rule.ID
	}
	if idsByFrom["1"] != "vr-2" || idsByFrom["2"] != "vr-1" || idsByFrom["3"] != "vr-3" {
		t.Fatalf("rule ids should be initialized once by sequence: %+v", config.TopologyRules)
	}
}
