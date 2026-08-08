package mobilecore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type mobileVRouteSettingsRequest struct {
	ExitNodes map[string]string `json:"exit_nodes"`
}

type mobileVRouteSettingsGroup struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ExitNodeID string `json:"exit_node_id"`
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
	if err := json.Unmarshal([]byte(strings.TrimSpace(payloadJSON)), &request); err != nil {
		return marshalRouteJSON(map[string]any{"ok": false, "error": "invalid route settings: " + err.Error()})
	}
	if request.ExitNodes == nil {
		return marshalRouteJSON(map[string]any{"ok": false, "error": "exit_nodes is required"})
	}
	response, err := requestMobileVRouteSettings(controllerURL, nodeID, nodeSecret, http.MethodPost, request)
	if err != nil {
		return marshalRouteJSON(map[string]any{"ok": false, "error": err.Error()})
	}
	if _, err := refreshConfigFiles(controllerURL, nodeID, nodeSecret, configDir); err != nil {
		response.Warning = "主控已保存，但本机配置刷新失败：" + err.Error()
		response.Message = response.Warning
		androidLogStore.add("route", "warn", response.Warning)
		return marshalRouteJSON(response)
	}
	resetAndroidVPNRouteCaches(configDir)
	closeMobileVRouteTrackedFlows("route_settings_updated")
	androidLogStore.add("route", "normal", fmt.Sprintf("controller route exit settings saved: groups=%d", len(request.ExitNodes)))
	if strings.TrimSpace(response.Message) == "" {
		response.Message = "路由设置已保存到主控"
	}
	return marshalRouteJSON(response)
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
	resp, err := http.DefaultClient.Do(req)
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
