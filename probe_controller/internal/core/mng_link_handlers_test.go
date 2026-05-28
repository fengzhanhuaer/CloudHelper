package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
