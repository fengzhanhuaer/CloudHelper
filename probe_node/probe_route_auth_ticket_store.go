package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const probeRouteAuthTicketCacheFileName = "probe_route_auth_ticket.json"

type probeRouteAuthTicketCacheFile struct {
	UpdatedAt string            `json:"updated_at"`
	Items     map[string]string `json:"items"`
}

func resolveProbeRouteAuthTicketCachePath() (string, error) {
	dataPath, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataPath, probeRouteAuthTicketCacheFileName), nil
}

func persistProbeRouteAuthTicketSnapshot(items map[string]string) error {
	cachePath, err := resolveProbeRouteAuthTicketCachePath()
	if err != nil {
		return err
	}
	clean := make(map[string]string, len(items))
	for routeID, ticket := range items {
		id := strings.TrimSpace(routeID)
		value := strings.TrimSpace(ticket)
		if id == "" || value == "" {
			continue
		}
		clean[id] = value
	}
	payload := probeRouteAuthTicketCacheFile{
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

func loadProbeRouteAuthTicketSnapshot() (map[string]string, error) {
	cachePath, err := resolveProbeRouteAuthTicketCachePath()
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
	var payload probeRouteAuthTicketCacheFile
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, fmt.Errorf("decode route auth ticket cache: %w", err)
	}
	clean := make(map[string]string, len(payload.Items))
	for routeID, ticket := range payload.Items {
		id := strings.TrimSpace(routeID)
		value := strings.TrimSpace(ticket)
		if id == "" || value == "" {
			continue
		}
		clean[id] = value
	}
	return clean, nil
}
