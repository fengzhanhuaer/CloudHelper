package core

import (
	"encoding/gob"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxProbeNetworkMonitorTasks        = 200
	maxProbeNetworkMonitorResults      = 50000
	defaultProbeNetworkMonitorCycleSec = 600
)

var probeNetworkMonitorResultStore = struct {
	mu  sync.Mutex
	dir string
}{
	dir: filepath.Join(".", "temp", "network_monitor_results"),
}

type probeNetworkMonitorTaskRecord struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
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
	TaskName   string                            `json:"task_name,omitempty"`
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
	taskName := normalizeProbeNetworkMonitorTaskName(req.Name)

	task := probeNetworkMonitorTaskRecord{
		ID:        taskID,
		Name:      taskName,
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

func appendProbeNetworkMonitorResult(record probeNetworkMonitorResultRecord) (probeNetworkMonitorResultRecord, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	record.ID = firstNonEmptyNetworkMonitor(strings.TrimSpace(record.ID), newProbeNetworkMonitorResultID())
	record.TaskID = strings.TrimSpace(record.TaskID)
	record.TaskName = normalizeOptionalProbeNetworkMonitorTaskName(record.TaskName)
	record.NodeID = normalizeProbeNodeID(record.NodeID)
	record.NodeName = strings.TrimSpace(record.NodeName)
	record.Error = strings.TrimSpace(record.Error)
	record.Timestamp = firstNonEmptyNetworkMonitor(strings.TrimSpace(record.Timestamp), now)
	if record.NodeID == "" {
		return record, fmt.Errorf("node id is required")
	}

	probeNetworkMonitorResultStore.mu.Lock()
	defer probeNetworkMonitorResultStore.mu.Unlock()

	items, err := loadProbeNetworkMonitorResultsForNodeFromDiskLocked(record.NodeID)
	if err != nil {
		return record, err
	}
	items = append(items, record)
	if len(items) > maxProbeNetworkMonitorResults {
		items = items[len(items)-maxProbeNetworkMonitorResults:]
	}
	if err := saveProbeNetworkMonitorResultsForNodeToDiskLocked(record.NodeID, items); err != nil {
		return record, err
	}
	return record, nil
}

func loadProbeNetworkMonitorTasksLocked() []probeNetworkMonitorTaskRecord {
	return normalizeProbeNetworkMonitorTasks(ProbeStore.data.NetworkMonitorTasks)
}

func loadProbeNetworkMonitorResultsLocked(limit int) []probeNetworkMonitorResultRecord {
	probeNetworkMonitorResultStore.mu.Lock()
	items, err := loadProbeNetworkMonitorResultsAllFromDiskLocked()
	probeNetworkMonitorResultStore.mu.Unlock()
	if err != nil {
		logControllerWarnf("failed to load network monitor results: %v", err)
		items = []probeNetworkMonitorResultRecord{}
	}
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

func loadProbeNetworkMonitorResultsAll() []probeNetworkMonitorResultRecord {
	probeNetworkMonitorResultStore.mu.Lock()
	items, err := loadProbeNetworkMonitorResultsAllFromDiskLocked()
	probeNetworkMonitorResultStore.mu.Unlock()
	if err != nil {
		logControllerWarnf("failed to load network monitor results: %v", err)
		return []probeNetworkMonitorResultRecord{}
	}
	return items
}

func loadProbeNetworkMonitorResultsForNode(nodeID string) []probeNetworkMonitorResultRecord {
	cleanID := normalizeProbeNodeID(nodeID)
	if cleanID == "" {
		return []probeNetworkMonitorResultRecord{}
	}
	probeNetworkMonitorResultStore.mu.Lock()
	items, err := loadProbeNetworkMonitorResultsForNodeFromDiskLocked(cleanID)
	probeNetworkMonitorResultStore.mu.Unlock()
	if err != nil {
		logControllerWarnf("failed to load network monitor results: node=%s err=%v", cleanID, err)
		return []probeNetworkMonitorResultRecord{}
	}
	return items
}

func listProbeNetworkMonitorResultNodeIDs() []string {
	probeNetworkMonitorResultStore.mu.Lock()
	nodeIDs, err := listProbeNetworkMonitorResultNodeIDsLocked()
	probeNetworkMonitorResultStore.mu.Unlock()
	if err != nil {
		logControllerWarnf("failed to list network monitor result nodes: %v", err)
		return []string{}
	}
	return nodeIDs
}

func loadProbeNetworkMonitorResultsAllFromDiskLocked() ([]probeNetworkMonitorResultRecord, error) {
	nodeIDs, err := listProbeNetworkMonitorResultNodeIDsLocked()
	if err != nil {
		return nil, err
	}
	out := make([]probeNetworkMonitorResultRecord, 0)
	for _, nodeID := range nodeIDs {
		items, err := loadProbeNetworkMonitorResultsForNodeFromDiskLocked(nodeID)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return normalizeProbeNetworkMonitorResults(out), nil
}

func listProbeNetworkMonitorResultNodeIDsLocked() ([]string, error) {
	entries, err := os.ReadDir(probeNetworkMonitorResultStore.dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	out := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "node_") || !strings.HasSuffix(name, ".gob") {
			continue
		}
		nodeID := normalizeProbeNodeID(strings.TrimSuffix(strings.TrimPrefix(name, "node_"), ".gob"))
		if nodeID == "" || seen[nodeID] {
			continue
		}
		seen[nodeID] = true
		out = append(out, nodeID)
	}
	sort.Strings(out)
	return out, nil
}

func loadProbeNetworkMonitorResultsForNodeFromDiskLocked(nodeID string) ([]probeNetworkMonitorResultRecord, error) {
	path, err := probeNetworkMonitorResultPathForNode(nodeID)
	if err != nil {
		return nil, err
	}
	content, err := os.Open(path)
	if os.IsNotExist(err) {
		return []probeNetworkMonitorResultRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer content.Close()

	var items []probeNetworkMonitorResultRecord
	if err := gob.NewDecoder(content).Decode(&items); err != nil {
		return nil, err
	}
	return normalizeProbeNetworkMonitorResultsForNode(items, nodeID), nil
}

func saveProbeNetworkMonitorResultsForNodeToDiskLocked(nodeID string, items []probeNetworkMonitorResultRecord) error {
	items = normalizeProbeNetworkMonitorResultsForNode(items, nodeID)
	if len(items) > maxProbeNetworkMonitorResults {
		items = items[len(items)-maxProbeNetworkMonitorResults:]
	}
	if err := os.MkdirAll(probeNetworkMonitorResultStore.dir, 0o755); err != nil {
		return err
	}
	path, err := probeNetworkMonitorResultPathForNode(nodeID)
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	encodeErr := gob.NewEncoder(file).Encode(items)
	closeErr := file.Close()
	if encodeErr != nil {
		_ = os.Remove(tmpPath)
		return encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}
	return os.Rename(tmpPath, path)
}

func probeNetworkMonitorResultPathForNode(nodeID string) (string, error) {
	cleanID := normalizeProbeNodeID(nodeID)
	if cleanID == "" {
		return "", fmt.Errorf("node id is required")
	}
	return filepath.Join(probeNetworkMonitorResultStore.dir, "node_"+cleanID+".gob"), nil
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
		item.Name = normalizeProbeNetworkMonitorTaskName(item.Name)
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
		item.TaskName = normalizeOptionalProbeNetworkMonitorTaskName(item.TaskName)
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

func normalizeProbeNetworkMonitorResultsForNode(raw []probeNetworkMonitorResultRecord, nodeID string) []probeNetworkMonitorResultRecord {
	cleanID := normalizeProbeNodeID(nodeID)
	if cleanID == "" {
		return []probeNetworkMonitorResultRecord{}
	}
	items := normalizeProbeNetworkMonitorResults(raw)
	out := make([]probeNetworkMonitorResultRecord, 0, len(items))
	for _, item := range items {
		if normalizeProbeNodeID(item.NodeID) != cleanID {
			continue
		}
		item.NodeID = cleanID
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
	if raw < 60 {
		return 60
	}
	if raw > 86400 {
		return 86400
	}
	return raw
}

func newProbeNetworkMonitorTaskID() string {
	return fmt.Sprintf("netmon-%d", time.Now().UnixNano())
}

func normalizeProbeNetworkMonitorTaskName(raw string) string {
	name := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if name == "" {
		return "网络测试任务"
	}
	runes := []rune(name)
	if len(runes) > 80 {
		return string(runes[:80])
	}
	return name
}

func normalizeOptionalProbeNetworkMonitorTaskName(raw string) string {
	name := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if name == "" {
		return ""
	}
	runes := []rune(name)
	if len(runes) > 80 {
		return string(runes[:80])
	}
	return name
}

func probeNetworkMonitorTaskNameByIDLocked(taskID string) string {
	cleanID := strings.TrimSpace(taskID)
	if cleanID == "" {
		return ""
	}
	for _, task := range normalizeProbeNetworkMonitorTasks(ProbeStore.data.NetworkMonitorTasks) {
		if strings.TrimSpace(task.ID) == cleanID {
			return normalizeProbeNetworkMonitorTaskName(task.Name)
		}
	}
	return ""
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

func SetProbeNetworkMonitorResultStorePathForTest(path string) func() {
	oldDir := probeNetworkMonitorResultStore.dir
	probeNetworkMonitorResultStore.dir = path
	return func() { probeNetworkMonitorResultStore.dir = oldDir }
}

func AppendProbeNetworkMonitorResultForTest(nodeID string, timestamp string, latencyAvgMS float64, lossPercent float64) error {
	_, err := appendProbeNetworkMonitorResult(probeNetworkMonitorResultRecord{
		NodeID:    nodeID,
		NodeNo:    0,
		OK:        true,
		Timestamp: timestamp,
		Results: []probeNetworkMonitorTargetResult{
			{
				Target:       "0.0.0.0",
				IPFamily:     "test",
				Sent:         1,
				Received:     1,
				LossPercent:  lossPercent,
				LatencyAvgMS: latencyAvgMS,
			},
		},
	})
	return err
}
