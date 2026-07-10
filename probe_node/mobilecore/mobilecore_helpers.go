package mobilecore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	mobileRouteConnectTimeout      = 12 * time.Second
	mobileRouteResponseReadTimeout = 10 * time.Second
	mobileDefaultConfigDirName     = "cloudhelper_config"
	mobileDefaultConfigDirEnv      = "CLOUDHELPER_MOBILE_CONFIG_DIR"
)

type mobileNodeIdentity struct {
	NodeID string
	Secret string
}

type androidRouteDecision struct {
	Direct          bool
	Reject          bool
	TargetAddr      string
	Group           string
	SelectedRouteID string
}

var mobileRouteConfigState = struct {
	mu        sync.Mutex
	configDir string
}{}

func setMobileRouteConfigDir(configDir string) {
	mobileRouteConfigState.mu.Lock()
	mobileRouteConfigState.configDir = strings.TrimSpace(configDir)
	mobileRouteConfigState.mu.Unlock()
}

func mobileRouteConfigDir() string {
	mobileRouteConfigState.mu.Lock()
	defer mobileRouteConfigState.mu.Unlock()
	return mobileRouteConfigState.configDir
}

func normalizeMobileConfigDir(configDir string) string {
	if clean := strings.TrimSpace(configDir); clean != "" {
		return clean
	}
	if current := mobileRouteConfigDir(); current != "" {
		return current
	}
	if env := strings.TrimSpace(os.Getenv(mobileDefaultConfigDirEnv)); env != "" {
		return env
	}
	if dir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, mobileDefaultConfigDirName)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, "."+mobileDefaultConfigDirName)
	}
	return filepath.Join(os.TempDir(), mobileDefaultConfigDirName)
}

func normalizeMobileRouteNodeID(id string) string {
	return strings.TrimSpace(id)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func marshalRouteJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return `{"ok":false,"error":"json marshal failed"}`
	}
	return string(data)
}
