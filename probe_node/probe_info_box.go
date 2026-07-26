package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	probeInfoBoxAPIPath        = "/api/probe/info_box"
	probeInfoBoxRequestTimeout = 15 * time.Second
)

type probeInfoBoxItem struct {
	ID        string `json:"id"`
	NodeID    string `json:"node_id"`
	NodeName  string `json:"node_name,omitempty"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

type probeInfoBoxPayload struct {
	Version   int                `json:"version"`
	UpdatedAt string             `json:"updated_at,omitempty"`
	Items     []probeInfoBoxItem `json:"items"`
}

type probeInfoBoxChangeEvent struct {
	UpdatedAt string `json:"updated_at,omitempty"`
}

var probeInfoBoxChangeHub = struct {
	mu          sync.Mutex
	updatedAt   string
	nextID      uint64
	subscribers map[uint64]chan probeInfoBoxChangeEvent
}{subscribers: make(map[uint64]chan probeInfoBoxChangeEvent)}

var probeLocalRequestInfoBox = requestProbeInfoBoxController

func publishProbeInfoBoxChanged(updatedAt string) {
	event := probeInfoBoxChangeEvent{UpdatedAt: strings.TrimSpace(updatedAt)}
	probeInfoBoxChangeHub.mu.Lock()
	if event.UpdatedAt != "" && event.UpdatedAt == probeInfoBoxChangeHub.updatedAt {
		probeInfoBoxChangeHub.mu.Unlock()
		return
	}
	probeInfoBoxChangeHub.updatedAt = event.UpdatedAt
	for _, subscriber := range probeInfoBoxChangeHub.subscribers {
		select {
		case subscriber <- event:
		default:
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- event:
			default:
			}
		}
	}
	probeInfoBoxChangeHub.mu.Unlock()
}

func subscribeProbeInfoBoxChanges() (<-chan probeInfoBoxChangeEvent, func()) {
	probeInfoBoxChangeHub.mu.Lock()
	probeInfoBoxChangeHub.nextID++
	id := probeInfoBoxChangeHub.nextID
	changes := make(chan probeInfoBoxChangeEvent, 1)
	probeInfoBoxChangeHub.subscribers[id] = changes
	lastUpdatedAt := probeInfoBoxChangeHub.updatedAt
	probeInfoBoxChangeHub.mu.Unlock()
	if lastUpdatedAt != "" {
		select {
		case changes <- probeInfoBoxChangeEvent{UpdatedAt: lastUpdatedAt}:
		default:
		}
	}
	return changes, func() {
		probeInfoBoxChangeHub.mu.Lock()
		delete(probeInfoBoxChangeHub.subscribers, id)
		probeInfoBoxChangeHub.mu.Unlock()
	}
}

func probeLocalInfoBoxEventsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if isProbeLocalConsoleTrusted(r.Context()) {
		http.Error(w, "streaming is not supported through the console bridge", http.StatusConflict)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
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

	changes, unsubscribe := subscribeProbeInfoBoxChanges()
	defer unsubscribe()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-changes:
			raw, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: info_box_changed\ndata: %s\n\n", raw); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, _, err := currentProbeLocalSessionFromRequest(r); err != nil {
				return
			}
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func requestProbeInfoBoxController(ctx context.Context, runtime probeLocalRouteRuntimeContext, method string, message string) (probeInfoBoxPayload, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(runtime.ControllerBaseURL), "/")
	if baseURL == "" {
		return probeInfoBoxPayload{}, errors.New("controller base url is not configured")
	}
	if strings.TrimSpace(runtime.Identity.NodeID) == "" || strings.TrimSpace(runtime.Identity.Secret) == "" {
		return probeInfoBoxPayload{}, errors.New("node identity is not configured")
	}

	var body io.Reader
	if method == http.MethodPost {
		raw, err := json.Marshal(map[string]string{"message": message})
		if err != nil {
			return probeInfoBoxPayload{}, err
		}
		body = bytes.NewReader(raw)
	}
	endpoint := baseURL + probeInfoBoxAPIPath
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return probeInfoBoxPayload{}, err
	}
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := applyProbeAuthHeaders(req, runtime.Identity); err != nil {
		return probeInfoBoxPayload{}, err
	}
	client, closeClient, err := newProbeResolvedHTTPClientForURL(endpoint, probeInfoBoxRequestTimeout)
	if err != nil {
		return probeInfoBoxPayload{}, err
	}
	defer closeClient()
	resp, err := client.Do(req)
	if err != nil {
		return probeInfoBoxPayload{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var payload map[string]string
		_ = json.Unmarshal(raw, &payload)
		if text := strings.TrimSpace(payload["error"]); text != "" {
			return probeInfoBoxPayload{}, errors.New(text)
		}
		return probeInfoBoxPayload{}, fmt.Errorf("info box request failed: status=%d", resp.StatusCode)
	}
	var payload probeInfoBoxPayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return probeInfoBoxPayload{}, err
	}
	if payload.Items == nil {
		payload.Items = []probeInfoBoxItem{}
	}
	return payload, nil
}
