package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeRouteSettingsHandlerUpdatesControllerRouteRules(t *testing.T) {
	oldProbeStore := ProbeStore
	oldRouteStore := ProbeRouteConfigStore
	oldSaveRouteSettings := saveProbeRouteSettings
	resetProbeAuthChallengeStateForTest()
	t.Cleanup(func() {
		ProbeStore = oldProbeStore
		ProbeRouteConfigStore = oldRouteStore
		saveProbeRouteSettings = oldSaveRouteSettings
		resetProbeAuthChallengeStateForTest()
	})

	tmpDir := t.TempDir()
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes: []probeNodeRecord{
			{NodeNo: 9, NodeName: "Android Phone"},
			{NodeNo: 17, NodeName: "Los Angeles"},
			{NodeNo: 18, NodeName: "Tokyo"},
		},
		ProbeSecrets: map[string]string{"9": "secret-9"},
	}}
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{VirtualRouter: probeVirtualRouterConfig{
			Enabled:    true,
			FakeIPCIDR: probeVirtualRouterDefaultCIDR,
			ProbeIPs: []probeVirtualRouterProbeIP{
				{NodeID: "9", IP: "198.18.0.9"},
				{NodeID: "17", IP: "198.18.0.17"},
				{NodeID: "18", IP: "198.18.0.18"},
			},
			RouteRules: []probeVirtualRouterRouteRule{{
				ID: "rr-ai", Name: "AI", Action: probeVirtualRouterRouteRuleActionExit, ExitNodeID: "17", Entries: []string{"domain_suffix:chatgpt.com"},
			}},
		}},
	}
	saveProbeRouteSettings = func() error { return ProbeRouteConfigStore.SaveWithoutAutoBackup() }

	getReq := httptest.NewRequest(http.MethodGet, "https://controller.example/api/probe/route/settings", nil)
	applyProbeChallengeAuthForTest(t, getReq, "9", "secret-9")
	getResponse := httptest.NewRecorder()
	ProbeRouteSettingsHandler(getResponse, getReq)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
	var initial probeRouteSettingsResponse
	if err := json.Unmarshal(getResponse.Body.Bytes(), &initial); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if len(initial.Groups) != 1 || initial.Groups[0].ExitNodeID != "17" {
		t.Fatalf("initial groups=%+v", initial.Groups)
	}
	if len(initial.Nodes) != 3 || initial.Nodes[0].DisplayName != "Android Phone" || initial.Nodes[1].DisplayName != "Los Angeles" || initial.Nodes[2].DisplayName != "Tokyo" {
		t.Fatalf("nodes=%+v", initial.Nodes)
	}

	postReq := httptest.NewRequest(http.MethodPost, "https://controller.example/api/probe/route/settings", bytes.NewBufferString(`{"exit_nodes":{"rr-ai":"18"}}`))
	applyProbeChallengeAuthForTest(t, postReq, "9", "secret-9")
	postResponse := httptest.NewRecorder()
	ProbeRouteSettingsHandler(postResponse, postReq)
	if postResponse.Code != http.StatusOK {
		t.Fatalf("post status=%d body=%s", postResponse.Code, postResponse.Body.String())
	}
	ProbeRouteConfigStore.mu.RLock()
	stored := ProbeRouteConfigStore.data.VirtualRouter.RouteRules[0]
	ProbeRouteConfigStore.mu.RUnlock()
	if stored.ExitNodeID != "18" {
		t.Fatalf("stored route rule=%+v", stored)
	}
	configForOtherNode := buildProbeVirtualRouterConfigForNodeLocked("17")
	if len(configForOtherNode.RouteRules) != 1 || configForOtherNode.RouteRules[0].ExitNodeID != "18" {
		t.Fatalf("config for another node=%+v", configForOtherNode.RouteRules)
	}
	raw, err := os.ReadFile(filepath.Join(tmpDir, "probe_route_config.json"))
	if err != nil {
		t.Fatalf("read persisted route config: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"exit_node_id": "18"`)) {
		t.Fatalf("persisted route config does not contain updated exit: %s", raw)
	}
}

func TestProbeRouteSettingsHandlerRejectsUnknownExitNode(t *testing.T) {
	oldProbeStore := ProbeStore
	oldRouteStore := ProbeRouteConfigStore
	resetProbeAuthChallengeStateForTest()
	t.Cleanup(func() {
		ProbeStore = oldProbeStore
		ProbeRouteConfigStore = oldRouteStore
		resetProbeAuthChallengeStateForTest()
	})
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 9}, {NodeNo: 17}},
		ProbeSecrets: map[string]string{"9": "secret-9"},
	}}
	ProbeRouteConfigStore = &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "probe_route_config.json"), data: probeRouteConfigStoreData{
		VirtualRouter: probeVirtualRouterConfig{
			Enabled:    true,
			ProbeIPs:   []probeVirtualRouterProbeIP{{NodeID: "9", IP: "198.18.0.9"}, {NodeID: "17", IP: "198.18.0.17"}},
			RouteRules: []probeVirtualRouterRouteRule{{ID: "rr-ai", Name: "AI", Action: probeVirtualRouterRouteRuleActionExit, ExitNodeID: "17"}},
		},
	}}

	req := httptest.NewRequest(http.MethodPost, "https://controller.example/api/probe/route/settings", bytes.NewBufferString(`{"exit_nodes":{"rr-ai":"99"}}`))
	applyProbeChallengeAuthForTest(t, req, "9", "secret-9")
	w := httptest.NewRecorder()
	ProbeRouteSettingsHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := ProbeRouteConfigStore.data.VirtualRouter.RouteRules[0].ExitNodeID; got != "17" {
		t.Fatalf("invalid request changed route exit to %s", got)
	}
}
