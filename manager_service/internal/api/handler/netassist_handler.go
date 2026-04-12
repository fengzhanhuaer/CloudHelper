package handler

import (
	"encoding/json"
	"net/http"

	"github.com/cloudhelper/manager_service/internal/adapter/netassist"
	"github.com/cloudhelper/manager_service/internal/api/response"
)

type NetAssistHandler struct {
	client *netassist.Client
}

func NewNetAssistHandler(client *netassist.Client) *NetAssistHandler {
	return &NetAssistHandler{client: client}
}

// GetStatus handles GET /api/network-assistant/status
func (h *NetAssistHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	ctx := r.Context()
	
	// Delegate to the underlying `probe_manager` using internal API
	raw, err := h.client.GetStatus(ctx, "")
	if err != nil {
		response.Internal(w, rid, "failed to fetch net assist status: "+err.Error())
		return
	}
	// `raw` here is a JSON blob from probe_manager; return it as raw data.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	// Create envelop
	envelope := map[string]interface{}{
		"code":       0,
		"message":    "success",
		"request_id": rid,
	}
	var data interface{}
	_ = json.Unmarshal(raw, &data)
	if data != nil {
		// handle probe_manager response wrapping
		envelope["data"] = data
	} else {
		envelope["data"] = json.RawMessage(raw)
	}

	json.NewEncoder(w).Encode(envelope)
}

// SwitchMode handles POST /api/network-assistant/mode
func (h *NetAssistHandler) SwitchMode(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	ctx := r.Context()

	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, rid, "invalid request body")
		return
	}

	raw, err := h.client.SwitchMode(ctx, "", req.Mode)
	if err != nil {
		response.Internal(w, rid, "failed to switch mode: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	envelope := map[string]interface{}{
		"code":       0,
		"message":    "success",
		"request_id": rid,
	}
	var data interface{}
	_ = json.Unmarshal(raw, &data)
	if data != nil {
		envelope["data"] = data
	} else {
		envelope["data"] = json.RawMessage(raw)
	}
	json.NewEncoder(w).Encode(envelope)
}
