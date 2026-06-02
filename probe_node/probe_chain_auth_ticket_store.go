package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const probeChainAuthTicketCacheFileName = "probe_chain_auth_ticket.json"

type probeChainAuthTicketCacheFile struct {
	UpdatedAt string            `json:"updated_at"`
	Items     map[string]string `json:"items"`
}

func resolveProbeChainAuthTicketCachePath() (string, error) {
	dataPath, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataPath, probeChainAuthTicketCacheFileName), nil
}

func persistProbeChainAuthTicketSnapshot(items map[string]string) error {
	cachePath, err := resolveProbeChainAuthTicketCachePath()
	if err != nil {
		return err
	}
	clean := make(map[string]string, len(items))
	for chainID, ticket := range items {
		id := strings.TrimSpace(chainID)
		value := strings.TrimSpace(ticket)
		if id == "" || value == "" {
			continue
		}
		clean[id] = value
	}
	payload := probeChainAuthTicketCacheFile{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Items:     clean,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cachePath, append(encoded, '\n'), 0o600)
}

func loadProbeChainAuthTicketSnapshot() (map[string]string, error) {
	cachePath, err := resolveProbeChainAuthTicketCachePath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return map[string]string{}, nil
	}
	var payload probeChainAuthTicketCacheFile
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, fmt.Errorf("decode chain auth ticket cache: %w", err)
	}
	clean := make(map[string]string, len(payload.Items))
	for chainID, ticket := range payload.Items {
		id := strings.TrimSpace(chainID)
		value := strings.TrimSpace(ticket)
		if id == "" || value == "" {
			continue
		}
		clean[id] = value
	}
	return clean, nil
}
