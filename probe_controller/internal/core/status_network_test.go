package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDashboardProbeNetworkLatestAndPublicContract(t *testing.T) {
	restore := SetProbeNodesAndNetworkMonitorTasksForTest(map[int]string{1: "probe"}, map[string]string{"a": "外网", "b": "待采样", "c": "停用", "d": "其他节点"})
	t.Cleanup(restore)
	t.Cleanup(SetProbeNetworkMonitorResultStorePathForTest(filepath.Join(t.TempDir(), "results")))
	t.Cleanup(setProbeTrafficStoreDirForTest(t.TempDir()))
	ResetProbeRuntimeStoreForTest()
	t.Cleanup(ResetProbeRuntimeStoreForTest)
	for i := range ProbeStore.data.NetworkMonitorTasks {
		task := &ProbeStore.data.NetworkMonitorTasks[i]
		if task.ID == "c" {
			task.Enabled = false
		}
		if task.ID == "d" {
			task.NodeIDs = []string{"2"}
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := AppendProbeNetworkMonitorResultForTest("1", "a", now.Add(-time.Minute).Format(time.RFC3339), 12, 0); err != nil {
		t.Fatal(err)
	}
	// A newer total-loss result must replace the previous healthy latency.
	_, err := appendProbeNetworkMonitorResult(probeNetworkMonitorResultRecord{
		ID: "legacy-result", NodeID: "1", TaskID: "a", Timestamp: now.Format(time.RFC3339),
		Error: "private-error-token", Results: []probeNetworkMonitorTargetResult{{
			Target: "private-target-token", Sent: 5, Received: 0, LossPercent: 100,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Late delivery of an older sample must not replace the latest snapshot.
	if err := AppendProbeNetworkMonitorResultForTest("1", "a", now.Add(-2*time.Minute).Format(time.RFC3339), 99, 0); err != nil {
		t.Fatal(err)
	}
	UpdateProbeRuntimeReportForTest("1", 10)
	rr := httptest.NewRecorder()
	dashboardProbesHandler(rr, httptest.NewRequest(http.MethodGet, "/dashboard/probes", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	for _, secret := range []string{"private-target-token", "private-error-token", "node_id", "targets"} {
		if strings.Contains(rr.Body.String(), secret) {
			t.Fatalf("public response leaked %q", secret)
		}
	}
	var response struct{ Items []dashboardPublicProbeItem }
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || len(response.Items[0].Network) != 2 {
		t.Fatalf("unexpected public items: %+v", response.Items)
	}
	byID := map[string]dashboardPublicProbeNetwork{}
	for _, item := range response.Items[0].Network {
		byID[item.TaskID] = item
	}
	a, b := byID["a"], byID["b"]
	if !a.Available || a.Stale || a.LossPercent != 100 || a.LatencyAvgMS != -1 || a.Timestamp != now.Format(time.RFC3339) {
		t.Fatalf("latest total-loss result=%+v", a)
	}
	if b.Available || b.Timestamp != "" || b.LatencyAvgMS != -1 || b.LossPercent != -1 {
		t.Fatalf("missing measurements must not become zero: %+v", b)
	}
}

func TestDashboardProbeNetworkFreshnessAndLegacyStore(t *testing.T) {
	t.Cleanup(SetProbeNetworkMonitorResultStorePathForTest(filepath.Join(t.TempDir(), "results")))
	now := time.Now().UTC().Truncate(time.Second)
	tasks := []probeNetworkMonitorTaskRecord{{ID: "a", Name: "A", NodeIDs: []string{"1"}, CycleSec: 60, Count: 1, TimeoutMS: 1000, Enabled: true}}
	// Exercise legacy raw-only stores without history.Latest or chart rollups.
	err := saveProbeNetworkMonitorResultsForNodeToDiskLocked("1", []probeNetworkMonitorResultRecord{{
		ID: "legacy-raw", NodeID: "1", TaskID: "a", Timestamp: now.Format(time.RFC3339),
		Results: []probeNetworkMonitorTargetResult{{Sent: 5, Received: 4, LatencyAvgMS: 23.5, LossPercent: 20}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		online bool
		now    time.Time
		stale  bool
	}{
		{"fresh", true, now, false},
		{"overdue", true, now.Add(3 * time.Minute), true},
		{"offline", false, now, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := dashboardProbeNetwork("1", tasks, tc.online, tc.now)
			if len(items) != 1 || !items[0].Available || items[0].Stale != tc.stale || items[0].LatencyAvgMS != 23.5 || items[0].LossPercent != 20 {
				t.Fatalf("unexpected summary: %+v", items)
			}
		})
	}
	if got := dashboardProbeNetwork("2", tasks, true, now); len(got) != 0 {
		t.Fatalf("other node must not inherit tasks: %+v", got)
	}
}
