package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

func (a *App) GetGlobalControllerIP() (string, error) {
	config, _, err := loadManagerGlobalConfig()
	if err != nil {
		return "", err
	}
	return sanitizeControllerIP(config.ControllerIP), nil
}

func (a *App) SetGlobalControllerIP(rawIP string) (string, error) {
	value, err := validateControllerIP(rawIP)
	if err != nil {
		return "", err
	}

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
	config.ControllerIP = sanitizeControllerIP(config.ControllerIP)
	return config, configPath, nil
}

func sanitizeControllerIP(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	return ip.String()
}

func validateControllerIP(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return "", errors.New("controller_ip must be a valid IP address")
	}
	return ip.String(), nil
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
