package backend

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

type aiDebugTCPConnectionItemPayload struct {
	ID          string `json:"id"`
	Target      string `json:"target,omitempty"`
	RouteTarget string `json:"route_target,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	Group       string `json:"group,omitempty"`
	Direct      bool   `json:"direct"`
	Transport   string `json:"transport,omitempty"`
	OpenedAt    string `json:"opened_at,omitempty"`
	LastActive  string `json:"last_active,omitempty"`
	AgeMS       int64  `json:"age_ms"`
	IdleMS      int64  `json:"idle_ms"`
	BytesUp     int64  `json:"bytes_up,omitempty"`
	BytesDown   int64  `json:"bytes_down,omitempty"`
}

type aiDebugTCPFailureItemPayload struct {
	Kind        string `json:"kind"`
	Reason      string `json:"reason,omitempty"`
	Target      string `json:"target,omitempty"`
	RouteTarget string `json:"route_target,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	Group       string `json:"group,omitempty"`
	Direct      bool   `json:"direct"`
	Transport   string `json:"transport,omitempty"`
	Error       string `json:"error,omitempty"`
	LastSeen    string `json:"last_seen,omitempty"`
}

type aiDebugTCPDebugPayload struct {
	Kind         string                            `json:"kind"`
	Scope        string                            `json:"scope"`
	NodeID       string                            `json:"node_id,omitempty"`
	ActiveCount  int                               `json:"active_count"`
	Active       []aiDebugTCPConnectionItemPayload `json:"active"`
	FailureCount int                               `json:"failure_count"`
	Failures     []aiDebugTCPFailureItemPayload    `json:"failures"`
	FetchedAt    string                            `json:"fetched_at"`
}

func (s *aiDebugService) handleNetworkTCPDebugManager(w http.ResponseWriter, r *http.Request) {
	service, err := aiDebugActiveNetworkAssistant()
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	payload, err := buildAIDebugManagerTCPPayload(service)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, payload)
}

func (s *aiDebugService) handleNetworkTCPDebugController(w http.ResponseWriter, r *http.Request) {
	payload, err := fetchControllerTCPDebugForDebug()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, payload)
}

func (s *aiDebugService) handleNetworkTCPDebugProbe(w http.ResponseWriter, r *http.Request) {
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	if nodeID == "" {
		s.writeErrorMessage(w, http.StatusBadRequest, "node_id is required")
		return
	}
	payload, err := fetchProbeTCPDebugForDebug(nodeID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, payload)
}

func buildAIDebugManagerTCPPayload(service *networkAssistantService) (aiDebugTCPDebugPayload, error) {
	if service == nil {
		return aiDebugTCPDebugPayload{}, errors.New("network assistant service is not initialized")
	}
	service.mu.RLock()
	stack := service.tunPacketStack
	service.mu.RUnlock()
	netstack, _ := stack.(*localTUNNetstack)
	if netstack == nil || netstack.tcpDebug == nil {
		return aiDebugTCPDebugPayload{
			Kind:      "network_tcp_debug",
			Scope:     "manager",
			Active:    []aiDebugTCPConnectionItemPayload{},
			Failures:  []aiDebugTCPFailureItemPayload{},
			FetchedAt: time.Now().Format(time.RFC3339),
		}, nil
	}
	return netstack.tcpDebug.snapshotPayload("manager", ""), nil
}

func fetchControllerTCPDebugForDebug() (aiDebugTCPDebugPayload, error) {
	token, baseURL, err := loadManagerDebugControllerAuth()
	if err != nil {
		return aiDebugTCPDebugPayload{}, err
	}
	payload, err := callAdminWSForDebug[aiDebugTCPDebugPayload](baseURL, token, "admin.tunnel.tcp.debug", map[string]any{})
	if err != nil {
		return aiDebugTCPDebugPayload{}, err
	}
	payload.Kind = firstNonEmpty(strings.TrimSpace(payload.Kind), "network_tcp_debug")
	payload.Scope = firstNonEmpty(strings.TrimSpace(payload.Scope), "controller")
	payload.FetchedAt = firstNonEmpty(strings.TrimSpace(payload.FetchedAt), time.Now().Format(time.RFC3339))
	if payload.Active == nil {
		payload.Active = []aiDebugTCPConnectionItemPayload{}
	}
	if payload.Failures == nil {
		payload.Failures = []aiDebugTCPFailureItemPayload{}
	}
	payload.ActiveCount = len(payload.Active)
	payload.FailureCount = len(payload.Failures)
	return payload, nil
}

func fetchProbeTCPDebugForDebug(nodeID string) (aiDebugTCPDebugPayload, error) {
	token, baseURL, err := loadManagerDebugControllerAuth()
	if err != nil {
		return aiDebugTCPDebugPayload{}, err
	}
	payload, err := callAdminWSForDebug[aiDebugTCPDebugPayload](baseURL, token, "admin.probe.tcp.debug.get", map[string]any{
		"node_id": strings.TrimSpace(nodeID),
	})
	if err != nil {
		return aiDebugTCPDebugPayload{}, err
	}
	payload.Kind = firstNonEmpty(strings.TrimSpace(payload.Kind), "network_tcp_debug")
	payload.Scope = firstNonEmpty(strings.TrimSpace(payload.Scope), "probe")
	payload.NodeID = firstNonEmpty(strings.TrimSpace(payload.NodeID), strings.TrimSpace(nodeID))
	payload.FetchedAt = firstNonEmpty(strings.TrimSpace(payload.FetchedAt), time.Now().Format(time.RFC3339))
	if payload.Active == nil {
		payload.Active = []aiDebugTCPConnectionItemPayload{}
	}
	if payload.Failures == nil {
		payload.Failures = []aiDebugTCPFailureItemPayload{}
	}
	payload.ActiveCount = len(payload.Active)
	payload.FailureCount = len(payload.Failures)
	return payload, nil
}
