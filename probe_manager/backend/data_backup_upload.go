package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	managerBackupUploadMaxArchiveSize = 64 * 1024 * 1024
)

func tryUploadManagerBackupArchive(archivePath string) error {
	archivePath = strings.TrimSpace(archivePath)
	if archivePath == "" {
		return errors.New("archive path is required")
	}

	baseURL, err := resolveManagerBackupControllerBaseURL()
	if err != nil {
		return err
	}
	if baseURL == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	sessionToken, err := loginControllerForBackupUpload(ctx, baseURL)
	if err != nil {
		return err
	}

	return uploadManagerBackupArchiveViaAdminWS(baseURL, sessionToken, archivePath)
}

func resolveManagerBackupControllerBaseURL() (string, error) {
	config, _, err := loadManagerGlobalConfig()
	if err != nil {
		return "", err
	}
	rawURL := strings.TrimSpace(config.ControllerURL)
	if rawURL == "" {
		rawURL = defaultControllerURL
	}
	return normalizeControllerBaseURL(rawURL)
}

func loginControllerForBackupUpload(ctx context.Context, baseURL string) (string, error) {
	nonceURL := strings.TrimRight(baseURL, "/") + "/api/auth/nonce"
	nonceReq, err := http.NewRequestWithContext(ctx, http.MethodGet, nonceURL, nil)
	if err != nil {
		return "", err
	}
	nonceReq.Header.Set("Accept", "application/json")
	nonceReq.Header.Set("X-Forwarded-Proto", "https")

	nonceResp, err := http.DefaultClient.Do(nonceReq)
	if err != nil {
		return "", err
	}
	defer nonceResp.Body.Close()

	if nonceResp.StatusCode < 200 || nonceResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(nonceResp.Body, 4096))
		return "", fmt.Errorf("request nonce failed: status=%d body=%s", nonceResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var noncePayload struct {
		Nonce string `json:"nonce"`
	}
	if err := json.NewDecoder(io.LimitReader(nonceResp.Body, 8192)).Decode(&noncePayload); err != nil {
		return "", err
	}
	nonce := strings.TrimSpace(noncePayload.Nonce)
	if nonce == "" {
		return "", errors.New("nonce is empty")
	}

	signature, err := signNonceWithLocalKey(nonce)
	if err != nil {
		return "", err
	}

	loginURL := strings.TrimRight(baseURL, "/") + "/api/auth/login"
	loginBody, err := json.Marshal(map[string]string{
		"nonce":     nonce,
		"signature": signature,
	})
	if err != nil {
		return "", err
	}
	loginReq, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, bytes.NewReader(loginBody))
	if err != nil {
		return "", err
	}
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Accept", "application/json")
	loginReq.Header.Set("X-Forwarded-Proto", "https")

	loginResp, err := http.DefaultClient.Do(loginReq)
	if err != nil {
		return "", err
	}
	defer loginResp.Body.Close()

	if loginResp.StatusCode < 200 || loginResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(loginResp.Body, 4096))
		return "", fmt.Errorf("login failed: status=%d body=%s", loginResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var loginPayload struct {
		SessionToken string `json:"session_token"`
	}
	if err := json.NewDecoder(io.LimitReader(loginResp.Body, 8192)).Decode(&loginPayload); err != nil {
		return "", err
	}
	token := strings.TrimSpace(loginPayload.SessionToken)
	if token == "" {
		return "", errors.New("session token is empty")
	}
	return token, nil
}

func uploadManagerBackupArchiveViaAdminWS(baseURL, sessionToken, archivePath string) error {
	wsURL, err := buildAdminWSURL(baseURL)
	if err != nil {
		return err
	}

	info, err := os.Stat(archivePath)
	if err != nil {
		return err
	}
	if info.Size() <= 0 {
		return errors.New("backup archive is empty")
	}
	if info.Size() > managerBackupUploadMaxArchiveSize {
		return fmt.Errorf("backup archive too large: %d > %d bytes", info.Size(), managerBackupUploadMaxArchiveSize)
	}

	archiveContent, err := os.ReadFile(archivePath)
	if err != nil {
		return err
	}

	controllerIP := loadManagerControllerIP()
	dialer := buildControllerWSDialer(baseURL, controllerIP)
	headers := http.Header{}
	headers.Set("X-Forwarded-Proto", "https")
	conn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return fmt.Errorf("admin ws handshake failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return err
	}
	defer conn.Close()

	deadline := time.Now().Add(90 * time.Second)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}

	authID := fmt.Sprintf("backup-auth-%d", time.Now().UnixNano())
	authReq := adminWSRequest{
		ID:      authID,
		Action:  "auth.session",
		Payload: map[string]string{"token": strings.TrimSpace(sessionToken)},
	}
	if err := conn.WriteJSON(authReq); err != nil {
		return err
	}
	authResp, err := readAdminWSResponseByID(conn, authID)
	if err != nil {
		return err
	}
	if !authResp.OK {
		return fmt.Errorf("admin ws auth failed: %s", strings.TrimSpace(authResp.Error))
	}

	uploadID := fmt.Sprintf("backup-upload-%d", time.Now().UnixNano())
	uploadReq := adminWSRequest{
		ID:     uploadID,
		Action: "admin.manager.backup.upload",
		Payload: map[string]string{
			"archive_name":   filepath.Base(archivePath),
			"archive_base64": base64.StdEncoding.EncodeToString(archiveContent),
		},
	}
	if err := conn.WriteJSON(uploadReq); err != nil {
		return err
	}
	uploadResp, err := readAdminWSResponseByID(conn, uploadID)
	if err != nil {
		return err
	}
	if !uploadResp.OK {
		return fmt.Errorf("upload manager backup failed: %s", strings.TrimSpace(uploadResp.Error))
	}
	return nil
}
