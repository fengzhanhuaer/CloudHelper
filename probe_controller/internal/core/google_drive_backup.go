package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultBackupGoogleFolder = "CloudHelper/controller"
	googleDriveScopeDriveFile = "https://www.googleapis.com/auth/drive.file"

	googleOAuthDeviceCodeURL = "https://oauth2.googleapis.com/device/code"
	googleOAuthTokenURL      = "https://oauth2.googleapis.com/token"
	googleDriveFilesURL      = "https://www.googleapis.com/drive/v3/files"
	googleDriveUploadURL     = "https://www.googleapis.com/upload/drive/v3/files"
)

type googleDriveToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"expiry"`
}

func (t googleDriveToken) HasRefreshToken() bool {
	return strings.TrimSpace(t.RefreshToken) != ""
}

func (t googleDriveToken) HasUsableAccessToken() bool {
	return strings.TrimSpace(t.AccessToken) != "" && time.Now().Before(t.Expiry.Add(-1*time.Minute))
}

type googleDeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURL         string `json:"verification_url"`
	VerificationURLComplete string `json:"verification_url_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type googleTokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

type googleDriveAuthSession struct {
	ID           string
	ClientID     string
	ClientSecret string
	DeviceCode   string
	UserCode     string
	VerifyURL    string
	CompleteURL  string
	ExpiresAt    time.Time
	IntervalSec  int
	CreatedAt    time.Time
}

var googleDriveAuthSessions = struct {
	mu   sync.Mutex
	data map[string]googleDriveAuthSession
}{data: map[string]googleDriveAuthSession{}}

func parseGoogleDriveTokenFromStore(raw any) googleDriveToken {
	if raw == nil {
		return googleDriveToken{}
	}
	var token googleDriveToken
	switch typed := raw.(type) {
	case googleDriveToken:
		return typed
	case map[string]any:
		if value, _ := typed["access_token"].(string); value != "" {
			token.AccessToken = strings.TrimSpace(value)
		}
		if value, _ := typed["refresh_token"].(string); value != "" {
			token.RefreshToken = strings.TrimSpace(value)
		}
		if value, _ := typed["token_type"].(string); value != "" {
			token.TokenType = strings.TrimSpace(value)
		}
		if value, _ := typed["expiry"].(string); value != "" {
			if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value)); err == nil {
				token.Expiry = parsed
			}
		}
	case map[string]string:
		token.AccessToken = strings.TrimSpace(typed["access_token"])
		token.RefreshToken = strings.TrimSpace(typed["refresh_token"])
		token.TokenType = strings.TrimSpace(typed["token_type"])
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(typed["expiry"])); err == nil {
			token.Expiry = parsed
		}
	}
	return token
}

func normalizeGoogleDriveFolderPath(raw string) string {
	path := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	path = strings.Trim(path, "/")
	if path == "" {
		return defaultBackupGoogleFolder
	}
	parts := []string{}
	for _, part := range strings.Split(path, "/") {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return defaultBackupGoogleFolder
	}
	return strings.Join(parts, "/")
}

func startGoogleDriveDeviceAuth(clientID string, clientSecret string) (googleDriveAuthSession, error) {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	if clientID == "" {
		return googleDriveAuthSession{}, fmt.Errorf("google client id is required")
	}
	if clientSecret == "" {
		return googleDriveAuthSession{}, fmt.Errorf("google client secret is required for device authorization; paste the OAuth JSON or fill OAuth Client Secret")
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", googleDriveScopeDriveFile)
	req, err := http.NewRequest(http.MethodPost, googleOAuthDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return googleDriveAuthSession{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return googleDriveAuthSession{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return googleDriveAuthSession{}, fmt.Errorf("google device auth failed: %s", strings.TrimSpace(string(body)))
	}
	var payload googleDeviceCodeResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return googleDriveAuthSession{}, err
	}
	if strings.TrimSpace(payload.DeviceCode) == "" || strings.TrimSpace(payload.UserCode) == "" {
		return googleDriveAuthSession{}, fmt.Errorf("google device auth response is invalid")
	}

	sessionID := randomHexID(18)
	if payload.Interval <= 0 {
		payload.Interval = 5
	}
	if payload.ExpiresIn <= 0 {
		payload.ExpiresIn = 1800
	}
	session := googleDriveAuthSession{
		ID:           sessionID,
		ClientID:     clientID,
		ClientSecret: strings.TrimSpace(clientSecret),
		DeviceCode:   strings.TrimSpace(payload.DeviceCode),
		UserCode:     strings.TrimSpace(payload.UserCode),
		VerifyURL:    strings.TrimSpace(payload.VerificationURL),
		CompleteURL:  strings.TrimSpace(payload.VerificationURLComplete),
		ExpiresAt:    time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
		IntervalSec:  payload.Interval,
		CreatedAt:    time.Now(),
	}

	googleDriveAuthSessions.mu.Lock()
	now := time.Now()
	for key, item := range googleDriveAuthSessions.data {
		if now.After(item.ExpiresAt) {
			delete(googleDriveAuthSessions.data, key)
		}
	}
	googleDriveAuthSessions.data[sessionID] = session
	googleDriveAuthSessions.mu.Unlock()
	return session, nil
}

func pollGoogleDriveDeviceAuth(sessionID string) (bool, string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, "", fmt.Errorf("session_id is required")
	}
	googleDriveAuthSessions.mu.Lock()
	session, ok := googleDriveAuthSessions.data[sessionID]
	if ok && time.Now().After(session.ExpiresAt) {
		delete(googleDriveAuthSessions.data, sessionID)
		ok = false
	}
	googleDriveAuthSessions.mu.Unlock()
	if !ok {
		return false, "", fmt.Errorf("google auth session not found or expired")
	}

	token, pending, err := exchangeGoogleDriveDeviceCode(session)
	if pending || err != nil {
		return false, "pending", err
	}
	if err := saveGoogleDriveToken(token); err != nil {
		return false, "", err
	}
	googleDriveAuthSessions.mu.Lock()
	delete(googleDriveAuthSessions.data, sessionID)
	googleDriveAuthSessions.mu.Unlock()
	return true, "authorized", nil
}

func exchangeGoogleDriveDeviceCode(session googleDriveAuthSession) (googleDriveToken, bool, error) {
	form := googleDeviceCodeTokenForm(session, true)
	resp, body, err := postGoogleOAuthForm(form)
	if err != nil {
		return googleDriveToken{}, false, err
	}
	if googleTokenExchangeShouldRetryWithoutSecret(resp.StatusCode, body, session.ClientSecret) {
		resp, body, err = postGoogleOAuthForm(googleDeviceCodeTokenForm(session, false))
		if err != nil {
			return googleDriveToken{}, false, err
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload googleTokenResponse
		_ = json.Unmarshal(body, &payload)
		switch payload.Error {
		case "authorization_pending", "slow_down":
			return googleDriveToken{}, true, nil
		case "expired_token":
			return googleDriveToken{}, false, fmt.Errorf("google authorization expired")
		case "access_denied":
			return googleDriveToken{}, false, fmt.Errorf("google authorization denied; if the OAuth app is in Testing, add this Google account under Google Auth Platform > Audience > Test users")
		default:
			msg := firstNonEmptyBackupString(payload.ErrorDesc, strings.TrimSpace(string(body)))
			lowerMsg := strings.ToLower(msg)
			if strings.Contains(lowerMsg, "missing required parameter") && strings.Contains(lowerMsg, "client_secret") {
				return googleDriveToken{}, false, fmt.Errorf("google token exchange failed: client_secret is missing; paste the OAuth JSON again or fill OAuth Client Secret")
			}
			return googleDriveToken{}, false, fmt.Errorf("google token exchange failed: %s", msg)
		}
	}
	return parseGoogleTokenResponse(body, ""), false, nil
}

func googleDeviceCodeTokenForm(session googleDriveAuthSession, includeSecret bool) url.Values {
	form := url.Values{}
	form.Set("client_id", session.ClientID)
	if includeSecret && strings.TrimSpace(session.ClientSecret) != "" {
		form.Set("client_secret", session.ClientSecret)
	}
	form.Set("device_code", session.DeviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	return form
}

func googleTokenExchangeShouldRetryWithoutSecret(statusCode int, body []byte, clientSecret string) bool {
	if strings.TrimSpace(clientSecret) == "" || statusCode < 400 {
		return false
	}
	var payload googleTokenResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	errCode := strings.TrimSpace(payload.Error)
	errDesc := strings.ToLower(strings.TrimSpace(payload.ErrorDesc))
	if errCode != "invalid_client" {
		return false
	}
	return strings.Contains(errDesc, "client_secret") &&
		(strings.Contains(errDesc, "not allowed") ||
			strings.Contains(errDesc, "must not") ||
			strings.Contains(errDesc, "should not"))
}

func refreshGoogleDriveAccessToken(settings backupSettings) (googleDriveToken, error) {
	if strings.TrimSpace(settings.GoogleClientID) == "" {
		return googleDriveToken{}, fmt.Errorf("google client id is required")
	}
	if !settings.GoogleToken.HasRefreshToken() {
		return googleDriveToken{}, fmt.Errorf("google drive is not authorized")
	}
	form := url.Values{}
	form.Set("client_id", settings.GoogleClientID)
	if strings.TrimSpace(settings.GoogleClientSecret) != "" {
		form.Set("client_secret", settings.GoogleClientSecret)
	}
	form.Set("refresh_token", settings.GoogleToken.RefreshToken)
	form.Set("grant_type", "refresh_token")
	resp, body, err := postGoogleOAuthForm(form)
	if err != nil {
		return googleDriveToken{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return googleDriveToken{}, fmt.Errorf("google token refresh failed: %s", strings.TrimSpace(string(body)))
	}
	token := parseGoogleTokenResponse(body, settings.GoogleToken.RefreshToken)
	if err := saveGoogleDriveToken(token); err != nil {
		return googleDriveToken{}, err
	}
	return token, nil
}

func postGoogleOAuthForm(form url.Values) (*http.Response, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp, body, nil
}

func parseGoogleTokenResponse(body []byte, fallbackRefreshToken string) googleDriveToken {
	var payload googleTokenResponse
	_ = json.Unmarshal(body, &payload)
	refreshToken := strings.TrimSpace(payload.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(fallbackRefreshToken)
	}
	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return googleDriveToken{
		AccessToken:  strings.TrimSpace(payload.AccessToken),
		RefreshToken: refreshToken,
		TokenType:    firstNonEmptyBackupString(strings.TrimSpace(payload.TokenType), "Bearer"),
		Expiry:       time.Now().Add(time.Duration(expiresIn) * time.Second),
	}
}

func saveGoogleDriveToken(token googleDriveToken) error {
	if Store == nil {
		return fmt.Errorf("store is not initialized")
	}
	Store.mu.Lock()
	Store.Data[backupGoogleTokenStoreField] = map[string]string{
		"access_token":  strings.TrimSpace(token.AccessToken),
		"refresh_token": strings.TrimSpace(token.RefreshToken),
		"token_type":    firstNonEmptyBackupString(strings.TrimSpace(token.TokenType), "Bearer"),
		"expiry":        token.Expiry.UTC().Format(time.RFC3339),
	}
	Store.mu.Unlock()
	return Store.SaveWithoutAutoBackup()
}

func clearGoogleDriveToken() error {
	if Store == nil {
		return fmt.Errorf("store is not initialized")
	}
	Store.mu.Lock()
	delete(Store.Data, backupGoogleTokenStoreField)
	Store.Data[backupEnabledStoreField] = false
	Store.mu.Unlock()
	return Store.SaveWithoutAutoBackup()
}

func uploadGoogleDriveBackupArchive(archivePath string, settings backupSettings) error {
	token, err := ensureGoogleDriveAccessToken(settings)
	if err != nil {
		return err
	}
	folderID, err := ensureGoogleDriveFolderPath(token.AccessToken, settings.GoogleFolder)
	if err != nil {
		return err
	}
	return uploadGoogleDriveFileResumable(token.AccessToken, folderID, archivePath)
}

func testGoogleDriveBackup(settings backupSettings) error {
	token, err := ensureGoogleDriveAccessToken(settings)
	if err != nil {
		return err
	}
	_, err = ensureGoogleDriveFolderPath(token.AccessToken, settings.GoogleFolder)
	return err
}

func ensureGoogleDriveAccessToken(settings backupSettings) (googleDriveToken, error) {
	if settings.GoogleToken.HasUsableAccessToken() {
		return settings.GoogleToken, nil
	}
	return refreshGoogleDriveAccessToken(settings)
}

func ensureGoogleDriveFolderPath(accessToken string, folderPath string) (string, error) {
	parentID := "root"
	for _, part := range strings.Split(normalizeGoogleDriveFolderPath(folderPath), "/") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		folderID, err := findGoogleDriveChildFolder(accessToken, parentID, part)
		if err != nil {
			return "", err
		}
		if folderID == "" {
			folderID, err = createGoogleDriveChildFolder(accessToken, parentID, part)
			if err != nil {
				return "", err
			}
		}
		parentID = folderID
	}
	return parentID, nil
}

func findGoogleDriveChildFolder(accessToken string, parentID string, name string) (string, error) {
	q := fmt.Sprintf("mimeType='application/vnd.google-apps.folder' and name='%s' and '%s' in parents and trashed=false", escapeGoogleDriveQueryString(name), escapeGoogleDriveQueryString(parentID))
	params := url.Values{}
	params.Set("q", q)
	params.Set("spaces", "drive")
	params.Set("fields", "files(id,name)")
	reqURL := googleDriveFilesURL + "?" + params.Encode()
	body, err := googleDriveJSONRequest(accessToken, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	var payload struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if len(payload.Files) == 0 {
		return "", nil
	}
	return strings.TrimSpace(payload.Files[0].ID), nil
}

func createGoogleDriveChildFolder(accessToken string, parentID string, name string) (string, error) {
	payload := map[string]any{
		"name":     name,
		"mimeType": "application/vnd.google-apps.folder",
		"parents":  []string{parentID},
	}
	params := url.Values{}
	params.Set("fields", "id")
	body, err := googleDriveJSONRequest(accessToken, http.MethodPost, googleDriveFilesURL+"?"+params.Encode(), payload)
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.ID) == "" {
		return "", fmt.Errorf("google drive folder create response is invalid")
	}
	return strings.TrimSpace(out.ID), nil
}

func uploadGoogleDriveFileResumable(accessToken string, parentID string, localPath string) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("google drive upload path is a directory: %s", localPath)
	}
	fileName := filepath.Base(localPath)
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	metadata := map[string]any{
		"name":    fileName,
		"parents": []string{parentID},
	}
	metaBytes, _ := json.Marshal(metadata)
	params := url.Values{}
	params.Set("uploadType", "resumable")
	params.Set("fields", "id,name,webViewLink")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleDriveUploadURL+"?"+params.Encode(), bytes.NewReader(metaBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("X-Upload-Content-Type", contentType)
	req.Header.Set("X-Upload-Content-Length", fmt.Sprintf("%d", info.Size()))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	initBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("google drive upload session create failed: %s", strings.TrimSpace(string(initBody)))
	}
	uploadURL := strings.TrimSpace(resp.Header.Get("Location"))
	if uploadURL == "" {
		return fmt.Errorf("google drive upload session location is empty")
	}

	in, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer in.Close()
	uploadCtx, uploadCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer uploadCancel()
	uploadReq, err := http.NewRequestWithContext(uploadCtx, http.MethodPut, uploadURL, in)
	if err != nil {
		return err
	}
	uploadReq.Header.Set("Content-Type", contentType)
	uploadReq.ContentLength = info.Size()
	uploadResp, err := http.DefaultClient.Do(uploadReq)
	if err != nil {
		return err
	}
	defer uploadResp.Body.Close()
	uploadBody, _ := io.ReadAll(io.LimitReader(uploadResp.Body, 1<<20))
	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
		return fmt.Errorf("google drive upload failed: %s", strings.TrimSpace(string(uploadBody)))
	}
	return nil
}

func googleDriveJSONRequest(accessToken string, method string, reqURL string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("google drive api failed: %s", strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

func escapeGoogleDriveQueryString(value string) string {
	return strings.ReplaceAll(value, "'", "\\'")
}

func randomHexID(byteLen int) string {
	if byteLen <= 0 {
		byteLen = 16
	}
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func firstNonEmptyBackupString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func googleDriveAuthSessionForStatus(sessionID string) (googleDriveAuthSession, bool) {
	googleDriveAuthSessions.mu.Lock()
	defer googleDriveAuthSessions.mu.Unlock()
	session, ok := googleDriveAuthSessions.data[strings.TrimSpace(sessionID)]
	return session, ok
}

var errGoogleAuthPending = errors.New("google authorization pending")
