package core

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxProbeNetworkMonitorTasks        = 200
	maxProbeNetworkMonitorResults      = 2000
	defaultProbeNetworkMonitorCycleSec = 60
)

type probeNetworkMonitorTaskRecord struct {
	ID        string   `json:"id"`
	NodeIDs   []string `json:"node_ids"`
	Targets   []string `json:"targets"`
	Count     int      `json:"count"`
	TimeoutMS int      `json:"timeout_ms"`
	CycleSec  int      `json:"cycle_sec"`
	Enabled   bool     `json:"enabled"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

type probeNetworkMonitorResultRecord struct {
	ID         string                            `json:"id"`
	TaskID     string                            `json:"task_id"`
	NodeID     string                            `json:"node_id"`
	NodeNo     int                               `json:"node_no"`
	NodeName   string                            `json:"node_name,omitempty"`
	OK         bool                              `json:"ok"`
	Count      int                               `json:"count,omitempty"`
	TimeoutMS  int                               `json:"timeout_ms,omitempty"`
	CycleSec   int                               `json:"cycle_sec,omitempty"`
	Results    []probeNetworkMonitorTargetResult `json:"results,omitempty"`
	Error      string                            `json:"error,omitempty"`
	StartedAt  string                            `json:"started_at,omitempty"`
	FinishedAt string                            `json:"finished_at,omitempty"`
	Timestamp  string                            `json:"timestamp"`
}

func upsertProbeNetworkMonitorTaskLocked(req mngProbeNetworkMonitorTaskUpsertRequest) (probeNetworkMonitorTaskRecord, error) {
	targets, err := normalizeProbeNetworkMonitorTargets(req.Targets)
	if err != nil {
		return probeNetworkMonitorTaskRecord{}, err
	}
	nodeIDs := normalizeMngProbeNetworkMonitorNodeIDs(req.NodeIDs)
	if len(nodeIDs) == 0 {
		return probeNetworkMonitorTaskRecord{}, fmt.Errorf("at least one probe node is required")
	}
	if len(nodeIDs) > 50 {
		return probeNetworkMonitorTaskRecord{}, fmt.Errorf("probe node count must be <= 50")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	taskID := strings.TrimSpace(req.ID)
	if taskID == "" {
		taskID = newProbeNetworkMonitorTaskID()
	}

	task := probeNetworkMonitorTaskRecord{
		ID:        taskID,
		NodeIDs:   nodeIDs,
		Targets:   targets,
		Count:     normalizeProbeNetworkMonitorCount(req.Count),
		TimeoutMS: normalizeProbeNetworkMonitorTimeoutMS(req.TimeoutMS),
		CycleSec:  normalizeProbeNetworkMonitorCycleSec(req.CycleSec),
		Enabled:   req.Enabled,
		CreatedAt: now,
		UpdatedAt: now,
	}

	items := normalizeProbeNetworkMonitorTasks(ProbeStore.data.NetworkMonitorTasks)
	replaced := false
	for i := range items {
		if strings.TrimSpace(items[i].ID) != taskID {
			continue
		}
		task.CreatedAt = firstNonEmptyNetworkMonitor(strings.TrimSpace(items[i].CreatedAt), now)
		items[i] = task
		replaced = true
		break
	}
	if !replaced {
		if len(items) >= maxProbeNetworkMonitorTasks {
			return probeNetworkMonitorTaskRecord{}, fmt.Errorf("network monitor task count exceeded limit (%d)", maxProbeNetworkMonitorTasks)
		}
		items = append(items, task)
	}
	ProbeStore.data.NetworkMonitorTasks = normalizeProbeNetworkMonitorTasks(items)
	return task, nil
}

func setProbeNetworkMonitorTaskEnabledLocked(taskID string, enabled bool) (probeNetworkMonitorTaskRecord, error) {
	cleanID := strings.TrimSpace(taskID)
	if cleanID == "" {
		return probeNetworkMonitorTaskRecord{}, fmt.Errorf("task id is required")
	}
	items := normalizeProbeNetworkMonitorTasks(ProbeStore.data.NetworkMonitorTasks)
	for i := range items {
		if strings.TrimSpace(items[i].ID) != cleanID {
			continue
		}
		items[i].Enabled = enabled
		items[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		ProbeStore.data.NetworkMonitorTasks = items
		return items[i], nil
	}
	return probeNetworkMonitorTaskRecord{}, fmt.Errorf("network monitor task not found")
}

func deleteProbeNetworkMonitorTaskLocked(taskID string) error {
	cleanID := strings.TrimSpace(taskID)
	if cleanID == "" {
		return fmt.Errorf("task id is required")
	}
	items := normalizeProbeNetworkMonitorTasks(ProbeStore.data.NetworkMonitorTasks)
	out := make([]probeNetworkMonitorTaskRecord, 0, len(items))
	removed := false
	for _, item := range items {
		if strings.TrimSpace(item.ID) == cleanID {
			removed = true
			continue
		}
		out = append(out, item)
	}
	if !removed {
		return fmt.Errorf("network monitor task not found")
	}
	ProbeStore.data.NetworkMonitorTasks = out
	return nil
}

func appendProbeNetworkMonitorResultLocked(record probeNetworkMonitorResultRecord) probeNetworkMonitorResultRecord {
	now := time.Now().UTC().Format(time.RFC3339)
	record.ID = firstNonEmptyNetworkMonitor(strings.TrimSpace(record.ID), newProbeNetworkMonitorResultID())
	record.TaskID = strings.TrimSpace(record.TaskID)
	record.NodeID = normalizeProbeNodeID(record.NodeID)
	record.NodeName = strings.TrimSpace(record.NodeName)
	record.Error = strings.TrimSpace(record.Error)
	record.Timestamp = firstNonEmptyNetworkMonitor(strings.TrimSpace(record.Timestamp), now)
	items := normalizeProbeNetworkMonitorResults(ProbeStore.data.NetworkMonitorResults)
	items = append(items, record)
	if len(items) > maxProbeNetworkMonitorResults {
		items = items[len(items)-maxProbeNetworkMonitorResults:]
	}
	ProbeStore.data.NetworkMonitorResults = items
	return record
}

func loadProbeNetworkMonitorTasksLocked() []probeNetworkMonitorTaskRecord {
	return normalizeProbeNetworkMonitorTasks(ProbeStore.data.NetworkMonitorTasks)
}

func loadProbeNetworkMonitorResultsLocked(limit int) []probeNetworkMonitorResultRecord {
	items := normalizeProbeNetworkMonitorResults(ProbeStore.data.NetworkMonitorResults)
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	if len(items) > limit {
		items = items[len(items)-limit:]
	}
	out := append([]probeNetworkMonitorResultRecord(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.TrimSpace(out[i].Timestamp) > strings.TrimSpace(out[j].Timestamp)
	})
	return out
}

func probeNetworkMonitorTasksForNodeLocked(nodeID string) []probeNetworkMonitorTaskRecord {
	cleanID := normalizeProbeNodeID(nodeID)
	if cleanID == "" {
		return []probeNetworkMonitorTaskRecord{}
	}
	items := normalizeProbeNetworkMonitorTasks(ProbeStore.data.NetworkMonitorTasks)
	out := make([]probeNetworkMonitorTaskRecord, 0)
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		for _, id := range item.NodeIDs {
			if normalizeProbeNodeID(id) == cleanID {
				out = append(out, item)
				break
			}
		}
	}
	return out
}

func networkMonitorAllTaskNodeIDsLocked() []string {
	seen := make(map[string]bool)
	out := make([]string, 0)
	for _, task := range normalizeProbeNetworkMonitorTasks(ProbeStore.data.NetworkMonitorTasks) {
		for _, id := range task.NodeIDs {
			cleanID := normalizeProbeNodeID(id)
			if cleanID == "" || seen[cleanID] {
				continue
			}
			seen[cleanID] = true
			out = append(out, cleanID)
		}
	}
	return out
}

func dispatchProbeNetworkMonitorTaskUpdates(nodeIDs []string) {
	seen := make(map[string]bool)
	for _, nodeID := range nodeIDs {
		cleanID := normalizeProbeNodeID(nodeID)
		if cleanID == "" || seen[cleanID] {
			continue
		}
		seen[cleanID] = true
		go dispatchProbeNetworkMonitorTasksForNode(cleanID)
	}
}

func normalizeProbeNetworkMonitorTasks(raw []probeNetworkMonitorTaskRecord) []probeNetworkMonitorTaskRecord {
	out := make([]probeNetworkMonitorTaskRecord, 0, len(raw))
	seen := make(map[string]bool)
	for _, item := range raw {
		id := strings.TrimSpace(item.ID)
		if id == "" || seen[id] {
			continue
		}
		targets, err := normalizeProbeNetworkMonitorTargets(item.Targets)
		if err != nil {
			continue
		}
		nodeIDs := normalizeMngProbeNetworkMonitorNodeIDs(item.NodeIDs)
		if len(nodeIDs) == 0 {
			continue
		}
		item.ID = id
		item.NodeIDs = nodeIDs
		item.Targets = targets
		item.Count = normalizeProbeNetworkMonitorCount(item.Count)
		item.TimeoutMS = normalizeProbeNetworkMonitorTimeoutMS(item.TimeoutMS)
		item.CycleSec = normalizeProbeNetworkMonitorCycleSec(item.CycleSec)
		item.CreatedAt = strings.TrimSpace(item.CreatedAt)
		item.UpdatedAt = strings.TrimSpace(item.UpdatedAt)
		seen[id] = true
		out = append(out, item)
	}
	return out
}

func normalizeProbeNetworkMonitorResults(raw []probeNetworkMonitorResultRecord) []probeNetworkMonitorResultRecord {
	out := make([]probeNetworkMonitorResultRecord, 0, len(raw))
	for _, item := range raw {
		item.ID = strings.TrimSpace(item.ID)
		item.TaskID = strings.TrimSpace(item.TaskID)
		item.NodeID = normalizeProbeNodeID(item.NodeID)
		item.NodeName = strings.TrimSpace(item.NodeName)
		item.Error = strings.TrimSpace(item.Error)
		item.Timestamp = strings.TrimSpace(item.Timestamp)
		if item.ID == "" || item.NodeID == "" || item.Timestamp == "" {
			continue
		}
		out = append(out, item)
	}
	if len(out) > maxProbeNetworkMonitorResults {
		out = out[len(out)-maxProbeNetworkMonitorResults:]
	}
	return out
}

func normalizeProbeNetworkMonitorCycleSec(raw int) int {
	if raw <= 0 {
		return defaultProbeNetworkMonitorCycleSec
	}
	if raw < 5 {
		return 5
	}
	if raw > 86400 {
		return 86400
	}
	return raw
}

func newProbeNetworkMonitorTaskID() string {
	return fmt.Sprintf("netmon-%d", time.Now().UnixNano())
}

func newProbeNetworkMonitorResultID() string {
	return fmt.Sprintf("netmon-result-%d", time.Now().UnixNano())
}

func nodeNameByNodeIDLocked(nodeID string) (int, string) {
	cleanID := normalizeProbeNodeID(nodeID)
	for _, node := range loadProbeNodesLocked() {
		id := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
		if id == cleanID {
			return node.NodeNo, strings.TrimSpace(node.NodeName)
		}
	}
	return 0, ""
}

func normalizeProbeNetworkMonitorTargets(raw []string) ([]string, error) {
	seen := make(map[string]bool)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		target := strings.TrimSpace(item)
		if target == "" {
			continue
		}
		ip := net.ParseIP(target)
		if ip == nil {
			return nil, fmt.Errorf("invalid target ip: %s", target)
		}
		normalized := ip.String()
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one target ip is required")
	}
	if len(out) > 20 {
		return nil, fmt.Errorf("target ip count must be <= 20")
	}
	return out, nil
}

func normalizeProbeNetworkMonitorCount(raw int) int {
	if raw <= 0 {
		return 4
	}
	if raw > 10 {
		return 10
	}
	return raw
}

func normalizeProbeNetworkMonitorTimeoutMS(raw int) int {
	if raw <= 0 {
		return 1000
	}
	if raw < 300 {
		return 300
	}
	if raw > 5000 {
		return 5000
	}
	return raw
}

func firstNonEmptyNetworkMonitor(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
