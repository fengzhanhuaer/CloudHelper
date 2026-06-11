package core

import (
	"path/filepath"
	"testing"
)

func TestUpsertProbeNetworkMonitorTaskSupportsNameAndEdit(t *testing.T) {
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{data: probeConfigData{}}
	t.Cleanup(func() { ProbeStore = oldStore })

	created, err := upsertProbeNetworkMonitorTaskLocked(mngProbeNetworkMonitorTaskUpsertRequest{
		Name:      "  外网 连通性  ",
		NodeIDs:   []string{"1"},
		Targets:   []string{"1.1.1.1"},
		Count:     2,
		TimeoutMS: 800,
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create task failed: %v", err)
	}
	if created.Name != "外网 连通性" {
		t.Fatalf("task name=%q", created.Name)
	}
	if defaultProbeNetworkMonitorCycleSec != 600 {
		t.Fatalf("default cycle const=%d want 600", defaultProbeNetworkMonitorCycleSec)
	}
	if created.CycleSec != 600 {
		t.Fatalf("default cycle=%d want 600", created.CycleSec)
	}

	updated, err := upsertProbeNetworkMonitorTaskLocked(mngProbeNetworkMonitorTaskUpsertRequest{
		ID:        created.ID,
		Name:      "内网测试",
		NodeIDs:   []string{"2"},
		Targets:   []string{"192.168.1.1"},
		Count:     3,
		TimeoutMS: 1200,
		CycleSec:  30,
		Enabled:   false,
	})
	if err != nil {
		t.Fatalf("update task failed: %v", err)
	}
	if updated.ID != created.ID {
		t.Fatalf("updated id=%q want %q", updated.ID, created.ID)
	}
	if updated.Name != "内网测试" {
		t.Fatalf("updated name=%q", updated.Name)
	}
	if updated.CycleSec != 60 {
		t.Fatalf("updated cycle=%d want 60", updated.CycleSec)
	}
	if len(ProbeStore.data.NetworkMonitorTasks) != 1 {
		t.Fatalf("task count=%d want 1", len(ProbeStore.data.NetworkMonitorTasks))
	}
	if got := ProbeStore.data.NetworkMonitorTasks[0]; got.Name != "内网测试" || got.NodeIDs[0] != "2" || got.Targets[0] != "192.168.1.1" {
		t.Fatalf("stored task=%+v", got)
	}
}

func TestProbeNetworkMonitorResultsPersistToPerNodeGobFiles(t *testing.T) {
	restorePath := SetProbeNetworkMonitorResultStorePathForTest(filepath.Join(t.TempDir(), "network_monitor_results"))
	t.Cleanup(restorePath)

	_, err := appendProbeNetworkMonitorResult(probeNetworkMonitorResultRecord{
		TaskID:    "task-1",
		NodeID:    "1",
		NodeNo:    1,
		NodeName:  "probe-a",
		OK:        true,
		Timestamp: "2026-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("append first result failed: %v", err)
	}
	_, err = appendProbeNetworkMonitorResult(probeNetworkMonitorResultRecord{
		TaskID:    "task-2",
		NodeID:    "2",
		NodeNo:    2,
		NodeName:  "probe-b",
		OK:        false,
		Error:     "timeout",
		Timestamp: "2026-01-01T00:01:00Z",
	})
	if err != nil {
		t.Fatalf("append second result failed: %v", err)
	}

	results := loadProbeNetworkMonitorResultsLocked(1)
	if len(results) != 1 {
		t.Fatalf("result count=%d want 1", len(results))
	}
	if results[0].TaskID != "task-2" || results[0].Error != "timeout" {
		t.Fatalf("latest result=%+v", results[0])
	}
	nodeOneResults := loadProbeNetworkMonitorResultsForNode("1")
	if len(nodeOneResults) != 1 || nodeOneResults[0].TaskID != "task-1" {
		t.Fatalf("node one results=%+v", nodeOneResults)
	}
	nodeTwoResults := loadProbeNetworkMonitorResultsForNode("2")
	if len(nodeTwoResults) != 1 || nodeTwoResults[0].TaskID != "task-2" {
		t.Fatalf("node two results=%+v", nodeTwoResults)
	}
}

func TestProbeNetworkMonitorLatestResultsByNodeTask(t *testing.T) {
	restorePath := SetProbeNetworkMonitorResultStorePathForTest(filepath.Join(t.TempDir(), "network_monitor_results"))
	t.Cleanup(restorePath)

	records := []probeNetworkMonitorResultRecord{
		{TaskID: "task-a", NodeID: "1", NodeNo: 1, NodeName: "probe-a", Timestamp: "2026-01-01T00:00:00Z", Error: "old"},
		{TaskID: "task-a", NodeID: "1", NodeNo: 1, NodeName: "probe-a", Timestamp: "2026-01-01T00:02:00Z"},
		{TaskID: "task-b", NodeID: "1", NodeNo: 1, NodeName: "probe-a", Timestamp: "2026-01-01T00:01:00Z"},
		{TaskID: "task-a", NodeID: "2", NodeNo: 2, NodeName: "probe-b", Timestamp: "2026-01-01T00:03:00Z"},
	}
	for _, record := range records {
		if _, err := appendProbeNetworkMonitorResult(record); err != nil {
			t.Fatalf("append result failed: %v", err)
		}
	}

	results := loadProbeNetworkMonitorLatestResultsByNodeTask()
	if len(results) != 3 {
		t.Fatalf("latest result count=%d want 3: %+v", len(results), results)
	}
	seen := map[string]probeNetworkMonitorResultRecord{}
	for _, result := range results {
		seen[result.NodeID+"|"+result.TaskID] = result
	}
	if got := seen["1|task-a"]; got.Timestamp != "2026-01-01T00:02:00Z" || got.Error != "" {
		t.Fatalf("node 1 task-a latest=%+v", got)
	}
	if got := seen["1|task-b"]; got.Timestamp != "2026-01-01T00:01:00Z" {
		t.Fatalf("node 1 task-b latest=%+v", got)
	}
	if got := seen["2|task-a"]; got.Timestamp != "2026-01-01T00:03:00Z" {
		t.Fatalf("node 2 task-a latest=%+v", got)
	}
}
