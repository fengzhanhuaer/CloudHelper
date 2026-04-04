package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const aiDebugListenAddr = "0.0.0.0:16031"

type aiDebugService struct {
	mu     sync.Mutex
	server *http.Server
	active bool
}

type aiDebugStateResponse struct {
	Service string `json:"service"`
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"`
}

type aiDebugLogsResponse struct {
	Kind         string         `json:"kind"`
	Target       string         `json:"target"`
	NodeID       string         `json:"node_id,omitempty"`
	NodeName     string         `json:"node_name,omitempty"`
	Source       string         `json:"source,omitempty"`
	FilePath     string         `json:"file_path,omitempty"`
	Lines        int            `json:"lines"`
	SinceMinutes int            `json:"since_minutes,omitempty"`
	MinLevel     string         `json:"min_level,omitempty"`
	Content      string         `json:"content"`
	Entries      []LogEntry     `json:"entries,omitempty"`
	FetchedAt    string         `json:"fetched_at"`
	Extra        map[string]any `json:"extra,omitempty"`
}

type aiDebugControllerLogsPayload struct {
	Source   string     `json:"source"`
	FilePath string     `json:"file_path"`
	Lines    int        `json:"lines"`
	Content  string     `json:"content"`
	Fetched  string     `json:"fetched_at"`
	Entries  []LogEntry `json:"entries,omitempty"`
}

type aiDebugProbeLogsPayload struct {
	NodeID       string     `json:"node_id"`
	NodeName     string     `json:"node_name"`
	Source       string     `json:"source"`
	FilePath     string     `json:"file_path"`
	Lines        int        `json:"lines"`
	SinceMinutes int        `json:"since_minutes"`
	MinLevel     string     `json:"min_level"`
	Content      string     `json:"content"`
	Entries      []LogEntry `json:"entries,omitempty"`
	Fetched      string     `json:"fetched"`
	Timestamp    string     `json:"timestamp"`
}

func newAIDebugService() *aiDebugService {
	return &aiDebugService{}
}

func (s *aiDebugService) ApplyFromConfig() error {
	config, _, err := loadManagerGlobalConfig()
	if err != nil {
		return err
	}
	if config.AIDebugListenEnabled {
		return s.Start()
	}
	return s.Stop()
}

func (s *aiDebugService) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return nil
	}

	listener, err := net.Listen("tcp", aiDebugListenAddr)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/debug/state", s.handleState)
	mux.HandleFunc("/debug/probe/nodes", s.handleProbeNodes)
	mux.HandleFunc("/debug/probe/chains", s.handleProbeChains)
	mux.HandleFunc("/debug/logs/manager", s.handleManagerLogs)
	mux.HandleFunc("/debug/logs/controller", s.handleControllerLogs)
	mux.HandleFunc("/debug/logs/probe", s.handleProbeLogs)

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.server = server
	s.active = true
	logManagerInfof("AI debug endpoint listening on %s", aiDebugListenAddr)

	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logManagerErrorf("AI debug endpoint serve failed: %v", err)
		}
		s.mu.Lock()
		if s.server == server {
			s.server = nil
			s.active = false
		}
		s.mu.Unlock()
	}()

	return nil
}

func (s *aiDebugService) Stop() error {
	s.mu.Lock()
	server := s.server
	if server == nil {
		s.active = false
		s.mu.Unlock()
		return nil
	}
	s.server = nil
	s.active = false
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logManagerInfof("AI debug endpoint stopped")
	return server.Shutdown(ctx)
}

func (s *aiDebugService) handleRoot(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, aiDebugStateResponse{
		Service: "probe_manager_ai_debug",
		Enabled: true,
		Listen:  aiDebugListenAddr,
	})
}

func (s *aiDebugService) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (s *aiDebugService) handleState(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"service": "probe_manager_ai_debug",
		"enabled": true,
		"listen":  aiDebugListenAddr,
		"time":    time.Now().Format(time.RFC3339),
	})
}

func (s *aiDebugService) handleProbeNodes(w http.ResponseWriter, r *http.Request) {
	app := NewApp()
	nodes, err := app.GetProbeNodes()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"items": nodes,
		"count": len(nodes),
	})
}

func (s *aiDebugService) handleProbeChains(w http.ResponseWriter, r *http.Request) {
	app := NewApp()
	items, err := app.GetProbeLinkChainsCache()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"count": len(items),
	})
}

func (s *aiDebugService) handleManagerLogs(w http.ResponseWriter, r *http.Request) {
	lines := normalizeLogViewLines(s.parseIntQuery(r, "lines", defaultLogViewLines))
	sinceMinutes := s.parseIntQuery(r, "since_minutes", 0)
	minLevel := strings.TrimSpace(r.URL.Query().Get("min_level"))
	app := NewApp()
	resp, err := app.GetLocalManagerLogs(lines, sinceMinutes, minLevel)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, aiDebugLogsResponse{
		Kind:         "manager_logs",
		Target:       "manager",
		Source:       resp.Source,
		FilePath:     resp.FilePath,
		Lines:        resp.Lines,
		SinceMinutes: sinceMinutes,
		MinLevel:     minLevel,
		Content:      resp.Content,
		Entries:      resp.Entries,
		FetchedAt:    resp.Fetched,
	})
}

func (s *aiDebugService) handleControllerLogs(w http.ResponseWriter, r *http.Request) {
	lines := normalizeLogViewLines(s.parseIntQuery(r, "lines", defaultLogViewLines))
	sinceMinutes := s.parseIntQuery(r, "since_minutes", 0)
	minLevel := strings.TrimSpace(r.URL.Query().Get("min_level"))
	payload, err := fetchControllerLogsForDebug(lines, sinceMinutes, minLevel)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, aiDebugLogsResponse{
		Kind:         "controller_logs",
		Target:       "controller",
		Source:       payload.Source,
		FilePath:     payload.FilePath,
		Lines:        payload.Lines,
		SinceMinutes: sinceMinutes,
		MinLevel:     minLevel,
		Content:      payload.Content,
		Entries:      payload.Entries,
		FetchedAt:    firstNonEmpty(payload.Fetched, time.Now().Format(time.RFC3339)),
	})
}

func (s *aiDebugService) handleProbeLogs(w http.ResponseWriter, r *http.Request) {
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	if nodeID == "" {
		s.writeErrorMessage(w, http.StatusBadRequest, "node_id is required")
		return
	}
	lines := normalizeLogViewLines(s.parseIntQuery(r, "lines", defaultLogViewLines))
	sinceMinutes := s.parseIntQuery(r, "since_minutes", 0)
	minLevel := strings.TrimSpace(r.URL.Query().Get("min_level"))
	payload, err := fetchProbeLogsForDebug(nodeID, lines, sinceMinutes, minLevel)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, aiDebugLogsResponse{
		Kind:         "probe_logs",
		Target:       "probe",
		NodeID:       payload.NodeID,
		NodeName:     payload.NodeName,
		Source:       payload.Source,
		FilePath:     payload.FilePath,
		Lines:        payload.Lines,
		SinceMinutes: payload.SinceMinutes,
		MinLevel:     payload.MinLevel,
		Content:      payload.Content,
		Entries:      payload.Entries,
		FetchedAt:    firstNonEmpty(payload.Fetched, payload.Timestamp, time.Now().Format(time.RFC3339)),
		Extra: map[string]any{
			"timestamp": payload.Timestamp,
		},
	})
}

func (s *aiDebugService) parseIntQuery(r *http.Request, key string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if key == "lines" {
		if value <= 0 {
			return fallback
		}
		return value
	}
	if value < 0 {
		return fallback
	}
	return value
}

func (s *aiDebugService) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logManagerErrorf("AI debug endpoint write JSON failed: %v", err)
	}
}

func (s *aiDebugService) writeError(w http.ResponseWriter, status int, err error) {
	s.writeErrorMessage(w, status, err.Error())
}

func (s *aiDebugService) writeErrorMessage(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, map[string]any{
		"error":   strings.TrimSpace(message),
		"status":  status,
		"service": "probe_manager_ai_debug",
	})
}

func fetchControllerLogsForDebug(lines int, sinceMinutes int, minLevel string) (aiDebugControllerLogsPayload, error) {
	token, baseURL, err := loadManagerDebugControllerAuth()
	if err != nil {
		return aiDebugControllerLogsPayload{}, err
	}
	payload, err := callAdminWSForDebug[aiDebugControllerLogsPayload](baseURL, token, "admin.logs", map[string]any{
		"lines":         lines,
		"since_minutes": sinceMinutes,
		"min_level":     normalizeLogLevel(string(normalizeLogLevel(minLevel))),
	})
	if err != nil {
		return aiDebugControllerLogsPayload{}, err
	}
	payload.Source = firstNonEmpty(payload.Source, "server")
	payload.Lines = normalizeLogViewLines(payload.Lines)
	return payload, nil
}

func fetchProbeLogsForDebug(nodeID string, lines int, sinceMinutes int, minLevel string) (aiDebugProbeLogsPayload, error) {
	token, baseURL, err := loadManagerDebugControllerAuth()
	if err != nil {
		return aiDebugProbeLogsPayload{}, err
	}
	payload, err := callAdminWSForDebug[aiDebugProbeLogsPayload](baseURL, token, "admin.probe.logs.get", map[string]any{
		"node_id":       strings.TrimSpace(nodeID),
		"lines":         lines,
		"since_minutes": sinceMinutes,
		"min_level":     normalizeLogLevel(string(normalizeLogLevel(minLevel))),
	})
	if err != nil {
		return aiDebugProbeLogsPayload{}, err
	}
	payload.NodeID = firstNonEmpty(strings.TrimSpace(payload.NodeID), strings.TrimSpace(nodeID))
	payload.NodeName = resolveDebugProbeNodeName(payload.NodeID, payload.NodeName)
	payload.Source = firstNonEmpty(payload.Source, "server")
	payload.Lines = normalizeLogViewLines(payload.Lines)
	payload.SinceMinutes = maxInt(payload.SinceMinutes, sinceMinutes)
	payload.MinLevel = firstNonEmpty(payload.MinLevel, string(normalizeLogLevel(minLevel)))
	return payload, nil
}

func loadManagerDebugControllerAuth() (string, string, error) {
	config, _, err := loadManagerGlobalConfig()
	if err != nil {
		return "", "", err
	}
	baseURL := strings.TrimSpace(config.ControllerURL)
	if baseURL == "" {
		baseURL = defaultControllerURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	token, err := loginControllerForBackupUpload(ctx, baseURL)
	if err != nil {
		return "", "", err
	}
	return token, baseURL, nil
}

func callAdminWSForDebug[T any](baseURL string, token string, action string, payload any) (T, error) {
	var zero T
	wsURL, err := buildAdminWSURL(baseURL)
	if err != nil {
		return zero, err
	}
	dialer := buildControllerWSDialer(baseURL)
	headers := http.Header{}
	headers.Set("X-Forwarded-Proto", "https")
	conn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return zero, fmt.Errorf("admin ws handshake failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return zero, err
	}
	defer conn.Close()

	deadline := time.Now().Add(30 * time.Second)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return zero, err
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return zero, err
	}

	authID := fmt.Sprintf("ai-debug-auth-%d", time.Now().UnixNano())
	authReq := adminWSRequest{ID: authID, Action: "auth.session", Payload: map[string]string{"token": strings.TrimSpace(token)}}
	if err := conn.WriteJSON(authReq); err != nil {
		return zero, err
	}
	authResp, err := readAdminWSResponseByID(conn, authID)
	if err != nil {
		return zero, err
	}
	if !authResp.OK {
		return zero, fmt.Errorf("admin ws auth failed: %s", strings.TrimSpace(authResp.Error))
	}

	queryID := fmt.Sprintf("ai-debug-query-%d", time.Now().UnixNano())
	queryReq := adminWSRequest{ID: queryID, Action: action, Payload: payload}
	if err := conn.WriteJSON(queryReq); err != nil {
		return zero, err
	}
	queryResp, err := readAdminWSResponseByID(conn, queryID)
	if err != nil {
		return zero, err
	}
	if !queryResp.OK {
		return zero, fmt.Errorf("%s failed: %s", strings.TrimSpace(action), strings.TrimSpace(queryResp.Error))
	}
	if len(queryResp.Data) == 0 {
		return zero, nil
	}
	var out T
	if err := json.Unmarshal(queryResp.Data, &out); err != nil {
		return zero, err
	}
	return out, nil
}

func resolveDebugProbeNodeName(nodeID string, fallback string) string {
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	app := NewApp()
	nodes, err := app.GetProbeNodes()
	if err != nil {
		return ""
	}
	cleanNodeID := strings.TrimSpace(nodeID)
	for _, item := range nodes {
		if strconv.Itoa(item.NodeNo) == cleanNodeID {
			return strings.TrimSpace(item.NodeName)
		}
		if strings.TrimSpace(item.NodeName) == cleanNodeID {
			return strings.TrimSpace(item.NodeName)
		}
		if strings.TrimSpace(item.NodeSecret) == cleanNodeID {
			return strings.TrimSpace(item.NodeName)
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func maxInt(values ...int) int {
	maxValue := 0
	for _, value := range values {
		if value > maxValue {
			maxValue = value
		}
	}
	return maxValue
}
