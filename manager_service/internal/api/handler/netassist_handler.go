package handler

import (
	"encoding/json"
	"net/http"

	"github.com/cloudhelper/manager_service/internal/adapter/netassist"
	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/gin-gonic/gin"
)

// NetAssistHandler proxies network-assistant requests to probe_manager.
// PKG-FIX-P1-01 / RQ-004
type NetAssistHandler struct {
	client *netassist.Client
}

func NewNetAssistHandler(client *netassist.Client) *NetAssistHandler {
	return &NetAssistHandler{client: client}
}

// GetStatus handles GET /api/network-assistant/status
func (h *NetAssistHandler) GetStatus(c *gin.Context) {
	rid := c.GetString("RequestID")
	// PKG-FIX-P1-01: pass the manager_service session token for upstream auth context.
	// The netassist adapter does not currently re-authenticate against probe_manager
	// (probe_manager uses a different auth model). We pass the token as a correlation header.
	token := extractToken(c)
	raw, err := h.client.GetStatus(c.Request.Context(), token)
	if err != nil {
		response.Internal(c, rid, "failed to fetch network assistant status: "+err.Error())
		return
	}
	var data interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		data = string(raw)
	}
	c.JSON(http.StatusOK, response.Envelope{Code: response.CodeOK, Message: "ok", Data: data, RequestID: rid})
}

// SwitchMode handles POST /api/network-assistant/mode
func (h *NetAssistHandler) SwitchMode(c *gin.Context) {
	rid := c.GetString("RequestID")
	var req struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Mode == "" {
		response.BadRequest(c, rid, "mode is required")
		return
	}
	token := extractToken(c)
	raw, err := h.client.SwitchMode(c.Request.Context(), token, req.Mode)
	if err != nil {
		response.Internal(c, rid, "failed to switch mode: "+err.Error())
		return
	}
	var data interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		data = string(raw)
	}
	c.JSON(http.StatusOK, response.Envelope{Code: response.CodeOK, Message: "ok", Data: data, RequestID: rid})
}

// extractToken pulls the Bearer token from the request (already validated by Auth middleware).
func extractToken(c *gin.Context) string {
	if t := c.GetHeader("X-Session-Token"); t != "" {
		return t
	}
	auth := c.GetHeader("Authorization")
	if len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}
	return ""
}
