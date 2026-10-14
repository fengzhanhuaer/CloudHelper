package core

import (
	"encoding/gob"
	"fmt"
	"math"
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
	probeNetworkMonitorHistoryVersion  = 1
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

type probeNetworkMonitorHistory struct {
	Version          int                                `json:"version"`
	CompactedThrough string                             `json:"compacted_through,omitempty"`
	Hourly           []probeNetworkMonitorHistoryBucket `json:"hourly,omitempty"`
	Daily            []probeNetworkMonitorHistoryBucket `json:"daily,omitempty"`
	Latest           []probeNetworkMonitorResultRecord  `json:"latest,omitempty"`
}

type probeNetworkMonitorHistoryBucket struct {
	TaskID       string  `json:"task_id"`
	Timestamp    string  `json:"timestamp"`
	LatencySum   float64 `json:"latency_sum"`
	LatencyCount int64   `json:"latency_count"`
	LossSum      float64 `json:"loss_sum"`
	LossCount    int64   `json:"loss_count"`
	OKCount      int64   `json:"ok_count"`
	ResultCount  int64   `json:"result_count"`
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
	history, err := loadProbeNetworkMonitorHistoryForNodeLocked(record.NodeID)
	if err != nil {
		return record, err
	}
	history.Latest = mergeProbeNetworkMonitorLatest(history.Latest, items, &record)
	compactProbeNetworkMonitorHistory(&history, items, time.Now().UTC())
	// Commit rollups before pruning raw; the watermark makes retries idempotent.
	if err := saveProbeNetworkMonitorHistoryForNodeLocked(record.NodeID, history); err != nil {
		return record, err
	}
	if err := saveProbeNetworkMonitorResultsForNodeToDiskLocked(record.NodeID, retainProbeNetworkMonitorRaw(items, time.Now().UTC())); err != nil {
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

func loadProbeNetworkMonitorLatestResultsByNodeTask() []probeNetworkMonitorResultRecord {
	probeNetworkMonitorResultStore.mu.Lock()
	nodeIDs, err := listProbeNetworkMonitorResultNodeIDsLocked()
	if err != nil {
		probeNetworkMonitorResultStore.mu.Unlock()
		logControllerWarnf("failed to list network monitor result nodes: %v", err)
		return []probeNetworkMonitorResultRecord{}
	}
	latest := map[string]probeNetworkMonitorResultRecord{}
	for _, nodeID := range nodeIDs {
		items, loadErr := loadProbeNetworkMonitorResultsForNodeFromDiskLocked(nodeID)
		if loadErr != nil {
			probeNetworkMonitorResultStore.mu.Unlock()
			logControllerWarnf("failed to load network monitor results: node=%s err=%v", nodeID, loadErr)
			return []probeNetworkMonitorResultRecord{}
		}
		history, historyErr := loadProbeNetworkMonitorHistoryForNodeLocked(nodeID)
		if historyErr != nil {
			probeNetworkMonitorResultStore.mu.Unlock()
			logControllerWarnf("failed to load network monitor history: node=%s err=%v", nodeID, historyErr)
			return []probeNetworkMonitorResultRecord{}
		}
		items = append(items, history.Latest...)
		for _, item := range items {
			taskID := strings.TrimSpace(item.TaskID)
			if taskID == "" {
				taskID = strings.TrimSpace(item.ID)
			}
			key := normalizeProbeNodeID(item.NodeID) + "|" + taskID
			if strings.TrimSpace(key) == "|" {
				continue
			}
			current, ok := latest[key]
			if !ok || probeNetworkMonitorResultTimestamp(item).After(probeNetworkMonitorResultTimestamp(current)) {
				latest[key] = item
			}
		}
	}
	probeNetworkMonitorResultStore.mu.Unlock()

	out := make([]probeNetworkMonitorResultRecord, 0, len(latest))
	for _, item := range latest {
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := probeNetworkMonitorResultTimestamp(out[i])
		right := probeNetworkMonitorResultTimestamp(out[j])
		if !left.Equal(right) {
			return left.After(right)
		}
		if out[i].NodeNo != out[j].NodeNo {
			return out[i].NodeNo < out[j].NodeNo
		}
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		return out[i].TaskID < out[j].TaskID
	})
	return out
}

func probeNetworkMonitorResultTimestamp(item probeNetworkMonitorResultRecord) time.Time {
	for _, raw := range []string{item.Timestamp, item.FinishedAt, item.StartedAt} {
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed
		}
		if parsed, err := time.Parse(time.RFC3339, text); err == nil {
			return parsed
		}
	}
	return time.Time{}
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

func loadProbeNetworkMonitorChartHistoryForNode(nodeID string) ([]probeNetworkMonitorResultRecord, probeNetworkMonitorHistory) {
	cleanID := normalizeProbeNodeID(nodeID)
	if cleanID == "" {
		return []probeNetworkMonitorResultRecord{}, probeNetworkMonitorHistory{}
	}
	probeNetworkMonitorResultStore.mu.Lock()
	items, resultErr := loadProbeNetworkMonitorResultsForNodeFromDiskLocked(cleanID)
	history, historyErr := loadProbeNetworkMonitorHistoryForNodeLocked(cleanID)
	if resultErr == nil && historyErr == nil && probeNetworkMonitorHistoryNeedsCompaction(history, items, time.Now().UTC()) {
		originalHistory := history
		originalHistory.Hourly = append([]probeNetworkMonitorHistoryBucket(nil), history.Hourly...)
		originalHistory.Daily = append([]probeNetworkMonitorHistoryBucket(nil), history.Daily...)
		originalHistory.Latest = append([]probeNetworkMonitorResultRecord(nil), history.Latest...)
		history.Latest = mergeProbeNetworkMonitorLatest(history.Latest, items, nil)
		now := time.Now().UTC()
		compactProbeNetworkMonitorHistory(&history, items, now)
		if saveErr := saveProbeNetworkMonitorHistoryForNodeLocked(cleanID, history); saveErr != nil {
			logControllerWarnf("failed to compact network monitor history: node=%s err=%v", cleanID, saveErr)
			history = originalHistory
		} else if saveErr := saveProbeNetworkMonitorResultsForNodeToDiskLocked(cleanID, retainProbeNetworkMonitorRaw(items, now)); saveErr != nil {
			logControllerWarnf("failed to prune compacted network monitor results: node=%s err=%v", cleanID, saveErr)
		}
	}
	probeNetworkMonitorResultStore.mu.Unlock()
	if resultErr != nil || historyErr != nil {
		logControllerWarnf("failed to load network monitor chart history: node=%s results=%v history=%v", cleanID, resultErr, historyErr)
		return []probeNetworkMonitorResultRecord{}, probeNetworkMonitorHistory{}
	}
	return items, history
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

func probeNetworkMonitorHistoryPathForNode(nodeID string) (string, error) {
	cleanID := normalizeProbeNodeID(nodeID)
	if cleanID == "" {
		return "", fmt.Errorf("node id is required")
	}
	return filepath.Join(probeNetworkMonitorResultStore.dir, "history", "node_"+cleanID+".gob"), nil
}

func loadProbeNetworkMonitorHistoryForNode(nodeID string) (probeNetworkMonitorHistory, error) {
	cleanID := normalizeProbeNodeID(nodeID)
	if cleanID == "" {
		return probeNetworkMonitorHistory{}, fmt.Errorf("node id is required")
	}
	probeNetworkMonitorResultStore.mu.Lock()
	defer probeNetworkMonitorResultStore.mu.Unlock()
	return loadProbeNetworkMonitorHistoryForNodeLocked(cleanID)
}

func loadProbeNetworkMonitorHistoryForNodeLocked(nodeID string) (probeNetworkMonitorHistory, error) {
	path, err := probeNetworkMonitorHistoryPathForNode(nodeID)
	if err != nil {
		return probeNetworkMonitorHistory{}, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return probeNetworkMonitorHistory{Version: probeNetworkMonitorHistoryVersion}, nil
	}
	if err != nil {
		return probeNetworkMonitorHistory{}, err
	}
	defer file.Close()
	var history probeNetworkMonitorHistory
	if err := gob.NewDecoder(file).Decode(&history); err != nil {
		return probeNetworkMonitorHistory{}, err
	}
	if history.Version != probeNetworkMonitorHistoryVersion {
		return probeNetworkMonitorHistory{}, fmt.Errorf("unsupported network monitor history version: %d", history.Version)
	}
	history.Hourly = normalizeProbeNetworkMonitorHistoryBuckets(history.Hourly)
	history.Daily = normalizeProbeNetworkMonitorHistoryBuckets(history.Daily)
	history.Latest = normalizeProbeNetworkMonitorResultsForNode(history.Latest, nodeID)
	return history, nil
}

func saveProbeNetworkMonitorHistoryForNodeLocked(nodeID string, history probeNetworkMonitorHistory) error {
	path, err := probeNetworkMonitorHistoryPathForNode(nodeID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	history.Version = probeNetworkMonitorHistoryVersion
	history.Hourly = normalizeProbeNetworkMonitorHistoryBuckets(history.Hourly)
	history.Daily = normalizeProbeNetworkMonitorHistoryBuckets(history.Daily)
	history.Latest = normalizeProbeNetworkMonitorResultsForNode(history.Latest, nodeID)
	tmpPath := path + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	encodeErr := gob.NewEncoder(file).Encode(history)
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

func normalizeProbeNetworkMonitorHistoryBuckets(raw []probeNetworkMonitorHistoryBucket) []probeNetworkMonitorHistoryBucket {
	byKey := make(map[string]probeNetworkMonitorHistoryBucket, len(raw))
	for _, bucket := range raw {
		bucket.TaskID = strings.TrimSpace(bucket.TaskID)
		bucket.Timestamp = strings.TrimSpace(bucket.Timestamp)
		if bucket.Timestamp == "" || bucket.ResultCount <= 0 {
			continue
		}
		key := bucket.TaskID + "|" + bucket.Timestamp
		byKey[key] = bucket
	}
	out := make([]probeNetworkMonitorHistoryBucket, 0, len(byKey))
	for _, bucket := range byKey {
		out = append(out, bucket)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Timestamp != out[j].Timestamp {
			return out[i].Timestamp < out[j].Timestamp
		}
		return out[i].TaskID < out[j].TaskID
	})
	return out
}

func upsertProbeNetworkMonitorLatest(items []probeNetworkMonitorResultRecord, record probeNetworkMonitorResultRecord) []probeNetworkMonitorResultRecord {
	key := probeNetworkMonitorResultKey(record)
	out := make([]probeNetworkMonitorResultRecord, 0, len(items)+1)
	replaced := false
	for _, item := range items {
		if probeNetworkMonitorResultKey(item) != key {
			out = append(out, item)
			continue
		}
		replaced = true
		if probeNetworkMonitorResultTimestamp(item).After(probeNetworkMonitorResultTimestamp(record)) {
			out = append(out, item)
		} else {
			out = append(out, record)
		}
	}
	if !replaced {
		out = append(out, record)
	}
	return out
}

func probeNetworkMonitorResultKey(item probeNetworkMonitorResultRecord) string {
	taskID := strings.TrimSpace(item.TaskID)
	if taskID == "" {
		taskID = strings.TrimSpace(item.ID)
	}
	return normalizeProbeNodeID(item.NodeID) + "|" + taskID
}

func compactProbeNetworkMonitorHistory(history *probeNetworkMonitorHistory, raw []probeNetworkMonitorResultRecord, now time.Time) {
	minuteCutoff := now.Add(-24 * time.Hour).Truncate(time.Minute)
	hourCutoff := now.Add(-30 * 24 * time.Hour).Truncate(time.Hour)
	dayCutoff := now.Add(-180 * 24 * time.Hour).Truncate(24 * time.Hour)
	previousWatermark, hasWatermark := time.Time{}, false
	if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(history.CompactedThrough)); err == nil {
		previousWatermark, hasWatermark = parsed.UTC(), true
	}

	hourly := make(map[string]probeNetworkMonitorHistoryBucket, len(history.Hourly))
	for _, bucket := range history.Hourly {
		hourly[bucket.TaskID+"|"+bucket.Timestamp] = bucket
	}
	daily := make(map[string]probeNetworkMonitorHistoryBucket, len(history.Daily))
	for _, bucket := range history.Daily {
		daily[bucket.TaskID+"|"+bucket.Timestamp] = bucket
	}
	for _, record := range raw {
		ts := probeNetworkMonitorHistoryTimestamp(record)
		if ts.IsZero() || !ts.Before(minuteCutoff) || (hasWatermark && ts.Before(previousWatermark)) {
			continue
		}
		bucketTime := ts.Truncate(time.Hour)
		bucketMap := hourly
		if ts.Before(hourCutoff) {
			bucketTime = time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
			bucketMap = daily
		}
		addProbeNetworkMonitorRecordToBucket(bucketMap, record, bucketTime)
	}

	for key, bucket := range hourly {
		bucketTime, err := time.Parse(time.RFC3339, bucket.Timestamp)
		if err != nil || !bucketTime.Before(hourCutoff) {
			continue
		}
		bucketTime = time.Date(bucketTime.Year(), bucketTime.Month(), bucketTime.Day(), 0, 0, 0, 0, time.UTC)
		addProbeNetworkMonitorBucket(daily, bucket, bucketTime)
		delete(hourly, key)
	}
	for key, bucket := range daily {
		bucketTime, err := time.Parse(time.RFC3339, bucket.Timestamp)
		if err != nil || bucketTime.Before(dayCutoff) {
			delete(daily, key)
		}
	}
	history.Hourly = normalizeProbeNetworkMonitorHistoryBuckets(valuesProbeNetworkMonitorBuckets(hourly))
	history.Daily = normalizeProbeNetworkMonitorHistoryBuckets(valuesProbeNetworkMonitorBuckets(daily))
	history.CompactedThrough = minuteCutoff.UTC().Format(time.RFC3339Nano)
}

func addProbeNetworkMonitorRecordToBucket(buckets map[string]probeNetworkMonitorHistoryBucket, record probeNetworkMonitorResultRecord, bucketTime time.Time) {
	taskID := strings.TrimSpace(record.TaskID)
	if taskID == "" {
		taskID = "default"
	}
	latency, loss := summarizeDashboardNetworkResult(record.Results)
	if latency < 0 && loss < 0 {
		return
	}
	timestamp := bucketTime.UTC().Format(time.RFC3339)
	key := taskID + "|" + timestamp
	bucket := buckets[key]
	bucket.TaskID = taskID
	bucket.Timestamp = timestamp
	bucket.ResultCount++
	if record.OK {
		bucket.OKCount++
	}
	if latency >= 0 && !math.IsNaN(latency) && !math.IsInf(latency, 0) {
		bucket.LatencySum += latency
		bucket.LatencyCount++
	}
	if loss >= 0 && !math.IsNaN(loss) && !math.IsInf(loss, 0) {
		bucket.LossSum += loss
		bucket.LossCount++
	}
	buckets[key] = bucket
}

func addProbeNetworkMonitorBucket(buckets map[string]probeNetworkMonitorHistoryBucket, source probeNetworkMonitorHistoryBucket, bucketTime time.Time) {
	timestamp := bucketTime.UTC().Format(time.RFC3339)
	key := source.TaskID + "|" + timestamp
	bucket := buckets[key]
	bucket.TaskID = source.TaskID
	bucket.Timestamp = timestamp
	bucket.LatencySum += source.LatencySum
	bucket.LatencyCount += source.LatencyCount
	bucket.LossSum += source.LossSum
	bucket.LossCount += source.LossCount
	bucket.OKCount += source.OKCount
	bucket.ResultCount += source.ResultCount
	buckets[key] = bucket
}

func valuesProbeNetworkMonitorBuckets(buckets map[string]probeNetworkMonitorHistoryBucket) []probeNetworkMonitorHistoryBucket {
	items := make([]probeNetworkMonitorHistoryBucket, 0, len(buckets))
	for _, bucket := range buckets {
		items = append(items, bucket)
	}
	return items
}

func retainProbeNetworkMonitorRaw(items []probeNetworkMonitorResultRecord, now time.Time) []probeNetworkMonitorResultRecord {
	cutoff := now.Add(-24 * time.Hour).Truncate(time.Minute)
	out := make([]probeNetworkMonitorResultRecord, 0, len(items))
	for _, item := range items {
		ts := probeNetworkMonitorHistoryTimestamp(item)
		if ts.IsZero() || !ts.Before(cutoff) {
			out = append(out, item)
		}
	}
	if len(out) > maxProbeNetworkMonitorResults {
		out = out[len(out)-maxProbeNetworkMonitorResults:]
	}
	return out
}

func probeNetworkMonitorHistoryTimestamp(item probeNetworkMonitorResultRecord) time.Time {
	for _, raw := range []string{item.FinishedAt, item.Timestamp, item.StartedAt} {
		if ts := parseNetworkMonitorTimestamp(raw); !ts.IsZero() {
			return ts
		}
	}
	return time.Time{}
}

func mergeProbeNetworkMonitorLatest(existing []probeNetworkMonitorResultRecord, candidates []probeNetworkMonitorResultRecord, latest *probeNetworkMonitorResultRecord) []probeNetworkMonitorResultRecord {
	byKey := make(map[string]probeNetworkMonitorResultRecord, len(existing)+len(candidates)+1)
	for _, item := range append(append([]probeNetworkMonitorResultRecord{}, existing...), candidates...) {
		key := probeNetworkMonitorResultKey(item)
		current, ok := byKey[key]
		if !ok || probeNetworkMonitorResultTimestamp(item).After(probeNetworkMonitorResultTimestamp(current)) {
			byKey[key] = item
		}
	}
	if latest != nil {
		key := probeNetworkMonitorResultKey(*latest)
		current, ok := byKey[key]
		if !ok || !probeNetworkMonitorResultTimestamp(current).After(probeNetworkMonitorResultTimestamp(*latest)) {
			byKey[key] = *latest
		}
	}
	out := make([]probeNetworkMonitorResultRecord, 0, len(byKey))
	for _, item := range byKey {
		out = append(out, item)
	}
	return out
}

func probeNetworkMonitorHistoryNeedsCompaction(history probeNetworkMonitorHistory, raw []probeNetworkMonitorResultRecord, now time.Time) bool {
	minuteCutoff := now.Add(-24 * time.Hour).Truncate(time.Minute)
	hourCutoff := now.Add(-30 * 24 * time.Hour).Truncate(time.Hour)
	dayCutoff := now.Add(-180 * 24 * time.Hour).Truncate(24 * time.Hour)
	watermark, hasWatermark := parseNetworkMonitorTimestamp(history.CompactedThrough), false
	hasWatermark = !watermark.IsZero()
	if !hasWatermark {
		return true
	}
	for _, record := range raw {
		ts := probeNetworkMonitorHistoryTimestamp(record)
		if !ts.IsZero() && ts.Before(minuteCutoff) {
			return true
		}
	}
	for _, bucket := range history.Hourly {
		if ts := parseNetworkMonitorTimestamp(bucket.Timestamp); !ts.IsZero() && ts.Before(hourCutoff) {
			return true
		}
	}
	for _, bucket := range history.Daily {
		if ts := parseNetworkMonitorTimestamp(bucket.Timestamp); !ts.IsZero() && ts.Before(dayCutoff) {
			return true
		}
	}
	return false
}

func parseNetworkMonitorTimestamp(raw string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(raw)); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
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

func probeNetworkMonitorTaskNamesByIDLocked() map[string]string {
	out := make(map[string]string)
	for _, task := range normalizeProbeNetworkMonitorTasks(ProbeStore.data.NetworkMonitorTasks) {
		taskID := strings.TrimSpace(task.ID)
		if taskID == "" {
			continue
		}
		out[taskID] = normalizeProbeNetworkMonitorTaskName(task.Name)
	}
	return out
}

func attachProbeNetworkMonitorResultTaskNames(results []probeNetworkMonitorResultRecord, taskNames map[string]string) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(results))
	for _, item := range results {
		taskID := strings.TrimSpace(item.TaskID)
		taskName := strings.TrimSpace(taskNames[taskID])
		if taskName == "" {
			taskName = taskID
		}
		out = append(out, map[string]interface{}{
			"id":          strings.TrimSpace(item.ID),
			"task_id":     taskID,
			"task_name":   taskName,
			"node_id":     strings.TrimSpace(item.NodeID),
			"node_no":     item.NodeNo,
			"node_name":   strings.TrimSpace(item.NodeName),
			"ok":          item.OK,
			"count":       item.Count,
			"timeout_ms":  item.TimeoutMS,
			"cycle_sec":   item.CycleSec,
			"results":     item.Results,
			"error":       strings.TrimSpace(item.Error),
			"started_at":  strings.TrimSpace(item.StartedAt),
			"finished_at": strings.TrimSpace(item.FinishedAt),
			"timestamp":   strings.TrimSpace(item.Timestamp),
		})
	}
	return out
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

func SetProbeNetworkMonitorTasksForTest(taskNames map[string]string) func() {
	oldStore := ProbeStore
	tasks := make([]probeNetworkMonitorTaskRecord, 0, len(taskNames))
	for taskID, taskName := range taskNames {
		tasks = append(tasks, probeNetworkMonitorTaskRecord{
			ID:      strings.TrimSpace(taskID),
			Name:    strings.TrimSpace(taskName),
			NodeIDs: []string{"1"},
			Targets: []string{"1.1.1.1"},
			Enabled: true,
		})
	}
	ProbeStore = &probeConfigStore{data: probeConfigData{
		NetworkMonitorTasks: tasks,
	}}
	return func() { ProbeStore = oldStore }
}

func SetProbeNodesAndNetworkMonitorTasksForTest(nodes map[int]string, taskNames map[string]string) func() {
	testNodes := make([]ProbeNodeForTest, 0, len(nodes))
	for nodeNo, nodeName := range nodes {
		testNodes = append(testNodes, ProbeNodeForTest{
			NodeNo:       nodeNo,
			NodeName:     nodeName,
			TargetSystem: "linux",
		})
	}
	return SetProbeNodeRecordsAndNetworkMonitorTasksForTest(testNodes, taskNames)
}

type ProbeNodeForTest struct {
	NodeNo       int
	NodeName     string
	TargetSystem string
	VendorName   string
	VendorURL    string
	Deleted      bool
}

func SetProbeNodeRecordsAndNetworkMonitorTasksForTest(nodes []ProbeNodeForTest, taskNames map[string]string) func() {
	oldStore := ProbeStore
	probeNodes := make([]probeNodeRecord, 0, len(nodes))
	deletedProbeNodes := make([]probeNodeRecord, 0)
	deletedProbeNodeNos := make([]int, 0)
	for _, node := range nodes {
		targetSystem := strings.TrimSpace(node.TargetSystem)
		if targetSystem == "" {
			targetSystem = "linux"
		}
		record := probeNodeRecord{
			NodeNo:       node.NodeNo,
			NodeName:     strings.TrimSpace(node.NodeName),
			TargetSystem: targetSystem,
			VendorName:   strings.TrimSpace(node.VendorName),
			VendorURL:    strings.TrimSpace(node.VendorURL),
		}
		if node.Deleted {
			deletedProbeNodes = append(deletedProbeNodes, record)
			if node.NodeNo > 0 {
				deletedProbeNodeNos = append(deletedProbeNodeNos, node.NodeNo)
			}
			continue
		}
		probeNodes = append(probeNodes, record)
	}
	sort.Slice(probeNodes, func(i, j int) bool {
		return probeNodes[i].NodeNo < probeNodes[j].NodeNo
	})
	sort.Slice(deletedProbeNodes, func(i, j int) bool {
		return deletedProbeNodes[i].NodeNo < deletedProbeNodes[j].NodeNo
	})
	sort.Ints(deletedProbeNodeNos)
	tasks := make([]probeNetworkMonitorTaskRecord, 0, len(taskNames))
	for taskID, taskName := range taskNames {
		tasks = append(tasks, probeNetworkMonitorTaskRecord{
			ID:      strings.TrimSpace(taskID),
			Name:    strings.TrimSpace(taskName),
			NodeIDs: []string{"1"},
			Targets: []string{"1.1.1.1"},
			Enabled: true,
		})
	}
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes:          probeNodes,
		DeletedProbeNodes:   deletedProbeNodes,
		DeletedProbeNodeNos: deletedProbeNodeNos,
		NetworkMonitorTasks: tasks,
	}}
	return func() { ProbeStore = oldStore }
}

func AppendProbeNetworkMonitorResultForTest(nodeID string, taskID string, timestamp string, latencyAvgMS float64, lossPercent float64) error {
	_, err := appendProbeNetworkMonitorResult(probeNetworkMonitorResultRecord{
		NodeID:    nodeID,
		TaskID:    taskID,
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
