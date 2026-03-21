package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	probeLinkInfoPath   = "/api/node/info"
	probeLinkHealthPath = "/healthz"
	probeLinkTimeout    = 5 * time.Second
)

type ProbeLinkConnectResult struct {
	OK           bool   `json:"ok"`
	NodeID       string `json:"node_id"`
	EndpointType string `json:"endpoint_type"`
	URL          string `json:"url"`
	StatusCode   int    `json:"status_code"`
	Service      string `json:"service"`
	Version      string `json:"version"`
	Message      string `json:"message"`
	ConnectedAt  string `json:"connected_at"`
	DurationMS   int64  `json:"duration_ms"`
}

type probeNodeInfoResponse struct {
	Service   string `json:"service"`
	NodeID    string `json:"node_id"`
	Version   string `json:"version"`
	Timestamp string `json:"timestamp"`
}

func (a *App) TestProbeLink(nodeID, endpointType, scheme, host string, port int) (ProbeLinkConnectResult, error) {
	return testProbeLink(nodeID, endpointType, scheme, host, port)
}

func testProbeLink(nodeID, endpointType, scheme, host string, port int) (ProbeLinkConnectResult, error) {
	normalizedType := strings.ToLower(strings.TrimSpace(endpointType))
	if normalizedType != "public" {
		normalizedType = "service"
	}

	normalizedScheme := normalizeProbeLinkScheme(scheme)
	trimmedHost := strings.TrimSpace(host)
	if trimmedHost == "" {
		return ProbeLinkConnectResult{}, fmt.Errorf("host is required")
	}
	if port <= 0 || port > 65535 {
		return ProbeLinkConnectResult{}, fmt.Errorf("port must be between 1 and 65535")
	}

	client := &http.Client{Timeout: probeLinkTimeout}
	paths := []string{probeLinkInfoPath, probeLinkHealthPath}
	var lastErr error
	for _, candidatePath := range paths {
		result, err := probeLinkRequest(client, strings.TrimSpace(nodeID), normalizedType, normalizedScheme, trimmedHost, port, candidatePath)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("probe link test failed")
	}
	return ProbeLinkConnectResult{}, lastErr
}

func probeLinkRequest(client *http.Client, nodeID, endpointType, scheme, host string, port int, pathValue string) (ProbeLinkConnectResult, error) {
	targetURL, err := buildProbeLinkURL(scheme, host, port, pathValue)
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}

	startedAt := time.Now()
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ProbeLinkConnectResult{}, fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	info := probeNodeInfoResponse{}
	if len(strings.TrimSpace(string(body))) > 0 {
		_ = json.Unmarshal(body, &info)
	}

	message := "probe link connected"
	normalizedExpected := normalizeProbeLinkNodeID(nodeID)
	normalizedActual := normalizeProbeLinkNodeID(info.NodeID)
	if normalizedExpected != "" && normalizedActual != "" && normalizedExpected != normalizedActual {
		message = fmt.Sprintf("probe link connected, but node_id mismatch: expected=%s actual=%s", normalizedExpected, normalizedActual)
	}

	return ProbeLinkConnectResult{
		OK:           true,
		NodeID:       firstNonEmptyString(strings.TrimSpace(info.NodeID), normalizedExpected),
		EndpointType: endpointType,
		URL:          targetURL,
		StatusCode:   resp.StatusCode,
		Service:      strings.TrimSpace(info.Service),
		Version:      strings.TrimSpace(info.Version),
		Message:      message,
		ConnectedAt:  time.Now().UTC().Format(time.RFC3339),
		DurationMS:   time.Since(startedAt).Milliseconds(),
	}, nil
}

func buildProbeLinkURL(scheme, host string, port int, pathValue string) (string, error) {
	normalizedScheme := normalizeProbeLinkScheme(scheme)
	trimmedHost := strings.TrimSpace(host)
	if trimmedHost == "" {
		return "", fmt.Errorf("host is required")
	}
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("port must be between 1 and 65535")
	}

	target := &url.URL{
		Scheme: normalizedScheme,
		Host:   net.JoinHostPort(trimmedHost, strconv.Itoa(port)),
		Path:   strings.TrimSpace(pathValue),
	}
	return target.String(), nil
}

func normalizeProbeLinkScheme(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "https" {
		return "https"
	}
	return "http"
}

func normalizeProbeLinkNodeID(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if n, err := strconv.Atoi(value); err == nil && n > 0 {
		return strconv.Itoa(n)
	}
	return value
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
