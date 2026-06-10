package core

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudhelper/probe_controller/internal/dashboard"
)

var serverStartTime time.Time

func PingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, statusPayload())
}

func dashboardStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dashboard/status" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, statusPayload())
}

func dashboardProbesHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dashboard/probes" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	items := publicDashboardProbeMetrics()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": items,
	})
}

func dashboardNetworkHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dashboard/network" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	scale := normalizeDashboardNetworkScale(r.URL.Query().Get("scale"))
	items := publicDashboardNetworkMetrics(scale)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"scale": scale,
		"items": items,
	})
}

type dashboardPublicProbeItem struct {
	NodeNo               int                `json:"node_no"`
	NodeName             string             `json:"node_name"`
	Online               bool               `json:"online"`
	LastSeen             string             `json:"last_seen"`
	MachineUptimeSeconds int64              `json:"machine_uptime_seconds,omitempty"`
	MachineBootTime      string             `json:"machine_boot_time,omitempty"`
	System               probeSystemMetrics `json:"system"`
}

type dashboardPublicNetworkPoint struct {
	Timestamp    string  `json:"timestamp"`
	LatencyAvgMS float64 `json:"latency_avg_ms"`
	LossPercent  float64 `json:"loss_percent"`
	OK           bool    `json:"ok"`
}

type dashboardPublicNetworkProbeItem struct {
	NodeNo   int                           `json:"node_no"`
	NodeName string                        `json:"node_name"`
	Scale    string                        `json:"scale"`
	Points   []dashboardPublicNetworkPoint `json:"points"`
}

func publicDashboardProbeMetrics() []dashboardPublicProbeItem {
	// Security note: /dashboard/* is public. Do not expose node_id/ip/version here.
	runtimes := listProbeRuntimes()
	type nodeMeta struct {
		no   int
		name string
	}
	metaMap := map[string]nodeMeta{}
	if ProbeStore != nil {
		ProbeStore.mu.RLock()
		for _, node := range loadProbeNodesLocked() {
			normalizedID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
			metaMap[normalizedID] = nodeMeta{
				no:   node.NodeNo,
				name: strings.TrimSpace(node.NodeName),
			}
		}
		ProbeStore.mu.RUnlock()
	}

	out := make([]dashboardPublicProbeItem, 0, len(runtimes))
	for _, rt := range runtimes {
		normalizedID := normalizeProbeNodeID(rt.NodeID)
		meta, ok := metaMap[normalizedID]
		nodeNo := 0
		nodeName := ""
		if ok {
			nodeNo = meta.no
			nodeName = meta.name
		}
		if nodeNo <= 0 {
			if n, err := strconv.Atoi(normalizedID); err == nil && n > 0 {
				nodeNo = n
			}
		}
		machineUptimeSeconds := normalizeProbeMachineUptimeSeconds(rt.MachineUptimeSeconds)
		machineBootTime := ""
		if machineUptimeSeconds > 0 {
			machineBootTime = time.Now().Add(-time.Duration(machineUptimeSeconds) * time.Second).UTC().Format(time.RFC3339)
		}
		out = append(out, dashboardPublicProbeItem{
			NodeNo:               nodeNo,
			NodeName:             nodeName,
			Online:               rt.Online,
			LastSeen:             strings.TrimSpace(rt.LastSeen),
			MachineUptimeSeconds: machineUptimeSeconds,
			MachineBootTime:      machineBootTime,
			System:               rt.System,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		leftNo := out[i].NodeNo
		rightNo := out[j].NodeNo
		switch {
		case leftNo > 0 && rightNo > 0 && leftNo != rightNo:
			return leftNo < rightNo
		case leftNo > 0 && rightNo <= 0:
			return true
		case leftNo <= 0 && rightNo > 0:
			return false
		}
		if out[i].NodeName != out[j].NodeName {
			return out[i].NodeName < out[j].NodeName
		}
		return i < j
	})
	return out
}

func publicDashboardNetworkMetrics(scale string) []dashboardPublicNetworkProbeItem {
	const maxDashboardNetworkPointsPerProbe = 720
	scale = normalizeDashboardNetworkScale(scale)

	type nodeMeta struct {
		no   int
		name string
	}
	metaMap := map[string]nodeMeta{}
	if ProbeStore != nil {
		ProbeStore.mu.RLock()
		for _, node := range loadProbeNodesLocked() {
			normalizedID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
			metaMap[normalizedID] = nodeMeta{
				no:   node.NodeNo,
				name: strings.TrimSpace(node.NodeName),
			}
		}
		ProbeStore.mu.RUnlock()
	}

	byNode := map[string]*dashboardPublicNetworkProbeItem{}
	for _, nodeID := range listProbeNetworkMonitorResultNodeIDs() {
		meta, ok := metaMap[nodeID]
		nodeNo := 0
		nodeName := ""
		if ok {
			if meta.no > 0 {
				nodeNo = meta.no
			}
			if meta.name != "" {
				nodeName = meta.name
			}
		}
		if nodeNo <= 0 {
			if n, err := strconv.Atoi(nodeID); err == nil && n > 0 {
				nodeNo = n
			}
		}
		results := loadProbeNetworkMonitorResultsForNode(nodeID)
		for _, result := range results {
			timestamp := strings.TrimSpace(firstNonEmptyNetworkMonitor(result.FinishedAt, result.Timestamp, result.StartedAt))
			if timestamp == "" {
				continue
			}
			latencyAvgMS, lossPercent := summarizeDashboardNetworkResult(result.Results)
			if latencyAvgMS < 0 && lossPercent < 0 {
				continue
			}
			if nodeNo <= 0 && result.NodeNo > 0 {
				nodeNo = result.NodeNo
			}
			if nodeName == "" {
				nodeName = strings.TrimSpace(result.NodeName)
			}
			item, ok := byNode[nodeID]
			if !ok {
				item = &dashboardPublicNetworkProbeItem{
					NodeNo:   nodeNo,
					NodeName: nodeName,
					Scale:    scale,
					Points:   []dashboardPublicNetworkPoint{},
				}
				byNode[nodeID] = item
			}
			item.Points = append(item.Points, dashboardPublicNetworkPoint{
				Timestamp:    timestamp,
				LatencyAvgMS: math.Max(0, latencyAvgMS),
				LossPercent:  math.Max(0, lossPercent),
				OK:           result.OK,
			})
		}
	}

	out := make([]dashboardPublicNetworkProbeItem, 0, len(byNode))
	for _, item := range byNode {
		sort.SliceStable(item.Points, func(i, j int) bool {
			return item.Points[i].Timestamp < item.Points[j].Timestamp
		})
		item.Points = bucketDashboardNetworkPoints(item.Points, scale)
		if len(item.Points) > maxDashboardNetworkPointsPerProbe {
			item.Points = downsampleDashboardNetworkPoints(item.Points, maxDashboardNetworkPointsPerProbe)
		}
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool {
		leftNo := out[i].NodeNo
		rightNo := out[j].NodeNo
		switch {
		case leftNo > 0 && rightNo > 0 && leftNo != rightNo:
			return leftNo < rightNo
		case leftNo > 0 && rightNo <= 0:
			return true
		case leftNo <= 0 && rightNo > 0:
			return false
		}
		if out[i].NodeName != out[j].NodeName {
			return out[i].NodeName < out[j].NodeName
		}
		return i < j
	})
	return out
}

func normalizeDashboardNetworkScale(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "minute", "minutes", "min":
		return "minute"
	case "hour", "hours":
		return "hour"
	case "day", "days":
		return "day"
	default:
		return "minute"
	}
}

func bucketDashboardNetworkPoints(points []dashboardPublicNetworkPoint, scale string) []dashboardPublicNetworkPoint {
	if len(points) == 0 {
		return []dashboardPublicNetworkPoint{}
	}
	scale = normalizeDashboardNetworkScale(scale)
	type bucketValue struct {
		ts           time.Time
		latencyTotal float64
		lossTotal    float64
		count        int
		okCount      int
	}
	buckets := make(map[string]*bucketValue)
	keys := make([]string, 0)
	for _, point := range points {
		ts, err := time.Parse(time.RFC3339, strings.TrimSpace(point.Timestamp))
		if err != nil {
			continue
		}
		bucketTS := truncateDashboardNetworkTime(ts.UTC(), scale)
		key := bucketTS.Format(time.RFC3339)
		bucket, ok := buckets[key]
		if !ok {
			bucket = &bucketValue{ts: bucketTS}
			buckets[key] = bucket
			keys = append(keys, key)
		}
		bucket.latencyTotal += math.Max(0, point.LatencyAvgMS)
		bucket.lossTotal += math.Max(0, point.LossPercent)
		bucket.count++
		if point.OK {
			bucket.okCount++
		}
	}
	sort.Strings(keys)
	out := make([]dashboardPublicNetworkPoint, 0, len(keys))
	for _, key := range keys {
		bucket := buckets[key]
		if bucket == nil || bucket.count == 0 {
			continue
		}
		out = append(out, dashboardPublicNetworkPoint{
			Timestamp:    bucket.ts.Format(time.RFC3339),
			LatencyAvgMS: bucket.latencyTotal / float64(bucket.count),
			LossPercent:  bucket.lossTotal / float64(bucket.count),
			OK:           bucket.okCount == bucket.count,
		})
	}
	return out
}

func truncateDashboardNetworkTime(ts time.Time, scale string) time.Time {
	switch normalizeDashboardNetworkScale(scale) {
	case "day":
		return time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
	case "hour":
		return ts.Truncate(time.Hour)
	default:
		return ts.Truncate(time.Minute)
	}
}

func downsampleDashboardNetworkPoints(points []dashboardPublicNetworkPoint, maxPoints int) []dashboardPublicNetworkPoint {
	if maxPoints <= 0 || len(points) <= maxPoints {
		return points
	}
	out := make([]dashboardPublicNetworkPoint, 0, maxPoints)
	for i := 0; i < maxPoints; i++ {
		index := int(math.Round(float64(i) * float64(len(points)-1) / float64(maxPoints-1)))
		if index < 0 {
			index = 0
		}
		if index >= len(points) {
			index = len(points) - 1
		}
		out = append(out, points[index])
	}
	return out
}

func summarizeDashboardNetworkResult(results []probeNetworkMonitorTargetResult) (float64, float64) {
	latencyTotal := 0.0
	latencyCount := 0
	lossTotal := 0.0
	lossCount := 0
	for _, item := range results {
		if item.LatencyAvgMS > 0 && !math.IsNaN(item.LatencyAvgMS) && !math.IsInf(item.LatencyAvgMS, 0) {
			latencyTotal += item.LatencyAvgMS
			latencyCount++
		}
		if item.LossPercent >= 0 && !math.IsNaN(item.LossPercent) && !math.IsInf(item.LossPercent, 0) {
			lossTotal += item.LossPercent
			lossCount++
		}
	}
	latencyAvgMS := -1.0
	if latencyCount > 0 {
		latencyAvgMS = latencyTotal / float64(latencyCount)
	}
	lossPercent := -1.0
	if lossCount > 0 {
		lossPercent = lossTotal / float64(lossCount)
	}
	return latencyAvgMS, lossPercent
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dashboard" {
		http.NotFound(w, r)
		return
	}
	dashboard.Handler(w, r)
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func statusPayload() map[string]interface{} {
	return map[string]interface{}{
		"uptime":     int(time.Since(serverStartTime).Seconds()),
		"started_at": serverStartTime.UTC().Format(time.RFC3339),
	}
}

func SetServerStartTimeForTest(ts time.Time) {
	serverStartTime = ts
}
