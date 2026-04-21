package core

import (
	"net/http"
	"strings"
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

func writeMngTGError(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unknown error"})
		return
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		msg = "unknown error"
	}
	lower := strings.ToLower(msg)
	status := http.StatusBadRequest
	switch {
	case strings.Contains(lower, "not initialized"):
		status = http.StatusInternalServerError
	case strings.Contains(lower, "not found"):
		status = http.StatusNotFound
	case strings.Contains(lower, "request failed"), strings.Contains(lower, "status="), strings.Contains(lower, "timeout"):
		status = http.StatusBadGateway
	}
	writeJSON(w, status, map[string]string{"error": msg})
}
