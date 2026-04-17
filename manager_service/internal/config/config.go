// Package config loads and validates manager_service configuration.
// RQ-002: service MUST only listen on 127.0.0.1:16033.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MandatoryListenAddr is the only permitted listen address (RQ-002).
	MandatoryListenAddr = "127.0.0.1:16033"

	configFileName = "manager_service_config.json"
)

// Config holds runtime configuration for manager_service.
type Config struct {
	// ListenAddr is always 127.0.0.1:16033 — not configurable by the user.
	ListenAddr string `json:"listen_addr"`

	// ControllerURL is the base URL of probe_controller (default: http://127.0.0.1:15030).
	ControllerURL string `json:"controller_url"`

	// DataDir is where credentials and state files are stored.
	DataDir string `json:"data_dir"`
}

const defaultControllerURL = "http://127.0.0.1:15030"

// Load reads the config file from the data directory, applies defaults,
// and enforces the mandatory listen address.
func Load() (*Config, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return nil, fmt.Errorf("resolve data dir: %w", err)
	}

	cfg := &Config{
		ListenAddr:    MandatoryListenAddr,
		ControllerURL: defaultControllerURL,
		DataDir:       dataDir,
	}

	cfgPath := filepath.Join(dataDir, configFileName)
	raw, err := os.ReadFile(cfgPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	if len(raw) > 0 {
		raw = stripUTF8BOM(raw)
		if err := json.Unmarshal(raw, cfg); err != nil {
			fallbackRaw := stripTrailingPowerShellLiteralEOL(raw)
			if bytes.Equal(fallbackRaw, raw) {
				return nil, fmt.Errorf("parse config file: %w", err)
			}
			if err2 := json.Unmarshal(fallbackRaw, cfg); err2 != nil {
				return nil, fmt.Errorf("parse config file: %w", err)
			}
		}
	}

	// Enforce mandatory listen address regardless of file content.
	cfg.ListenAddr = MandatoryListenAddr

	if strings.TrimSpace(cfg.ControllerURL) == "" {
		cfg.ControllerURL = defaultControllerURL
	}

	// Persist defaults on first run.
	if errors.Is(err, os.ErrNotExist) {
		if writeErr := writeConfig(cfgPath, cfg); writeErr != nil {
			// Non-fatal: log warning upstream, but still proceed.
			_ = writeErr
		}
	}

	return cfg, nil
}

// Save writes the current config back to disk.
func Save(cfg *Config) error {
	cfgPath := filepath.Join(cfg.DataDir, configFileName)
	return writeConfig(cfgPath, cfg)
}

func writeConfig(path string, cfg *Config) error {
	// Always clamp listen addr before writing.
	cfg.ListenAddr = MandatoryListenAddr

	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

func stripUTF8BOM(raw []byte) []byte {
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		return raw[3:]
	}
	return raw
}

func stripTrailingPowerShellLiteralEOL(raw []byte) []byte {
	cleaned := bytes.TrimRight(raw, " \t\r\n")
	for {
		switched := false
		for _, suffix := range [][]byte{[]byte("`r`n"), []byte("`n"), []byte("`r")} {
			if bytes.HasSuffix(cleaned, suffix) {
				cleaned = bytes.TrimRight(cleaned[:len(cleaned)-len(suffix)], " \t\r\n")
				switched = true
			}
		}
		if !switched {
			break
		}
	}
	return cleaned
}

// resolveDataDir returns the directory where manager_service stores its state.
// Constraint: data MUST be stored under install directory (exe-dir/data),
// and MUST NOT be redirected to external paths.
func resolveDataDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}

	installDir := filepath.Dir(exe)
	dataDir := filepath.Join(installDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("create install data dir: %w", err)
	}
	return dataDir, nil
}
