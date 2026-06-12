package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudhelper/probe_controller/internal/core"
)

func TestDashboardRouteAndRootRedirect(t *testing.T) {
	mux := core.NewMux()

	reqDashboard := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rrDashboard := httptest.NewRecorder()
	mux.ServeHTTP(rrDashboard, reqDashboard)

	if rrDashboard.Code != http.StatusOK {
		t.Fatalf("expected GET /dashboard 200, got %d", rrDashboard.Code)
	}

	reqRoot := httptest.NewRequest(http.MethodGet, "/", nil)
	rrRoot := httptest.NewRecorder()
	mux.ServeHTTP(rrRoot, reqRoot)

	if rrRoot.Code != http.StatusFound {
		t.Fatalf("expected GET / 302, got %d", rrRoot.Code)
	}
	if got := rrRoot.Header().Get("Location"); got != "/dashboard" {
		t.Fatalf("expected redirect to /dashboard, got %q", got)
	}
}

func TestDashboardStatusRouteNoAuthRequired(t *testing.T) {
	core.SetServerStartTimeForTest(time.Now().Add(-1 * time.Minute))
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected GET /dashboard/status 200, got %d", rr.Code)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to parse /dashboard/status response: %v", err)
	}
	if _, ok := payload["uptime"]; !ok {
		t.Fatalf("expected uptime field in /dashboard/status response")
	}
}

func TestFaviconRoutesNoAuthRequired(t *testing.T) {
	mux := core.NewMux()

	reqSVG := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	rrSVG := httptest.NewRecorder()
	mux.ServeHTTP(rrSVG, reqSVG)
	if rrSVG.Code != http.StatusOK {
		t.Fatalf("expected GET /favicon.svg 200, got %d", rrSVG.Code)
	}
	if got := rrSVG.Header().Get("Content-Type"); got == "" {
		t.Fatalf("expected content-type for /favicon.svg")
	}

	reqICO := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	rrICO := httptest.NewRecorder()
	mux.ServeHTTP(rrICO, reqICO)
	if rrICO.Code != http.StatusFound {
		t.Fatalf("expected GET /favicon.ico 302, got %d", rrICO.Code)
	}
	if got := rrICO.Header().Get("Location"); got != "/favicon.svg" {
		t.Fatalf("expected /favicon.ico redirect to /favicon.svg, got %q", got)
	}
}

func TestDashboardProbesExposeMachineUptime(t *testing.T) {
	core.ResetProbeRuntimeStoreForTest()
	t.Cleanup(core.ResetProbeRuntimeStoreForTest)
	restoreStore := core.SetProbeNodesAndNetworkMonitorTasksForTest(map[int]string{1: "probe-a"}, nil)
	t.Cleanup(restoreStore)
	core.UpdateProbeRuntimeReportForTest("1", 3661)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/probes", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected GET /dashboard/probes 200, got %d", rr.Code)
	}
	var payload struct {
		Items []struct {
			MachineUptimeSeconds int64 `json:"machine_uptime_seconds"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to parse /dashboard/probes response: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("expected one probe item, got %d", len(payload.Items))
	}
	if payload.Items[0].MachineUptimeSeconds != 3661 {
		t.Fatalf("machine uptime=%d want 3661", payload.Items[0].MachineUptimeSeconds)
	}
}

func TestDashboardProbesExposeRuntimeLocations(t *testing.T) {
	core.ResetProbeRuntimeStoreForTest()
	t.Cleanup(core.ResetProbeRuntimeStoreForTest)
	restoreStore := core.SetProbeNodesAndNetworkMonitorTasksForTest(map[int]string{1: "probe-a"}, nil)
	t.Cleanup(restoreStore)
	core.UpdateProbeRuntimeReportWithIPsForTest("1", []string{"10.0.0.8", "192.168.1.9"}, nil)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/probes", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected GET /dashboard/probes 200, got %d", rr.Code)
	}
	var payload struct {
		Items []struct {
			NodeNo    int      `json:"node_no"`
			Locations []string `json:"locations"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to parse /dashboard/probes response: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].NodeNo != 1 {
		t.Fatalf("unexpected probe items: %+v", payload.Items)
	}
	if len(payload.Items[0].Locations) != 1 || payload.Items[0].Locations[0] != "内网" {
		t.Fatalf("locations=%v, want [内网]", payload.Items[0].Locations)
	}
}

func TestDashboardProbesHideDeletedAndAndroidClients(t *testing.T) {
	core.ResetProbeRuntimeStoreForTest()
	t.Cleanup(core.ResetProbeRuntimeStoreForTest)
	restoreStore := core.SetProbeNodeRecordsAndNetworkMonitorTasksForTest([]core.ProbeNodeForTest{
		{NodeNo: 1, NodeName: "probe-a", TargetSystem: "linux"},
		{NodeNo: 2, NodeName: "android-client", TargetSystem: "android"},
		{NodeNo: 3, NodeName: "deleted-probe", TargetSystem: "linux", Deleted: true},
	}, nil)
	t.Cleanup(restoreStore)
	core.UpdateProbeRuntimeReportForTest("1", 100)
	core.UpdateProbeRuntimeReportForTest("2", 200)
	core.UpdateProbeRuntimeReportForTest("3", 300)
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/probes", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected GET /dashboard/probes 200, got %d", rr.Code)
	}
	var payload struct {
		Items []struct {
			NodeNo int `json:"node_no"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to parse /dashboard/probes response: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].NodeNo != 1 {
		t.Fatalf("expected only linux active probe 1, got %+v", payload.Items)
	}
}

func TestDashboardNetworkRouteNoAuthRequired(t *testing.T) {
	restorePath := core.SetProbeNetworkMonitorResultStorePathForTest(filepath.Join(t.TempDir(), "network_monitor_results"))
	t.Cleanup(restorePath)
	restoreTasks := core.SetProbeNodesAndNetworkMonitorTasksForTest(map[int]string{1: "probe-a"}, map[string]string{"task-1": "外网测试"})
	t.Cleanup(restoreTasks)
	if err := core.AppendProbeNetworkMonitorResultForTest("1", "task-1", "2026-01-01T00:00:00Z", 23.5, 1.5); err != nil {
		t.Fatalf("AppendProbeNetworkMonitorResultForTest failed: %v", err)
	}
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/network", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected GET /dashboard/network 200, got %d", rr.Code)
	}
	var payload struct {
		Items []struct {
			NodeNo int `json:"node_no"`
			Series []struct {
				TaskName string `json:"task_name"`
				Points   []struct {
					LatencyAvgMS float64 `json:"latency_avg_ms"`
					LossPercent  float64 `json:"loss_percent"`
				} `json:"points"`
			} `json:"series"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to parse /dashboard/network response: %v", err)
	}
	if len(payload.Items) != 1 || len(payload.Items[0].Series) != 1 || len(payload.Items[0].Series[0].Points) != 1 {
		t.Fatalf("unexpected network payload: %+v", payload)
	}
	if payload.Items[0].NodeNo != 1 {
		t.Fatalf("node_no=%d want 1", payload.Items[0].NodeNo)
	}
	if payload.Items[0].Series[0].TaskName != "外网测试" {
		t.Fatalf("task_name=%q want 外网测试", payload.Items[0].Series[0].TaskName)
	}
	if payload.Items[0].Series[0].Points[0].LatencyAvgMS != 23.5 || payload.Items[0].Series[0].Points[0].LossPercent != 1.5 {
		t.Fatalf("point=%+v", payload.Items[0].Series[0].Points[0])
	}
}

func TestDashboardNetworkExposesLocationsAndVendor(t *testing.T) {
	core.ResetProbeRuntimeStoreForTest()
	t.Cleanup(core.ResetProbeRuntimeStoreForTest)
	restorePath := core.SetProbeNetworkMonitorResultStorePathForTest(filepath.Join(t.TempDir(), "network_monitor_results"))
	t.Cleanup(restorePath)
	restoreStore := core.SetProbeNodeRecordsAndNetworkMonitorTasksForTest([]core.ProbeNodeForTest{
		{NodeNo: 1, NodeName: "probe-a", TargetSystem: "linux", VendorName: "Acme", VendorURL: "https://vendor.example.com"},
	}, map[string]string{"task-1": "外网测试"})
	t.Cleanup(restoreStore)
	core.UpdateProbeRuntimeReportWithIPsForTest("1", []string{"10.0.0.8"}, nil)
	if err := core.AppendProbeNetworkMonitorResultForTest("1", "task-1", "2026-01-01T00:00:00Z", 23.5, 1.5); err != nil {
		t.Fatalf("AppendProbeNetworkMonitorResultForTest failed: %v", err)
	}
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/network", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected GET /dashboard/network 200, got %d", rr.Code)
	}
	var payload struct {
		Items []struct {
			NodeNo     int      `json:"node_no"`
			VendorName string   `json:"vendor_name"`
			VendorURL  string   `json:"vendor_url"`
			Locations  []string `json:"locations"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to parse /dashboard/network response: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].NodeNo != 1 {
		t.Fatalf("unexpected network items: %+v", payload.Items)
	}
	if payload.Items[0].VendorName != "Acme" || payload.Items[0].VendorURL != "https://vendor.example.com" {
		t.Fatalf("vendor=%q/%q", payload.Items[0].VendorName, payload.Items[0].VendorURL)
	}
	if len(payload.Items[0].Locations) != 1 || payload.Items[0].Locations[0] != "内网" {
		t.Fatalf("locations=%v, want [内网]", payload.Items[0].Locations)
	}
}

func TestDashboardNetworkHideDeletedAndAndroidClients(t *testing.T) {
	restorePath := core.SetProbeNetworkMonitorResultStorePathForTest(filepath.Join(t.TempDir(), "network_monitor_results"))
	t.Cleanup(restorePath)
	restoreStore := core.SetProbeNodeRecordsAndNetworkMonitorTasksForTest([]core.ProbeNodeForTest{
		{NodeNo: 1, NodeName: "probe-a", TargetSystem: "linux"},
		{NodeNo: 2, NodeName: "android-client", TargetSystem: "android"},
		{NodeNo: 3, NodeName: "deleted-probe", TargetSystem: "linux", Deleted: true},
	}, map[string]string{"task-1": "外网测试"})
	t.Cleanup(restoreStore)
	for _, nodeID := range []string{"1", "2", "3"} {
		if err := core.AppendProbeNetworkMonitorResultForTest(nodeID, "task-1", "2026-01-01T00:00:00Z", 23.5, 1.5); err != nil {
			t.Fatalf("AppendProbeNetworkMonitorResultForTest node %s failed: %v", nodeID, err)
		}
	}
	mux := core.NewMux()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/network", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected GET /dashboard/network 200, got %d", rr.Code)
	}
	var payload struct {
		Items []struct {
			NodeNo int `json:"node_no"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to parse /dashboard/network response: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].NodeNo != 1 {
		t.Fatalf("expected only linux active probe 1, got %+v", payload.Items)
	}
}

func TestDashboardPrefixedFaviconRoutesNoAuthRequired(t *testing.T) {
	mux := core.NewMux()

	reqSVG := httptest.NewRequest(http.MethodGet, "/dashboard/favicon.svg", nil)
	rrSVG := httptest.NewRecorder()
	mux.ServeHTTP(rrSVG, reqSVG)
	if rrSVG.Code != http.StatusOK {
		t.Fatalf("expected GET /dashboard/favicon.svg 200, got %d", rrSVG.Code)
	}
	if got := rrSVG.Header().Get("Content-Type"); got == "" {
		t.Fatalf("expected content-type for /dashboard/favicon.svg")
	}

	reqICO := httptest.NewRequest(http.MethodGet, "/dashboard/favicon.ico", nil)
	rrICO := httptest.NewRecorder()
	mux.ServeHTTP(rrICO, reqICO)
	if rrICO.Code != http.StatusFound {
		t.Fatalf("expected GET /dashboard/favicon.ico 302, got %d", rrICO.Code)
	}
	if got := rrICO.Header().Get("Location"); got != "/dashboard/favicon.svg" {
		t.Fatalf("expected /dashboard/favicon.ico redirect to /dashboard/favicon.svg, got %q", got)
	}
}
