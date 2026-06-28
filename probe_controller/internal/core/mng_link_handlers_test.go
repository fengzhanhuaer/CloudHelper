package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestMngLinkRelayStatusHandlerReturnsReportedRelayStatus(t *testing.T) {
	probeRuntimeStore.mu.Lock()
	oldRuntimeData := probeRuntimeStore.data
	probeRuntimeStore.data = make(map[string]probeRuntimeStatus)
	probeRuntimeStore.mu.Unlock()
	defer func() {
		probeRuntimeStore.mu.Lock()
		probeRuntimeStore.data = oldRuntimeData
		probeRuntimeStore.mu.Unlock()
	}()

	updateProbeRuntimeReportWithRelay("1", nil, nil, probeSystemMetrics{}, "v1.2.3", []probeRelayStatusItem{
		{
			ChainID:    "chain-1",
			ChainName:  "relay-chain",
			Role:       "relay",
			ListenHost: "0.0.0.0",
			ListenPort: 16030,
			ListenState: &probeRelayProtocolStateSnapshot{
				Endpoint: "0.0.0.0:16030",
				ListenerStatuses: []probeRelayListenerStatus{
					{Protocol: "websocket", Status: "listening", Listen: "0.0.0.0:16030"},
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/mng/api/link/relay_status", nil)
	rr := httptest.NewRecorder()
	mngLinkRelayStatusHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Items []mngLinkRelayStatusView `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode payload: %v body=%s", err, rr.Body.String())
	}
	if len(payload.Items) != 1 {
		t.Fatalf("expected 1 item, got %d payload=%s", len(payload.Items), rr.Body.String())
	}
	item := payload.Items[0]
	if item.NodeID != "1" || item.ChainID != "chain-1" || item.ListenPort != 16030 {
		t.Fatalf("unexpected relay status item: %+v", item)
	}
	if item.ListenState == nil || len(item.ListenState.ListenerStatuses) != 1 {
		t.Fatalf("expected listener status in relay status item: %+v", item)
	}
	if strings.TrimSpace(item.ListenState.ListenerStatuses[0].Status) != "listening" {
		t.Fatalf("expected listening listener status, got %+v", item.ListenState.ListenerStatuses[0])
	}
}

func TestMngVirtualRouterSideStatsErrorIgnoresStaleErrorAfterBridgeReconnect(t *testing.T) {
	side := mngVirtualRouterRouteSideStatus{
		VirtualRouter: &probeVirtualRouterRuntimeStats{
			LastPingError: "upstream bridge is unavailable",
			LastPingAt:    "2026-06-28T00:12:00Z",
		},
		BridgeStatus: &probeChainBridgeRuntimeStatus{
			UpstreamActive: 1,
			Sessions: []probeChainBridgeSessionSnapshot{
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
	if got := mngVirtualRouterSideStatsError(side); got != "upstream bridge is unavailable" {
		t.Fatalf("current error=%q, want upstream bridge is unavailable", got)
	}
}

func TestMngLinkVirtualRouterStatusHandlerReturnsRuleRuntimeStatus(t *testing.T) {
	oldStore := ProbeLinkChainStore
	oldProbeStore := ProbeStore
	probeRuntimeStore.mu.Lock()
	oldRuntimeData := probeRuntimeStore.data
	probeRuntimeStore.data = make(map[string]probeRuntimeStatus)
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		ProbeLinkChainStore = oldStore
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
		Direction:       probeVirtualRouterDirectionTwoWay,
		FromServicePort: 12040,
		ToServicePort:   12040,
		Enabled:         true,
	}
	ProbeLinkChainStore = &probeLinkChainStore{
		path: filepath.Join(tmpDir, "probe_link_chains.json"),
		data: probeLinkChainStoreData{
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
	chainID := probeVirtualRouterRuntimeChainID(rule)
	updateProbeRuntimeReportWithRelay("1", nil, nil, probeSystemMetrics{}, "v1", []probeRelayStatusItem{
		{
			ChainID:    chainID,
			ChainType:  "virtual_router",
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
			BridgeSessions: []probeChainBridgeSessionSnapshot{
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
				PacketsForwarded:  2,
				BytesForwarded:    200,
				FramesSent:        2,
				FrameBytesSent:    200,
				PingCount:         1,
				LastPingLatencyMS: 12,
				LastPingAt:        "2026-06-27T00:00:01Z",
				LastPacketAt:      "2026-06-27T00:00:01Z",
				LastFrameAt:       "2026-06-27T00:00:01Z",
				TUNDataPlane:      true,
				TUNRXPackets:      11,
				TUNRXBytes:        1100,
				TUNTXPackets:      5,
				TUNTXBytes:        500,
			},
		},
	})
	updateProbeRuntimeReportWithRelay("2", nil, nil, probeSystemMetrics{}, "v1", []probeRelayStatusItem{
		{
			ChainID:    chainID,
			ChainType:  "virtual_router",
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

	req := httptest.NewRequest(http.MethodGet, "/mng/api/link/virtual_router/status", nil)
	rr := httptest.NewRecorder()
	mngLinkVirtualRouterStatusHandler(rr, req)
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
	if item.Status != "ready" || item.ChainID != chainID {
		t.Fatalf("unexpected route status: %+v", item)
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
	if len(item.From.BridgeSessions) != 1 ||
		item.From.BridgeSessions[0].FramesSent != 7 ||
		item.From.BridgeSessions[0].FrameBytesSent != 700 ||
		item.From.BridgeSessions[0].FramesReceived != 8 ||
		item.From.BridgeSessions[0].FrameBytesReceived != 800 {
		t.Fatalf("unexpected bridge session frame stats: %+v", item.From.BridgeSessions)
	}
}

func TestMngLinkVirtualRouterHandlerSaveAndGet(t *testing.T) {
	oldStore := ProbeLinkChainStore
	oldProbeStore := ProbeStore
	t.Cleanup(func() {
		ProbeLinkChainStore = oldStore
		ProbeStore = oldProbeStore
	})

	tmpDir := t.TempDir()
	ProbeLinkChainStore = &probeLinkChainStore{
		path: filepath.Join(tmpDir, "probe_link_chains.json"),
		data: probeLinkChainStoreData{
			Chains:        []probeLinkChainRecord{},
			DeletedChains: []probeLinkChainRecord{},
			NextChainID:   1,
			EntryProfiles: []probeLinkEntryProfileRecord{},
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
	saveReq := httptest.NewRequest(http.MethodPost, "/mng/api/link/virtual_router", bytes.NewReader(body))
	saveRR := httptest.NewRecorder()
	mngLinkVirtualRouterHandler(saveRR, saveReq)
	if saveRR.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", saveRR.Code, saveRR.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/mng/api/link/virtual_router", nil)
	getRR := httptest.NewRecorder()
	mngLinkVirtualRouterHandler(getRR, getReq)
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
	if len(payload.Item.TopologyRules) != 2 || payload.Item.TopologyRules[0].Direction != probeVirtualRouterDirectionTwoWay {
		t.Fatalf("topology rules=%+v", payload.Item.TopologyRules)
	}
	first := payload.Item.TopologyRules[0]
	if first.FromServiceDomain != "edge-a.example.com" || first.FromServicePort != 443 || first.ToServiceDomain != "edge-b.internal.lan" || first.ToServicePort != 443 {
		t.Fatalf("service config not persisted: %+v", first)
	}
	if strings.TrimSpace(first.Secret) == "" {
		t.Fatalf("virtual router rule secret should be generated")
	}
	if payload.Item.TopologyRules[1].FromServicePort != 443 || payload.Item.TopologyRules[1].ToServicePort != 443 {
		t.Fatalf("service port reuse should be allowed: %+v", payload.Item.TopologyRules)
	}
}

func TestMngLinkVirtualRouterHandlerRejectsProbeIPOutsideReservedPool(t *testing.T) {
	oldStore := ProbeLinkChainStore
	t.Cleanup(func() { ProbeLinkChainStore = oldStore })
	ProbeLinkChainStore = &probeLinkChainStore{path: filepath.Join(t.TempDir(), "probe_link_chains.json")}

	body := []byte(`{"probe_ips":[{"node_id":"1","ip":"198.18.4.1"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/mng/api/link/virtual_router", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mngLinkVirtualRouterHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rr.Code, rr.Body.String())
	}
}

func TestMngLinkVirtualRouterHandlerRejectsInvalidServicePort(t *testing.T) {
	oldStore := ProbeLinkChainStore
	t.Cleanup(func() { ProbeLinkChainStore = oldStore })
	ProbeLinkChainStore = &probeLinkChainStore{path: filepath.Join(t.TempDir(), "probe_link_chains.json")}

	body := []byte(`{
  "probe_ips":[
    {"node_id":"1","ip":"198.18.0.3"},
    {"node_id":"2","ip":"198.18.0.4"}
  ],
  "topology_rules":[
    {"from_node_id":"1","to_node_id":"2","direction":"bidirectional","from_service_port":65536,"enabled":true}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/mng/api/link/virtual_router", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mngLinkVirtualRouterHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rr.Code, rr.Body.String())
	}
}
