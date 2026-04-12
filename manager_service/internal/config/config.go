// Package config loads and validates manager_service configuration.
// RQ-002: service MUST only listen on 127.0.0.1:16033.
package config

import (
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
		if err := json.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("parse config file: %w", err)
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

// resolveDataDir returns the directory where manager_service stores its state.
// Priority: env MANAGER_SERVICE_DATA_DIR → exe-relative "data" → "./data".
func resolveDataDir() (string, error) {
	if env := strings.TrimSpace(os.Getenv("MANAGER_SERVICE_DATA_DIR")); env != "" {
		if err := os.MkdirAll(env, 0o755); err != nil {
			return "", fmt.Errorf("create data dir from env: %w", err)
		}
		return env, nil
	}

	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "data"))
	}
	candidates = append(candidates, filepath.Join(".", "data"))

	for _, dir := range candidates {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			return dir, nil
		}
	}

	return "", errors.New("cannot create data directory")
}
