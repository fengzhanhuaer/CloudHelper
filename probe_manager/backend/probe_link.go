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

var probeLinkTryPingExistingMux = func(service *networkAssistantService, nodeID string) (time.Duration, bool) {
	if service == nil {
		return 0, false
	}
	return service.tryPingExistingMux(nodeID)
}

var probeLinkEnsureMuxForNode = func(service *networkAssistantService, nodeID string) error {
	if service == nil {
		return fmt.Errorf("network assistant service is nil")
	}
	_, err := service.ensureTunnelMuxClientForNode(nodeID)
	return err
}

var probeLinkEnableOnDemandMux = false

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
	targetID := normalizeProbeChainPingTargetID(chainID)
	if targetID == "" {
		return ProbeChainPingResult{}, fmt.Errorf("chain_id is required")
	}

	candidateChainIDs, explicitChainTarget := buildProbeChainPingCandidateChainIDs(targetID)
	resolvedChainID := targetID
	if len(candidateChainIDs) > 0 {
		resolvedChainID = candidateChainIDs[0]
	}

	// 非 chain 目标（如 cloudserver）仅使用本地已有连接，不新建到主控的连接。
	if !explicitChainTarget {
		return pingNetworkAssistantTunnelNode(a.networkAssistant, targetID)
	}

	// 优先使用内存缓存（启动时从 probe_chain.json 加载，运行时由 refreshAvailableNodes 刷新）。
	// 避免每次测试都发起 WebSocket 请求到主控。
	a.networkAssistant.mu.RLock()
	cachedTargets := copyProbeChainTargets(a.networkAssistant.chainTargets)
	a.networkAssistant.mu.RUnlock()

	var endpoint probeChainEndpoint
	found := false
	for _, candidateID := range candidateChainIDs {
		if key := buildChainTargetNodeID(candidateID); key != "" {
			if item, ok := cachedTargets[key]; ok {
				endpoint = item
				resolvedChainID = strings.TrimSpace(item.ChainID)
				found = true
				break
			}
		}
	}
	if !found {
		candidateSet := make(map[string]struct{}, len(candidateChainIDs))
		for _, candidateID := range candidateChainIDs {
			clean := strings.ToLower(strings.TrimSpace(candidateID))
			if clean == "" {
				continue
			}
			candidateSet[clean] = struct{}{}
		}
		for _, t := range cachedTargets {
			if _, ok := candidateSet[strings.ToLower(strings.TrimSpace(t.ChainID))]; ok {
				endpoint = t
				resolvedChainID = strings.TrimSpace(t.ChainID)
				found = true
				break
			}
		}
	}

	if !found {
		return ProbeChainPingResult{}, fmt.Errorf("chain not found in local cache: %s", targetID)
	}
	startedAt := time.Now()

	// 优先复用已有 mux 连接（yamux Ping，代表保活已建立）
	if rtt, ok := a.networkAssistant.tryPingExistingMux(endpoint.TargetID); ok {
		return ProbeChainPingResult{
			OK:         true,
			ChainID:    resolvedChainID,
			EntryHost:  endpoint.EntryHost,
			EntryPort:  endpoint.EntryPort,
			LinkLayer:  endpoint.LinkLayer,
			DurationMS: rtt.Milliseconds(),
			Message:    fmt.Sprintf("连接成功（保活已建立）(%dms)", rtt.Milliseconds()),
		}, nil
	}

	// 默认不在“测试链路”里按需建链。近期修改引入这里后，会进入较深的建链路径，
	// 一旦底层存在等待或锁竞争，前端就会表现为长时间卡住。
	if probeLinkEnableOnDemandMux {
		if err := probeLinkEnsureMuxForNode(a.networkAssistant, endpoint.TargetID); err == nil {
			if rtt, ok := a.networkAssistant.tryPingExistingMux(endpoint.TargetID); ok {
				return ProbeChainPingResult{
					OK:         true,
					ChainID:    resolvedChainID,
					EntryHost:  endpoint.EntryHost,
					EntryPort:  endpoint.EntryPort,
					LinkLayer:  endpoint.LinkLayer,
					DurationMS: rtt.Milliseconds(),
					Message:    fmt.Sprintf("连接成功（按需建链并建立保活）(%dms)", rtt.Milliseconds()),
				}, nil
			}
		}
	}

	// 兜底：仅验证入口可达性（不代表保活建立）。
	hop, err := openProbeChainRelayHop(endpoint)
	elapsed := time.Since(startedAt)
	if err != nil {
		return ProbeChainPingResult{
			OK:        false,
			ChainID:   resolvedChainID,
			EntryHost: endpoint.EntryHost,
			EntryPort: endpoint.EntryPort,
			LinkLayer: endpoint.LinkLayer,
			Message:   fmt.Sprintf("连接失败: %v", err),
		}, nil
	}
	if hop.CloseFn != nil {
		_ = hop.CloseFn()
	}

	return ProbeChainPingResult{
		OK:         true,
		ChainID:    resolvedChainID,
		EntryHost:  endpoint.EntryHost,
		EntryPort:  endpoint.EntryPort,
		LinkLayer:  endpoint.LinkLayer,
		DurationMS: elapsed.Milliseconds(),
		Message:    fmt.Sprintf("入口可达（保活未建立）(%dms)", elapsed.Milliseconds()),
	}, nil
}

func normalizeProbeChainPingTargetID(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.TrimLeft(value, "\ufeff\u200b\u2060")
	value = strings.Trim(value, "\"'`")
	value = strings.TrimSpace(value)
	replacer := strings.NewReplacer("：", ":", "／", "/")
	value = replacer.Replace(value)
	return strings.TrimSpace(value)
}

func buildProbeChainPingCandidateChainIDs(targetID string) ([]string, bool) {
	cleanTarget := normalizeProbeChainPingTargetID(targetID)
	ids := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	add := func(raw string) {
		value := normalizeProbeChainPingTargetID(raw)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		ids = append(ids, value)
	}

	explicitChainTarget := false
	if chainID, ok := parseChainTargetNodeID(cleanTarget); ok {
		explicitChainTarget = true
		add(chainID)
		add(cleanTarget)
	}

	lower := strings.ToLower(cleanTarget)
	if strings.HasPrefix(lower, "chain://") {
		explicitChainTarget = true
		add(strings.TrimSpace(cleanTarget[len("chain://"):]))
		add(cleanTarget)
	}

	if !explicitChainTarget {
		if idx := strings.Index(lower, chainTargetNodePrefix); idx >= 0 {
			tail := strings.TrimSpace(cleanTarget[idx+len(chainTargetNodePrefix):])
			if tail != "" {
				explicitChainTarget = true
				add(tail)
				add(cleanTarget)
			}
		}
	}

	if len(ids) == 0 {
		add(cleanTarget)
	}
	return ids, explicitChainTarget
}

func pingNetworkAssistantTunnelNode(service *networkAssistantService, nodeID string) (ProbeChainPingResult, error) {
	trimmedNodeID := strings.TrimSpace(nodeID)
	if trimmedNodeID == "" {
		return ProbeChainPingResult{}, fmt.Errorf("node_id is required")
	}

	if service == nil {
		return ProbeChainPingResult{
			OK:        false,
			ChainID:   trimmedNodeID,
			LinkLayer: "ws",
			Message:   "连接失败: 本地无可复用链路",
		}, nil
	}

	// 1) 优先复用已有 mux 连接（yamux Ping）
	if rtt, ok := probeLinkTryPingExistingMux(service, trimmedNodeID); ok {
		return ProbeChainPingResult{
			OK:         true,
			ChainID:    trimmedNodeID,
			LinkLayer:  "ws",
			DurationMS: rtt.Milliseconds(),
			Message:    fmt.Sprintf("连接成功（已有连接）(%dms)", rtt.Milliseconds()),
		}, nil
	}

	// 默认不在“测试链路”里触发按需建链，避免测试动作进入完整建链流程导致界面卡住。
	if probeLinkEnableOnDemandMux {
		if err := probeLinkEnsureMuxForNode(service, trimmedNodeID); err != nil {
			return ProbeChainPingResult{
				OK:        false,
				ChainID:   trimmedNodeID,
				LinkLayer: "ws",
				Message:   fmt.Sprintf("连接失败: 本地无可复用链路，按需建链失败: %v", err),
			}, nil
		}
		if rtt, ok := probeLinkTryPingExistingMux(service, trimmedNodeID); ok {
			return ProbeChainPingResult{
				OK:         true,
				ChainID:    trimmedNodeID,
				LinkLayer:  "ws",
				DurationMS: rtt.Milliseconds(),
				Message:    fmt.Sprintf("连接成功（按需建链）(%dms)", rtt.Milliseconds()),
			}, nil
		}
		return ProbeChainPingResult{
			OK:        false,
			ChainID:   trimmedNodeID,
			LinkLayer: "ws",
			Message:   "连接失败: 本地无可复用链路，按需建链后链路仍不可用",
		}, nil
	}

	return ProbeChainPingResult{
		OK:        false,
		ChainID:   trimmedNodeID,
		LinkLayer: "ws",
		Message:   "连接失败: 本地无可复用链路",
	}, nil
}
