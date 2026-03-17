package main

import (
	"encoding/json"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	defaultProbeLogLines    = 200
	maxProbeLogLines        = 2000
	maxProbeLogSinceMinutes = 2000
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
	return probeLogSourceName, probeLogSourcePath, probeLogStore.Tail(lines, sinceMinutes), nil
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
