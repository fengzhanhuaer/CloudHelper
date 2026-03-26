package core

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	managerRuleRoutesFileName = "rule_routes.txt"
	managerRuleRoutesMaxBytes = 2 * 1024 * 1024
)

type adminManagerRuleRoutesUploadRequest struct {
	FileName      string `json:"file_name"`
	Content       string `json:"content"`
	ContentBase64 string `json:"content_base64"`
}

type adminManagerRuleRoutesDownloadRequest struct {
	FileName string `json:"file_name"`
}

func handleAdminWSManagerRuleRoutesUpload(payload json.RawMessage) (interface{}, error) {
	var req adminManagerRuleRoutesUploadRequest
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
	}

	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = managerRuleRoutesFileName
	}
	if !strings.EqualFold(fileName, managerRuleRoutesFileName) {
		return nil, fmt.Errorf("unsupported file_name: %s", fileName)
	}

	content, err := decodeManagerRuleRoutesContent(req.Content, req.ContentBase64)
	if err != nil {
		return nil, err
	}
	if len(content) > managerRuleRoutesMaxBytes {
		return nil, fmt.Errorf("rule_routes.txt exceeds size limit (%d bytes)", managerRuleRoutesMaxBytes)
	}

	username, _, _ := currentIdentityClaims()
	targetDir := filepath.Join(dataDir, sanitizeManagerBackupUsername(username))
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}

	targetPath := filepath.Join(targetDir, managerRuleRoutesFileName)
	if err := os.WriteFile(targetPath, content, 0o644); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"message":     "rule_routes uploaded",
		"file_name":   managerRuleRoutesFileName,
		"target_path": targetPath,
		"size":        len(content),
		"updated_at":  time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func handleAdminWSManagerRuleRoutesDownload(payload json.RawMessage) (interface{}, error) {
	var req adminManagerRuleRoutesDownloadRequest
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload")
		}
	}

	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = managerRuleRoutesFileName
	}
	if !strings.EqualFold(fileName, managerRuleRoutesFileName) {
		return nil, fmt.Errorf("unsupported file_name: %s", fileName)
	}

	username, _, _ := currentIdentityClaims()
	targetPath := filepath.Join(dataDir, sanitizeManagerBackupUsername(username), managerRuleRoutesFileName)
	content, err := os.ReadFile(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("rule_routes.txt not found in controller backup")
		}
		return nil, err
	}

	if len(content) > managerRuleRoutesMaxBytes {
		return nil, fmt.Errorf("rule_routes.txt exceeds size limit (%d bytes)", managerRuleRoutesMaxBytes)
	}

	return map[string]interface{}{
		"file_name":      managerRuleRoutesFileName,
		"target_path":    targetPath,
		"size":           len(content),
		"content_base64": base64.StdEncoding.EncodeToString(content),
	}, nil
}

func decodeManagerRuleRoutesContent(content, contentBase64 string) ([]byte, error) {
	if strings.TrimSpace(contentBase64) != "" {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(contentBase64))
		if err != nil {
			return nil, fmt.Errorf("invalid content_base64")
		}
		if len(decoded) == 0 {
			return nil, errors.New("content is empty")
		}
		return decoded, nil
	}

	text := strings.ReplaceAll(content, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("content is required")
	}
	return []byte(text), nil
}
