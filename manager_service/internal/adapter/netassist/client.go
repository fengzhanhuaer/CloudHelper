// Package netassist provides a thin proxy adapter for the network assistant HTTP API.
// It proxies status and mode-switch requests to the running network assistant service
// inside probe_manager (which remains frozen per RQ-008).
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

// StatusResponse is the network assistant status payload forwarded to the caller.
type StatusResponse struct {
	// Raw is the unmodified JSON blob returned by probe_manager.
	// Prevents drift between manager_service and probe_manager field definitions.
	Raw json.RawMessage `json:"raw"`
}

// ModeRequest is the body for a mode switch.
type ModeRequest struct {
	Mode string `json:"mode"`
}

// Client is a proxy client for the network assistant service endpoint.
// It communicates over localhost with the probe_manager backend.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a Client pointing to managerBaseURL (e.g. http://127.0.0.1:16033).
// For W2 this is expected to talk to probe_manager's internal API surface; in W3
// it will be replaced with direct calls once the network assistant is migrated.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient: &http.Client{Timeout: clientTimeout},
	}
}

// GetStatus returns the current network assistant status.
func (c *Client) GetStatus(ctx context.Context, sessionToken string) (json.RawMessage, error) {
	return c.get(ctx, "/api/network-assistant/status", sessionToken)
}

// SwitchMode sends a mode switch request to the network assistant.
func (c *Client) SwitchMode(ctx context.Context, sessionToken, mode string) (json.RawMessage, error) {
	m := strings.TrimSpace(mode)
	if m == "" {
		return nil, fmt.Errorf("mode is required")
	}
	payload, _ := json.Marshal(ModeRequest{Mode: m})
	return c.post(ctx, "/api/network-assistant/mode", sessionToken, payload)
}

func (c *Client) get(ctx context.Context, path, token string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req, token)
	return c.do(req)
}

func (c *Client) post(ctx context.Context, path, token string, body []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path,
		strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
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
