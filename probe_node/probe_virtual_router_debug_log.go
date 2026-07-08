package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	probeVirtualRouterDebugLogTimeout          = 8 * time.Second
	probeVirtualRouterDebugLogDefaultLines     = 200
	probeVirtualRouterDebugLogMaxLines         = 500
	probeVirtualRouterDebugLogPayloadSoftLimit = probeVirtualRouterFrameMaxDataBytes - 4096
)

type probeVirtualRouterDebugLogPayload struct {
	RequestID         string              `json:"request_id"`
	SourceNodeID      string              `json:"source_node_id,omitempty"`
	TargetNodeID      string              `json:"target_node_id,omitempty"`
	Path              []string            `json:"path,omitempty"`
	Lines             int                 `json:"lines,omitempty"`
	SinceMinutes      int                 `json:"since_minutes,omitempty"`
	MinLevel          string              `json:"min_level,omitempty"`
	Keyword           string              `json:"keyword,omitempty"`
	Source            string              `json:"source,omitempty"`
	FilePath          string              `json:"file_path,omitempty"`
	Content           string              `json:"content,omitempty"`
	Entries           []probeLogViewEntry `json:"entries,omitempty"`
	Count             int                 `json:"count,omitempty"`
	Truncated         bool                `json:"truncated,omitempty"`
	OK                bool                `json:"ok,omitempty"`
	Error             string              `json:"error,omitempty"`
	Responder         string              `json:"responder,omitempty"`
	CreatedAtUnixNano int64               `json:"created_at_unix_nano,omitempty"`
	RespondedUnixNano int64               `json:"responded_unix_nano,omitempty"`
}

var probeVirtualRouterDebugLogState = struct {
	mu      sync.Mutex
	pending map[string]chan probeVirtualRouterDebugLogPayload
}{pending: make(map[string]chan probeVirtualRouterDebugLogPayload)}

func runProbeVirtualRouterDebugLogFetch(targetNodeID string, lines int, sinceMinutes int, minLevel string, keyword string, timeout time.Duration) (probeVirtualRouterDebugLogPayload, error) {
	localNodeID := normalizeProbeRouteNodeID(currentProbeVirtualRouterLocalNodeID())
	targetNodeID = normalizeProbeRouteNodeID(targetNodeID)
	if localNodeID == "" {
		return probeVirtualRouterDebugLogPayload{}, errors.New("local virtual router node id is empty")
	}
	if targetNodeID == "" {
		return probeVirtualRouterDebugLogPayload{}, errors.New("target_node_id is required")
	}
	if timeout <= 0 {
		timeout = probeVirtualRouterDebugLogTimeout
	}
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	msg := probeVirtualRouterDebugLogPayload{
		RequestID:         newProbeTCPDebugFlowID("vrouter_debug_log", targetNodeID),
		SourceNodeID:      localNodeID,
		TargetNodeID:      targetNodeID,
		Lines:             normalizeProbeVirtualRouterDebugLogLines(lines),
		SinceMinutes:      normalizeProbeLogSinceMinutes(sinceMinutes),
		MinLevel:          normalizeProbeVirtualRouterDebugLogMinLevel(minLevel),
		Keyword:           strings.TrimSpace(keyword),
		CreatedAtUnixNano: time.Now().UnixNano(),
	}
	if targetNodeID == localNodeID {
		response := collectProbeVirtualRouterDebugLogResponse(msg, nil)
		response.Path = []string{localNodeID}
		return response, nil
	}
	path := currentProbeVirtualRouterPathBetweenNodes(localNodeID, targetNodeID)
	msg.Path = cleanProbeVirtualRouterPath(path)
	if len(msg.Path) < 2 {
		return probeVirtualRouterDebugLogPayload{}, fmt.Errorf("virtual router debug log path is unavailable: source=%s target=%s", localNodeID, targetNodeID)
	}
	waiter := registerProbeVirtualRouterDebugLogResponse(msg.RequestID)
	defer unregisterProbeVirtualRouterDebugLogResponse(msg.RequestID)
	payload, err := json.Marshal(msg)
	if err != nil {
		return probeVirtualRouterDebugLogPayload{}, err
	}
	if err := forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeDebugLog, probeVirtualRouterDebugLogSubTypeQuery, payload, msg.Path); err != nil {
		return probeVirtualRouterDebugLogPayload{}, err
	}
	response, err := waitProbeVirtualRouterDebugLogResponse(waiter, timeout)
	if err != nil {
		return probeVirtualRouterDebugLogPayload{}, fmt.Errorf("%w: request_id=%s target=%s path=%s", err, msg.RequestID, targetNodeID, strings.Join(msg.Path, ">"))
	}
	return response, nil
}

func handleProbeVirtualRouterDebugLogFrame(runtime *probeVirtualRouterRuntime, subType uint16, msg probeVirtualRouterDebugLogPayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if localNodeID == "" {
		localNodeID = currentProbeVirtualRouterLocalNodeID()
	}
	localNodeID = normalizeProbeRouteNodeID(localNodeID)
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	if localNodeID == "" || strings.TrimSpace(msg.RequestID) == "" || len(msg.Path) < 2 {
		return errors.New("virtual router debug log frame is incomplete")
	}
	switch subType {
	case probeVirtualRouterDebugLogSubTypeQuery:
		targetNodeID := normalizeProbeRouteNodeID(msg.TargetNodeID)
		if targetNodeID == "" {
			targetNodeID = msg.Path[len(msg.Path)-1]
		}
		if targetNodeID == localNodeID || probeVirtualRouterNextHopInPath(msg.Path, localNodeID) == "" {
			return sendProbeVirtualRouterDebugLogResponse(msg, runtime)
		}
		payload, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeDebugLog, probeVirtualRouterDebugLogSubTypeQuery, payload, msg.Path)
	case probeVirtualRouterDebugLogSubTypeResponse:
		if normalizeProbeRouteNodeID(msg.SourceNodeID) == localNodeID || msg.Path[len(msg.Path)-1] == localNodeID {
			completeProbeVirtualRouterDebugLogResponse(msg)
			return nil
		}
		payload, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeDebugLog, probeVirtualRouterDebugLogSubTypeResponse, payload, msg.Path)
	default:
		return fmt.Errorf("unsupported virtual router debug log subtype=%d", subType)
	}
}

func sendProbeVirtualRouterDebugLogResponse(request probeVirtualRouterDebugLogPayload, runtime *probeVirtualRouterRuntime) error {
	response := collectProbeVirtualRouterDebugLogResponse(request, runtime)
	response.Path = probeVirtualRouterReversePath(request.Path)
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeDebugLog, probeVirtualRouterDebugLogSubTypeResponse, payload, response.Path)
}

func collectProbeVirtualRouterDebugLogResponse(request probeVirtualRouterDebugLogPayload, runtime *probeVirtualRouterRuntime) probeVirtualRouterDebugLogPayload {
	response := request
	response.Lines = normalizeProbeVirtualRouterDebugLogLines(request.Lines)
	response.SinceMinutes = normalizeProbeLogSinceMinutes(request.SinceMinutes)
	response.MinLevel = normalizeProbeVirtualRouterDebugLogMinLevel(request.MinLevel)
	response.Keyword = strings.TrimSpace(request.Keyword)
	response.OK = true
	response.Error = ""
	response.Responder = currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if response.Responder == "" {
		response.Responder = currentProbeVirtualRouterLocalNodeID()
	}
	response.Responder = normalizeProbeRouteNodeID(response.Responder)
	response.RespondedUnixNano = time.Now().UnixNano()
	source, filePath, content, entries, err := collectProbeLocalLogsForView(response.Lines, response.SinceMinutes, response.MinLevel, response.Keyword)
	response.Source = strings.TrimSpace(source)
	response.FilePath = strings.TrimSpace(filePath)
	response.Content = content
	response.Entries = entries
	response.Count = len(entries)
	if err != nil {
		response.OK = false
		response.Error = err.Error()
	}
	fitProbeVirtualRouterDebugLogPayload(&response)
	return response
}

func registerProbeVirtualRouterDebugLogResponse(requestID string) chan probeVirtualRouterDebugLogPayload {
	requestID = strings.TrimSpace(requestID)
	ch := make(chan probeVirtualRouterDebugLogPayload, 1)
	if requestID == "" {
		return ch
	}
	probeVirtualRouterDebugLogState.mu.Lock()
	if probeVirtualRouterDebugLogState.pending == nil {
		probeVirtualRouterDebugLogState.pending = make(map[string]chan probeVirtualRouterDebugLogPayload)
	}
	probeVirtualRouterDebugLogState.pending[requestID] = ch
	probeVirtualRouterDebugLogState.mu.Unlock()
	return ch
}

func unregisterProbeVirtualRouterDebugLogResponse(requestID string) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterDebugLogState.mu.Lock()
	delete(probeVirtualRouterDebugLogState.pending, requestID)
	probeVirtualRouterDebugLogState.mu.Unlock()
}

func waitProbeVirtualRouterDebugLogResponse(ch chan probeVirtualRouterDebugLogPayload, timeout time.Duration) (probeVirtualRouterDebugLogPayload, error) {
	if ch == nil {
		return probeVirtualRouterDebugLogPayload{}, errors.New("virtual router debug log response waiter is nil")
	}
	if timeout <= 0 {
		timeout = probeVirtualRouterDebugLogTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-ch:
		return response, nil
	case <-timer.C:
		return probeVirtualRouterDebugLogPayload{}, errors.New("virtual router debug log response timeout")
	}
}

func completeProbeVirtualRouterDebugLogResponse(msg probeVirtualRouterDebugLogPayload) {
	requestID := strings.TrimSpace(msg.RequestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterDebugLogState.mu.Lock()
	ch := probeVirtualRouterDebugLogState.pending[requestID]
	probeVirtualRouterDebugLogState.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func normalizeProbeVirtualRouterDebugLogLines(lines int) int {
	if lines <= 0 {
		lines = probeVirtualRouterDebugLogDefaultLines
	}
	lines = normalizeProbeLogLines(lines)
	if lines > probeVirtualRouterDebugLogMaxLines {
		return probeVirtualRouterDebugLogMaxLines
	}
	return lines
}

func normalizeProbeVirtualRouterDebugLogMinLevel(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return string(probeLogLevelRealtime)
	}
	return value
}

func fitProbeVirtualRouterDebugLogPayload(msg *probeVirtualRouterDebugLogPayload) {
	if msg == nil {
		return
	}
	for len(msg.Entries) > 0 {
		raw, err := json.Marshal(msg)
		if err != nil || len(raw) <= probeVirtualRouterDebugLogPayloadSoftLimit {
			return
		}
		drop := len(msg.Entries) / 4
		if drop < 1 {
			drop = 1
		}
		msg.Entries = msg.Entries[drop:]
		msg.Content = buildProbeLocalLogContent(msg.Entries)
		msg.Count = len(msg.Entries)
		msg.Truncated = true
	}
	raw, err := json.Marshal(msg)
	if err != nil || len(raw) <= probeVirtualRouterDebugLogPayloadSoftLimit {
		return
	}
	msg.Content = trimProbeVirtualRouterDebugLogStringSuffix(msg.Content, 16*1024)
	msg.Truncated = true
	raw, err = json.Marshal(msg)
	if err == nil && len(raw) <= probeVirtualRouterDebugLogPayloadSoftLimit {
		return
	}
	msg.Content = trimProbeVirtualRouterDebugLogStringSuffix(msg.Content, 4*1024)
}

func trimProbeVirtualRouterDebugLogStringSuffix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	raw := []byte(value)
	if len(raw) <= maxBytes {
		return value
	}
	start := len(raw) - maxBytes
	for start < len(raw) && raw[start]&0xc0 == 0x80 {
		start++
	}
	if start >= len(raw) {
		return ""
	}
	return string(raw[start:])
}
