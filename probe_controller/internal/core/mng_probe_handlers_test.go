package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMngProbeNodesHandlerIncludesRuntimeVersionWithoutStatusCall(t *testing.T) {
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{
		data: probeConfigData{
			ProbeNodes: []probeNodeRecord{
				{NodeNo: 1, NodeName: "node-1", TargetSystem: "linux"},
			},
			DeletedProbeNodes:   []probeNodeRecord{},
			ProbeSecrets:        map[string]string{},
			ProbeShellShortcuts: []probeShellShortcutRecord{},
			DeletedProbeNodeNos: []int{},
		},
	}
	defer func() {
		ProbeStore = oldStore
	}()

	probeRuntimeStore.mu.Lock()
	oldRuntimeData := probeRuntimeStore.data
	probeRuntimeStore.data = make(map[string]probeRuntimeStatus)
	probeRuntimeStore.mu.Unlock()
	defer func() {
		probeRuntimeStore.mu.Lock()
		probeRuntimeStore.data = oldRuntimeData
		probeRuntimeStore.mu.Unlock()
	}()

	updateProbeRuntimeReport("1", nil, nil, probeSystemMetrics{}, "v1.2.3")

	req := httptest.NewRequest(http.MethodGet, "/mng/api/probe/nodes", nil)
	rr := httptest.NewRecorder()
	mngProbeNodesHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Nodes []struct {
			NodeNo  int `json:"node_no"`
			Runtime struct {
				Version string `json:"version"`
			} `json:"runtime"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode payload: %v body=%s", err, rr.Body.String())
	}
	if len(payload.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d payload=%s", len(payload.Nodes), rr.Body.String())
	}
	if payload.Nodes[0].NodeNo != 1 {
		t.Fatalf("expected node_no=1, got %d", payload.Nodes[0].NodeNo)
	}
	if strings.TrimSpace(payload.Nodes[0].Runtime.Version) != "v1.2.3" {
		t.Fatalf("expected runtime.version=v1.2.3, got %q", payload.Nodes[0].Runtime.Version)
	}
}
