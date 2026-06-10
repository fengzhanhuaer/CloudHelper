package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type probeNetworkMonitorTargetResult struct {
	Target       string  `json:"target"`
	IPFamily     string  `json:"ip_family"`
	Sent         int     `json:"sent"`
	Received     int     `json:"received"`
	LossPercent  float64 `json:"loss_percent"`
	LatencyMinMS float64 `json:"latency_min_ms,omitempty"`
	LatencyAvgMS float64 `json:"latency_avg_ms,omitempty"`
	LatencyMaxMS float64 `json:"latency_max_ms,omitempty"`
	Error        string  `json:"error,omitempty"`
}

type probeNetworkMonitorTaskPayload struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	NodeIDs   []string `json:"node_ids,omitempty"`
	Targets   []string `json:"targets"`
	Count     int      `json:"count"`
	TimeoutMS int      `json:"timeout_ms"`
	CycleSec  int      `json:"cycle_sec"`
	Enabled   bool     `json:"enabled"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

type probeNetworkMonitorResultPayload struct {
	Type       string                            `json:"type"`
	RequestID  string                            `json:"request_id"`
	TaskID     string                            `json:"task_id,omitempty"`
	NodeID     string                            `json:"node_id"`
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

var probeNetworkMonitorRTTPattern = regexp.MustCompile(`(?i)(?:time|时间|平均|avg)[=<>：]?\s*([0-9]+(?:[.,][0-9]+)?)\s*ms`)

var probeNetworkMonitorTaskState = struct {
	mu      sync.Mutex
	runners map[string]context.CancelFunc
}{runners: make(map[string]context.CancelFunc)}

func applyProbeNetworkMonitorTasks(cmd probeControlMessage, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	active := make(map[string]bool)
	for _, task := range normalizeProbeNetworkMonitorTasks(cmd.NetworkMonitorTasks) {
		active[task.ID] = true
		startProbeNetworkMonitorTask(task, identity, stream, encoder, writeMu)
	}

	probeNetworkMonitorTaskState.mu.Lock()
	for id, cancel := range probeNetworkMonitorTaskState.runners {
		if active[id] {
			continue
		}
		cancel()
		delete(probeNetworkMonitorTaskState.runners, id)
	}
	probeNetworkMonitorTaskState.mu.Unlock()
}

func startProbeNetworkMonitorTask(task probeNetworkMonitorTaskPayload, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	if strings.TrimSpace(task.ID) == "" || !task.Enabled {
		return
	}
	probeNetworkMonitorTaskState.mu.Lock()
	if cancel, ok := probeNetworkMonitorTaskState.runners[task.ID]; ok {
		cancel()
		delete(probeNetworkMonitorTaskState.runners, task.ID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	probeNetworkMonitorTaskState.runners[task.ID] = cancel
	probeNetworkMonitorTaskState.mu.Unlock()

	go func() {
		if !runProbeNetworkMonitorTaskOnce(ctx, task, identity, stream, encoder, writeMu) {
			return
		}
		ticker := time.NewTicker(time.Duration(task.CycleSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !runProbeNetworkMonitorTaskOnce(ctx, task, identity, stream, encoder, writeMu) {
					return
				}
			}
		}
	}()
}

func runProbeNetworkMonitorTaskOnce(ctx context.Context, task probeNetworkMonitorTaskPayload, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}
	startedAt := time.Now().UTC()
	payload := runProbeNetworkMonitorTargets(task.Targets, task.Count, task.TimeoutMS, identity)
	payload.TaskID = strings.TrimSpace(task.ID)
	payload.CycleSec = task.CycleSec
	payload.StartedAt = startedAt.Format(time.RFC3339)
	payload.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	payload.Timestamp = payload.FinishedAt
	return sendProbeNetworkMonitorResult(stream, encoder, writeMu, payload)
}

func runProbeNetworkMonitorTest(cmd probeControlMessage, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	requestID := strings.TrimSpace(cmd.RequestID)
	if requestID == "" {
		return
	}

	targets, normalizeErr := normalizeProbeNetworkMonitorTargets(cmd.Targets)
	count := normalizeProbeNetworkMonitorCount(cmd.Count)
	timeoutMS := normalizeProbeNetworkMonitorTimeoutMS(cmd.TimeoutMS)
	payload := probeNetworkMonitorResultPayload{
		Type:      "network_monitor_result",
		RequestID: requestID,
		NodeID:    strings.TrimSpace(identity.NodeID),
		OK:        normalizeErr == nil,
		Count:     count,
		TimeoutMS: timeoutMS,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if normalizeErr != nil {
		payload.Error = normalizeErr.Error()
		sendProbeNetworkMonitorResult(stream, encoder, writeMu, payload)
		return
	}

	payload = runProbeNetworkMonitorTargets(targets, count, timeoutMS, identity)
	payload.RequestID = requestID
	sendProbeNetworkMonitorResult(stream, encoder, writeMu, payload)
}

func runProbeNetworkMonitorTargets(targets []string, count int, timeoutMS int, identity nodeIdentity) probeNetworkMonitorResultPayload {
	results := make([]probeNetworkMonitorTargetResult, 0, len(targets))
	for _, target := range targets {
		results = append(results, probeNetworkMonitorTarget(target, count, timeoutMS))
	}
	return probeNetworkMonitorResultPayload{
		Type:      "network_monitor_result",
		NodeID:    strings.TrimSpace(identity.NodeID),
		OK:        true,
		Count:     count,
		TimeoutMS: timeoutMS,
		Results:   results,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

func sendProbeNetworkMonitorResult(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, payload probeNetworkMonitorResultPayload) bool {
	if writeErr := writeProbeStreamJSON(stream, encoder, writeMu, payload); writeErr != nil {
		log.Printf("probe network monitor response send failed: request_id=%s err=%v", strings.TrimSpace(payload.RequestID), writeErr)
		return false
	}
	return true
}

func probeNetworkMonitorTarget(target string, count int, timeoutMS int) probeNetworkMonitorTargetResult {
	ip := net.ParseIP(strings.TrimSpace(target))
	result := probeNetworkMonitorTargetResult{
		Target:      strings.TrimSpace(target),
		IPFamily:    probeNetworkMonitorIPFamily(ip),
		Sent:        count,
		LossPercent: 100,
	}
	if ip == nil {
		result.Error = "invalid target ip"
		return result
	}

	latencies := make([]float64, 0, count)
	var errorsOut []string
	for i := 0; i < count; i++ {
		latencyMS, err := runProbeNetworkMonitorPing(ip.String(), timeoutMS)
		if err != nil {
			if len(errorsOut) < 2 {
				errorsOut = append(errorsOut, err.Error())
			}
			continue
		}
		latencies = append(latencies, latencyMS)
	}

	result.Received = len(latencies)
	if result.Sent > 0 {
		result.LossPercent = roundFloat(float64(result.Sent-result.Received)*100/float64(result.Sent), 2)
	}
	if len(latencies) > 0 {
		minValue := latencies[0]
		maxValue := latencies[0]
		sum := 0.0
		for _, value := range latencies {
			if value < minValue {
				minValue = value
			}
			if value > maxValue {
				maxValue = value
			}
			sum += value
		}
		result.LatencyMinMS = roundFloat(minValue, 2)
		result.LatencyAvgMS = roundFloat(sum/float64(len(latencies)), 2)
		result.LatencyMaxMS = roundFloat(maxValue, 2)
	}
	if result.Received == 0 && len(errorsOut) > 0 {
		result.Error = strings.Join(errorsOut, "; ")
	}
	return result
}

func runProbeNetworkMonitorPing(target string, timeoutMS int) (float64, error) {
	timeout := time.Duration(timeoutMS) * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout+2*time.Second)
	defer cancel()

	commandName, args := probeNetworkMonitorPingCommand(target, timeoutMS)
	command := exec.CommandContext(ctx, commandName, args...)
	output, err := command.CombinedOutput()
	text := string(output)
	if ctx.Err() != nil {
		return 0, fmt.Errorf("ping timeout")
	}
	if latency, ok := parseProbeNetworkMonitorLatency(text); ok {
		return latency, nil
	}
	if err != nil {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			trimmed = err.Error()
		}
		return 0, fmt.Errorf("%s", trimmed)
	}
	return 0, fmt.Errorf("ping latency not found")
}

func probeNetworkMonitorPingCommand(target string, timeoutMS int) (string, []string) {
	timeoutSec := int(math.Ceil(float64(timeoutMS) / 1000))
	if timeoutSec < 1 {
		timeoutSec = 1
	}
	ip := net.ParseIP(strings.TrimSpace(target))
	if runtime.GOOS == "windows" {
		return "ping", []string{"-n", "1", "-w", strconv.Itoa(timeoutMS), target}
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "freebsd" || runtime.GOOS == "openbsd" || runtime.GOOS == "netbsd" {
		if ip != nil && ip.To4() == nil {
			return "ping6", []string{"-c", "1", target}
		}
		return "ping", []string{"-c", "1", "-W", strconv.Itoa(timeoutMS), target}
	}
	return "ping", []string{"-c", "1", "-W", strconv.Itoa(timeoutSec), target}
}

func parseProbeNetworkMonitorLatency(output string) (float64, bool) {
	matches := probeNetworkMonitorRTTPattern.FindAllStringSubmatch(output, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		text := strings.ReplaceAll(strings.TrimSpace(match[1]), ",", ".")
		value, err := strconv.ParseFloat(text, 64)
		if err == nil && value >= 0 {
			return value, true
		}
	}
	return 0, false
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

func normalizeProbeNetworkMonitorTasks(raw []probeNetworkMonitorTaskPayload) []probeNetworkMonitorTaskPayload {
	seen := make(map[string]bool)
	out := make([]probeNetworkMonitorTaskPayload, 0, len(raw))
	for _, item := range raw {
		id := strings.TrimSpace(item.ID)
		if id == "" || seen[id] || !item.Enabled {
			continue
		}
		targets, err := normalizeProbeNetworkMonitorTargets(item.Targets)
		if err != nil {
			continue
		}
		item.ID = id
		item.Name = strings.Join(strings.Fields(strings.TrimSpace(item.Name)), " ")
		item.Targets = targets
		item.Count = normalizeProbeNetworkMonitorCount(item.Count)
		item.TimeoutMS = normalizeProbeNetworkMonitorTimeoutMS(item.TimeoutMS)
		item.CycleSec = normalizeProbeNetworkMonitorCycleSec(item.CycleSec)
		seen[id] = true
		out = append(out, item)
	}
	return out
}

func normalizeProbeNetworkMonitorCycleSec(raw int) int {
	if raw <= 0 {
		return 60
	}
	if raw < 5 {
		return 5
	}
	if raw > 86400 {
		return 86400
	}
	return raw
}

func probeNetworkMonitorIPFamily(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if ip.To4() != nil {
		return "ipv4"
	}
	return "ipv6"
}

func roundFloat(value float64, precision int) float64 {
	scale := math.Pow10(precision)
	return math.Round(value*scale) / scale
}
