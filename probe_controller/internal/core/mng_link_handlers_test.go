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
    {"from_node_id":"1","to_node_id":"2","direction":"bidirectional","enabled":true}
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
	if len(payload.Item.TopologyRules) != 1 || payload.Item.TopologyRules[0].Direction != probeVirtualRouterDirectionTwoWay {
		t.Fatalf("topology rules=%+v", payload.Item.TopologyRules)
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
