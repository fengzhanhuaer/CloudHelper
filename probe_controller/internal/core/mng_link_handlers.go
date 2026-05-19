package core

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
)

func mngLinkPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/link" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngLinkPageHTML))
}

func mngLinkUsersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := getMngProbeLinkUsers()
	writeMngLinkResult(w, result, err)
}

func mngLinkUserPublicKeyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	username := strings.TrimSpace(r.URL.Query().Get("username"))
	payload := json.RawMessage(`{}`)
	if username != "" {
		raw, err := json.Marshal(map[string]string{"username": username})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to build request payload"})
			return
		}
		payload = raw
	}
	result, err := getMngProbeLinkUserPublicKey(payload)
	writeMngLinkResult(w, result, err)
}

func mngLinkChainsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := listMngProbeLinkChains()
	writeMngLinkResult(w, result, err)
}

func mngLinkNodeDomainsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodeID := normalizeProbeNodeID(r.URL.Query().Get("node_id"))
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return
	}
	domains := listProbeLinkNodeDomains(nodeID)
	sort.Strings(domains)
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id": nodeID,
		"domains": domains,
	})
}

func mngLinkRelayStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": listMngLinkRelayStatus(),
	})
}

type mngLinkRelayStatusView struct {
	NodeID        string                           `json:"node_id"`
	Online        bool                             `json:"online"`
	LastSeen      string                           `json:"last_seen,omitempty"`
	ChainID       string                           `json:"chain_id"`
	ChainName     string                           `json:"chain_name,omitempty"`
	ChainType     string                           `json:"chain_type,omitempty"`
	Role          string                           `json:"role,omitempty"`
	ListenHost    string                           `json:"listen_host,omitempty"`
	ListenPort    int                              `json:"listen_port,omitempty"`
	LinkLayer     string                           `json:"link_layer,omitempty"`
	NextHost      string                           `json:"next_host,omitempty"`
	NextPort      int                              `json:"next_port,omitempty"`
	NextLinkLayer string                           `json:"next_link_layer,omitempty"`
	PrevHost      string                           `json:"prev_host,omitempty"`
	PrevPort      int                              `json:"prev_port,omitempty"`
	PrevLinkLayer string                           `json:"prev_link_layer,omitempty"`
	ListenState   *probeRelayProtocolStateSnapshot `json:"listen_state,omitempty"`
	NextState     *probeRelayProtocolStateSnapshot `json:"next_state,omitempty"`
	PrevState     *probeRelayProtocolStateSnapshot `json:"prev_state,omitempty"`
	UpdatedAt     string                           `json:"updated_at,omitempty"`
}

func listMngLinkRelayStatus() []mngLinkRelayStatusView {
	runtimes := listProbeRuntimes()
	items := make([]mngLinkRelayStatusView, 0)
	for _, runtime := range runtimes {
		for _, status := range runtime.RelayStatus {
			chainID := strings.TrimSpace(status.ChainID)
			if chainID == "" {
				continue
			}
			items = append(items, mngLinkRelayStatusView{
				NodeID:        strings.TrimSpace(runtime.NodeID),
				Online:        runtime.Online,
				LastSeen:      strings.TrimSpace(runtime.LastSeen),
				ChainID:       chainID,
				ChainName:     strings.TrimSpace(status.ChainName),
				ChainType:     strings.TrimSpace(status.ChainType),
				Role:          strings.TrimSpace(status.Role),
				ListenHost:    strings.TrimSpace(status.ListenHost),
				ListenPort:    status.ListenPort,
				LinkLayer:     strings.TrimSpace(status.LinkLayer),
				NextHost:      strings.TrimSpace(status.NextHost),
				NextPort:      status.NextPort,
				NextLinkLayer: strings.TrimSpace(status.NextLinkLayer),
				PrevHost:      strings.TrimSpace(status.PrevHost),
				PrevPort:      status.PrevPort,
				PrevLinkLayer: strings.TrimSpace(status.PrevLinkLayer),
				ListenState:   status.ListenState,
				NextState:     status.NextState,
				PrevState:     status.PrevState,
				UpdatedAt:     strings.TrimSpace(status.UpdatedAt),
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].NodeID != items[j].NodeID {
			return items[i].NodeID < items[j].NodeID
		}
		if items[i].ChainID != items[j].ChainID {
			return items[i].ChainID < items[j].ChainID
		}
		return items[i].Role < items[j].Role
	})
	return items
}

func mngLinkChainUpsertHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload, err := readMngRawJSONPayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	result, err := upsertMngProbeLinkChain(payload, controllerBaseURLFromRequest(r))
	writeMngLinkResult(w, result, err)
}

func mngLinkChainDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload, err := readMngRawJSONPayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	result, err := deleteMngProbeLinkChain(payload)
	writeMngLinkResult(w, result, err)
}

func readMngRawJSONPayload(r *http.Request) (json.RawMessage, error) {
	if r == nil || r.Body == nil {
		return json.RawMessage(`{}`), nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(trimmed) {
		return nil, io.ErrUnexpectedEOF
	}
	out := make([]byte, len(trimmed))
	copy(out, trimmed)
	return json.RawMessage(out), nil
}

func writeMngLinkError(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unknown error"})
		return
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		msg = "unknown error"
	}
	lower := strings.ToLower(msg)
	status := http.StatusInternalServerError
	switch {
	case strings.Contains(lower, "invalid payload"),
		strings.Contains(lower, " is required"),
		strings.Contains(lower, " must be"),
		strings.Contains(lower, "exceeded limit"):
		status = http.StatusBadRequest
	case strings.Contains(lower, "not found"):
		status = http.StatusNotFound
	case strings.Contains(lower, "not initialized"):
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeMngLinkResult(w http.ResponseWriter, result map[string]interface{}, err error) {
	if err != nil {
		writeMngLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
