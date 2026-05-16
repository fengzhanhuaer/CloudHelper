package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	probeProxyGroupBackupFileName        = "proxy_group.json"
	probeProxyGroupBackupReadBodyMaxLen  = 1024 * 1024
	probeProxyGroupBackupContentMaxBytes = 512 * 1024
	probeProxyGroupBackupGlobalKey       = "global"
)

type probeProxyGroupBackupRecord struct {
	NodeID        string `json:"node_id"`
	FileName      string `json:"file_name"`
	ContentBase64 string `json:"content_base64"`
	UpdatedAt     string `json:"updated_at"`
}

type probeProxyGroupBackupUploadRequest struct {
	NodeID        string `json:"node_id"`
	FileName      string `json:"file_name"`
	ContentBase64 string `json:"content_base64"`
}

func ProbeProxyGroupBackupHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		probeProxyGroupBackupUploadHandler(w, r)
	case http.MethodGet:
		probeProxyGroupBackupDownloadHandler(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func probeProxyGroupBackupUploadHandler(w http.ResponseWriter, r *http.Request) {
	nodeID, err := authenticateProbeRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	body := http.MaxBytesReader(w, r.Body, probeProxyGroupBackupReadBodyMaxLen)
	defer body.Close()

	var req probeProxyGroupBackupUploadRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	record, err := buildProbeProxyGroupBackupRecord(nodeID, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := saveProbeProxyGroupBackup(record); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist proxy group backup"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"node_id":    record.NodeID,
		"file_name":  record.FileName,
		"updated_at": record.UpdatedAt,
	})
}

func probeProxyGroupBackupDownloadHandler(w http.ResponseWriter, r *http.Request) {
	nodeID, err := authenticateProbeRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	record, ok := getProbeProxyGroupBackup(nodeID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "proxy group backup not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"node_id":        record.NodeID,
		"file_name":      record.FileName,
		"content_base64": record.ContentBase64,
		"updated_at":     record.UpdatedAt,
	})
}

func buildProbeProxyGroupBackupRecord(authNodeID string, req probeProxyGroupBackupUploadRequest) (probeProxyGroupBackupRecord, error) {
	nodeID := normalizeProbeNodeID(authNodeID)
	if nodeID == "" {
		return probeProxyGroupBackupRecord{}, fmt.Errorf("node_id is required")
	}
	if bodyNodeID := normalizeProbeNodeID(req.NodeID); bodyNodeID != "" && bodyNodeID != nodeID {
		return probeProxyGroupBackupRecord{}, fmt.Errorf("node_id does not match authenticated probe")
	}
	fileName := firstNonEmptyProbeProxyGroupBackup(strings.TrimSpace(req.FileName), probeProxyGroupBackupFileName)
	if fileName != probeProxyGroupBackupFileName {
		return probeProxyGroupBackupRecord{}, fmt.Errorf("file_name must be %s", probeProxyGroupBackupFileName)
	}
	contentBase64 := strings.TrimSpace(req.ContentBase64)
	if contentBase64 == "" {
		return probeProxyGroupBackupRecord{}, fmt.Errorf("content_base64 is required")
	}
	decoded, err := base64.StdEncoding.DecodeString(contentBase64)
	if err != nil {
		return probeProxyGroupBackupRecord{}, fmt.Errorf("content_base64 is invalid")
	}
	if len(decoded) == 0 {
		return probeProxyGroupBackupRecord{}, fmt.Errorf("backup content is empty")
	}
	if len(decoded) > probeProxyGroupBackupContentMaxBytes {
		return probeProxyGroupBackupRecord{}, fmt.Errorf("backup content exceeds %d bytes", probeProxyGroupBackupContentMaxBytes)
	}
	if !json.Valid(decoded) {
		return probeProxyGroupBackupRecord{}, fmt.Errorf("backup content must be json")
	}
	return probeProxyGroupBackupRecord{
		NodeID:        nodeID,
		FileName:      fileName,
		ContentBase64: base64.StdEncoding.EncodeToString(decoded),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func saveProbeProxyGroupBackup(record probeProxyGroupBackupRecord) error {
	if ProbeStore == nil {
		return fmt.Errorf("probe store is not initialized")
	}
	ProbeStore.mu.Lock()
	if ProbeStore.data.ProbeProxyGroupBackups == nil {
		ProbeStore.data.ProbeProxyGroupBackups = map[string]probeProxyGroupBackupRecord{}
	}
	ProbeStore.data.ProbeProxyGroupBackups[probeProxyGroupBackupGlobalKey] = record
	ProbeStore.mu.Unlock()
	return ProbeStore.Save()
}

func getProbeProxyGroupBackup(nodeID string) (probeProxyGroupBackupRecord, bool) {
	if ProbeStore == nil {
		return probeProxyGroupBackupRecord{}, false
	}
	ProbeStore.mu.RLock()
	record, ok := ProbeStore.data.ProbeProxyGroupBackups[probeProxyGroupBackupGlobalKey]
	ProbeStore.mu.RUnlock()
	if !ok || strings.TrimSpace(record.ContentBase64) == "" {
		return probeProxyGroupBackupRecord{}, false
	}
	if strings.TrimSpace(record.NodeID) == "" {
		record.NodeID = normalizeProbeNodeID(nodeID)
	}
	record.FileName = firstNonEmptyProbeProxyGroupBackup(strings.TrimSpace(record.FileName), probeProxyGroupBackupFileName)
	return record, true
}

func normalizeProbeProxyGroupBackups(items map[string]probeProxyGroupBackupRecord) map[string]probeProxyGroupBackupRecord {
	out := make(map[string]probeProxyGroupBackupRecord, 1)
	var selected probeProxyGroupBackupRecord
	var hasSelected bool
	var selectedAt time.Time
	for key, item := range items {
		nodeID := normalizeProbeNodeID(firstNonEmptyProbeProxyGroupBackup(item.NodeID, key))
		if nodeID == "" {
			continue
		}
		fileName := firstNonEmptyProbeProxyGroupBackup(strings.TrimSpace(item.FileName), probeProxyGroupBackupFileName)
		if fileName != probeProxyGroupBackupFileName {
			continue
		}
		contentBase64 := strings.TrimSpace(item.ContentBase64)
		decoded, err := base64.StdEncoding.DecodeString(contentBase64)
		if err != nil || len(decoded) == 0 || len(decoded) > probeProxyGroupBackupContentMaxBytes || !json.Valid(decoded) {
			continue
		}
		updatedAt := strings.TrimSpace(item.UpdatedAt)
		record := probeProxyGroupBackupRecord{
			NodeID:        nodeID,
			FileName:      fileName,
			ContentBase64: base64.StdEncoding.EncodeToString(decoded),
			UpdatedAt:     updatedAt,
		}
		if !hasSelected {
			selected = record
			hasSelected = true
			selectedAt = parseProbeProxyGroupBackupTime(updatedAt)
			continue
		}
		if currentAt := parseProbeProxyGroupBackupTime(updatedAt); currentAt.After(selectedAt) {
			selected = record
			selectedAt = currentAt
		}
	}
	if hasSelected {
		out[probeProxyGroupBackupGlobalKey] = selected
	}
	return out
}

func firstNonEmptyProbeProxyGroupBackup(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseProbeProxyGroupBackupTime(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err == nil {
		return parsed
	}
	parsed, err = time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err == nil {
		return parsed
	}
	return time.Time{}
}
