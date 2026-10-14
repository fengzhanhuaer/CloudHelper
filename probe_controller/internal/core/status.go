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
	VendorName           string             `json:"vendor_name,omitempty"`
	VendorURL            string             `json:"vendor_url,omitempty"`
	Locations            []string           `json:"locations,omitempty"`
	Online               bool               `json:"online"`
	LastSeen             string             `json:"last_seen"`
	MachineUptimeSeconds int64              `json:"machine_uptime_seconds,omitempty"`
	MachineBootTime      string             `json:"machine_boot_time,omitempty"`
	Traffic              probeTrafficUsage  `json:"traffic"`
	System               probeSystemMetrics `json:"system"`
}

type dashboardPublicNetworkPoint struct {
	Timestamp    string  `json:"timestamp"`
	LatencyAvgMS float64 `json:"latency_avg_ms"`
	LossPercent  float64 `json:"loss_percent"`
	OK           bool    `json:"ok"`
}

type dashboardPublicNetworkSeries struct {
	TaskID   string                        `json:"task_id"`
	TaskName string                        `json:"task_name"`
	Points   []dashboardPublicNetworkPoint `json:"points"`
}

type dashboardPublicNetworkProbeItem struct {
	NodeNo     int                            `json:"node_no"`
	NodeName   string                         `json:"node_name"`
	VendorName string                         `json:"vendor_name,omitempty"`
	VendorURL  string                         `json:"vendor_url,omitempty"`
	Locations  []string                       `json:"locations,omitempty"`
	Scale      string                         `json:"scale"`
	Series     []dashboardPublicNetworkSeries `json:"series"`
}

type dashboardNetworkWeightedPoint struct {
	Timestamp    string
	LatencySum   float64
	LatencyCount int64
	LossSum      float64
	LossCount    int64
	OKCount      int64
	ResultCount  int64
}

func publicDashboardProbeMetrics() []dashboardPublicProbeItem {
	// Security note: /dashboard/* is public. Do not expose node_id/ip/version here.
	runtimes := listProbeRuntimes()
	type nodeMeta struct {
		no              int
		name            string
		vendor          string
		vendorURL       string
		trafficStartDay int
	}
	metaMap := map[string]nodeMeta{}
	if ProbeStore != nil {
		ProbeStore.mu.RLock()
		for _, node := range loadProbeNodesLocked() {
			if !isDashboardVisibleProbeNode(node) {
				continue
			}
			normalizedID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
			metaMap[normalizedID] = nodeMeta{
				no:              node.NodeNo,
				name:            strings.TrimSpace(node.NodeName),
				vendor:          strings.TrimSpace(node.VendorName),
				vendorURL:       strings.TrimSpace(node.VendorURL),
				trafficStartDay: normalizeMonthlyTrafficStartDay(node.MonthlyTrafficStartDay),
			}
		}
		ProbeStore.mu.RUnlock()
	}

	out := make([]dashboardPublicProbeItem, 0, len(runtimes))
	for _, rt := range runtimes {
		normalizedID := normalizeProbeNodeID(rt.NodeID)
		meta, ok := metaMap[normalizedID]
		if !ok {
			continue
		}
		nodeNo := 0
		nodeName := ""
		nodeNo = meta.no
		nodeName = meta.name
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
		traffic, trafficErr := loadProbeTrafficUsage(normalizedID, meta.trafficStartDay, time.Now().UTC())
		if trafficErr != nil {
			logControllerWarnf("failed to load probe traffic: node=%s err=%v", normalizedID, trafficErr)
			traffic = probeTrafficUsage{StartDay: meta.trafficStartDay}
		}
		out = append(out, dashboardPublicProbeItem{
			NodeNo:               nodeNo,
			NodeName:             nodeName,
			VendorName:           meta.vendor,
			VendorURL:            meta.vendorURL,
			Locations:            dashboardProbeRuntimeLocations(rt),
			Online:               rt.Online,
			LastSeen:             strings.TrimSpace(rt.LastSeen),
			MachineUptimeSeconds: machineUptimeSeconds,
			MachineBootTime:      machineBootTime,
			Traffic:              traffic,
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

func dashboardProbeRuntimeLocations(rt probeRuntimeStatus) []string {
	values := make([]string, 0, len(rt.IPLocations))
	seen := map[string]struct{}{}
	for _, ip := range append(append([]string{}, rt.IPv4...), rt.IPv6...) {
		label := strings.TrimSpace(rt.IPLocations[strings.TrimSpace(ip)])
		if label == "" || label == "未知" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		values = append(values, label)
	}
	sort.Strings(values)
	return values
}

func publicDashboardNetworkMetrics(scale string) []dashboardPublicNetworkProbeItem {
	const maxDashboardNetworkPointsPerProbe = 1440
	scale = normalizeDashboardNetworkScale(scale)
	now := time.Now().UTC()
	window := 24 * time.Hour
	if scale == "hour" {
		window = 30 * 24 * time.Hour
	} else if scale == "day" {
		window = 180 * 24 * time.Hour
	}
	windowStart := now.Add(-window)
	windowStart = truncateDashboardNetworkTime(windowStart, scale)

	type nodeMeta struct {
		no        int
		name      string
		vendor    string
		vendorURL string
	}
	metaMap := map[string]nodeMeta{}
	taskNameMap := map[string]string{}
	if ProbeStore != nil {
		ProbeStore.mu.RLock()
		for _, node := range loadProbeNodesLocked() {
			if !isDashboardVisibleProbeNode(node) {
				continue
			}
			normalizedID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
			metaMap[normalizedID] = nodeMeta{
				no:        node.NodeNo,
				name:      strings.TrimSpace(node.NodeName),
				vendor:    strings.TrimSpace(node.VendorName),
				vendorURL: strings.TrimSpace(node.VendorURL),
			}
		}
		for _, task := range loadProbeNetworkMonitorTasksLocked() {
			taskID := strings.TrimSpace(task.ID)
			if taskID == "" {
				continue
			}
			taskNameMap[taskID] = normalizeProbeNetworkMonitorTaskName(task.Name)
		}
		ProbeStore.mu.RUnlock()
	}

	type nodeAccumulator struct {
		item   *dashboardPublicNetworkProbeItem
		points map[string][]dashboardNetworkWeightedPoint
	}
	byNode := map[string]*nodeAccumulator{}
	for _, nodeID := range listProbeNetworkMonitorResultNodeIDs() {
		meta, ok := metaMap[nodeID]
		if !ok {
			continue
		}
		nodeNo := 0
		nodeName := ""
		if meta.no > 0 {
			nodeNo = meta.no
		}
		if meta.name != "" {
			nodeName = meta.name
		}
		if nodeNo <= 0 {
			if n, err := strconv.Atoi(nodeID); err == nil && n > 0 {
				nodeNo = n
			}
		}
		rt, _ := getProbeRuntime(nodeID)
		results, history := loadProbeNetworkMonitorChartHistoryForNode(nodeID)
		acc := &nodeAccumulator{
			item: &dashboardPublicNetworkProbeItem{
				NodeNo:     nodeNo,
				NodeName:   nodeName,
				VendorName: meta.vendor,
				VendorURL:  meta.vendorURL,
				Locations:  dashboardProbeRuntimeLocations(rt),
				Scale:      scale,
				Series:     []dashboardPublicNetworkSeries{},
			},
			points: map[string][]dashboardNetworkWeightedPoint{},
		}
		appendPoint := func(taskID string, point dashboardNetworkWeightedPoint) {
			if taskID == "" {
				taskID = "default"
			}
			if _, exists := acc.points[taskID]; !exists {
				acc.points[taskID] = []dashboardNetworkWeightedPoint{}
			}
			acc.points[taskID] = append(acc.points[taskID], point)
		}
		for _, result := range results {
			timestamp := strings.TrimSpace(firstNonEmptyNetworkMonitor(result.FinishedAt, result.Timestamp, result.StartedAt))
			ts := parseDashboardNetworkTimestamp(timestamp)
			if ts.IsZero() || ts.Before(windowStart) {
				continue
			}
			if watermark, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(history.CompactedThrough)); err == nil && ts.Before(watermark) {
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
			taskID, _ := dashboardNetworkTaskIdentity(result, taskNameMap)
			weighted := dashboardNetworkWeightedPoint{Timestamp: ts.UTC().Format(time.RFC3339), ResultCount: 1}
			if latencyAvgMS >= 0 {
				weighted.LatencySum, weighted.LatencyCount = latencyAvgMS, 1
			}
			if lossPercent >= 0 {
				weighted.LossSum, weighted.LossCount = lossPercent, 1
			}
			if result.OK {
				weighted.OKCount = 1
			}
			appendPoint(taskID, weighted)
		}
		appendArchived := func(buckets []probeNetworkMonitorHistoryBucket) {
			for _, bucket := range buckets {
				ts := parseDashboardNetworkTimestamp(bucket.Timestamp)
				if ts.IsZero() || ts.Before(windowStart) {
					continue
				}
				appendPoint(bucket.TaskID, dashboardNetworkWeightedPoint{
					Timestamp:    ts.UTC().Format(time.RFC3339),
					LatencySum:   bucket.LatencySum,
					LatencyCount: bucket.LatencyCount,
					LossSum:      bucket.LossSum,
					LossCount:    bucket.LossCount,
					OKCount:      bucket.OKCount,
					ResultCount:  bucket.ResultCount,
				})
			}
		}
		if scale == "hour" || scale == "day" {
			appendArchived(history.Hourly)
		}
		if scale == "day" {
			appendArchived(history.Daily)
		}
		byNode[nodeID] = acc
	}

	out := make([]dashboardPublicNetworkProbeItem, 0, len(byNode))
	for _, acc := range byNode {
		seriesList := make([]dashboardPublicNetworkSeries, 0, len(acc.points))
		for taskID, points := range acc.points {
			taskName := strings.TrimSpace(taskNameMap[taskID])
			if taskName == "" {
				if taskID == "default" {
					taskName = "未命名任务"
				} else {
					taskName = taskID
				}
			}
			series := dashboardPublicNetworkSeries{TaskID: taskID, TaskName: taskName, Points: bucketDashboardWeightedNetworkPoints(points, scale)}
			if len(series.Points) > maxDashboardNetworkPointsPerProbe {
				series.Points = downsampleDashboardNetworkPoints(series.Points, maxDashboardNetworkPointsPerProbe)
			}
			if len(series.Points) == 0 {
				continue
			}
			seriesList = append(seriesList, series)
		}
		sort.Slice(seriesList, func(i, j int) bool {
			if seriesList[i].TaskName != seriesList[j].TaskName {
				return seriesList[i].TaskName < seriesList[j].TaskName
			}
			return seriesList[i].TaskID < seriesList[j].TaskID
		})
		if len(seriesList) == 0 {
			continue
		}
		acc.item.Series = seriesList
		out = append(out, *acc.item)
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

func parseDashboardNetworkTimestamp(raw string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(raw)); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func bucketDashboardWeightedNetworkPoints(points []dashboardNetworkWeightedPoint, scale string) []dashboardPublicNetworkPoint {
	type aggregate struct {
		ts           time.Time
		latencySum   float64
		latencyCount int64
		lossSum      float64
		lossCount    int64
		okCount      int64
		resultCount  int64
	}
	buckets := map[string]*aggregate{}
	for _, point := range points {
		ts := parseDashboardNetworkTimestamp(point.Timestamp)
		if ts.IsZero() {
			continue
		}
		bucketTime := truncateDashboardNetworkTime(ts, scale)
		key := bucketTime.Format(time.RFC3339)
		bucket := buckets[key]
		if bucket == nil {
			bucket = &aggregate{ts: bucketTime}
			buckets[key] = bucket
		}
		bucket.latencySum += point.LatencySum
		bucket.latencyCount += point.LatencyCount
		bucket.lossSum += point.LossSum
		bucket.lossCount += point.LossCount
		bucket.okCount += point.OKCount
		bucket.resultCount += point.ResultCount
	}
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]dashboardPublicNetworkPoint, 0, len(keys))
	for _, key := range keys {
		bucket := buckets[key]
		if bucket.resultCount == 0 {
			continue
		}
		latencyAvg, lossAvg := 0.0, 0.0
		if bucket.latencyCount > 0 {
			latencyAvg = bucket.latencySum / float64(bucket.latencyCount)
		}
		if bucket.lossCount > 0 {
			lossAvg = bucket.lossSum / float64(bucket.lossCount)
		}
		out = append(out, dashboardPublicNetworkPoint{
			Timestamp:    bucket.ts.Format(time.RFC3339),
			LatencyAvgMS: latencyAvg,
			LossPercent:  lossAvg,
			OK:           bucket.okCount == bucket.resultCount,
		})
	}
	return out
}

func isDashboardVisibleProbeNode(node probeNodeRecord) bool {
	if node.NodeNo <= 0 {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(node.TargetSystem), "android")
}

func dashboardNetworkTaskIdentity(result probeNetworkMonitorResultRecord, taskNameMap map[string]string) (string, string) {
	taskID := strings.TrimSpace(result.TaskID)
	if taskID == "" {
		taskID = "default"
	}
	taskName := ""
	if taskNameMap != nil {
		taskName = strings.TrimSpace(taskNameMap[taskID])
	}
	if taskName == "" {
		if taskID != "default" {
			taskName = taskID
		} else {
			taskName = "未命名任务"
		}
	}
	return taskID, taskName
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
