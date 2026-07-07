package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMngVirtualRouterSideStatsErrorIgnoresStaleErrorAfterBridgeReconnect(t *testing.T) {
	side := mngVirtualRouterRouteSideStatus{
		VirtualRouter: &probeVirtualRouterRuntimeStats{
			LastPingError: "upstream bridge is unavailable",
			LastPingAt:    "2026-06-28T00:12:00Z",
		},
		BridgeStatus: &probeRouteBridgeRuntimeStatus{
			UpstreamActive: 1,
			Sessions: []probeRouteBridgeSessionSnapshot{
				{
					Direction:   "upstream",
					ConnectedAt: "2026-06-28T00:13:23Z",
				},
			},
		},
	}
	if got := mngVirtualRouterSideStatsError(side); got != "" {
		t.Fatalf("stale error=%q, want empty", got)
	}

	side.VirtualRouter.LastPingAt = "2026-06-28T00:14:00Z"
	if got := mngVirtualRouterSideStatsError(side); got != "bridge is unavailable" {
		t.Fatalf("current error=%q, want bridge is unavailable", got)
	}
}

func TestMngLinkVirtualRouterStatusHandlerReturnsRuleRuntimeStatus(t *testing.T) {
	oldRouteStore := ProbeRouteConfigStore
	oldProbeStore := ProbeStore
	probeRuntimeStore.mu.Lock()
	oldRuntimeData := probeRuntimeStore.data
	probeRuntimeStore.data = make(map[string]probeRuntimeStatus)
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		ProbeRouteConfigStore = oldRouteStore
		ProbeStore = oldProbeStore
		probeRuntimeStore.mu.Lock()
		probeRuntimeStore.data = oldRuntimeData
		probeRuntimeStore.mu.Unlock()
	})

	tmpDir := t.TempDir()
	rule := probeVirtualRouterTopologyRule{
		ID:              "rule-ab",
		Name:            "A-B",
		FromNodeID:      "1",
		ToNodeID:        "2",
		Direction:       probeVirtualRouterDirectionForward,
		FromServicePort: 12040,
		ToServicePort:   12040,
		Enabled:         true,
	}
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
				ProbeIPs: []probeVirtualRouterProbeIP{
					{NodeID: "1", IP: "198.18.0.3"},
					{NodeID: "2", IP: "198.18.0.4"},
				},
				TopologyRules: []probeVirtualRouterTopologyRule{rule},
			},
		},
	}
	ProbeStore = &probeConfigStore{
		data: probeConfigData{
			ProbeNodes: []probeNodeRecord{
				{NodeNo: 1, NodeName: "node-1"},
				{NodeNo: 2, NodeName: "node-2"},
			},
		},
	}
	routeID := probeVirtualRouterRuntimeRouteID(rule)
	updateProbeRuntimeReportWithRelay("1", nil, nil, probeSystemMetrics{}, "v1", []probeRelayStatusItem{
		{
			RouteID:    routeID,
			RouteType:  "virtual_router",
			Role:       "relay",
			ListenHost: "0.0.0.0",
			ListenPort: 12040,
			NextHost:   "node-2.local",
			NextPort:   12040,
			ListenState: &probeRelayProtocolStateSnapshot{
				Endpoint: "0.0.0.0:12040",
				ListenerStatuses: []probeRelayListenerStatus{
					{Protocol: "websocket", Status: "listening", Listen: "0.0.0.0:12040"},
				},
			},
			NextState: &probeRelayProtocolStateSnapshot{Endpoint: "node-2.local:12040", SelectedProtocol: "websocket"},
			BridgeSessions: []probeRouteBridgeSessionSnapshot{
				{
					Direction:           "upstream",
					RemoteAddr:          "node-2.local:12040",
					StreamsCurrent:      1,
					FramesSent:          7,
					FrameBytesSent:      700,
					FramesReceived:      8,
					FrameBytesReceived:  800,
					LastFrameSentAt:     "2026-06-27T00:00:03Z",
					LastFrameReceivedAt: "2026-06-27T00:00:04Z",
				},
			},
			VirtualRouter: &probeVirtualRouterRuntimeStats{
				PacketsForwarded:          2,
				BytesForwarded:            200,
				FramesSent:                2,
				FrameBytesSent:            200,
				PingCount:                 1,
				LastPingLatencyMS:         12,
				LastPingAt:                "2026-06-27T00:00:01Z",
				LastPingBridgeConnections: 1,
				LastPacketAt:              "2026-06-27T00:00:01Z",
				LastFrameAt:               "2026-06-27T00:00:01Z",
				TUNDataPlane:              true,
				TUNRXPackets:              11,
				TUNRXBytes:                1100,
				TUNTXPackets:              5,
				TUNTXBytes:                500,
			},
		},
	})
	updateProbeRuntimeReportWithRelay("2", nil, nil, probeSystemMetrics{}, "v1", []probeRelayStatusItem{
		{
			RouteID:    routeID,
			RouteType:  "virtual_router",
			Role:       "relay",
			ListenHost: "0.0.0.0",
			ListenPort: 12040,
			ListenState: &probeRelayProtocolStateSnapshot{
				Endpoint: "0.0.0.0:12040",
				ListenerStatuses: []probeRelayListenerStatus{
					{Protocol: "websocket", Status: "listening", Listen: "0.0.0.0:12040"},
				},
			},
			VirtualRouter: &probeVirtualRouterRuntimeStats{
				PacketsReceived:    3,
				BytesReceived:      300,
				FramesReceived:     3,
				FrameBytesReceived: 300,
				LastPacketAt:       "2026-06-27T00:00:02Z",
				LastFrameAt:        "2026-06-27T00:00:02Z",
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/mng/api/route/virtual_router/status", nil)
	rr := httptest.NewRecorder()
	mngRouteVirtualRouterStatusHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var payload struct {
		Items []mngVirtualRouterRouteStatusView `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload failed: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items=%d body=%s", len(payload.Items), rr.Body.String())
	}
	item := payload.Items[0]
	if item.Status != "ready" || item.RouteID != routeID {
		t.Fatalf("unexpected route status: %+v", item)
	}
	if item.Direction != "A->B" {
		t.Fatalf("route physical direction=%q, want A->B", item.Direction)
	}
	if item.Packets != 5 || item.Bytes != 500 || item.LastLatencyMS != 12 || item.LastPacketAt != "2026-06-27T00:00:02Z" {
		t.Fatalf("unexpected route stats: %+v", item)
	}
	if item.PacketsForwarded != 2 || item.BytesForwarded != 200 || item.PacketsReceived != 3 || item.BytesReceived != 300 || item.PacketsDelivered != 0 || item.BytesDelivered != 0 {
		t.Fatalf("unexpected route packet lifecycle stats: %+v", item)
	}
	if item.FramesSent != 2 || item.FrameBytesSent != 200 || item.FramesReceived != 3 || item.FrameBytesReceived != 300 || item.LastFrameAt != "2026-06-27T00:00:02Z" {
		t.Fatalf("unexpected route frame stats: %+v", item)
	}
	if item.From.Status != "connected" || item.To.Status != "listening" {
		t.Fatalf("unexpected side status: from=%+v to=%+v", item.From, item.To)
	}
	if item.From.VirtualRouter == nil || !item.From.VirtualRouter.TUNDataPlane || item.From.VirtualRouter.TUNRXPackets != 11 || item.From.VirtualRouter.TUNRXBytes != 1100 || item.From.VirtualRouter.TUNTXPackets != 5 || item.From.VirtualRouter.TUNTXBytes != 500 {
		t.Fatalf("unexpected tun data plane stats: %+v", item.From.VirtualRouter)
	}
	if item.From.VirtualRouter.LastPingBridgeConnections != 1 {
		t.Fatalf("unexpected bridge connection count: %+v", item.From.VirtualRouter)
	}
	if len(item.From.BridgeSessions) != 1 ||
		item.From.BridgeSessions[0].FramesSent != 7 ||
		item.From.BridgeSessions[0].FrameBytesSent != 700 ||
		item.From.BridgeSessions[0].FramesReceived != 8 ||
		item.From.BridgeSessions[0].FrameBytesReceived != 800 {
		t.Fatalf("unexpected bridge session frame stats: %+v", item.From.BridgeSessions)
	}
}

func TestMngLinkVirtualRouterStatusHandlerPostDispatchesReportOnce(t *testing.T) {
	oldRouteStore := ProbeRouteConfigStore
	oldProbeStore := ProbeStore
	t.Cleanup(func() {
		ProbeRouteConfigStore = oldRouteStore
		ProbeStore = oldProbeStore
	})

	tmpDir := t.TempDir()
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
				ProbeIPs:   []probeVirtualRouterProbeIP{{NodeID: "1", IP: "198.18.0.3"}},
			},
		},
	}
	ProbeStore = &probeConfigStore{
		data: probeConfigData{
			ProbeNodes: []probeNodeRecord{{NodeNo: 1, NodeName: "node-1"}},
		},
	}
	commandCh, cleanupSession := attachProbeVirtualRouterTestSession(t, "1")
	defer cleanupSession()

	req := httptest.NewRequest(http.MethodPost, "/mng/api/route/virtual_router/status", nil)
	rr := httptest.NewRecorder()
	mngRouteVirtualRouterStatusHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var payload struct {
		Sync probeRouteConfigSyncDispatchResult `json:"sync"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload failed: %v", err)
	}
	if payload.Sync.Total != 1 || payload.Sync.Dispatched != 1 || payload.Sync.Failed != 0 {
		t.Fatalf("sync result=%+v", payload.Sync)
	}
	assertProbeVirtualRouterCommandType(t, commandCh, "report_once")
}

func TestLastMngVirtualRouterRouteLatencyUsesNewestPingReport(t *testing.T) {
	oldHigh := &probeVirtualRouterRuntimeStats{
		LastPingLatencyMS: 232,
		LastPingAt:        "2026-06-28T14:30:00Z",
	}
	newLow := &probeVirtualRouterRuntimeStats{
		LastPingLatencyMS: 1,
		LastPingAt:        "2026-06-28T14:31:00Z",
	}
	if got := lastMngVirtualRouterRouteLatency(oldHigh, newLow); got != 1 {
		t.Fatalf("latency=%d, want newest report latency 1", got)
	}
}

func TestMngLinkVirtualRouterHandlerSaveAndGet(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	oldProbeStore := ProbeStore
	t.Cleanup(func() {
		ProbeRouteConfigStore = oldStore
		ProbeStore = oldProbeStore
	})

	tmpDir := t.TempDir()
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: defaultProbeVirtualRouterConfig(),
		},
	}
	ProbeStore = &probeConfigStore{
		data: probeConfigData{
			ProbeNodes: []probeNodeRecord{
				{NodeNo: 1, NodeName: "node-1"},
				{NodeNo: 2, NodeName: "node-2"},
			},
			ProbeSecrets: map[string]string{},
		},
	}

	body := []byte(`{
  "enabled": true,
  "fake_ip_cidr": "198.18.0.0/15",
  "probe_ips": [
    {"node_id":"1","ip":"198.18.0.3"},
    {"node_id":"2","ip":"198.18.0.4"}
  ],
  "topology_rules": [
    {
      "id":"rule-a",
      "from_node_id":"1",
      "to_node_id":"2",
      "direction":"bidirectional",
      "from_service_domain":"edge-a.example.com",
      "from_service_port":443,
      "to_service_domain":"edge-b.internal.lan",
      "to_service_port":443,
      "enabled":true
    },
    {
      "id":"rule-b",
      "from_node_id":"1",
      "to_node_id":"2",
      "direction":"bidirectional",
      "from_service_domain":"edge-a-alt.example.com",
      "from_service_port":443,
      "to_service_domain":"edge-b-alt.internal.lan",
      "to_service_port":443,
      "enabled":true
    }
  ]
}`)
	saveReq := httptest.NewRequest(http.MethodPost, "/mng/api/route/virtual_router", bytes.NewReader(body))
	saveRR := httptest.NewRecorder()
	mngRouteVirtualRouterHandler(saveRR, saveReq)
	if saveRR.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", saveRR.Code, saveRR.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/mng/api/route/virtual_router", nil)
	getRR := httptest.NewRecorder()
	mngRouteVirtualRouterHandler(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getRR.Code, getRR.Body.String())
	}
	var payload struct {
		Item probeVirtualRouterConfig `json:"item"`
	}
	if err := json.Unmarshal(getRR.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode get payload failed: %v", err)
	}
	if len(payload.Item.ProbeIPs) != 2 {
		t.Fatalf("probe ips=%+v, want 2", payload.Item.ProbeIPs)
	}
	if len(payload.Item.TopologyRules) != 2 || payload.Item.TopologyRules[0].Direction != probeVirtualRouterDirectionForward {
		t.Fatalf("topology rules=%+v", payload.Item.TopologyRules)
	}
	first := payload.Item.TopologyRules[0]
	if first.FromServiceDomain != "" || first.FromServicePort != 0 || first.ToServiceDomain != "edge-b.internal.lan" || first.ToServicePort != 443 {
		t.Fatalf("service config not persisted: %+v", first)
	}
	if strings.TrimSpace(first.Secret) == "" {
		t.Fatalf("virtual router rule secret should be generated")
	}
	if payload.Item.TopologyRules[1].FromServicePort != 0 || payload.Item.TopologyRules[1].ToServicePort != 443 {
		t.Fatalf("service port reuse should be allowed: %+v", payload.Item.TopologyRules)
	}

	raw, err := os.ReadFile(filepath.Join(tmpDir, "probe_route_config.json"))
	if err != nil {
		t.Fatalf("read route config store failed: %v", err)
	}
	var stored probeRouteConfigStoreData
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode route config store failed: %v", err)
	}
	if len(stored.VirtualRouter.TopologyRules) != 2 {
		t.Fatalf("route config store topology rules=%+v, want 2", stored.VirtualRouter.TopologyRules)
	}
}

func TestMngLinkVirtualRouterHandlerRejectsProbeIPOutsideReservedPool(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore })
	ProbeRouteConfigStore = &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "probe_route_config.json")}

	body := []byte(`{"probe_ips":[{"node_id":"1","ip":"198.18.4.1"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/mng/api/route/virtual_router", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mngRouteVirtualRouterHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rr.Code, rr.Body.String())
	}
}

func TestMngLinkVirtualRouterHandlerRejectsInvalidServicePort(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore })
	ProbeRouteConfigStore = &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "probe_route_config.json")}

	body := []byte(`{
  "probe_ips":[
    {"node_id":"1","ip":"198.18.0.3"},
    {"node_id":"2","ip":"198.18.0.4"}
  ],
  "topology_rules":[
    {"from_node_id":"1","to_node_id":"2","direction":"bidirectional","to_service_port":65536,"enabled":true}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/mng/api/route/virtual_router", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mngRouteVirtualRouterHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rr.Code, rr.Body.String())
	}
}

func TestMngLinkVirtualRouterRouteRulesHandlerSaveSortsAndTopologySavePreserves(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	oldProbeStore := ProbeStore
	t.Cleanup(func() {
		ProbeRouteConfigStore = oldStore
		ProbeStore = oldProbeStore
	})

	tmpDir := t.TempDir()
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
			},
		},
	}
	ProbeStore = &probeConfigStore{
		data: probeConfigData{
			ProbeNodes: []probeNodeRecord{
				{NodeNo: 1, NodeName: "node-1"},
				{NodeNo: 2, NodeName: "node-2"},
			},
			ProbeSecrets: map[string]string{},
		},
	}

	body := []byte(`{
  "items": [
    {
      "name": "media",
      "action": "probe_exit",
      "exit_node_id": "2",
      "entries": [
        "\"domain_suffix:.Reddit.COM\",",
        "\"cidr:91.108.4.9/22\",",
        "domain_keyword:API.AAAA,",
        "\"domain_suffix:reddit.com\"",
        "- DOMAIN-SUFFIX,githubusercontent.com,Github",
        "- 'DOMAIN-SUFFIX,cdn-telegram.org,Telegram'",
        "- 'IP-CIDR,91.108.8.0/22,Telegram'"
      ]
    },
    {
      "name": "alpha",
      "action": "reject",
      "entries": ["- 'DOMAIN-PREFIX,API.AAAA,Test'"]
    }
  ]
}`)
	saveReq := httptest.NewRequest(http.MethodPost, "/mng/api/route/virtual_router/route_rules", bytes.NewReader(body))
	saveRR := httptest.NewRecorder()
	mngRouteVirtualRouterRouteRulesHandler(saveRR, saveReq)
	if saveRR.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", saveRR.Code, saveRR.Body.String())
	}
	var savePayload struct {
		Items []probeVirtualRouterRouteRule `json:"items"`
	}
	if err := json.Unmarshal(saveRR.Body.Bytes(), &savePayload); err != nil {
		t.Fatalf("decode save payload failed: %v", err)
	}
	if len(savePayload.Items) != 2 || savePayload.Items[0].Name != "alpha" || savePayload.Items[1].Name != "media" {
		t.Fatalf("groups not sorted by name: %+v", savePayload.Items)
	}
	if savePayload.Items[0].Action != probeVirtualRouterRouteRuleActionReject || savePayload.Items[0].ExitNodeID != "" {
		t.Fatalf("alpha action=%q exit=%q, want reject without exit", savePayload.Items[0].Action, savePayload.Items[0].ExitNodeID)
	}
	media := savePayload.Items[1]
	if media.Action != probeVirtualRouterRouteRuleActionExit || media.ExitNodeID != "2" {
		t.Fatalf("media action=%q exit=%q, want probe_exit node 2", media.Action, media.ExitNodeID)
	}
	wantEntries := []string{
		"cidr:91.108.4.0/22",
		"cidr:91.108.8.0/22",
		"domain_keyword:api.aaaa",
		"domain_suffix:cdn-telegram.org",
		"domain_suffix:githubusercontent.com",
		"domain_suffix:reddit.com",
	}
	if strings.Join(media.Entries, "\n") != strings.Join(wantEntries, "\n") {
		t.Fatalf("entries=%+v, want %+v", media.Entries, wantEntries)
	}

	topologyBody := []byte(`{
  "enabled": true,
  "fake_ip_cidr": "198.18.0.0/15",
  "probe_ips": [
    {"node_id":"1","ip":"198.18.0.3"},
    {"node_id":"2","ip":"198.18.0.4"}
  ],
  "topology_rules": [
    {"id":"rule-a","from_node_id":"1","to_node_id":"2","to_service_port":12040,"enabled":true}
  ]
}`)
	topologyReq := httptest.NewRequest(http.MethodPost, "/mng/api/route/virtual_router", bytes.NewReader(topologyBody))
	topologyRR := httptest.NewRecorder()
	mngRouteVirtualRouterHandler(topologyRR, topologyReq)
	if topologyRR.Code != http.StatusOK {
		t.Fatalf("topology save status=%d body=%s", topologyRR.Code, topologyRR.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/mng/api/route/virtual_router/route_rules", nil)
	getRR := httptest.NewRecorder()
	mngRouteVirtualRouterRouteRulesHandler(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getRR.Code, getRR.Body.String())
	}
	var getPayload struct {
		Items []probeVirtualRouterRouteRule `json:"items"`
	}
	if err := json.Unmarshal(getRR.Body.Bytes(), &getPayload); err != nil {
		t.Fatalf("decode get payload failed: %v", err)
	}
	if len(getPayload.Items) != 2 || getPayload.Items[1].Name != "media" {
		t.Fatalf("route rules should survive topology save: %+v", getPayload.Items)
	}
	if getPayload.Items[0].Action != probeVirtualRouterRouteRuleActionReject || getPayload.Items[1].Action != probeVirtualRouterRouteRuleActionExit || getPayload.Items[1].ExitNodeID != "2" {
		t.Fatalf("route rule actions should survive topology save: %+v", getPayload.Items)
	}
}

func TestMngLinkVirtualRouterRouteRulesHandlerUpdatesFakeIPExit(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	oldProbeStore := ProbeStore
	t.Cleanup(func() {
		ProbeRouteConfigStore = oldStore
		ProbeStore = oldProbeStore
	})

	tmpDir := t.TempDir()
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
				RouteRules: []probeVirtualRouterRouteRule{{
					ID:         "rr-media",
					Name:       "media",
					Action:     probeVirtualRouterRouteRuleActionExit,
					ExitNodeID: "2",
					Entries:    []string{"domain_suffix:reddit.com"},
				}},
			},
			VirtualRouterFakeIP: probeVirtualRouterFakeIPLibrary{
				Version:   7,
				UpdatedAt: "2026-07-07T00:00:00Z",
				Items: []probeVirtualRouterFakeIPEntry{{
					Domain:     "www.reddit.com",
					FakeIP:     "198.18.4.9",
					RuleID:     "rr-media",
					Action:     probeVirtualRouterRouteRuleActionExit,
					ExitNodeID: "2",
					ExpiresAt:  time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339),
					UpdatedAt:  "2026-07-07T00:00:00Z",
				}},
			},
		},
	}
	ProbeStore = &probeConfigStore{
		data: probeConfigData{
			ProbeNodes: []probeNodeRecord{
				{NodeNo: 1, NodeName: "node-1"},
				{NodeNo: 2, NodeName: "node-2"},
				{NodeNo: 3, NodeName: "node-3"},
			},
			ProbeSecrets: map[string]string{},
		},
	}

	body := []byte(`{
  "items": [
    {
      "id": "rr-media",
      "name": "media",
      "action": "probe_exit",
      "exit_node_id": "3",
      "entries": ["domain_suffix:reddit.com"]
    }
  ]
}`)
	saveReq := httptest.NewRequest(http.MethodPost, "/mng/api/route/virtual_router/route_rules", bytes.NewReader(body))
	saveRR := httptest.NewRecorder()
	mngRouteVirtualRouterRouteRulesHandler(saveRR, saveReq)
	if saveRR.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", saveRR.Code, saveRR.Body.String())
	}

	ProbeRouteConfigStore.mu.RLock()
	library := normalizeProbeVirtualRouterFakeIPLibrary(ProbeRouteConfigStore.data.VirtualRouterFakeIP)
	ProbeRouteConfigStore.mu.RUnlock()
	if library.Version != 8 || len(library.Items) != 1 {
		t.Fatalf("fake ip library=%+v", library)
	}
	item := library.Items[0]
	if item.Domain != "www.reddit.com" || item.FakeIP != "198.18.4.9" || item.RuleID != "rr-media" || item.ExitNodeID != "3" {
		t.Fatalf("fake ip entry did not follow route exit update: %+v", item)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/mng/api/route/virtual_router", nil)
	getRR := httptest.NewRecorder()
	mngRouteVirtualRouterHandler(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("get virtual router status=%d body=%s", getRR.Code, getRR.Body.String())
	}
	var getPayload struct {
		Item probeVirtualRouterConfig `json:"item"`
	}
	if err := json.Unmarshal(getRR.Body.Bytes(), &getPayload); err != nil {
		t.Fatalf("decode virtual router payload failed: %v", err)
	}
	if len(getPayload.Item.FakeIPLibrary.Items) != 1 || getPayload.Item.FakeIPLibrary.Items[0].ExitNodeID != "3" {
		t.Fatalf("visible fake ip library did not update: %+v", getPayload.Item.FakeIPLibrary)
	}
}
