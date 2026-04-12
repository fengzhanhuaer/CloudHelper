package handler

import (
	"net/http"
	"strconv"

	"github.com/cloudhelper/manager_service/internal/adapter/netassist"
	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/gin-gonic/gin"
)

// NetAssistHandler proxies network-assistant requests to probe_manager.
// PKG-FIX-P1-01 / R3-BE / RQ-004
type NetAssistHandler struct {
	client *netassist.Client
}

func NewNetAssistHandler(client *netassist.Client) *NetAssistHandler {
	return &NetAssistHandler{client: client}
}

// ─── Generic proxy helper ─────────────────────────────────────────────────────

func (h *NetAssistHandler) proxyRaw(c *gin.Context, raw []byte, err error) {
	rid := c.GetString("RequestID")
	if err != nil {
		response.Internal(c, rid, err.Error())
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

// ─── Endpoints ───────────────────────────────────────────────────────────────

// GetStatus handles GET /api/network-assistant/status
func (h *NetAssistHandler) GetStatus(c *gin.Context) {
	raw, err := h.client.GetStatus(c.Request.Context(), extractToken(c))
	h.proxyRaw(c, raw, err)
}

// SwitchMode handles POST /api/network-assistant/mode
func (h *NetAssistHandler) SwitchMode(c *gin.Context) {
	var req struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Mode == "" {
		rid := c.GetString("RequestID")
		response.BadRequest(c, rid, "mode is required")
		return
	}
	raw, err := h.client.SwitchMode(c.Request.Context(), extractToken(c), req.Mode)
	h.proxyRaw(c, raw, err)
}

// GetLogs handles GET /api/network-assistant/logs?lines=N
func (h *NetAssistHandler) GetLogs(c *gin.Context) {
	lines := 200
	if v := c.Query("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}
	raw, err := h.client.GetLogs(c.Request.Context(), extractToken(c), lines)
	h.proxyRaw(c, raw, err)
}

// GetDNSCache handles GET /api/network-assistant/dns/cache?query=Q
func (h *NetAssistHandler) GetDNSCache(c *gin.Context) {
	query := c.Query("query")
	raw, err := h.client.GetDNSCache(c.Request.Context(), extractToken(c), query)
	h.proxyRaw(c, raw, err)
}

// GetProcesses handles GET /api/network-assistant/processes
func (h *NetAssistHandler) GetProcesses(c *gin.Context) {
	raw, err := h.client.GetProcesses(c.Request.Context(), extractToken(c))
	h.proxyRaw(c, raw, err)
}

// StartMonitor handles POST /api/network-assistant/monitor/start
func (h *NetAssistHandler) StartMonitor(c *gin.Context) {
	raw, err := h.client.StartMonitor(c.Request.Context(), extractToken(c))
	h.proxyRaw(c, raw, err)
}

// StopMonitor handles POST /api/network-assistant/monitor/stop
func (h *NetAssistHandler) StopMonitor(c *gin.Context) {
	raw, err := h.client.StopMonitor(c.Request.Context(), extractToken(c))
	h.proxyRaw(c, raw, err)
}

// ClearMonitorEvents handles POST /api/network-assistant/monitor/clear
func (h *NetAssistHandler) ClearMonitorEvents(c *gin.Context) {
	raw, err := h.client.ClearMonitorEvents(c.Request.Context(), extractToken(c))
	h.proxyRaw(c, raw, err)
}

// GetMonitorEvents handles GET /api/network-assistant/monitor/events?since=0
func (h *NetAssistHandler) GetMonitorEvents(c *gin.Context) {
	var since int64
	if v := c.Query("since"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			since = n
		}
	}
	raw, err := h.client.GetMonitorEvents(c.Request.Context(), extractToken(c), since)
	h.proxyRaw(c, raw, err)
}

// InstallTUN handles POST /api/network-assistant/tun/install
func (h *NetAssistHandler) InstallTUN(c *gin.Context) {
	raw, err := h.client.InstallTUN(c.Request.Context(), extractToken(c))
	h.proxyRaw(c, raw, err)
}

// EnableTUN handles POST /api/network-assistant/tun/enable
func (h *NetAssistHandler) EnableTUN(c *gin.Context) {
	raw, err := h.client.EnableTUN(c.Request.Context(), extractToken(c))
	h.proxyRaw(c, raw, err)
}

// RestoreDirect handles POST /api/network-assistant/direct/restore
func (h *NetAssistHandler) RestoreDirect(c *gin.Context) {
	raw, err := h.client.RestoreDirect(c.Request.Context(), extractToken(c))
	h.proxyRaw(c, raw, err)
}

// GetRuleConfig handles GET /api/network-assistant/rules
func (h *NetAssistHandler) GetRuleConfig(c *gin.Context) {
	raw, err := h.client.GetRuleConfig(c.Request.Context(), extractToken(c))
	h.proxyRaw(c, raw, err)
}

// SetRulePolicy handles POST /api/network-assistant/rules/policy
func (h *NetAssistHandler) SetRulePolicy(c *gin.Context) {
	var req struct {
		Group        string `json:"group"`
		Action       string `json:"action"`
		TunnelNodeID string `json:"tunnelNodeID"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		rid := c.GetString("RequestID")
		response.BadRequest(c, rid, "group and action are required")
		return
	}
	raw, err := h.client.SetRulePolicy(c.Request.Context(), extractToken(c), req.Group, req.Action, req.TunnelNodeID)
	h.proxyRaw(c, raw, err)
}

// extractToken pulls the Bearer token from the request.
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
