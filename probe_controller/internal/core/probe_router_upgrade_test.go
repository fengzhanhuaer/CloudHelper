package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRouterUpgradeReportProgressAndLegacyClear(t *testing.T) {
	const id = "9822"
	probeRuntimeStore.mu.Lock()
	previous, existed := probeRuntimeStore.data[id]
	probeRuntimeStore.data[id] = probeRuntimeStatus{BuildKind: probeNodeKindAsus56UV2, RouterPluginUpgrade: true}
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		probeRuntimeStore.mu.Lock()
		defer probeRuntimeStore.mu.Unlock()
		if existed {
			probeRuntimeStore.data[id] = previous
		} else {
			delete(probeRuntimeStore.data, id)
		}
	})
	var report probeReportMessage
	if err := json.Unmarshal([]byte(`{"router_plugin_upgrade_status":{"phase":"failed","message":"下载未完成","source":"controller_ip","endpoint":"https://192.0.2.1:15030","downloaded_bytes":1500,"total_bytes":137177,"http_status":503,"curl_code":18,"error":"partial file"}}`), &report); err != nil {
		t.Fatal(err)
	}
	updateProbeRuntimeRouterPluginUpgradeStatus(id, report.RouterPluginUpgradeStatus)
	probeRuntimeStore.mu.RLock()
	got := probeRuntimeStore.data[id].RouterPluginUpgradeStatus
	probeRuntimeStore.mu.RUnlock()
	if got == nil || got.DownloadedBytes != 1500 || got.TotalBytes != 137177 || got.Source != "controller_ip" || got.HTTPStatus != 503 || got.Error != "partial file" {
		t.Fatalf("lost failure diagnostics: %+v", got)
	}
	report.RouterPluginUpgradeStatus.Error = strings.Repeat("x", 1000)
	if got.Error != "partial file" {
		t.Fatal("stored report aliases inbound object")
	}
	updateProbeRuntimeRouterPluginUpgradeStatus(id, report.RouterPluginUpgradeStatus)
	probeRuntimeStore.mu.RLock()
	length := len(probeRuntimeStore.data[id].RouterPluginUpgradeStatus.Error)
	probeRuntimeStore.mu.RUnlock()
	if length != 256 {
		t.Fatalf("unbounded error length: %d", length)
	}
	updateProbeRuntimeRouterPluginUpgradeStatus(id, nil)
	probeRuntimeStore.mu.RLock()
	got = probeRuntimeStore.data[id].RouterPluginUpgradeStatus
	probeRuntimeStore.mu.RUnlock()
	if got != nil {
		t.Fatal("legacy report retained stale upgrade task")
	}
}
