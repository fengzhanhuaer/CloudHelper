package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type mngRegisterRequest struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
}

type mngLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func mngEntryHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngEntryPageHTML))
}

func mngPanelHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/panel" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngPanelPageHTML))
}

func mngSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/settings" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngSettingsPageHTML))
}

func mngBootstrapHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mgr, err := ensureMngAuthManager()
	if err != nil {
		writeMngError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"registered": mgr.registered()})
}

func mngRegisterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mgr, err := ensureMngAuthManager()
	if err != nil {
		writeMngError(w, err)
		return
	}

	var req mngRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	if err := mgr.register(req.Username, req.Password, req.ConfirmPassword); err != nil {
		writeMngError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"registered": true,
	})
}

func mngLoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mgr, err := ensureMngAuthManager()
	if err != nil {
		writeMngError(w, err)
		return
	}

	var req mngLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	ip, _ := getClientIP(r)
	token, session, err := mgr.login(ip, req.Username, req.Password)
	if err != nil {
		writeMngError(w, err)
		return
	}
	setMngSessionCookie(w, r, token, session.ExpiresAt)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"username":   session.Username,
		"expires_at": session.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func mngLogoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mgr, err := ensureMngAuthManager()
	if err != nil {
		writeMngError(w, err)
		return
	}
	token, _ := extractMngSessionToken(r)
	mgr.logoutToken(token)
	clearMngSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func mngSessionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, _, err := currentMngSessionFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"authenticated": false,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"authenticated": true,
		"username":      session.Username,
		"expires_at":    session.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func mngPanelSummaryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload := map[string]interface{}{
		"uptime":      int(time.Since(serverStartTime).Seconds()),
		"server_time": time.Now().UTC().Format(time.RFC3339),
		"version":     strings.TrimSpace(currentControllerVersion()),
	}
	if strings.TrimSpace(payload["version"].(string)) == "" {
		payload["version"] = "dev"
	}
	writeJSON(w, http.StatusOK, payload)
}

func mngSystemVersionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	current := strings.TrimSpace(currentControllerVersion())
	if current == "" {
		current = "dev"
	}
	repo := releaseRepo()
	resp := map[string]interface{}{
		"current_version":   current,
		"latest_version":    "",
		"release_repo":      repo,
		"upgrade_available": false,
		"message":           "",
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	release, err := fetchLatestRelease(ctx, repo)
	if err != nil {
		resp["message"] = fmt.Sprintf("failed to query latest release: %v", err)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	latest := strings.TrimSpace(release.TagName)
	resp["latest_version"] = latest
	if latest != "" {
		resp["upgrade_available"] = normalizeVersion(current) != normalizeVersion(latest)
	} else {
		resp["message"] = "latest release tag is empty"
	}
	writeJSON(w, http.StatusOK, resp)
}

func mngSystemUpgradeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	result, err := triggerControllerUpgradeTask()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"accepted":        false,
			"error":           err.Error(),
			"current_version": result.CurrentVersion,
			"latest_version":  result.LatestVersion,
		})
		return
	}

	accepted := true
	msg := strings.ToLower(strings.TrimSpace(result.Message))
	if strings.Contains(msg, "already running") {
		accepted = false
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"accepted":        accepted,
		"message":         result.Message,
		"current_version": result.CurrentVersion,
		"latest_version":  result.LatestVersion,
		"updated":         result.Updated,
	})
}

func mngSystemUpgradeProgressHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	progress := getControllerUpgradeProgress()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"active":          progress.Active,
		"phase":           progress.Phase,
		"percent":         progress.Percent,
		"message":         progress.Message,
		"current_version": strings.TrimSpace(currentControllerVersion()),
	})
}

func mngSystemReconnectCheckHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":          true,
		"server_time": time.Now().UTC().Format(time.RFC3339),
		"version":     strings.TrimSpace(currentControllerVersion()),
	})
}
