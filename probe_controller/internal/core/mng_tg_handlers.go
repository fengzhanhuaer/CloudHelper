package core

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gotd/td/tgerr"
)

func mngTGPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/tg" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngTGPageHTML))
}

func mngTGSessionPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/tg/session" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngTGSessionPageHTML))
}

func mngTGAPIGetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, getTGAssistantAPIKey())
}

func mngTGAPISetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantAPIKeyRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	result, err := setTGAssistantAPIKey(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func mngTGAccountsListHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"accounts": listTGAssistantAccounts(),
	})
}

func mngTGAccountsRefreshHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	accounts, err := refreshTGAssistantAccounts()
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"accounts": accounts,
	})
}

func mngTGAccountAddHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantAddAccountRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	account, err := addTGAssistantAccount(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"account": account,
	})
}

func mngTGAccountRemoveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantAccountIDRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	accounts, err := removeTGAssistantAccount(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"accounts": accounts,
	})
}

func mngTGAccountSendCodeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantAccountIDRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	account, err := sendTGAssistantLoginCode(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"account": account,
	})
}

func mngTGAccountSignInHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantSignInRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	account, err := completeTGAssistantLogin(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"account": account,
	})
}

func mngTGAccountTokenLoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantSessionTokenLoginRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	account, err := loginTGAssistantAccountBySessionToken(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"account": account,
	})
}

func mngTGAccountLogoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantAccountIDRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	account, err := logoutTGAssistantAccount(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"account": account,
	})
}

func mngTGBotGetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantAccountIDRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	result, err := getTGAssistantBotAPIKey(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func mngTGBotSetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantBotAPIKeyRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	result, err := setTGAssistantBotAPIKey(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func mngTGBotTestSendHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantBotTestSendRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	result, err := testSendTGAssistantBotMessage(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"result": result,
	})
}

func mngTGTargetsListHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantAccountIDRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	targets, err := listTGAssistantTargets(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"targets": targets,
	})
}

func mngTGTargetsRefreshHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantAccountIDRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	targets, err := refreshTGAssistantTargets(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"targets": targets,
	})
}

func mngTGSessionMessagesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantSessionMessagesRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	messages, err := listTGAssistantSessionMessages(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"messages": messages,
	})
}

func mngTGSessionSendHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantSessionSendRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	message, err := sendTGAssistantSessionMessage(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": message,
	})
}

func mngTGScheduleListHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantAccountIDRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	schedules, err := listTGAssistantSchedules(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schedules": schedules,
	})
}

func mngTGScheduleAddHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantScheduleAddRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	schedules, err := addTGAssistantSchedule(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schedules": schedules,
	})
}

func mngTGScheduleUpdateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantScheduleUpdateRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	schedules, err := updateTGAssistantSchedule(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schedules": schedules,
	})
}

func mngTGScheduleRemoveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantScheduleRemoveRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	schedules, err := removeTGAssistantSchedule(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schedules": schedules,
	})
}

func mngTGScheduleSetEnabledHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantScheduleSetEnabledRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	schedules, err := setTGAssistantScheduleEnabled(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schedules": schedules,
	})
}

func mngTGScheduleSendNowHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantScheduleSendNowRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	result, err := sendNowTGAssistantSchedule(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"result": result,
	})
}

func mngTGScheduleHistoryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantScheduleHistoryRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	history, err := listTGAssistantScheduleTaskHistory(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"history": history,
	})
}

func mngTGSchedulePendingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantAccountIDRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	pending, err := listTGAssistantPendingTasks(req)
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pending": pending,
	})
}

func mngTGNotifyGetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, getTGAssistantNotifyOverview())
}

func mngTGNotifySetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tgAssistantNotifySettingsRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	if _, err := setTGAssistantNotifySettings(req); err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, getTGAssistantNotifyOverview())
}

func mngTGNotifyTestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := testTGAssistantNotifyPush()
	if err != nil {
		writeMngTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"result": result,
	})
}

func writeMngTGError(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unknown error"})
		return
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		msg = "unknown error"
	}
	if rpcMsg := formatMngTGRPCError(err); rpcMsg != "" {
		msg = rpcMsg
	}
	lower := strings.ToLower(msg)
	status := http.StatusBadRequest
	switch {
	case strings.Contains(lower, "flood_wait"), strings.Contains(lower, "peer_flood"):
		status = http.StatusTooManyRequests
	case strings.Contains(lower, "not initialized"):
		status = http.StatusInternalServerError
	case strings.Contains(lower, "not found"):
		status = http.StatusNotFound
	case strings.Contains(lower, "forbidden"), strings.Contains(lower, "banned"), strings.Contains(lower, "write"):
		status = http.StatusForbidden
	case strings.Contains(lower, "request failed"), strings.Contains(lower, "status="), strings.Contains(lower, "timeout"):
		status = http.StatusBadGateway
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

func formatMngTGRPCError(err error) string {
	var rpcErr *tgerr.Error
	if !errors.As(err, &rpcErr) || rpcErr == nil {
		return ""
	}
	switch rpcErr.Type {
	case "FLOOD_WAIT":
		if rpcErr.Argument > 0 {
			return "Telegram 限流 FLOOD_WAIT，需要等待 " + strconv.Itoa(rpcErr.Argument) + " 秒后再试"
		}
		return "Telegram 限流 FLOOD_WAIT，请稍后再试"
	case "PEER_FLOOD":
		return "Telegram 返回 PEER_FLOOD，账号疑似触发风控，建议暂停主动发送一段时间"
	case "USER_BANNED_IN_CHANNEL":
		return "Telegram 返回 USER_BANNED_IN_CHANNEL，账号在该频道/群组中受限"
	case "CHAT_WRITE_FORBIDDEN":
		return "Telegram 返回 CHAT_WRITE_FORBIDDEN，当前会话不允许此账号发送消息"
	case "USER_IS_BLOCKED":
		return "Telegram 返回 USER_IS_BLOCKED，对方已屏蔽当前账号"
	case "INPUT_USER_DEACTIVATED":
		return "Telegram 返回 INPUT_USER_DEACTIVATED，对方账号已注销"
	}
	if strings.TrimSpace(rpcErr.Type) != "" {
		return "Telegram RPC 错误 " + strings.TrimSpace(rpcErr.Type) + ": " + strings.TrimSpace(rpcErr.Message)
	}
	return ""
}
