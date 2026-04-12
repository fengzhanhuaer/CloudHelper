package handler

// cloudflare_proxy_handler.go — R5-BE: Cloudflare 域代理端点实现
//
// 通过 controller.Session.CallWS 将 Cloudflare 相关操作代理至主控 WS-RPC。
//
// 端点映射:
//   GET  /api/cloudflare/api-key               → admin.cloudflare.api.get
//   POST /api/cloudflare/api-key               → admin.cloudflare.api.set
//   GET  /api/cloudflare/zone                  → admin.cloudflare.zone.get
//   POST /api/cloudflare/zone                  → admin.cloudflare.zone.set
//   GET  /api/cloudflare/ddns/records          → admin.cloudflare.ddns.records
//   POST /api/cloudflare/ddns/apply            → admin.cloudflare.ddns.apply
//   GET  /api/cloudflare/zerotrust/whitelist   → admin.cloudflare.zerotrust.whitelist.get
//   POST /api/cloudflare/zerotrust/whitelist   → admin.cloudflare.zerotrust.whitelist.set
//   POST /api/cloudflare/zerotrust/sync        → admin.cloudflare.zerotrust.whitelist.run

import (
	"time"

	"github.com/cloudhelper/manager_service/internal/adapter/controller"
	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/gin-gonic/gin"
)

// CloudflareProxyHandler proxies Cloudflare management operations to the controller WS-RPC.
type CloudflareProxyHandler struct {
	session *controller.Session
}

// NewCloudflareProxyHandler creates a new CloudflareProxyHandler.
func NewCloudflareProxyHandler(session *controller.Session) *CloudflareProxyHandler {
	return &CloudflareProxyHandler{session: session}
}

func (h *CloudflareProxyHandler) requireSession(c *gin.Context, rid string) bool {
	if !h.session.HasToken() {
		response.BadRequest(c, rid, "controller session not established: call POST /api/controller/session/set first")
		return false
	}
	return true
}

func (h *CloudflareProxyHandler) callWS(c *gin.Context, rid, action string, payload interface{}, timeout time.Duration) {
	raw, err := h.session.CallWS(c.Request.Context(), action, payload, timeout)
	if err != nil {
		response.Internal(c, rid, "controller ws-rpc error: "+err.Error())
		return
	}
	response.OKRaw(c, rid, raw)
}

// GetAPIKey handles GET /api/cloudflare/api-key
func (h *CloudflareProxyHandler) GetAPIKey(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.cloudflare.api.get", nil, 0)
}

// SetAPIKey handles POST /api/cloudflare/api-key
func (h *CloudflareProxyHandler) SetAPIKey(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	h.callWS(c, rid, "admin.cloudflare.api.set", payload, 0)
}

// GetZone handles GET /api/cloudflare/zone
func (h *CloudflareProxyHandler) GetZone(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.cloudflare.zone.get", nil, 0)
}

// SetZone handles POST /api/cloudflare/zone
func (h *CloudflareProxyHandler) SetZone(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	h.callWS(c, rid, "admin.cloudflare.zone.set", payload, 0)
}

// GetDDNSRecords handles GET /api/cloudflare/ddns/records
func (h *CloudflareProxyHandler) GetDDNSRecords(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.cloudflare.ddns.records", nil, 30*time.Second)
}

// ApplyDDNS handles POST /api/cloudflare/ddns/apply
func (h *CloudflareProxyHandler) ApplyDDNS(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	_ = c.ShouldBindJSON(&payload)
	h.callWS(c, rid, "admin.cloudflare.ddns.apply", payload, 120*time.Second)
}

// GetZeroTrustWhitelist handles GET /api/cloudflare/zerotrust/whitelist
func (h *CloudflareProxyHandler) GetZeroTrustWhitelist(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.cloudflare.zerotrust.whitelist.get", nil, 0)
}

// SetZeroTrustWhitelist handles POST /api/cloudflare/zerotrust/whitelist
func (h *CloudflareProxyHandler) SetZeroTrustWhitelist(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	h.callWS(c, rid, "admin.cloudflare.zerotrust.whitelist.set", payload, 0)
}

// RunZeroTrustSync handles POST /api/cloudflare/zerotrust/sync
func (h *CloudflareProxyHandler) RunZeroTrustSync(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	_ = c.ShouldBindJSON(&payload)
	h.callWS(c, rid, "admin.cloudflare.zerotrust.whitelist.run", payload, 120*time.Second)
}
