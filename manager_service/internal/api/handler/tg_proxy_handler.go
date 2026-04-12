package handler

// tg_proxy_handler.go — R6-BE: TG助手域代理端点实现
//
// 通过 controller.Session.CallWS 将 TG 相关操作代理至主控 WS-RPC。
//
// 端点映射:
//   GET  /api/tg/api-key                  → admin.tg.api.get
//   POST /api/tg/api-key                  → admin.tg.api.set
//   GET  /api/tg/accounts                 → admin.tg.accounts.list
//   POST /api/tg/accounts/refresh         → admin.tg.accounts.refresh
//   POST /api/tg/accounts/add             → admin.tg.account.add
//   POST /api/tg/accounts/send-code       → admin.tg.account.send_code
//   POST /api/tg/accounts/sign-in         → admin.tg.account.sign_in
//   POST /api/tg/accounts/logout          → admin.tg.account.logout
//   POST /api/tg/accounts/remove          → admin.tg.account.remove
//   GET  /api/tg/schedules                → admin.tg.schedule.list
//   POST /api/tg/schedules                → admin.tg.schedule.add
//   PUT  /api/tg/schedules/:id            → admin.tg.schedule.update
//   DELETE /api/tg/schedules/:id          → admin.tg.schedule.remove
//   POST /api/tg/schedules/:id/enable     → admin.tg.schedule.set_enabled  {enabled:true}
//   POST /api/tg/schedules/:id/disable    → admin.tg.schedule.set_enabled  {enabled:false}
//   POST /api/tg/schedules/:id/send-now   → admin.tg.schedule.send_now
//   GET  /api/tg/schedules/pending        → admin.tg.schedule.pending
//   GET  /api/tg/schedules/history        → admin.tg.schedule.history
//   GET  /api/tg/targets                  → admin.tg.targets.list
//   POST /api/tg/targets/refresh          → admin.tg.targets.refresh
//   GET  /api/tg/bot                      → admin.tg.bot.get
//   POST /api/tg/bot                      → admin.tg.bot.set
//   POST /api/tg/bot/test-send            → admin.tg.bot.test_send

import (
	"time"

	"github.com/cloudhelper/manager_service/internal/adapter/controller"
	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/gin-gonic/gin"
)

// TGProxyHandler proxies TG Assistant operations to the controller WS-RPC.
type TGProxyHandler struct {
	session *controller.Session
}

// NewTGProxyHandler creates a new TGProxyHandler.
func NewTGProxyHandler(session *controller.Session) *TGProxyHandler {
	return &TGProxyHandler{session: session}
}

func (h *TGProxyHandler) requireSession(c *gin.Context, rid string) bool {
	if !h.session.HasToken() {
		response.BadRequest(c, rid, "controller session not established: call POST /api/controller/session/set first")
		return false
	}
	return true
}

func (h *TGProxyHandler) callWS(c *gin.Context, rid, action string, payload interface{}, timeout time.Duration) {
	raw, err := h.session.CallWS(c.Request.Context(), action, payload, timeout)
	if err != nil {
		response.Internal(c, rid, "controller ws-rpc error: "+err.Error())
		return
	}
	response.OKRaw(c, rid, raw)
}

func (h *TGProxyHandler) bindAndCall(c *gin.Context, rid, action string, timeout time.Duration) {
	var payload interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	h.callWS(c, rid, action, payload, timeout)
}

// ── TG API Key ────────────────────────────────────────────────────────────────

func (h *TGProxyHandler) GetAPIKey(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.tg.api.get", nil, 0)
}

func (h *TGProxyHandler) SetAPIKey(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.bindAndCall(c, rid, "admin.tg.api.set", 0)
}

// ── TG Accounts ───────────────────────────────────────────────────────────────

func (h *TGProxyHandler) ListAccounts(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.tg.accounts.list", nil, 0)
}

func (h *TGProxyHandler) RefreshAccounts(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.tg.accounts.refresh", nil, 30*time.Second)
}

func (h *TGProxyHandler) AddAccount(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.bindAndCall(c, rid, "admin.tg.account.add", 0)
}

func (h *TGProxyHandler) SendCode(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.bindAndCall(c, rid, "admin.tg.account.send_code", 60*time.Second)
}

func (h *TGProxyHandler) SignIn(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.bindAndCall(c, rid, "admin.tg.account.sign_in", 60*time.Second)
}

func (h *TGProxyHandler) LogoutAccount(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.bindAndCall(c, rid, "admin.tg.account.logout", 30*time.Second)
}

func (h *TGProxyHandler) RemoveAccount(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.bindAndCall(c, rid, "admin.tg.account.remove", 0)
}

// ── TG Schedules ──────────────────────────────────────────────────────────────

func (h *TGProxyHandler) ListSchedules(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.tg.schedule.list", nil, 0)
}

func (h *TGProxyHandler) AddSchedule(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.bindAndCall(c, rid, "admin.tg.schedule.add", 0)
}

func (h *TGProxyHandler) UpdateSchedule(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload map[string]interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	if id := c.Param("id"); id != "" {
		payload["id"] = id
	}
	h.callWS(c, rid, "admin.tg.schedule.update", payload, 0)
}

func (h *TGProxyHandler) RemoveSchedule(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	id := c.Param("id")
	h.callWS(c, rid, "admin.tg.schedule.remove", map[string]string{"id": id}, 0)
}

func (h *TGProxyHandler) EnableSchedule(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	id := c.Param("id")
	h.callWS(c, rid, "admin.tg.schedule.set_enabled", map[string]interface{}{"id": id, "enabled": true}, 0)
}

func (h *TGProxyHandler) DisableSchedule(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	id := c.Param("id")
	h.callWS(c, rid, "admin.tg.schedule.set_enabled", map[string]interface{}{"id": id, "enabled": false}, 0)
}

func (h *TGProxyHandler) SendNow(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	id := c.Param("id")
	h.callWS(c, rid, "admin.tg.schedule.send_now", map[string]string{"id": id}, 120*time.Second)
}

func (h *TGProxyHandler) GetPendingTasks(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.tg.schedule.pending", nil, 0)
}

func (h *TGProxyHandler) GetHistory(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	var payload interface{}
	_ = c.ShouldBindJSON(&payload)
	h.callWS(c, rid, "admin.tg.schedule.history", payload, 0)
}

// ── TG Targets ────────────────────────────────────────────────────────────────

func (h *TGProxyHandler) ListTargets(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.tg.targets.list", nil, 0)
}

func (h *TGProxyHandler) RefreshTargets(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.tg.targets.refresh", nil, 60*time.Second)
}

// ── TG Bot ────────────────────────────────────────────────────────────────────

func (h *TGProxyHandler) GetBot(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.callWS(c, rid, "admin.tg.bot.get", nil, 0)
}

func (h *TGProxyHandler) SetBot(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.bindAndCall(c, rid, "admin.tg.bot.set", 0)
}

func (h *TGProxyHandler) TestBotSend(c *gin.Context) {
	rid := c.GetString("RequestID")
	if !h.requireSession(c, rid) {
		return
	}
	h.bindAndCall(c, rid, "admin.tg.bot.test_send", 60*time.Second)
}
