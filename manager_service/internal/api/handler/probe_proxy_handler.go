package handler

// probe_proxy_handler.go — R2-BE: 探针代理端点实现
//
// 将前端 R2-PENDING 占位替换为真实的主控 HTTP 代理调用。
// 所有端点均通过 controller.Session（Bearer token）转发至主控 admin API。
//
// 端点映射（manager_service → probe_controller admin API）：
//   GET  /api/probe/nodes/status                → GET  /api/admin/probe/runtime/status
//   GET  /api/probe/nodes/:node_no/logs         → GET  /api/admin/probe/runtime/logs/:node_no
//   DELETE /api/probe/nodes/:node_no            → DELETE /api/admin/probe/node/:node_no
//   POST /api/probe/nodes/:node_no/restore      → POST /api/admin/probe/node/:node_no/restore
//   POST /api/probe/nodes/:node_no/upgrade      → POST /api/admin/probe/upgrade/dispatch
//   POST /api/probe/nodes/upgrade-all           → POST /api/admin/probe/upgrade/all
//   GET  /api/probe/nodes/report-interval       → GET  /api/admin/probe/report-interval
//   POST /api/probe/nodes/report-interval       → POST /api/admin/probe/report-interval
//   POST /api/probe/nodes/:node_no/shell/start  → POST /api/admin/probe/shell/start
//   POST /api/probe/nodes/:node_no/shell/exec   → POST /api/admin/probe/shell/exec
//   POST /api/probe/nodes/:node_no/shell/stop   → POST /api/admin/probe/shell/stop
//   GET  /api/probe/nodes/shell/shortcuts       → GET  /api/admin/probe/shell/shortcuts
//   POST /api/probe/nodes/shell/shortcuts       → POST /api/admin/probe/shell/shortcuts
//   DELETE /api/probe/nodes/shell/shortcuts/:name → DELETE /api/admin/probe/shell/shortcuts/:name

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cloudhelper/manager_service/internal/adapter/controller"
	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/gin-gonic/gin"
)

// ProbeProxyHandler proxies probe management requests to the probe_controller admin API.
type ProbeProxyHandler struct {
	session *controller.Session
}

// NewProbeProxyHandler creates a new ProbeProxyHandler.
func NewProbeProxyHandler(session *controller.Session) *ProbeProxyHandler {
	return &ProbeProxyHandler{session: session}
}

// requireSession returns false and writes an error response if no controller session is available.
func (h *ProbeProxyHandler) requireSession(c *gin.Context, rid string) bool {
	if !h.session.HasToken() {
		response.BadRequest(c, rid, "controller session not established: call POST /api/controller/session/set first")
		return false
	}
	return true
}

// proxyGet proxies a GET to the controller admin API and returns the raw JSON body.
func (h *ProbeProxyHandler) proxyGet(c *gin.Context, rid string, path string) {
	ctx := c.Request.Context()
	body, status, err := h.session.AuthorizedGet(ctx, path)
	if err != nil {
		response.Internal(c, rid, "controller proxy error: "+err.Error())
		return
	}
	c.Data(status, "application/json; charset=utf-8", body)
}

// proxyPost proxies a POST to the controller admin API with the given JSON payload.
func (h *ProbeProxyHandler) proxyPost(c *gin.Context, rid string, path string, payload []byte) {
	ctx := c.Request.Context()
	body, status, err := h.session.AuthorizedPost(ctx, path, payload)
	if err != nil {
		response.Internal(c, rid, "controller proxy error: "+err.Error())
		return
	}
	c.Data(status, "application/json; charset=utf-8", body)
}

// proxyDelete proxies a DELETE to the controller admin API.
func (h *ProbeProxyHandler) proxyDelete(c *gin.Context, rid string, path string) {
	ctx := c.Request.Context()
	body, status, err := h.session.AuthorizedDelete(ctx, path)
	if err != nil {
		response.Internal(c, rid, "controller proxy error: "+err.Error())
		return
	}
	c.Data(status, "application/json; charset=utf-8", body)
}

// parseNodeNo parses the :node_no path param. Returns 0 and writes error on failure.
func parseNodeNo(c *gin.Context, rid string) (int, bool) {
	n, err := strconv.Atoi(c.Param("node_no"))
	if err != nil || n <= 0 {
		response.BadRequest(c, rid, "invalid node_no")
		return 0, false
	}
	return n, true
}

// ── Status ────────────────────────────────────────────────────────────────────

// GetNodesStatus handles GET /api/probe/nodes/status[?node_no=N]
func (h *ProbeProxyHandler) GetNodesStatus(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	path := "/api/admin/probe/runtime/status"
	if nodeNoStr := c.Query("node_no"); nodeNoStr != "" {
		path += "?node_no=" + url.QueryEscape(nodeNoStr)
	}
	h.proxyGet(c, rid, path)
}

// ── Logs ─────────────────────────────────────────────────────────────────────

// GetNodeLogs handles GET /api/probe/nodes/:node_no/logs
func (h *ProbeProxyHandler) GetNodeLogs(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	nodeNo, ok := parseNodeNo(c, rid)
	if !ok {
		return
	}
	q := url.Values{}
	if v := c.Query("lines"); v != "" {
		q.Set("lines", v)
	}
	if v := c.Query("since_minutes"); v != "" {
		q.Set("since_minutes", v)
	}
	if v := c.Query("min_level"); v != "" {
		q.Set("min_level", v)
	}
	path := fmt.Sprintf("/api/admin/probe/runtime/logs/%d", nodeNo)
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	h.proxyGet(c, rid, path)
}

// ── Delete / Restore ─────────────────────────────────────────────────────────

// DeleteNode handles DELETE /api/probe/nodes/:node_no
func (h *ProbeProxyHandler) DeleteNode(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	nodeNo, ok := parseNodeNo(c, rid)
	if !ok {
		return
	}
	h.proxyDelete(c, rid, fmt.Sprintf("/api/admin/probe/node/%d", nodeNo))
}

// RestoreNode handles POST /api/probe/nodes/:node_no/restore
func (h *ProbeProxyHandler) RestoreNode(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	nodeNo, ok := parseNodeNo(c, rid)
	if !ok {
		return
	}
	h.proxyPost(c, rid, fmt.Sprintf("/api/admin/probe/node/%d/restore", nodeNo), nil)
}

// ── Upgrade ───────────────────────────────────────────────────────────────────

// UpgradeNode handles POST /api/probe/nodes/:node_no/upgrade
func (h *ProbeProxyHandler) UpgradeNode(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	nodeNo, ok := parseNodeNo(c, rid)
	if !ok {
		return
	}
	payload, _ := json.Marshal(map[string]int{"node_no": nodeNo})
	h.proxyPost(c, rid, "/api/admin/probe/upgrade/dispatch", payload)
}

// UpgradeAllNodes handles POST /api/probe/nodes/upgrade-all
func (h *ProbeProxyHandler) UpgradeAllNodes(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.proxyPost(c, rid, "/api/admin/probe/upgrade/all", nil)
}

// ── Report interval ──────────────────────────────────────────────────────────

// GetReportInterval handles GET /api/probe/nodes/report-interval
func (h *ProbeProxyHandler) GetReportInterval(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.proxyGet(c, rid, "/api/admin/probe/report-interval")
}

// SetReportInterval handles POST /api/probe/nodes/report-interval
func (h *ProbeProxyHandler) SetReportInterval(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var req struct {
		IntervalSec int `json:"interval_sec"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IntervalSec <= 0 {
		response.BadRequest(c, rid, "interval_sec must be a positive integer")
		return
	}
	payload, _ := json.Marshal(req)
	h.proxyPost(c, rid, "/api/admin/probe/report-interval", payload)
}

// ── Shell ─────────────────────────────────────────────────────────────────────

// StartShell handles POST /api/probe/nodes/:node_no/shell/start
func (h *ProbeProxyHandler) StartShell(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	nodeNo, ok := parseNodeNo(c, rid)
	if !ok {
		return
	}
	payload, _ := json.Marshal(map[string]int{"node_no": nodeNo})
	h.proxyPost(c, rid, "/api/admin/probe/shell/start", payload)
}

// ExecShell handles POST /api/probe/nodes/:node_no/shell/exec
func (h *ProbeProxyHandler) ExecShell(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	nodeNo, ok := parseNodeNo(c, rid)
	if !ok {
		return
	}
	var req struct {
		SessionID  string `json:"session_id"`
		Command    string `json:"command"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.SessionID == "" || req.Command == "" {
		response.BadRequest(c, rid, "session_id and command are required")
		return
	}
	if req.TimeoutSec <= 0 {
		req.TimeoutSec = 60
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"node_no":     nodeNo,
		"session_id":  req.SessionID,
		"command":     req.Command,
		"timeout_sec": req.TimeoutSec,
	})
	
	ctx := c.Request.Context()
	body, status, err := h.session.AuthorizedPost(ctx, "/api/admin/probe/shell/exec", payload)
	if err != nil {
		response.Internal(c, rid, "controller proxy error: "+err.Error())
		return
	}
	c.Data(status, "application/json; charset=utf-8", body)
}

// StopShell handles POST /api/probe/nodes/:node_no/shell/stop
func (h *ProbeProxyHandler) StopShell(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	nodeNo, ok := parseNodeNo(c, rid)
	if !ok {
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.SessionID == "" {
		response.BadRequest(c, rid, "session_id is required")
		return
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"node_no":    nodeNo,
		"session_id": req.SessionID,
	})
	h.proxyPost(c, rid, "/api/admin/probe/shell/stop", payload)
}

// ── Shell shortcuts ───────────────────────────────────────────────────────────

// GetShellShortcuts handles GET /api/probe/nodes/shell/shortcuts
func (h *ProbeProxyHandler) GetShellShortcuts(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.proxyGet(c, rid, "/api/admin/probe/shell/shortcuts")
}

// SaveShellShortcut handles POST /api/probe/nodes/shell/shortcuts
func (h *ProbeProxyHandler) SaveShellShortcut(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var req struct {
		Name    string `json:"name"`
		Command string `json:"command"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" || req.Command == "" {
		response.BadRequest(c, rid, "name and command are required")
		return
	}
	payload, _ := json.Marshal(req)
	h.proxyPost(c, rid, "/api/admin/probe/shell/shortcuts", payload)
}

// DeleteShellShortcut handles DELETE /api/probe/nodes/shell/shortcuts/:name
func (h *ProbeProxyHandler) DeleteShellShortcut(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	name := c.Param("name")
	if name == "" {
		response.BadRequest(c, rid, "shortcut name is required")
		return
	}
	h.proxyDelete(c, rid, "/api/admin/probe/shell/shortcuts/"+url.PathEscape(name))
}

// suppress unused import warnings from time
var _ = time.Second
var _ = http.MethodGet
