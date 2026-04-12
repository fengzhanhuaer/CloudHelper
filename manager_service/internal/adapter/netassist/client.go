// Package netassist provides a thin proxy adapter for the network assistant HTTP API.
// It proxies requests to the running network assistant service inside probe_manager
// (which remains frozen per RQ-008).
// PKG-W2-03 / RQ-004
package netassist

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const clientTimeout = 10 * time.Second

// Client is a proxy client for the network assistant service endpoint.
// It communicates over localhost with the probe_manager backend.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a Client pointing to probe_manager's base URL.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient: &http.Client{Timeout: clientTimeout},
	}
}

// GetStatus proxies GET /api/network-assistant/status
func (c *Client) GetStatus(ctx context.Context, sessionToken string) (json.RawMessage, error) {
	return c.get(ctx, "/api/network-assistant/status", sessionToken)
}

// SwitchMode proxies POST /api/network-assistant/mode
func (c *Client) SwitchMode(ctx context.Context, sessionToken, mode string) (json.RawMessage, error) {
	m := strings.TrimSpace(mode)
	if m == "" {
		return nil, fmt.Errorf("mode is required")
	}
	payload, _ := json.Marshal(map[string]string{"mode": m})
	return c.post(ctx, "/api/network-assistant/mode", sessionToken, payload)
}

// GetLogs proxies GET /api/network-assistant/logs?lines=N
func (c *Client) GetLogs(ctx context.Context, sessionToken string, lines int) (json.RawMessage, error) {
	path := fmt.Sprintf("/api/network-assistant/logs?lines=%d", lines)
	return c.get(ctx, path, sessionToken)
}

// GetDNSCache proxies GET /api/network-assistant/dns/cache?query=Q
func (c *Client) GetDNSCache(ctx context.Context, sessionToken, query string) (json.RawMessage, error) {
	path := "/api/network-assistant/dns/cache"
	if query != "" {
		path += "?query=" + query
	}
	return c.get(ctx, path, sessionToken)
}

// GetProcesses proxies GET /api/network-assistant/processes
func (c *Client) GetProcesses(ctx context.Context, sessionToken string) (json.RawMessage, error) {
	return c.get(ctx, "/api/network-assistant/processes", sessionToken)
}

// StartMonitor proxies POST /api/network-assistant/monitor/start
func (c *Client) StartMonitor(ctx context.Context, sessionToken string) (json.RawMessage, error) {
	return c.post(ctx, "/api/network-assistant/monitor/start", sessionToken, nil)
}

// StopMonitor proxies POST /api/network-assistant/monitor/stop
func (c *Client) StopMonitor(ctx context.Context, sessionToken string) (json.RawMessage, error) {
	return c.post(ctx, "/api/network-assistant/monitor/stop", sessionToken, nil)
}

// ClearMonitorEvents proxies POST /api/network-assistant/monitor/clear
func (c *Client) ClearMonitorEvents(ctx context.Context, sessionToken string) (json.RawMessage, error) {
	return c.post(ctx, "/api/network-assistant/monitor/clear", sessionToken, nil)
}

// GetMonitorEvents proxies GET /api/network-assistant/monitor/events
func (c *Client) GetMonitorEvents(ctx context.Context, sessionToken string, since int64) (json.RawMessage, error) {
	path := fmt.Sprintf("/api/network-assistant/monitor/events?since=%d", since)
	return c.get(ctx, path, sessionToken)
}

// InstallTUN proxies POST /api/network-assistant/tun/install
func (c *Client) InstallTUN(ctx context.Context, sessionToken string) (json.RawMessage, error) {
	return c.post(ctx, "/api/network-assistant/tun/install", sessionToken, nil)
}

// EnableTUN proxies POST /api/network-assistant/tun/enable
func (c *Client) EnableTUN(ctx context.Context, sessionToken string) (json.RawMessage, error) {
	return c.post(ctx, "/api/network-assistant/tun/enable", sessionToken, nil)
}

// RestoreDirect proxies POST /api/network-assistant/direct/restore
func (c *Client) RestoreDirect(ctx context.Context, sessionToken string) (json.RawMessage, error) {
	return c.post(ctx, "/api/network-assistant/direct/restore", sessionToken, nil)
}

// GetRuleConfig proxies GET /api/network-assistant/rules
func (c *Client) GetRuleConfig(ctx context.Context, sessionToken string) (json.RawMessage, error) {
	return c.get(ctx, "/api/network-assistant/rules", sessionToken)
}

// SetRulePolicy proxies POST /api/network-assistant/rules/policy
func (c *Client) SetRulePolicy(ctx context.Context, sessionToken, group, action, tunnelNodeID string) (json.RawMessage, error) {
	payload, _ := json.Marshal(map[string]string{
		"group":         group,
		"action":        action,
		"tunnelNodeID":  tunnelNodeID,
	})
	return c.post(ctx, "/api/network-assistant/rules/policy", sessionToken, payload)
}

// ─── internal helpers ────────────────────────────────────────────────────────

func (c *Client) get(ctx context.Context, path, token string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req, token)
	return c.do(req)
}

func (c *Client) post(ctx context.Context, path, token string, body []byte) (json.RawMessage, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.setHeaders(req, token)
	return c.do(req)
}

func (c *Client) setHeaders(req *http.Request, token string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func (c *Client) do(req *http.Request) (json.RawMessage, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.RawMessage(raw), nil
}
