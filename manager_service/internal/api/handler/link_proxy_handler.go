package handler

// link_proxy_handler.go — R4-BE: 链路管理代理端点实现
//
// 通过 controller.Session.CallWS 将前端 R4-PENDING 占位函数替换为真实的
// probe_controller WS-RPC 代理调用。
//
// 端点映射（manager_service REST → probe_controller WS-RPC action）：
//   GET    /api/link/chains                 → admin.probe.link.chains.get
//   POST   /api/link/chains                 → admin.probe.link.chain.upsert
//   DELETE /api/link/chains/:chain_id       → admin.probe.link.chain.delete
//   GET    /api/link/users                  → admin.probe.link.users.get
//   GET    /api/link/users/:username/pubkey → admin.probe.link.user.public_key.get
//   POST   /api/link/nodes/update           → admin.probe.link.update
//   POST   /api/link/test/start             → admin.probe.link.test.start
//   POST   /api/link/test/stop              → admin.probe.link.test.stop

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/cloudhelper/manager_service/internal/adapter/controller"
	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/gin-gonic/gin"
)

// LinkProxyHandler proxies link-management requests to the probe_controller admin WS-RPC API.
type LinkProxyHandler struct {
	session *controller.Session
}

// NewLinkProxyHandler creates a new LinkProxyHandler.
func NewLinkProxyHandler(session *controller.Session) *LinkProxyHandler {
	return &LinkProxyHandler{session: session}
}

// requireSession writes a clear error if no controller token is configured.
func (h *LinkProxyHandler) requireSession(c *gin.Context, rid string) bool {
	if !h.session.HasToken() {
		response.BadRequest(c, rid, "controller session not established: call POST /api/controller/session/set first")
		return false
	}
	return true
}

// callWS is a convenience wrapper that calls, marshals the result, and writes it.
func (h *LinkProxyHandler) callWS(c *gin.Context, rid, action string, payload interface{}, timeout time.Duration) {
	ctx := c.Request.Context()
	raw, err := h.session.CallWS(ctx, action, payload, timeout)
	if err != nil {
		response.Internal(c, rid, "controller ws-rpc error: "+err.Error())
		return
	}
	// raw is already a JSON value; write it directly as the "data" field.
	response.OKRaw(c, rid, raw)
}

// ── Link Chains ───────────────────────────────────────────────────────────────

// GetChains handles GET /api/link/chains
func (h *LinkProxyHandler) GetChains(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.probe.link.chains.get", nil, 0)
}

// UpsertChain handles POST /api/link/chains
func (h *LinkProxyHandler) UpsertChain(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	h.callWS(c, rid, "admin.probe.link.chain.upsert", payload, 120*time.Second)
}

// DeleteChain handles DELETE /api/link/chains/:chain_id
func (h *LinkProxyHandler) DeleteChain(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	chainID := c.Param("chain_id")
	if chainID == "" {
		response.BadRequest(c, rid, "chain_id is required")
		return
	}
	payload := map[string]string{"chain_id": chainID}
	h.callWS(c, rid, "admin.probe.link.chain.delete", payload, 120*time.Second)
}

// ── Link Users ────────────────────────────────────────────────────────────────

// GetUsers handles GET /api/link/users
func (h *LinkProxyHandler) GetUsers(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.probe.link.users.get", nil, 0)
}

// GetUserPublicKey handles GET /api/link/users/:username/pubkey
func (h *LinkProxyHandler) GetUserPublicKey(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	username := c.Param("username")
	if username == "" {
		response.BadRequest(c, rid, "username is required")
		return
	}
	payload := map[string]string{"username": username}
	h.callWS(c, rid, "admin.probe.link.user.public_key.get", payload, 0)
}

// ── Node link address update ──────────────────────────────────────────────────

// UpdateNodeLink handles POST /api/link/nodes/update
func (h *LinkProxyHandler) UpdateNodeLink(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	h.callWS(c, rid, "admin.probe.link.update", payload, 0)
}

// ── Link test ─────────────────────────────────────────────────────────────────

// StartLinkTest handles POST /api/link/test/start
func (h *LinkProxyHandler) StartLinkTest(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	h.callWS(c, rid, "admin.probe.link.test.start", payload, 130*time.Second)
}

// StopLinkTest handles POST /api/link/test/stop
func (h *LinkProxyHandler) StopLinkTest(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	h.callWS(c, rid, "admin.probe.link.test.stop", payload, 60*time.Second)
}

// ── DNS refresh (probe_controller WS proxy) ───────────────────────────────────

// RefreshProbeDNS handles POST /api/link/dns/refresh  (admin.probe.dns.refresh.cache)
func (h *LinkProxyHandler) RefreshProbeDNS(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	_ = c.ShouldBindJSON(&payload)
	h.callWS(c, rid, "admin.probe.dns.refresh.cache", payload, 30*time.Second)
}

// suppress unused import warning
var _ = json.RawMessage(nil)
var _ = http.MethodGet
