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

func TestDashboardNetworkRouteNoAuthRequired(t *testing.T) {
	restorePath := core.SetProbeNetworkMonitorResultStorePathForTest(filepath.Join(t.TempDir(), "network_monitor_results"))
	t.Cleanup(restorePath)
	if err := core.AppendProbeNetworkMonitorResultForTest("1", "2026-01-01T00:00:00Z", 23.5, 1.5); err != nil {
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
	if payload.Items[0].Series[0].Points[0].LatencyAvgMS != 23.5 || payload.Items[0].Series[0].Points[0].LossPercent != 1.5 {
		t.Fatalf("point=%+v", payload.Items[0].Series[0].Points[0])
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
