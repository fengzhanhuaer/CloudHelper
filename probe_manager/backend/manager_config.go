package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	managerGlobalConfigFile = "manager_global_config.json"
	defaultControllerURL    = "http://127.0.0.1:15030"
)

type managerGlobalConfig struct {
	ControllerURL        string `json:"controller_url"`
	// ControllerIP 为可选的主控 IP，配置后连接主控时直接使用该 IP，跳过 DNS 解析。
	// 格式为纯 IPv4 或 IPv6 地址，不含端口，留空则正常走 DNS。
	ControllerIP         string `json:"controller_ip"`
	AIDebugListenEnabled bool   `json:"ai_debug_listen_enabled"`
}

func (a *App) GetGlobalControllerURL() (string, error) {
	config, _, err := loadManagerGlobalConfig()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(config.ControllerURL)
	if value == "" {
		return defaultControllerURL, nil
	}
	return value, nil
}

// GetGlobalControllerIP 返回当前配置的主控 IP（可能为空）。
func (a *App) GetGlobalControllerIP() (string, error) {
	config, _, err := loadManagerGlobalConfig()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(config.ControllerIP), nil
}

// SetGlobalControllerIP 设置主控 IP 并持久化配置。传入空字符串则清除配置。
func (a *App) SetGlobalControllerIP(ip string) (string, error) {
	value := strings.TrimSpace(ip)

	config, configPath, err := loadManagerGlobalConfig()
	if err != nil {
		return "", err
	}
	config.ControllerIP = value

	if err := writeManagerGlobalConfig(configPath, config); err != nil {
		return "", err
	}
	return value, nil
}

// loadManagerControllerIP 直接读取配置文件中的主控 IP，供内部模块使用。
// 返回空字符串表示未配置，调用方应走正常 DNS 流程。
func loadManagerControllerIP() string {
	config, _, err := loadManagerGlobalConfig()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(config.ControllerIP)
}

func (a *App) SetGlobalControllerURL(rawURL string) (string, error) {
	value := strings.TrimSpace(rawURL)
	if value == "" {
		value = defaultControllerURL
	}

	config, configPath, err := loadManagerGlobalConfig()
	if err != nil {
		return "", err
	}
	config.ControllerURL = value

	if err := writeManagerGlobalConfig(configPath, config); err != nil {
		return "", err
	}
	return value, nil
}

func (a *App) GetAIDebugListenEnabled() (bool, error) {
	config, _, err := loadManagerGlobalConfig()
	if err != nil {
		return false, err
	}
	return config.AIDebugListenEnabled, nil
}

func (a *App) SetAIDebugListenEnabled(enabled bool) (bool, error) {
	config, configPath, err := loadManagerGlobalConfig()
	if err != nil {
		return false, err
	}
	config.AIDebugListenEnabled = enabled
	if err := writeManagerGlobalConfig(configPath, config); err != nil {
		return false, err
	}
	if enabled {
		if err := a.startAIDebugServer(); err != nil {
			return false, err
		}
		logManagerInfof("AI debug listen enabled in global config")
	} else {
		if err := a.stopAIDebugServer(); err != nil {
			return false, err
		}
		logManagerInfof("AI debug listen disabled in global config")
	}
	return enabled, nil
}

func loadManagerGlobalConfig() (managerGlobalConfig, string, error) {
	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return managerGlobalConfig{}, "", err
	}
	configPath := filepath.Join(dataDir, managerGlobalConfigFile)

	raw, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			defaultConfig := managerGlobalConfig{ControllerURL: defaultControllerURL, ControllerIP: "", AIDebugListenEnabled: false}
			if writeErr := writeManagerGlobalConfig(configPath, defaultConfig); writeErr != nil {
				return managerGlobalConfig{}, configPath, writeErr
			}
			return defaultConfig, configPath, nil
		}
		return managerGlobalConfig{}, configPath, err
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		defaultConfig := managerGlobalConfig{ControllerURL: defaultControllerURL, ControllerIP: "", AIDebugListenEnabled: false}
		if writeErr := writeManagerGlobalConfig(configPath, defaultConfig); writeErr != nil {
			return managerGlobalConfig{}, configPath, writeErr
		}
		return defaultConfig, configPath, nil
	}

	var config managerGlobalConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return managerGlobalConfig{}, configPath, fmt.Errorf("failed to parse global config: %w", err)
	}
	if strings.TrimSpace(config.ControllerURL) == "" {
		config.ControllerURL = defaultControllerURL
	}
	return config, configPath, nil
}

func writeManagerGlobalConfig(configPath string, config managerGlobalConfig) error {
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(configPath, raw, 0o644); err != nil {
		return err
	}
	if err := autoBackupManagerData(); err != nil {
		return err
	}
	return nil
}
