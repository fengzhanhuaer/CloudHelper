package backend

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/quic-go/quic-go/http3"
)

const (
	probeLinkInfoPath     = "/api/node/info"
	probeLinkHealthPath   = "/healthz"
	probeLinkTestPingPath = "/api/node/link-test/ping"
	probeLinkTimeout      = 8 * time.Second
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

type probeLinkTestPingResponse struct {
	OK        bool   `json:"ok"`
	NodeID    string `json:"node_id"`
	Protocol  string `json:"protocol"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

func (a *App) TestProbeLink(nodeID, endpointType, scheme, host string, port int) (ProbeLinkConnectResult, error) {
	return testProbeLink(nodeID, endpointType, scheme, host, port)
}

func testProbeLink(nodeID, endpointType, scheme, host string, port int) (ProbeLinkConnectResult, error) {
	protocol := normalizeProbeLinkTestProtocol(endpointType)
	if protocol != "" {
		return testProbeLinkByProtocol(nodeID, protocol, host, port)
	}

	// Backward compatibility for old service/public HTTP link checks.
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

func testProbeLinkByProtocol(nodeID string, protocol string, host string, port int) (ProbeLinkConnectResult, error) {
	switch protocol {
	case "http":
		return probeLinkHTTPPing(nodeID, protocol, host, port)
	case "https":
		return probeLinkHTTPPing(nodeID, protocol, host, port)
	case "http3":
		return probeLinkHTTP3Ping(nodeID, host, port)
	default:
		return ProbeLinkConnectResult{}, fmt.Errorf("unsupported protocol: %s", protocol)
	}
}

func probeLinkHTTPPing(nodeID string, protocol string, host string, port int) (ProbeLinkConnectResult, error) {
	targetURL, nonce, err := buildProbeLinkTestURL(host, port, protocol)
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}

	transport := &http.Transport{}
	if protocol == "https" {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	client := &http.Client{Timeout: probeLinkTimeout, Transport: transport}

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

	var data probeLinkTestPingResponse
	if len(strings.TrimSpace(string(body))) > 0 {
		_ = json.Unmarshal(body, &data)
	}
	message := strings.TrimSpace(data.Message)
	if message == "" {
		message = "probe link ping success"
	}
	if strings.TrimSpace(nonce) != "" {
		message = message + ", nonce=" + nonce
	}

	normalizedExpected := normalizeProbeLinkNodeID(nodeID)
	normalizedActual := normalizeProbeLinkNodeID(data.NodeID)
	if normalizedExpected != "" && normalizedActual != "" && normalizedExpected != normalizedActual {
		message = fmt.Sprintf("%s, but node_id mismatch: expected=%s actual=%s", message, normalizedExpected, normalizedActual)
	}

	return ProbeLinkConnectResult{
		OK:           true,
		NodeID:       firstNonEmptyString(normalizedActual, normalizedExpected),
		EndpointType: protocol,
		URL:          targetURL,
		StatusCode:   resp.StatusCode,
		Service:      "probe_link_test",
		Version:      "",
		Message:      message,
		ConnectedAt:  time.Now().UTC().Format(time.RFC3339),
		DurationMS:   time.Since(startedAt).Milliseconds(),
	}, nil
}

func probeLinkHTTP3Ping(nodeID string, host string, port int) (ProbeLinkConnectResult, error) {
	targetURL, nonce, err := buildProbeLinkTestURL(host, port, "http3")
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}

	transport := &http3.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13,
			NextProtos: []string{"h3"},
		},
	}
	defer transport.Close()

	client := &http.Client{
		Timeout:   probeLinkTimeout,
		Transport: transport,
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

	var data probeLinkTestPingResponse
	if len(strings.TrimSpace(string(body))) > 0 {
		_ = json.Unmarshal(body, &data)
	}
	message := strings.TrimSpace(data.Message)
	if message == "" {
		message = "probe link http3 ping success"
	}
	if strings.TrimSpace(nonce) != "" {
		message = message + ", nonce=" + nonce
	}

	normalizedExpected := normalizeProbeLinkNodeID(nodeID)
	normalizedActual := normalizeProbeLinkNodeID(data.NodeID)
	if normalizedExpected != "" && normalizedActual != "" && normalizedExpected != normalizedActual {
		message = fmt.Sprintf("%s, but node_id mismatch: expected=%s actual=%s", message, normalizedExpected, normalizedActual)
	}

	return ProbeLinkConnectResult{
		OK:           true,
		NodeID:       firstNonEmptyString(normalizedActual, normalizedExpected),
		EndpointType: "http3",
		URL:          targetURL,
		StatusCode:   resp.StatusCode,
		Service:      "probe_link_test",
		Version:      "",
		Message:      message,
		ConnectedAt:  time.Now().UTC().Format(time.RFC3339),
		DurationMS:   time.Since(startedAt).Milliseconds(),
	}, nil
}

func buildProbeLinkTestURL(host string, port int, protocol string) (string, string, error) {
	trimmedHost := strings.TrimSpace(host)
	if trimmedHost == "" {
		return "", "", fmt.Errorf("host is required")
	}
	if port <= 0 || port > 65535 {
		return "", "", fmt.Errorf("port must be between 1 and 65535")
	}

	nonce := strconv.FormatInt(time.Now().UnixNano(), 36)
	scheme := "https"
	if strings.EqualFold(strings.TrimSpace(protocol), "http") {
		scheme = "http"
	}
	target := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(trimmedHost, strconv.Itoa(port)),
		Path:   probeLinkTestPingPath,
	}
	query := target.Query()
	query.Set("nonce", nonce)
	target.RawQuery = query.Encode()
	return target.String(), nonce, nil
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

func normalizeProbeLinkTestProtocol(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "http":
		return "http"
	case "https":
		return "https"
	case "http3", "h3":
		return "http3"
	default:
		return ""
	}
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

// ProbeChainPingResult is the result of a chain connectivity test.
type ProbeChainPingResult struct {
	OK         bool   `json:"ok"`
	ChainID    string `json:"chain_id"`
	EntryHost  string `json:"entry_host"`
	EntryPort  int    `json:"entry_port"`
	LinkLayer  string `json:"link_layer"`
	DurationMS int64  `json:"duration_ms"`
	Message    string `json:"message"`
}

// PingProbeChain tests end-to-end chain connectivity from the manager.
// It resolves the chain entry endpoint and attempts a real relay hop connection,
// measuring the handshake latency. This is completely different from the probe
// link test (which tests individual relay service readiness on the probe node).
func (a *App) PingProbeChain(chainID string) (ProbeChainPingResult, error) {
	trimmedID := strings.TrimSpace(chainID)
	if trimmedID == "" {
		return ProbeChainPingResult{}, fmt.Errorf("chain_id is required")
	}

	a.networkAssistant.mu.RLock()
	baseURL := a.networkAssistant.controllerBaseURL
	token := a.networkAssistant.sessionToken
	a.networkAssistant.mu.RUnlock()

	if baseURL == "" || token == "" {
		return ProbeChainPingResult{}, fmt.Errorf("not connected to controller")
	}

	warnBuf := make([]string, 0)
	targets, _, err := fetchProbeChainTargetsViaAdminWS(baseURL, token, func(format string, args ...any) {
		warnBuf = append(warnBuf, fmt.Sprintf(format, args...))
	})
	if err != nil {
		return ProbeChainPingResult{}, fmt.Errorf("fetch chain targets failed: %w", err)
	}

	// Find the target matching our chain_id.
	var endpoint probeChainEndpoint
	found := false
	for _, t := range targets {
		if strings.TrimSpace(t.ChainID) == trimmedID {
			endpoint = t
			found = true
			break
		}
	}
	if !found {
		if len(warnBuf) > 0 {
			return ProbeChainPingResult{}, fmt.Errorf("chain entry not resolved: %s", strings.Join(warnBuf, "; "))
		}
		return ProbeChainPingResult{}, fmt.Errorf("chain not found: %s", trimmedID)
	}

	startedAt := time.Now()
	hop, err := openProbeChainRelayHop(endpoint)
	elapsed := time.Since(startedAt)
	if err != nil {
		return ProbeChainPingResult{
			OK:        false,
			ChainID:   trimmedID,
			EntryHost: endpoint.EntryHost,
			EntryPort: endpoint.EntryPort,
			LinkLayer: endpoint.LinkLayer,
			Message:   fmt.Sprintf("连接失败: %v", err),
		}, nil
	}
	// Close immediately — we only care about whether the connection can be established.
	if hop.CloseFn != nil {
		_ = hop.CloseFn()
	}

	return ProbeChainPingResult{
		OK:         true,
		ChainID:    trimmedID,
		EntryHost:  endpoint.EntryHost,
		EntryPort:  endpoint.EntryPort,
		LinkLayer:  endpoint.LinkLayer,
		DurationMS: elapsed.Milliseconds(),
		Message:    fmt.Sprintf("连接成功 (%dms)", elapsed.Milliseconds()),
	}, nil
}
