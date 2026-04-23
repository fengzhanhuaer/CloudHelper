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
	mngLinkDispatchAction(w, r, "admin.probe.link.users.get", nil)
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
	mngLinkDispatchAction(w, r, "admin.probe.link.user.public_key.get", payload)
}

func mngLinkChainsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mngLinkDispatchAction(w, r, "admin.probe.link.chains.get", nil)
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
	mngLinkDispatchAction(w, r, "admin.probe.link.chain.upsert", payload)
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
	mngLinkDispatchAction(w, r, "admin.probe.link.chain.delete", payload)
}

func mngLinkDispatchAction(w http.ResponseWriter, r *http.Request, action string, payload json.RawMessage) {
	result, err := handleAdminWSAction(action, payload, controllerBaseURLFromRequest(r))
	if err != nil {
		writeMngLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
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
