package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	probeInfoBoxMaxItems        = 200
	probeInfoBoxMaxMessageRunes = 4000
)

var probeInfoBoxStore = struct {
	mu   sync.Mutex
	path string
}{path: filepath.Join(".", "temp", "probe_info_box.json")}

var probeInfoBoxChangeHub = struct {
	mu          sync.Mutex
	nextID      uint64
	last        probeInfoBoxChangedCommand
	subscribers map[uint64]chan probeInfoBoxChangedCommand
}{subscribers: make(map[uint64]chan probeInfoBoxChangedCommand)}

type probeInfoBoxItem struct {
	ID        string `json:"id"`
	NodeID    string `json:"node_id"`
	NodeName  string `json:"node_name,omitempty"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

type probeInfoBoxFile struct {
	Version   int                `json:"version"`
	UpdatedAt string             `json:"updated_at,omitempty"`
	Items     []probeInfoBoxItem `json:"items"`
}

type probeInfoBoxChangedCommand struct {
	Type      string `json:"type"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func ProbeInfoBoxHandler(w http.ResponseWriter, r *http.Request) {
	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}
	nodeID, err := authenticateProbeRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	switch r.Method {
	case http.MethodGet:
		view, err := getProbeInfoBox()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, view)
	case http.MethodPost:
		var request struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
		view, err := appendProbeInfoBoxItem(nodeID, request.Message)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, view)
	case http.MethodDelete:
		view, err := clearProbeInfoBox()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, view)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func getProbeInfoBox() (probeInfoBoxFile, error) {
	probeInfoBoxStore.mu.Lock()
	defer probeInfoBoxStore.mu.Unlock()
	return loadProbeInfoBoxLocked()
}

func appendProbeInfoBoxItem(nodeID string, message string) (probeInfoBoxFile, error) {
	cleanMessage, err := normalizeProbeInfoBoxMessage(message)
	if err != nil {
		return probeInfoBoxFile{}, err
	}
	cleanNodeID := normalizeProbeNodeID(nodeID)
	if cleanNodeID == "" {
		return probeInfoBoxFile{}, fmt.Errorf("node id is required")
	}

	probeInfoBoxStore.mu.Lock()
	view, err := loadProbeInfoBoxLocked()
	if err != nil {
		probeInfoBoxStore.mu.Unlock()
		return probeInfoBoxFile{}, err
	}
	now := time.Now().UTC()
	view.Items = append(view.Items, probeInfoBoxItem{
		ID:        fmt.Sprintf("info-%d", now.UnixNano()),
		NodeID:    cleanNodeID,
		NodeName:  probeInfoBoxNodeName(cleanNodeID),
		Message:   cleanMessage,
		CreatedAt: now.Format(time.RFC3339Nano),
	})
	view.UpdatedAt = now.Format(time.RFC3339Nano)
	view = normalizeProbeInfoBoxFile(view)
	if err := saveProbeInfoBoxLocked(view); err != nil {
		probeInfoBoxStore.mu.Unlock()
		return probeInfoBoxFile{}, err
	}
	probeInfoBoxStore.mu.Unlock()
	broadcastProbeInfoBoxChanged(view.UpdatedAt)
	return view, nil
}

func clearProbeInfoBox() (probeInfoBoxFile, error) {
	probeInfoBoxStore.mu.Lock()
	view := normalizeProbeInfoBoxFile(probeInfoBoxFile{
		Version:   1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Items:     []probeInfoBoxItem{},
	})
	if err := saveProbeInfoBoxLocked(view); err != nil {
		probeInfoBoxStore.mu.Unlock()
		return probeInfoBoxFile{}, err
	}
	probeInfoBoxStore.mu.Unlock()
	broadcastProbeInfoBoxChanged(view.UpdatedAt)
	return view, nil
}

func broadcastProbeInfoBoxChanged(updatedAt string) {
	command := probeInfoBoxChangedCommand{
		Type:      "info_box_changed",
		UpdatedAt: strings.TrimSpace(updatedAt),
	}
	publishControllerProbeInfoBoxChanged(command)
	probeSessions.mu.RLock()
	sessions := make([]*probeSession, 0, len(probeSessions.data))
	for _, session := range probeSessions.data {
		sessions = append(sessions, session)
	}
	probeSessions.mu.RUnlock()
	for _, session := range sessions {
		go func(target *probeSession) {
			_ = target.writeJSON(command)
		}(session)
	}
}

func publishControllerProbeInfoBoxChanged(command probeInfoBoxChangedCommand) {
	probeInfoBoxChangeHub.mu.Lock()
	probeInfoBoxChangeHub.last = command
	for _, subscriber := range probeInfoBoxChangeHub.subscribers {
		select {
		case subscriber <- command:
		default:
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- command:
			default:
			}
		}
	}
	probeInfoBoxChangeHub.mu.Unlock()
}

func subscribeControllerProbeInfoBoxChanges() (<-chan probeInfoBoxChangedCommand, func()) {
	probeInfoBoxChangeHub.mu.Lock()
	probeInfoBoxChangeHub.nextID++
	id := probeInfoBoxChangeHub.nextID
	changes := make(chan probeInfoBoxChangedCommand, 1)
	probeInfoBoxChangeHub.subscribers[id] = changes
	last := probeInfoBoxChangeHub.last
	probeInfoBoxChangeHub.mu.Unlock()
	if strings.TrimSpace(last.Type) == "" {
		if view, err := getProbeInfoBox(); err == nil {
			last = probeInfoBoxChangedCommand{Type: "info_box_changed", UpdatedAt: strings.TrimSpace(view.UpdatedAt)}
		}
	}
	if strings.TrimSpace(last.Type) != "" {
		select {
		case changes <- last:
		default:
		}
	}
	return changes, func() {
		probeInfoBoxChangeHub.mu.Lock()
		delete(probeInfoBoxChangeHub.subscribers, id)
		probeInfoBoxChangeHub.mu.Unlock()
	}
}

func serveMngProbeInfoBoxEvents(w http.ResponseWriter, r *http.Request, expiresAt time.Time) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()

	changes, unsubscribe := subscribeControllerProbeInfoBoxChanges()
	defer unsubscribe()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case command := <-changes:
			raw, err := json.Marshal(command)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: info_box_changed\ndata: %s\n\n", raw); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if !expiresAt.IsZero() && time.Now().After(expiresAt) {
				return
			}
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func dispatchCurrentProbeInfoBoxRevision(session *probeSession) {
	if session == nil {
		return
	}
	view, err := getProbeInfoBox()
	if err != nil {
		return
	}
	_ = session.writeJSON(probeInfoBoxChangedCommand{
		Type:      "info_box_changed",
		UpdatedAt: strings.TrimSpace(view.UpdatedAt),
	})
}

func loadProbeInfoBoxLocked() (probeInfoBoxFile, error) {
	raw, err := os.ReadFile(probeInfoBoxStore.path)
	if os.IsNotExist(err) {
		return normalizeProbeInfoBoxFile(probeInfoBoxFile{Version: 1, Items: []probeInfoBoxItem{}}), nil
	}
	if err != nil {
		return probeInfoBoxFile{}, err
	}
	var view probeInfoBoxFile
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &view); err != nil {
			return probeInfoBoxFile{}, err
		}
	}
	return normalizeProbeInfoBoxFile(view), nil
}

func saveProbeInfoBoxLocked(view probeInfoBoxFile) error {
	view = normalizeProbeInfoBoxFile(view)
	if err := os.MkdirAll(filepath.Dir(probeInfoBoxStore.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(probeInfoBoxStore.path, raw, 0o600)
}

func normalizeProbeInfoBoxFile(view probeInfoBoxFile) probeInfoBoxFile {
	view.Version = 1
	items := make([]probeInfoBoxItem, 0, len(view.Items))
	for _, item := range view.Items {
		message, err := normalizeProbeInfoBoxMessage(item.Message)
		if err != nil {
			continue
		}
		item.ID = strings.TrimSpace(item.ID)
		item.NodeID = normalizeProbeNodeID(item.NodeID)
		item.NodeName = strings.TrimSpace(item.NodeName)
		item.Message = message
		item.CreatedAt = strings.TrimSpace(item.CreatedAt)
		if item.ID == "" || item.NodeID == "" || item.CreatedAt == "" {
			continue
		}
		items = append(items, item)
	}
	if len(items) > probeInfoBoxMaxItems {
		items = items[len(items)-probeInfoBoxMaxItems:]
	}
	view.Items = items
	view.UpdatedAt = strings.TrimSpace(view.UpdatedAt)
	if view.UpdatedAt == "" {
		view.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return view
}

func normalizeProbeInfoBoxMessage(message string) (string, error) {
	clean := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(message, "\r\n", "\n"), "\r", "\n"))
	if clean == "" {
		return "", fmt.Errorf("message is required")
	}
	if len([]rune(clean)) > probeInfoBoxMaxMessageRunes {
		return "", fmt.Errorf("message is too long")
	}
	return clean, nil
}

func probeInfoBoxNodeName(nodeID string) string {
	if ProbeStore == nil {
		return ""
	}
	ProbeStore.mu.RLock()
	_, name := nodeNameByNodeIDLocked(nodeID)
	ProbeStore.mu.RUnlock()
	return strings.TrimSpace(name)
}

func setProbeInfoBoxStorePathForTest(path string) func() {
	probeInfoBoxStore.mu.Lock()
	oldPath := probeInfoBoxStore.path
	probeInfoBoxStore.path = path
	probeInfoBoxStore.mu.Unlock()
	return func() {
		probeInfoBoxStore.mu.Lock()
		probeInfoBoxStore.path = oldPath
		probeInfoBoxStore.mu.Unlock()
	}
}
