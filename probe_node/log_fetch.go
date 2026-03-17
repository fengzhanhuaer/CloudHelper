package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultProbeLogLines    = 200
	maxProbeLogLines        = 2000
	maxProbeLogSinceMinutes = 2000
	probeLogLineTimeLayout  = "2006/01/02 15:04:05.000000"
)

type probeLogsResultPayload struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id"`
	NodeID       string `json:"node_id"`
	OK           bool   `json:"ok"`
	Source       string `json:"source,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
	Lines        int    `json:"lines"`
	SinceMinutes int    `json:"since_minutes"`
	Content      string `json:"content,omitempty"`
	Error        string `json:"error,omitempty"`
	Timestamp    string `json:"timestamp"`
}

func runProbeLogFetch(cmd probeControlMessage, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	requestID := strings.TrimSpace(cmd.RequestID)
	if requestID == "" {
		return
	}
	lines := normalizeProbeLogLines(cmd.Lines)
	sinceMinutes := normalizeProbeLogSinceMinutes(cmd.SinceMinutes)

	source, filePath, content, err := collectProbeLogs(lines, sinceMinutes)
	payload := probeLogsResultPayload{
		Type:         "logs_result",
		RequestID:    requestID,
		NodeID:       strings.TrimSpace(identity.NodeID),
		OK:           err == nil,
		Source:       strings.TrimSpace(source),
		FilePath:     strings.TrimSpace(filePath),
		Lines:        lines,
		SinceMinutes: sinceMinutes,
		Content:      content,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
	}
	if err != nil {
		payload.Error = err.Error()
	}

	if writeErr := writeProbeStreamJSON(stream, encoder, writeMu, payload); writeErr != nil {
		log.Printf("probe logs response send failed: request_id=%s err=%v", requestID, writeErr)
	}
}

func collectProbeLogs(lines int, sinceMinutes int) (source string, filePath string, content string, err error) {
	var lastErr error

	for _, candidate := range probeLogFileCandidates() {
		stat, statErr := os.Stat(candidate)
		if statErr != nil || stat.IsDir() {
			continue
		}
		text, readErr := readProbeLogTailLines(candidate, lines, sinceMinutes)
		if readErr != nil {
			lastErr = readErr
			continue
		}
		return "file", candidate, text, nil
	}

	if runtime.GOOS == "linux" {
		text, unit, journalErr := readProbeJournalctl(lines, sinceMinutes)
		if journalErr == nil {
			return "journalctl", unit, text, nil
		}
		lastErr = journalErr
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("probe log source not found")
	}
	return "", "", "", lastErr
}

func probeLogFileCandidates() []string {
	candidates := make([]string, 0, 8)
	if v := strings.TrimSpace(os.Getenv("PROBE_NODE_LOG_FILE")); v != "" {
		candidates = append(candidates, v)
	}
	candidates = append(candidates, filepath.Join(".", "log", "probe_node.log"))
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		candidates = append(candidates,
			filepath.Join(exeDir, "log", "probe_node.log"),
			filepath.Join(exeDir, "..", "log", "probe_node.log"),
		)
	}
	candidates = append(candidates, "/opt/cloudhelper/probe_node/log/probe_node.log")

	seen := map[string]struct{}{}
	out := make([]string, 0, len(candidates))
	for _, raw := range candidates {
		cleaned := strings.TrimSpace(raw)
		if cleaned == "" {
			continue
		}
		absPath := cleaned
		if v, err := filepath.Abs(cleaned); err == nil {
			absPath = v
		}
		if _, ok := seen[absPath]; ok {
			continue
		}
		seen[absPath] = struct{}{}
		out = append(out, absPath)
	}
	return out
}

func readProbeLogTailLines(filePath string, lines int, sinceMinutes int) (string, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read log file failed: %w", err)
	}
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	allLines := strings.Split(normalized, "\n")

	filtered := make([]string, 0, len(allLines))
	if sinceMinutes > 0 {
		cutoff := time.Now().Add(-time.Duration(sinceMinutes) * time.Minute)
		for _, line := range allLines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			lineTime, ok := parseProbeLogLineTime(line)
			if !ok || lineTime.Before(cutoff) {
				continue
			}
			filtered = append(filtered, line)
		}
	} else {
		for _, line := range allLines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			filtered = append(filtered, line)
		}
	}

	limit := normalizeProbeLogLines(lines)
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return strings.Join(filtered, "\n"), nil
}

func parseProbeLogLineTime(line string) (time.Time, bool) {
	if len(line) < len(probeLogLineTimeLayout) {
		return time.Time{}, false
	}
	prefix := line[:len(probeLogLineTimeLayout)]
	parsed, err := time.ParseInLocation(probeLogLineTimeLayout, prefix, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func readProbeJournalctl(lines int, sinceMinutes int) (string, string, error) {
	units := []string{"probe_node", "probe-node"}
	var lastErr error
	for _, unit := range units {
		args := []string{"-u", unit, "-n", strconv.Itoa(normalizeProbeLogLines(lines)), "--no-pager"}
		if sinceMinutes > 0 {
			args = append(args, "--since", fmt.Sprintf("%d minutes ago", normalizeProbeLogSinceMinutes(sinceMinutes)))
		}
		cmd := exec.Command("journalctl", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			lastErr = fmt.Errorf("journalctl -u %s failed: %s", unit, msg)
			continue
		}
		return strings.TrimRight(string(out), "\r\n"), unit, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("journalctl is unavailable")
	}
	return "", "", lastErr
}

func normalizeProbeLogLines(lines int) int {
	if lines <= 0 {
		return defaultProbeLogLines
	}
	if lines > maxProbeLogLines {
		return maxProbeLogLines
	}
	return lines
}

func normalizeProbeLogSinceMinutes(sinceMinutes int) int {
	if sinceMinutes <= 0 {
		return 0
	}
	if sinceMinutes > maxProbeLogSinceMinutes {
		return maxProbeLogSinceMinutes
	}
	return sinceMinutes
}
