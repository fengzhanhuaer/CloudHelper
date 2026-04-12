package handler

// system_proxy_handler.go — R8-BE: 系统设置域代理端点实现
//
// 通过 controller.Session.CallWS 将系统备份与主控配置操作代理至主控 WS-RPC。
//
// 端点映射:
//   GET  /api/system/backup-settings        → admin.backup.settings.get
//   POST /api/system/backup-settings        → admin.backup.settings.set
//   POST /api/system/backup-settings/test   → admin.backup.settings.test

import (
	"time"

	"github.com/cloudhelper/manager_service/internal/adapter/controller"
	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/gin-gonic/gin"
)

// SystemProxyHandler proxies system/backup management operations to the controller WS-RPC.
type SystemProxyHandler struct {
	session *controller.Session
}

// NewSystemProxyHandler creates a new SystemProxyHandler.
func NewSystemProxyHandler(session *controller.Session) *SystemProxyHandler {
	return &SystemProxyHandler{session: session}
}

func (h *SystemProxyHandler) requireSession(c *gin.Context, rid string) bool {
	if !h.session.HasToken() {
		response.BadRequest(c, rid, "controller session not established: call POST /api/controller/session/set first")
		return false
	}
	return true
}

func (h *SystemProxyHandler) callWS(c *gin.Context, rid, action string, payload interface{}, timeout time.Duration) {
	raw, err := h.session.CallWS(c.Request.Context(), action, payload, timeout)
	if err != nil {
		response.Internal(c, rid, "controller ws-rpc error: "+err.Error())
		return
	}
	response.OKRaw(c, rid, raw)
}

// GetBackupSettings handles GET /api/system/backup-settings
func (h *SystemProxyHandler) GetBackupSettings(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.backup.settings.get", nil, 0)
}

// SetBackupSettings handles POST /api/system/backup-settings
func (h *SystemProxyHandler) SetBackupSettings(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	h.callWS(c, rid, "admin.backup.settings.set", payload, 0)
}

// TestBackupSettings handles POST /api/system/backup-settings/test
func (h *SystemProxyHandler) TestBackupSettings(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	h.callWS(c, rid, "admin.backup.settings.test", payload, 60*time.Second)
}

// ── 主控日志 ─────────────────────────────────────────────────────────────────

// GetControllerLogs handles GET /api/system/controller-logs
// Query params: lines, since_minutes, min_level
func (h *SystemProxyHandler) GetControllerLogs(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	_ = c.ShouldBindJSON(&payload)
	// Also accept query params for GET requests
	if payload == nil {
		m := map[string]interface{}{}
		if v := c.Query("lines"); v != "" {
			m["lines"] = v
		}
		if v := c.Query("since_minutes"); v != "" {
			m["since_minutes"] = v
		}
		if v := c.Query("min_level"); v != "" {
			m["min_level"] = v
		}
		if len(m) > 0 {
			payload = m
		}
	}
	h.callWS(c, rid, "admin.logs", payload, 30*time.Second)
}

// ── 主控版本与升级 ────────────────────────────────────────────────────────────

// GetControllerVersion handles GET /api/system/controller-version
func (h *SystemProxyHandler) GetControllerVersion(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.version", nil, 30*time.Second)
}

// UpgradeController handles POST /api/system/controller-upgrade
func (h *SystemProxyHandler) UpgradeController(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.upgrade", nil, 120*time.Second)
}

// GetControllerUpgradeProgress handles GET /api/system/controller-upgrade-progress
func (h *SystemProxyHandler) GetControllerUpgradeProgress(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.upgrade.progress", nil, 0)
}

// ── Rule routes 备份同步 ──────────────────────────────────────────────────────

// UploadRuleRoutes handles POST /api/system/rule-routes/upload
func (h *SystemProxyHandler) UploadRuleRoutes(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	h.callWS(c, rid, "admin.manager.rule_routes.upload", payload, 60*time.Second)
}

// DownloadRuleRoutes handles POST /api/system/rule-routes/download
func (h *SystemProxyHandler) DownloadRuleRoutes(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	_ = c.ShouldBindJSON(&payload)
	h.callWS(c, rid, "admin.manager.rule_routes.download", payload, 60*time.Second)
}

