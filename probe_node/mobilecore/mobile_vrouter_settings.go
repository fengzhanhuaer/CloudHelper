package mobilecore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type mobileVRouteSettingsRequest struct {
	ExitNodes map[string]string `json:"exit_nodes"`
	Reset     bool              `json:"reset"`
}

type mobileVRouteSettingsGroup struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	ExitNodeID        string `json:"exit_node_id"`
	DefaultExitNodeID string `json:"default_exit_node_id"`
	LocalExitNodeID   string `json:"local_exit_node_id,omitempty"`
}

func loadMobileVRouteExitChoices(configDir string) map[string]string {
	if normalizeMobileConfigDir(configDir) == "" {
		return map[string]string{}
	}
	values := map[string]string{}
	if raw, err := os.ReadFile(filepath.Join(normalizeMobileConfigDir(configDir), "mobile_vroute_settings.json")); err == nil {
		if json.Unmarshal(raw, &values) != nil {
			return map[string]string{}
		}
	}
	return values
}

func VRouteLocalSettings(controllerURL string, nodeID string, nodeSecret string, configDir string) string {
	config, err := loadMobileVRouteConfig(configDir)
	if err != nil {
		config, err = refreshMobileVRouteConfig(controllerURL, nodeID, nodeSecret, configDir)
	}
	if err != nil {
		return marshalRouteJSON(map[string]any{"ok": false, "error": err.Error()})
	}
	return marshalRouteJSON(mobileVRouteLocalSettingsResponse(config, loadMobileVRouteExitChoices(configDir)))
}

func mobileVRouteLocalSettingsResponse(config mobileVRouteConfig, choices map[string]string) mobileVRouteSettingsResponse {
	response := mobileVRouteSettingsResponse{OK: true, Groups: []mobileVRouteSettingsGroup{}, Nodes: []mobileVRouteSettingsNode{}, UpdatedAt: config.UpdatedAt}
	for _, node := range config.ProbeIPs {
		response.Nodes = append(response.Nodes, mobileVRouteSettingsNode{NodeID: node.NodeID, DisplayName: node.DisplayName})
	}
	for _, rule := range config.RouteRules {
		if normalizeMobileVRouteRuleAction(rule.Action, rule.ExitNodeID) != "probe_exit" {
			continue
		}
		exit := rule.ExitNodeID
		if choices[rule.ID] != "" {
			exit = choices[rule.ID]
		}
		response.Groups = append(response.Groups, mobileVRouteSettingsGroup{ID: rule.ID, Name: rule.Name, ExitNodeID: exit, DefaultExitNodeID: rule.ExitNodeID, LocalExitNodeID: choices[rule.ID]})
	}
	return response
}

type mobileVRouteSettingsNode struct {
	NodeID      string `json:"node_id"`
	DisplayName string `json:"display_name"`
}

type mobileVRouteSettingsResponse struct {
	OK        bool                        `json:"ok"`
	Groups    []mobileVRouteSettingsGroup `json:"groups"`
	Nodes     []mobileVRouteSettingsNode  `json:"nodes"`
	UpdatedAt string                      `json:"updated_at,omitempty"`
	Message   string                      `json:"message,omitempty"`
	Warning   string                      `json:"warning,omitempty"`
}

func VRouteSettings(controllerURL string, nodeID string, nodeSecret string) string {
	response, err := requestMobileVRouteSettings(controllerURL, nodeID, nodeSecret, http.MethodGet, nil)
	if err != nil {
		return marshalRouteJSON(map[string]any{"ok": false, "error": err.Error()})
	}
	return marshalRouteJSON(response)
}

func SaveVRouteSettings(controllerURL string, nodeID string, nodeSecret string, configDir string, payloadJSON string) string {
	var request mobileVRouteSettingsRequest
	decoder := json.NewDecoder(strings.NewReader(payloadJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return marshalRouteJSON(map[string]any{"ok": false, "error": "invalid route settings: " + err.Error()})
	}
	if (request.ExitNodes == nil && !request.Reset) || (request.Reset && len(request.ExitNodes) > 0) {
		return marshalRouteJSON(map[string]any{"ok": false, "error": "exit_nodes is required"})
	}
	config, err := loadMobileVRouteConfig(configDir)
	if err != nil {
		return marshalRouteJSON(map[string]any{"ok": false, "error": err.Error()})
	}
	rules := map[string]bool{}
	nodes := map[string]bool{}
	for _, r := range config.RouteRules {
		rules[r.ID] = normalizeMobileVRouteRuleAction(r.Action, r.ExitNodeID) == "probe_exit"
	}
	for _, n := range config.ProbeIPs {
		nodes[n.NodeID] = true
	}
	choices := map[string]string{}
	previous := loadMobileVRouteExitChoices(configDir)
	if !request.Reset {
		for id, node := range request.ExitNodes {
			if !rules[id] || (node != "" && !nodes[node] && previous[id] != node) {
				return marshalRouteJSON(map[string]any{"ok": false, "error": "unknown proxy rule or exit node"})
			}
			if node != "" {
				choices[id] = node
			}
		}
	}
	if normalizeMobileConfigDir(configDir) == "" {
		return marshalRouteJSON(map[string]any{"ok": false, "error": "config dir is required"})
	}
	if err := persistMobileVRouteExitChoices(configDir, choices); err != nil {
		return marshalRouteJSON(map[string]any{"ok": false, "error": err.Error()})
	}
	// Existing carriers and streams remain attached; invalidate only routing
	// decisions, without dropping shared Fake IP mappings or controller config.
	config.LocalExitNodes = choices
	reconcileAndroidVPNDNSRoutes(config, time.Now())
	response := mobileVRouteLocalSettingsResponse(config, choices)
	response.Message = "出口选择已保存在本机"
	if request.Reset {
		response.Message = "已恢复主控默认出口"
	}
	androidLogStore.add("route", "normal", response.Message)
	return marshalRouteJSON(response)
}

func persistMobileVRouteExitChoices(configDir string, choices map[string]string) error {
	configDir = normalizeMobileConfigDir(configDir)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(choices)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(configDir, ".route-exits-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(configDir, "mobile_vroute_settings.json"))
}

func requestMobileVRouteSettings(controllerURL string, nodeID string, nodeSecret string, method string, payload any) (mobileVRouteSettingsResponse, error) {
	baseURL, err := normalizeControllerBaseURL(controllerURL)
	if err != nil {
		return mobileVRouteSettingsResponse{}, err
	}
	nodeID = strings.TrimSpace(nodeID)
	nodeSecret = strings.TrimSpace(nodeSecret)
	if nodeID == "" || nodeSecret == "" {
		return mobileVRouteSettingsResponse{}, errors.New("node ID and node secret are required")
	}
	setMobileVRouteControllerIdentity(baseURL, nodeID, nodeSecret)

	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return mobileVRouteSettingsResponse{}, err
		}
		body = strings.NewReader(string(raw))
	}
	ctx, cancel := context.WithTimeout(context.Background(), mobileVRouteConfigFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+mobileVRouteSettingsAPIPath, body)
	if err != nil {
		return mobileVRouteSettingsResponse{}, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := applyAuthHeaders(req, nodeID, nodeSecret); err != nil {
		return mobileVRouteSettingsResponse{}, err
	}
	resp, err := mobileControllerDo(req)
	if err != nil {
		return mobileVRouteSettingsResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return mobileVRouteSettingsResponse{}, fmt.Errorf("request route settings failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var response mobileVRouteSettingsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&response); err != nil {
		return mobileVRouteSettingsResponse{}, err
	}
	if !response.OK {
		return mobileVRouteSettingsResponse{}, errors.New("controller rejected route settings")
	}
	if response.Groups == nil {
		response.Groups = []mobileVRouteSettingsGroup{}
	}
	if response.Nodes == nil {
		response.Nodes = []mobileVRouteSettingsNode{}
	}
	return response, nil
}
