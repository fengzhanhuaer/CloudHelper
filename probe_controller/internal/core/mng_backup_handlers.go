package core

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
)

type mngBackupSettingsRequest struct {
	Enabled            bool     `json:"enabled"`
	LocalDir           string   `json:"local_dir"`
	SourceDirs         []string `json:"source_dirs"`
	GoogleClientID     string   `json:"google_client_id"`
	GoogleClientSecret string   `json:"google_client_secret"`
	GoogleFolder       string   `json:"google_folder"`
}

type mngBackupGoogleAuthStartRequest struct {
	GoogleClientID     string `json:"google_client_id"`
	GoogleClientSecret string `json:"google_client_secret"`
}

type mngBackupGoogleAuthPollRequest struct {
	SessionID string `json:"session_id"`
}

func mngBackupPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/backup" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngBackupPageHTML))
}

func mngBackupStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	settings := getBackupSettings()
	dataPath, _ := filepath.Abs(dataDir)
	backupPath, _ := resolveBackupLocalDir(settings, dataPath)
	sourceDirs, sourceDirsErr := resolveBackupSourceDirs(settings)
	payload := map[string]any{
		"settings": map[string]any{
			"enabled":                 settings.Enabled,
			"local_dir":               settings.LocalDir,
			"source_dirs":             sourceDirs,
			"source_error":            errorString(sourceDirsErr),
			"google_client_id":        settings.GoogleClientID,
			"google_client_secret":    secretConfiguredLabel(settings.GoogleClientSecret),
			"google_folder":           settings.GoogleFolder,
			"google_authorized":       settings.GoogleToken.HasRefreshToken(),
			"google_token_expires_at": googleTokenExpiryString(settings.GoogleToken),
		},
		"runtime": getControllerBackupRuntimeStatus(),
		"paths": map[string]any{
			"data_dir":   dataPath,
			"backup_dir": backupPath,
		},
	}
	writeJSON(w, http.StatusOK, payload)
}

func mngBackupSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req mngBackupSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	req.GoogleClientSecret = resolveSubmittedGoogleClientSecret(req.GoogleClientSecret)
	settings, err := setBackupSettings(req.Enabled, req.LocalDir, req.SourceDirs, req.GoogleClientID, req.GoogleClientSecret, req.GoogleFolder)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"settings": map[string]any{
			"enabled":                 settings.Enabled,
			"local_dir":               settings.LocalDir,
			"source_dirs":             settings.SourceDirs,
			"google_client_id":        settings.GoogleClientID,
			"google_client_secret":    secretConfiguredLabel(settings.GoogleClientSecret),
			"google_folder":           settings.GoogleFolder,
			"google_authorized":       settings.GoogleToken.HasRefreshToken(),
			"google_token_expires_at": googleTokenExpiryString(settings.GoogleToken),
		},
	})
}

func mngBackupTestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := testGoogleDriveBackup(getBackupSettings()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func mngBackupRunHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	triggerAutoBackupControllerDataAsync("mng_manual")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true,
		"runtime":  getControllerBackupRuntimeStatus(),
	})
}

func mngBackupGoogleAuthStartHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req mngBackupGoogleAuthStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	req.GoogleClientSecret = resolveSubmittedGoogleClientSecret(req.GoogleClientSecret)
	session, err := startGoogleDriveDeviceAuth(req.GoogleClientID, req.GoogleClientSecret)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":                session.ID,
		"user_code":                 session.UserCode,
		"verification_url":          session.VerifyURL,
		"verification_url_complete": session.CompleteURL,
		"expires_at":                session.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"interval_sec":              session.IntervalSec,
	})
}

func mngBackupGoogleAuthPollHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req mngBackupGoogleAuthPollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	authorized, status, err := pollGoogleDriveDeviceAuth(req.SessionID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "status": status})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authorized": authorized,
		"status":     status,
	})
}

func mngBackupGoogleDisconnectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := clearGoogleDriveToken(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func secretConfiguredLabel(secret string) string {
	if strings.TrimSpace(secret) == "" {
		return ""
	}
	return "(已保存)"
}

func resolveSubmittedGoogleClientSecret(secret string) string {
	if strings.TrimSpace(secret) == secretConfiguredLabel("x") {
		return getBackupSettings().GoogleClientSecret
	}
	return secret
}

func googleTokenExpiryString(token googleDriveToken) string {
	if token.Expiry.IsZero() {
		return ""
	}
	return token.Expiry.UTC().Format("2006-01-02T15:04:05Z07:00")
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
