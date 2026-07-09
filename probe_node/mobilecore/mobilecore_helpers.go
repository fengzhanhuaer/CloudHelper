package mobilecore

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

const (
	mobileRouteConnectTimeout      = 12 * time.Second
	mobileRouteResponseReadTimeout = 10 * time.Second
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
