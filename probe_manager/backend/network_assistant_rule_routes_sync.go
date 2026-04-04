package backend

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	managerRuleRoutesUploadAction = "admin.manager.rule_routes.upload"
	managerRuleRoutesFileName     = "rule_routes.txt"
	managerRuleRoutesUploadMax    = 2 * 1024 * 1024
)

func (a *App) UploadNetworkAssistantRuleRoutes(controllerBaseURL, sessionToken string) (string, error) {
	routing, err := loadOrCreateTunnelRuleRouting()
	if err != nil {
		return "", err
	}

	rulePath := strings.TrimSpace(routing.RuleFilePath)
	if rulePath == "" {
		return "", errors.New("rule_routes path is empty")
	}

	content, err := os.ReadFile(rulePath)
	if err != nil {
		return "", fmt.Errorf("read rule_routes failed: %w", err)
	}
	if len(content) == 0 {
		return "", errors.New("rule_routes.txt is empty")
	}
	if len(content) > managerRuleRoutesUploadMax {
		return "", fmt.Errorf("rule_routes.txt too large: %d > %d bytes", len(content), managerRuleRoutesUploadMax)
	}

	if err := uploadRuleRoutesViaAdminWS(controllerBaseURL, sessionToken, content); err != nil {
		return "", err
	}

	return "上传成功", nil
}

func uploadRuleRoutesViaAdminWS(baseURL, sessionToken string, content []byte) error {
	wsURL, err := buildAdminWSURL(baseURL)
	if err != nil {
		return err
	}

	dialer := buildControllerWSDialer(baseURL)
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

	deadline := time.Now().Add(45 * time.Second)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}

	authID := fmt.Sprintf("na-rule-routes-auth-%d", time.Now().UnixNano())
	authReq := adminWSRequest{ID: authID, Action: "auth.session", Payload: map[string]string{"token": strings.TrimSpace(sessionToken)}}
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

	uploadID := fmt.Sprintf("na-rule-routes-upload-%d", time.Now().UnixNano())
	uploadReq := adminWSRequest{
		ID:     uploadID,
		Action: managerRuleRoutesUploadAction,
		Payload: map[string]string{
			"file_name":      managerRuleRoutesFileName,
			"content_base64": base64.StdEncoding.EncodeToString(content),
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
		return fmt.Errorf("upload rule_routes failed: %s", strings.TrimSpace(uploadResp.Error))
	}
	return nil
}
