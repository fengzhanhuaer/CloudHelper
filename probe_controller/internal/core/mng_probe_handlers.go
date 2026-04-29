package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type mngProbeShellSessionStartRequest struct {
	NodeID string `json:"node_id"`
}

type mngProbeShellSessionExecRequest struct {
	NodeID     string `json:"node_id"`
	SessionID  string `json:"session_id"`
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec"`
}

type mngProbeShellSessionStopRequest struct {
	NodeID    string `json:"node_id"`
	SessionID string `json:"session_id"`
}

type mngProbeShellShortcutUpsertRequest struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

type mngProbeShellShortcutDeleteRequest struct {
	Name string `json:"name"`
}

func mngProbePageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/probe" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngProbePageHTML))
}

func mngProbeNodesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if ProbeStore == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"nodes":         []probeNodeRecord{},
			"deleted_nodes": []probeNodeRecord{},
		})
		return
	}

	ProbeStore.mu.RLock()
	nodes := loadProbeNodesLocked()
	deletedNodes := loadDeletedProbeNodesLocked()
	ProbeStore.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"nodes":         attachProbeRuntimeToNodes(nodes),
		"deleted_nodes": deletedNodes,
	})
}

func mngProbeNodeCreateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req probeNodeCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	ProbeStore.mu.Lock()
	node, err := createProbeNodeLocked(req.NodeName)
	ProbeStore.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := ProbeStore.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist probe node"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"node": node})
}

func mngProbeNodeUpdateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req probeNodeUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	ProbeStore.mu.Lock()
	node, err := updateProbeNodeLocked(req)
	ProbeStore.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := ProbeStore.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist probe node"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"node": node})
}

func mngProbeNodeDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req probeNodeDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	ProbeStore.mu.Lock()
	node, nodes, deletedNodes, err := deleteProbeNodeLocked(req.NodeNo)
	ProbeStore.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := ProbeStore.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist deleted probe node"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":            true,
		"node":          node,
		"nodes":         nodes,
		"deleted_nodes": deletedNodes,
	})
}

func mngProbeNodeRestoreHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req probeNodeRestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	ProbeStore.mu.Lock()
	node, nodes, deletedNodes, err := restoreDeletedProbeNodeLocked(req.NodeNo)
	ProbeStore.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := ProbeStore.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist restored probe node"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":            true,
		"node":          node,
		"nodes":         nodes,
		"deleted_nodes": deletedNodes,
	})
}

func mngProbeStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	nodeID := normalizeProbeNodeID(r.URL.Query().Get("node_id"))
	if nodeID != "" {
		ProbeStore.mu.RLock()
		item, ok := loadProbeNodeStatusByIDLocked(nodeID)
		ProbeStore.mu.RUnlock()
		if !ok {
			writeJSON(w, http.StatusOK, map[string]interface{}{"items": []probeNodeStatusRecord{}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": []probeNodeStatusRecord{item}})
		return
	}

	ProbeStore.mu.RLock()
	items := loadProbeNodeStatusLocked()
	ProbeStore.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func mngProbeLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	nodeID := normalizeProbeNodeID(r.URL.Query().Get("node_id"))
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return
	}

	lines := normalizeAdminLogLines(r.URL.Query().Get("lines"))
	sinceMinutes := normalizeAdminSinceMinutes(r.URL.Query().Get("since_minutes"))
	minLevel := strings.TrimSpace(r.URL.Query().Get("min_level"))

	result, err := fetchProbeLogsFromNode(nodeID, lines, sinceMinutes, minLevel)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	nodeName := ""
	if node, ok := getProbeNodeByID(nodeID); ok {
		nodeName = strings.TrimSpace(node.NodeName)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"node_id":       nodeID,
		"node_name":     nodeName,
		"source":        strings.TrimSpace(result.Source),
		"file_path":     strings.TrimSpace(result.FilePath),
		"lines":         result.Lines,
		"since_minutes": result.SinceMinutes,
		"min_level":     strings.TrimSpace(result.MinLevel),
		"content":       result.Content,
		"entries":       result.Entries,
		"fetched":       time.Now().UTC().Format(time.RFC3339),
		"timestamp":     strings.TrimSpace(result.Timestamp),
	})
}

func mngProbeUpgradeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req probeUpgradeDispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	nodeID := normalizeProbeNodeID(req.NodeID)
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return
	}

	node, ok := getProbeNodeByID(nodeID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "probe node not found"})
		return
	}

	result, err := dispatchUpgradeToProbe(node, controllerBaseURLFromRequest(r))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func mngProbeUpgradeAllHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ProbeStore.mu.RLock()
	nodes := loadProbeNodesLocked()
	ProbeStore.mu.RUnlock()

	success := 0
	items := make([]probeUpgradeDispatchResult, 0, len(nodes))
	failures := make([]string, 0)
	controllerBaseURL := controllerBaseURLFromRequest(r)
	for _, node := range nodes {
		result, err := dispatchUpgradeToProbe(node, controllerBaseURL)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%d:%v", node.NodeNo, err))
			continue
		}
		items = append(items, result)
		success++
	}
	message := fmt.Sprintf("upgrade dispatch completed: success=%d total=%d failures=%d", success, len(nodes), len(failures))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  success,
		"total":    len(nodes),
		"failures": failures,
		"items":    items,
		"message":  message,
	})
}

func mngProbeShellSessionStartHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req mngProbeShellSessionStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	nodeID := normalizeProbeNodeID(req.NodeID)
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return
	}
	if _, ok := getProbeNodeByID(nodeID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "probe node not found"})
		return
	}

	result, err := dispatchProbeShellSessionControl(nodeID, "start", "", "", 0)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         result.OK,
		"node_id":    nodeID,
		"action":     "start",
		"session_id": strings.TrimSpace(result.SessionID),
		"message":    strings.TrimSpace(result.Message),
		"timestamp":  strings.TrimSpace(result.Timestamp),
	})
}

func mngProbeShellSessionExecHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req mngProbeShellSessionExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	nodeID := normalizeProbeNodeID(req.NodeID)
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return
	}
	if _, ok := getProbeNodeByID(nodeID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "probe node not found"})
		return
	}

	result, err := dispatchProbeShellSessionControl(nodeID, "exec", req.SessionID, req.Command, req.TimeoutSec)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":          false,
			"node_id":     nodeID,
			"action":      "exec",
			"session_id":  strings.TrimSpace(req.SessionID),
			"command":     strings.TrimSpace(req.Command),
			"stdout":      result.Stdout,
			"stderr":      result.Stderr,
			"error":       err.Error(),
			"started_at":  strings.TrimSpace(result.StartedAt),
			"finished_at": strings.TrimSpace(result.FinishedAt),
			"duration_ms": result.DurationMS,
			"timestamp":   strings.TrimSpace(result.Timestamp),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":          true,
		"node_id":     nodeID,
		"action":      "exec",
		"session_id":  strings.TrimSpace(result.SessionID),
		"command":     strings.TrimSpace(req.Command),
		"stdout":      result.Stdout,
		"stderr":      result.Stderr,
		"error":       strings.TrimSpace(result.Error),
		"started_at":  strings.TrimSpace(result.StartedAt),
		"finished_at": strings.TrimSpace(result.FinishedAt),
		"duration_ms": result.DurationMS,
		"timestamp":   strings.TrimSpace(result.Timestamp),
	})
}

func mngProbeShellSessionStopHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req mngProbeShellSessionStopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	nodeID := normalizeProbeNodeID(req.NodeID)
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return
	}
	if _, ok := getProbeNodeByID(nodeID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "probe node not found"})
		return
	}

	result, err := dispatchProbeShellSessionControl(nodeID, "stop", req.SessionID, "", 0)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         result.OK,
		"node_id":    nodeID,
		"action":     "stop",
		"session_id": strings.TrimSpace(req.SessionID),
		"message":    strings.TrimSpace(result.Message),
		"timestamp":  strings.TrimSpace(result.Timestamp),
	})
}

func mngProbeShellShortcutsGetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ProbeStore.mu.RLock()
	items := loadProbeShellShortcutsLocked()
	ProbeStore.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func mngProbeShellShortcutsUpsertHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req mngProbeShellShortcutUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	ProbeStore.mu.Lock()
	items, err := upsertProbeShellShortcutLocked(req.Name, req.Command)
	ProbeStore.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := ProbeStore.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist shell shortcuts"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func mngProbeShellShortcutsDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req mngProbeShellShortcutDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	ProbeStore.mu.Lock()
	items, err := removeProbeShellShortcutLocked(req.Name)
	ProbeStore.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := ProbeStore.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist shell shortcuts"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}
