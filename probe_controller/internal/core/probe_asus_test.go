package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAsusNodeKindPersistsAndRejectsGenericInstallAndUpgrade(t *testing.T) {
	old := ProbeStore
	ProbeStore = &probeConfigStore{data: probeConfigData{}}
	t.Cleanup(func() { ProbeStore = old })
	node, err := createProbeNodeWithKindLocked("asus-test", probeNodeKindAsus56UV2)
	if err != nil {
		t.Fatal(err)
	}
	if node.NodeKind != probeNodeKindAsus56UV2 || node.TargetSystem != "linux" || node.LocalConsoleEnabled {
		t.Fatalf("unexpected ASUS capabilities: kind=%s os=%s local=%v", node.NodeKind, node.TargetSystem, node.LocalConsoleEnabled)
	}
	if probeNodeSupportsSpecialExit(node.NodeKind) {
		t.Fatal("ASUS must not inherit Mihomo")
	}
	updated, err := updateProbeNodeLocked(probeNodeUpdateRequest{NodeNo: node.NodeNo, NodeName: node.NodeName, TargetSystem: "linux", LocalConsoleEnabled: true})
	if err != nil || updated.NodeKind != probeNodeKindAsus56UV2 || updated.LocalConsoleEnabled {
		t.Fatal("update lost ASUS capabilities", err)
	}
	if _, err = updateProbeNodeLocked(probeNodeUpdateRequest{NodeNo: node.NodeNo, NodeName: node.NodeName, TargetSystem: "windows"}); err == nil {
		t.Fatal("accepted Windows")
	}
	if _, err = dispatchUpgradeToProbe(node, "http://controller.test"); err == nil || !strings.Contains(err.Error(), "JFFS插件") {
		t.Fatal("did not reject binary upgrade before session dispatch", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/probe/proxy/probe-node/install-script", nil)
	q := r.URL.Query()
	q.Set("node_id", "1")
	q.Set("secret", node.NodeSecret)
	r.URL.RawQuery = q.Encode()
	w := httptest.NewRecorder()
	ProbeProxyInstallScriptHandler(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "原厂系统JFFS插件") {
		t.Fatalf("generic install status=%d", w.Code)
	}
	w = httptest.NewRecorder()
	mngProbeNodeInstallHandler(w, httptest.NewRequest(http.MethodGet, "/mng/api/probe/node/install?node_id=1", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"install_mode":"jffs_plugin"`) || strings.Contains(w.Body.String(), "curl ") {
		t.Fatalf("invalid install guidance status=%d", w.Code)
	}
}

func TestAsusEdgesUseTLSWebSocketInBothEndpointSnapshots(t *testing.T) {
	setProbeVirtualRouterTestProbeStore(t, probeConfigData{ProbeNodes: []probeNodeRecord{
		{NodeNo: 1, NodeKind: probeNodeKindNormal},
		{NodeNo: 2, NodeKind: probeNodeKindAsus56UV2},
		{NodeNo: 3, NodeKind: probeNodeKindNormal},
	}})
	old := ProbeRouteConfigStore
	t.Cleanup(func() { ProbeRouteConfigStore = old })
	ProbeRouteConfigStore = &probeRouteConfigStore{data: probeRouteConfigStoreData{VirtualRouter: probeVirtualRouterConfig{
		Enabled: true, FakeIPCIDR: "198.18.0.0/15",
		TopologyRules: []probeVirtualRouterTopologyRule{
			{ID: "in", FromNodeID: "1", ToNodeID: "2", RouteLayer: "http3", CarrierCount: 64, Enabled: true},
			{ID: "out", FromNodeID: "2", ToNodeID: "3", RouteLayer: "auto", CarrierCount: 16, Enabled: true},
			{ID: "ordinary", FromNodeID: "1", ToNodeID: "3", RouteLayer: "http3", CarrierCount: 4, Enabled: true},
		},
	}}}
	for _, nodeID := range []string{"1", "2", "3"} {
		config := buildProbeVirtualRouterConfigForNodeLocked(nodeID)
		for _, edge := range config.TopologyRules {
			if edge.ID == "ordinary" {
				if edge.RouteLayer != "http3" || edge.CarrierCount != 4 {
					t.Fatal("ordinary edge capabilities changed")
				}
			} else if edge.RouteLayer != "http2" || edge.CarrierCount != 1 {
				t.Fatalf("node %s edge %s requires unsupported transport: %s/%d", nodeID, edge.ID, edge.RouteLayer, edge.CarrierCount)
			}
		}
	}
	stored := ProbeRouteConfigStore.data.VirtualRouter.TopologyRules
	if stored[0].RouteLayer != "http3" || stored[0].CarrierCount != 64 || stored[1].RouteLayer != "auto" {
		t.Fatal("capability adaptation mutated stored topology")
	}
}

func TestAsusInstallInfoExportsImportableConfig(t *testing.T) {
	old := ProbeStore
	ProbeStore = &probeConfigStore{data: probeConfigData{}}
	t.Cleanup(func() { ProbeStore = old })
	node, err := createProbeNodeWithKindLocked("asus-import-test", probeNodeKindAsus56UV2)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://controller.example/mng/api/probe/node/install?node_id=1", nil)
	w := httptest.NewRecorder()
	mngProbeNodeInstallHandler(w, req)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%s", w.Code, w.Header().Get("Cache-Control"))
	}
	var response struct {
		Command string                 `json:"command"`
		Config  map[string]interface{} `json:"config"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var copied map[string]interface{}
	if err := json.Unmarshal([]byte(response.Command), &copied); err != nil {
		t.Fatal("copy content is not JSON", err)
	}
	for _, config := range []map[string]interface{}{response.Config, copied} {
		if config["schema_version"] != float64(1) || config["product"] != "cloudhelper-asus56uv2" || config["controller"] != "https://controller.example" || config["node_id"] != "1" || config["node_key"] != node.NodeSecret {
			t.Fatal("exported config does not match node identity")
		}
	}
}
