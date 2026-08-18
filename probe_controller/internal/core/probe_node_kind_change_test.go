package core

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestClearProbeNodeKindConfigsRemovesOnlyConvertedNode(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(t.TempDir(), "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: defaultProbeVirtualRouterConfig(),
			SpecialExits: []probeSpecialExitConfig{
				{NodeID: "19"},
				{NodeID: "20"},
			},
			LinuxRouters: []probeLinuxRouterConfig{
				{NodeID: "19"},
				{NodeID: "21"},
			},
		},
	}
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore })

	if err := clearProbeNodeKindConfigs("19"); err != nil {
		t.Fatal(err)
	}
	if len(ProbeRouteConfigStore.data.SpecialExits) != 1 || ProbeRouteConfigStore.data.SpecialExits[0].NodeID != "20" {
		t.Fatalf("special exits = %+v", ProbeRouteConfigStore.data.SpecialExits)
	}
	if len(ProbeRouteConfigStore.data.LinuxRouters) != 1 || ProbeRouteConfigStore.data.LinuxRouters[0].NodeID != "21" {
		t.Fatalf("linux routers = %+v", ProbeRouteConfigStore.data.LinuxRouters)
	}
}

func TestMngProbeNodeKindChangeRequiresReinstallAndClosesOldSession(t *testing.T) {
	oldProbeStore := ProbeStore
	oldRouteStore := ProbeRouteConfigStore
	ProbeStore = &probeConfigStore{
		path: filepath.Join(t.TempDir(), "probe_store.json"),
		data: probeConfigData{
			ProbeNodes:   []probeNodeRecord{{NodeNo: 7, NodeName: "node-7", NodeKind: probeNodeKindNormal, TargetSystem: "linux", NodeSecret: "old-secret"}},
			ProbeSecrets: map[string]string{"7": "old-secret"},
		},
	}
	ProbeRouteConfigStore = nil
	probeRuntimeStore.mu.Lock()
	oldRuntimeData := probeRuntimeStore.data
	probeRuntimeStore.data = make(map[string]probeRuntimeStatus)
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		ProbeStore = oldProbeStore
		ProbeRouteConfigStore = oldRouteStore
		probeRuntimeStore.mu.Lock()
		probeRuntimeStore.data = oldRuntimeData
		probeRuntimeStore.mu.Unlock()
	})

	controllerConn, probeConn := net.Pipe()
	t.Cleanup(func() {
		_ = controllerConn.Close()
		_ = probeConn.Close()
	})
	session := &probeSession{nodeID: "7", stream: controllerConn, enc: json.NewEncoder(controllerConn)}
	probeSessions.mu.Lock()
	previousSession := probeSessions.data["7"]
	probeSessions.data["7"] = session
	probeSessions.mu.Unlock()
	t.Cleanup(func() {
		probeSessions.mu.Lock()
		if previousSession != nil {
			probeSessions.data["7"] = previousSession
		} else {
			delete(probeSessions.data, "7")
		}
		probeSessions.mu.Unlock()
	})

	body := bytes.NewBufferString(`{"node_no":7,"node_name":"node-7","node_kind":"linux_router","target_system":"linux"}`)
	rr := httptest.NewRecorder()
	mngProbeNodeUpdateHandler(rr, httptest.NewRequest(http.MethodPost, "/mng/api/probe/node/update", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response struct {
		Node                  probeNodeRecord `json:"node"`
		NodeKindChanged       bool            `json:"node_kind_changed"`
		ReinstallRequired     bool            `json:"reinstall_required"`
		NodeSecretRotated     bool            `json:"node_secret_rotated"`
		PreviousSessionClosed bool            `json:"previous_session_closed"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.NodeKindChanged || !response.ReinstallRequired || !response.NodeSecretRotated || !response.PreviousSessionClosed {
		t.Fatalf("unexpected response: %+v body=%s", response, rr.Body.String())
	}
	if response.Node.NodeKind != probeNodeKindLinuxRouter || response.Node.NodeSecret == "" || response.Node.NodeSecret == "old-secret" {
		t.Fatalf("node was not converted safely: %+v", response.Node)
	}
	if _, ok := getProbeSession("7"); ok {
		t.Fatal("old probe session remained registered")
	}
}
