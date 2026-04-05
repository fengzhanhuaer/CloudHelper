package backend

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/quic-go/quic-go/http3"
)

const (
	probeLinkInfoPath           = "/api/node/info"
	probeLinkHealthPath         = "/healthz"
	probeLinkTestPingPath       = "/api/node/link-test/ping"
	probeLinkTimeout            = 8 * time.Second
	controlPlaneBypassStageName = "control_plane_bypass"
)

const (
	probeLinkStagePrepare     = "prepare"
	probeLinkStageDNSStart    = "dns_start"
	probeLinkStageDNSSuccess  = "dns_success"
	probeLinkStageConnect     = "connect"
	probeLinkStageConnected   = "connected"
	probeLinkStageTLSStart    = "tls_start"
	probeLinkStageTLSSuccess  = "tls_success"
	probeLinkStageRequest     = "request"
	probeLinkStageResponse    = "response"
	probeLinkStageBodyRead    = "body_read"
	probeLinkStageSuccess     = "success"
	probeLinkStageFailure     = "failure"
)

var probeLinkTryPingExistingMux = func(service *networkAssistantService, nodeID string) (time.Duration, bool) {
	if service == nil {
		return 0, false
	}
	return service.tryPingExistingMux(nodeID)
}


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

type probeLinkProgressSnapshot struct {
	Stage      string `json:"stage"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	URL        string `json:"url,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	ElapsedMS  int64  `json:"elapsed_ms,omitempty"`
	UpdatedAt  string `json:"updated_at"`
}

type probeLinkResolvedTarget struct {
	OriginalHost string
	DialHost     string
	IPs          []string
}

type probeLinkProgressReporter struct {
	service        *networkAssistantService
	nodeID         string
	host           string
	resolvedTarget probeLinkResolvedTarget
	port           int
	protocol       string
	targetURL      string
	startedAt      time.Time
}

func newProbeLinkProgressReporter(service *networkAssistantService, nodeID, protocol, host string, port int) *probeLinkProgressReporter {
	trimmedHost := strings.TrimSpace(host)
	return &probeLinkProgressReporter{
		service: service,
		nodeID:  strings.TrimSpace(nodeID),
		host:    trimmedHost,
		resolvedTarget: probeLinkResolvedTarget{
			OriginalHost: trimmedHost,
			DialHost:     trimmedHost,
		},
		port:      port,
		protocol:  strings.TrimSpace(protocol),
		startedAt: time.Now(),
	}
}

func (r *probeLinkProgressReporter) withURL(targetURL string) {
	if r == nil {
		return
	}
	r.targetURL = strings.TrimSpace(targetURL)
}

func (r *probeLinkProgressReporter) setResolvedTarget(target probeLinkResolvedTarget) {
	if r == nil {
		return
	}
	if strings.TrimSpace(target.OriginalHost) == "" {
		target.OriginalHost = r.host
	}
	if strings.TrimSpace(target.DialHost) == "" {
		target.DialHost = target.OriginalHost
	}
	r.resolvedTarget = target
}

func (r *probeLinkProgressReporter) stage(stage string, status string, detail string) {
	if r == nil {
		return
	}
	trimmedStatus := strings.TrimSpace(status)
	trimmedDetail := strings.TrimSpace(detail)
	elapsed := time.Since(r.startedAt).Milliseconds()
	snapshot := probeLinkProgressSnapshot{
		Stage:     strings.TrimSpace(stage),
		Status:    trimmedStatus,
		Detail:    trimmedDetail,
		URL:       r.targetURL,
		Host:      r.host,
		Port:      r.port,
		Protocol:  r.protocol,
		NodeID:    r.nodeID,
		ElapsedMS: elapsed,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if r.service != nil {
		payload, err := json.Marshal(snapshot)
		if err == nil {
			r.service.setTunnelStatus("probe_link_progress " + string(payload))
		}
		r.service.logf("probe link progress: stage=%s protocol=%s target=%s:%d url=%s elapsed=%dms status=%s detail=%s", snapshot.Stage, snapshot.Protocol, snapshot.Host, snapshot.Port, snapshot.URL, snapshot.ElapsedMS, snapshot.Status, snapshot.Detail)
	}
}

func (r *probeLinkProgressReporter) classifyError(err error) string {
	if err == nil {
		return ""
	}
	if isProbeLinkDNSResolveError(err) {
		return "dns 解析异常: " + strings.TrimSpace(err.Error())
	}
	if isProbeLinkConnectError(err) {
		return "连接异常: " + strings.TrimSpace(err.Error())
	}
	if isProbeLinkTimeoutError(err) {
		return "请求超时: " + strings.TrimSpace(err.Error())
	}
	return strings.TrimSpace(err.Error())
}

func probeLinkNetworkAssistantService(app *App) *networkAssistantService {
	if app != nil && app.networkAssistant != nil {
		return app.networkAssistant
	}
	if globalNetworkAssistantService != nil {
		return globalNetworkAssistantService
	}
	return nil
}

func (a *App) TestProbeLink(nodeID, endpointType, scheme, host string, port int) (ProbeLinkConnectResult, error) {
	return testProbeLinkWithProgress(probeLinkNetworkAssistantService(a), nodeID, endpointType, scheme, host, port)
}

func testProbeLink(nodeID, endpointType, scheme, host string, port int) (ProbeLinkConnectResult, error) {
	return testProbeLinkWithProgress(nil, nodeID, endpointType, scheme, host, port)
}

func testProbeLinkWithProgress(service *networkAssistantService, nodeID, endpointType, scheme, host string, port int) (ProbeLinkConnectResult, error) {
	protocol := normalizeProbeLinkTestProtocol(endpointType)
	reporterProtocol := protocol
	if reporterProtocol == "" {
		reporterProtocol = normalizeProbeLinkScheme(scheme)
	}
	reporter := newProbeLinkProgressReporter(service, nodeID, reporterProtocol, host, port)
	reporter.stage(probeLinkStagePrepare, "开始测试连接", fmt.Sprintf("endpoint_type=%s scheme=%s", strings.TrimSpace(endpointType), strings.TrimSpace(scheme)))
	if err := ensureControlPlaneReadyForOperation(service, reporter, "probe_link"); err != nil {
		reporter.stage(probeLinkStageFailure, "控制面绕行预热失败", reporter.classifyError(err))
		return ProbeLinkConnectResult{}, err
	}

	trimmedHost := strings.TrimSpace(host)
	if trimmedHost == "" {
		err := fmt.Errorf("host is required")
		reporter.stage(probeLinkStageFailure, "测试连接失败", reporter.classifyError(err))
		return ProbeLinkConnectResult{}, err
	}
	if port <= 0 || port > 65535 {
		err := fmt.Errorf("port must be between 1 and 65535")
		reporter.stage(probeLinkStageFailure, "测试连接失败", reporter.classifyError(err))
		return ProbeLinkConnectResult{}, err
	}

	resolvedTarget, err := resolveProbeLinkTarget(trimmedHost, reporter)
	if err != nil {
		reporter.stage(probeLinkStageFailure, "测试连接失败", reporter.classifyError(err))
		return ProbeLinkConnectResult{}, err
	}
	reporter.setResolvedTarget(resolvedTarget)
	if protocol != "" {
		return testProbeLinkByProtocol(nodeID, protocol, resolvedTarget, port, reporter)
	}

	// Backward compatibility for old service/public HTTP link checks.
	normalizedType := strings.ToLower(strings.TrimSpace(endpointType))
	if normalizedType != "public" {
		normalizedType = "service"
	}

	normalizedScheme := normalizeProbeLinkScheme(scheme)
	client := &http.Client{Timeout: probeLinkTimeout}
	paths := []string{probeLinkInfoPath, probeLinkHealthPath}
	var lastErr error
	for _, candidatePath := range paths {
		reporter.stage(probeLinkStageRequest, "准备请求探针服务信息", fmt.Sprintf("path=%s", candidatePath))
		result, err := probeLinkRequest(client, strings.TrimSpace(nodeID), normalizedType, normalizedScheme, resolvedTarget, port, candidatePath, reporter)
		if err == nil {
			reporter.stage(probeLinkStageSuccess, "测试连接成功", result.Message)
			return result, nil
		}
		lastErr = err
		reporter.stage(probeLinkStageFailure, "当前探测路径失败", reporter.classifyError(err))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("probe link test failed")
	}
	reporter.stage(probeLinkStageFailure, "测试连接失败", reporter.classifyError(lastErr))
	return ProbeLinkConnectResult{}, lastErr
}

func testProbeLinkByProtocol(nodeID string, protocol string, target probeLinkResolvedTarget, port int, reporter *probeLinkProgressReporter) (ProbeLinkConnectResult, error) {
	switch protocol {
	case "http":
		return probeLinkHTTPPing(nodeID, protocol, target, port, reporter)
	case "https":
		return probeLinkHTTPPing(nodeID, protocol, target, port, reporter)
	case "http3":
		return probeLinkHTTP3Ping(nodeID, target, port, reporter)
	default:
		err := fmt.Errorf("unsupported protocol: %s", protocol)
		if reporter != nil {
			reporter.stage(probeLinkStageFailure, "协议不支持", err.Error())
		}
		return ProbeLinkConnectResult{}, err
	}
}

func probeLinkHTTPPing(nodeID string, protocol string, target probeLinkResolvedTarget, port int, reporter *probeLinkProgressReporter) (ProbeLinkConnectResult, error) {
	targetURL, nonce, err := buildProbeLinkTestURL(target.OriginalHost, port, protocol)
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}
	if reporter != nil {
		reporter.withURL(targetURL)
	}

	transport := newProbeLinkHTTPTransport(protocol, target, reporter)
	if protocol == "https" {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	client := &http.Client{Timeout: probeLinkTimeout, Transport: transport}

	startedAt := time.Now()
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}
	if reporter != nil {
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), newProbeLinkClientTrace(reporter)))
		reporter.stage(probeLinkStageRequest, "开始发送 HTTP 请求", fmt.Sprintf("method=%s", http.MethodGet))
	}
	resp, err := client.Do(req)
	if err != nil {
		if reporter != nil {
			reporter.stage(probeLinkStageFailure, "HTTP 请求失败", reporter.classifyError(err))
		}
		return ProbeLinkConnectResult{}, err
	}
	defer resp.Body.Close()
	if reporter != nil {
		reporter.stage(probeLinkStageResponse, "已收到 HTTP 响应", fmt.Sprintf("status=%d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		if reporter != nil {
			reporter.stage(probeLinkStageFailure, "读取响应体失败", reporter.classifyError(err))
		}
		return ProbeLinkConnectResult{}, err
	}
	if reporter != nil {
		reporter.stage(probeLinkStageBodyRead, "响应体读取完成", fmt.Sprintf("bytes=%d", len(body)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
		if reporter != nil {
			reporter.stage(probeLinkStageFailure, "HTTP 状态异常", err.Error())
		}
		return ProbeLinkConnectResult{}, err
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

	result := ProbeLinkConnectResult{
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
	}
	if reporter != nil {
		reporter.stage(probeLinkStageSuccess, "HTTP 探测成功", result.Message)
	}
	return result, nil
}

func probeLinkHTTP3Ping(nodeID string, target probeLinkResolvedTarget, port int, reporter *probeLinkProgressReporter) (ProbeLinkConnectResult, error) {
	targetURL, nonce, err := buildProbeLinkTestURL(target.OriginalHost, port, "http3")
	if err != nil {
		return ProbeLinkConnectResult{}, err
	}
	if reporter != nil {
		reporter.withURL(targetURL)
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
	if reporter != nil {
		reporter.stage(probeLinkStageConnect, "开始建立 HTTP/3 连接", "等待 QUIC/TLS 握手")
		reporter.stage(probeLinkStageRequest, "开始发送 HTTP/3 请求", fmt.Sprintf("method=%s", http.MethodGet))
	}
	resp, err := client.Do(req)
	if err != nil {
		if reporter != nil {
			reporter.stage(probeLinkStageFailure, "HTTP/3 请求失败", reporter.classifyError(err))
		}
		return ProbeLinkConnectResult{}, err
	}
	defer resp.Body.Close()
	if reporter != nil {
		reporter.stage(probeLinkStageResponse, "已收到 HTTP/3 响应", fmt.Sprintf("status=%d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		if reporter != nil {
			reporter.stage(probeLinkStageFailure, "读取 HTTP/3 响应体失败", reporter.classifyError(err))
		}
		return ProbeLinkConnectResult{}, err
	}
	if reporter != nil {
		reporter.stage(probeLinkStageBodyRead, "HTTP/3 响应体读取完成", fmt.Sprintf("bytes=%d", len(body)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
		if reporter != nil {
			reporter.stage(probeLinkStageFailure, "HTTP/3 状态异常", err.Error())
		}
		return ProbeLinkConnectResult{}, err
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

	result := ProbeLinkConnectResult{
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
	}
	if reporter != nil {
		reporter.stage(probeLinkStageSuccess, "HTTP/3 探测成功", result.Message)
	}
	return result, nil
}

func newProbeLinkHTTPTransport(protocol string, target probeLinkResolvedTarget, reporter *probeLinkProgressReporter) *http.Transport {
	transport := &http.Transport{}
	if strings.EqualFold(strings.TrimSpace(protocol), "https") {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialHost := strings.TrimSpace(target.DialHost)
		if dialHost == "" {
			host, _, splitErr := net.SplitHostPort(addr)
			if splitErr != nil {
				host = addr
			}
			dialHost = strings.TrimSpace(host)
		}
		_, port, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			port = ""
		}
		resolvedAddr := addr
		if dialHost != "" && strings.TrimSpace(port) != "" {
			resolvedAddr = net.JoinHostPort(dialHost, port)
		}
		if reporter != nil {
			detail := fmt.Sprintf("network=%s host=%s", network, strings.TrimSpace(target.OriginalHost))
			if len(target.IPs) > 0 {
				detail = detail + " ips=" + strings.Join(target.IPs, ",")
			}
			reporter.stage(probeLinkStageDNSSuccess, "复用初始化阶段 DNS 结果", detail)
			reporter.stage(probeLinkStageConnect, "开始建立 TCP 连接", fmt.Sprintf("network=%s addr=%s", network, resolvedAddr))
		}
		dialer := &net.Dialer{Timeout: probeLinkTimeout}
		conn, err := dialer.DialContext(ctx, network, resolvedAddr)
		if err != nil {
			if reporter != nil {
				reporter.stage(probeLinkStageFailure, "TCP 连接失败", reporter.classifyError(err))
			}
			return nil, err
		}
		if reporter != nil {
			reporter.stage(probeLinkStageConnected, "TCP 连接已建立", conn.RemoteAddr().String())
		}
		return conn, nil
	}
	return transport
}

func newProbeLinkClientTrace(reporter *probeLinkProgressReporter) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		TLSHandshakeStart: func() {
			if reporter != nil {
				reporter.stage(probeLinkStageTLSStart, "开始 TLS 握手", "")
			}
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if reporter == nil {
				return
			}
			if err != nil {
				reporter.stage(probeLinkStageFailure, "TLS 握手失败", reporter.classifyError(err))
				return
			}
			reporter.stage(probeLinkStageTLSSuccess, "TLS 握手成功", "")
		},
		GotConn: func(info httptrace.GotConnInfo) {
			if reporter != nil {
				detail := "reused=false"
				if info.Reused {
					detail = "reused=true"
				}
				reporter.stage(probeLinkStageConnected, "HTTP 连接可用", detail)
			}
		},
	}
}

func isProbeLinkTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	errText := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(errText, "timeout") || strings.Contains(errText, "deadline exceeded")
}

func isProbeLinkDNSResolveError(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var dnsInner *net.DNSError
		if errors.As(opErr.Err, &dnsInner) {
			return true
		}
	}
	errText := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(errText, "no such host") || strings.Contains(errText, "server misbehaving") || strings.Contains(errText, "dns")
}

func isProbeLinkConnectError(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Op == "dial" || opErr.Op == "read" || opErr.Op == "write" {
			return true
		}
	}
	var sysErr *os.SyscallError
	if errors.As(err, &sysErr) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.ETIMEDOUT:
			return true
		}
	}
	errText := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(errText, "connection refused") || strings.Contains(errText, "actively refused") || strings.Contains(errText, "network is unreachable") || strings.Contains(errText, "no route to host") || strings.Contains(errText, "connection reset")
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

func probeLinkRequest(client *http.Client, nodeID, endpointType, scheme string, target probeLinkResolvedTarget, port int, pathValue string, reporter *probeLinkProgressReporter) (ProbeLinkConnectResult, error) {
	targetURL, err := buildProbeLinkURL(target.OriginalHost, scheme, port, pathValue)
	if err != nil {
		if reporter != nil {
			reporter.stage(probeLinkStageFailure, "构造探测 URL 失败", reporter.classifyError(err))
		}
		return ProbeLinkConnectResult{}, err
	}
	if reporter != nil {
		reporter.withURL(targetURL)
	}

	startedAt := time.Now()
	transport := newProbeLinkHTTPTransport(scheme, target, reporter)
	clientToUse := client
	if transport != nil {
		baseClient := client
		if baseClient == nil {
			baseClient = &http.Client{Timeout: probeLinkTimeout}
		}
		clientCopy := *baseClient
		clientCopy.Transport = transport
		clientToUse = &clientCopy
	}

	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		if reporter != nil {
			reporter.stage(probeLinkStageFailure, "创建探测请求失败", reporter.classifyError(err))
		}
		return ProbeLinkConnectResult{}, err
	}
	if reporter != nil {
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), newProbeLinkClientTrace(reporter)))
		reporter.stage(probeLinkStageRequest, "开始发送探测请求", fmt.Sprintf("method=%s path=%s", http.MethodGet, strings.TrimSpace(pathValue)))
	}

	resp, err := clientToUse.Do(req)
	if err != nil {
		if reporter != nil {
			reporter.stage(probeLinkStageFailure, "探测请求失败", reporter.classifyError(err))
		}
		return ProbeLinkConnectResult{}, err
	}
	defer resp.Body.Close()
	if reporter != nil {
		reporter.stage(probeLinkStageResponse, "已收到探测响应", fmt.Sprintf("status=%d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		if reporter != nil {
			reporter.stage(probeLinkStageFailure, "读取探测响应体失败", reporter.classifyError(err))
		}
		return ProbeLinkConnectResult{}, err
	}
	if reporter != nil {
		reporter.stage(probeLinkStageBodyRead, "探测响应体读取完成", fmt.Sprintf("bytes=%d", len(body)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
		if reporter != nil {
			reporter.stage(probeLinkStageFailure, "探测 HTTP 状态异常", err.Error())
		}
		return ProbeLinkConnectResult{}, err
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

	result := ProbeLinkConnectResult{
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
	}
	if reporter != nil {
		reporter.stage(probeLinkStageSuccess, "探测请求成功", result.Message)
	}
	return result, nil
}

func resolveProbeLinkTarget(host string, reporter *probeLinkProgressReporter) (probeLinkResolvedTarget, error) {
	trimmedHost := strings.TrimSpace(host)
	if trimmedHost == "" {
		return probeLinkResolvedTarget{}, fmt.Errorf("host is required")
	}
	resolved := probeLinkResolvedTarget{
		OriginalHost: trimmedHost,
		DialHost:     trimmedHost,
	}
	if ip := net.ParseIP(trimmedHost); ip != nil {
		canonical := canonicalIP(ip)
		resolved.DialHost = canonical
		resolved.IPs = []string{canonical}
		if reporter != nil {
			reporter.stage(probeLinkStageDNSSuccess, "目标为 IP，跳过 DNS 解析", canonical)
		}
		return resolved, nil
	}
	if reporter != nil {
		reporter.stage(probeLinkStageDNSStart, "初始化阶段开始 DNS 解析", fmt.Sprintf("host=%s", trimmedHost))
	}
	ips, err := net.DefaultResolver.LookupIPAddr(context.Background(), trimmedHost)
	if err != nil {
		if reporter != nil {
			reporter.stage(probeLinkStageFailure, "初始化阶段 DNS 解析失败", reporter.classifyError(err))
		}
		return probeLinkResolvedTarget{}, err
	}
	seen := make(map[string]struct{}, len(ips))
	for _, item := range ips {
		if item.IP == nil {
			continue
		}
		canonical := canonicalIP(item.IP)
		if canonical == "" {
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		resolved.IPs = append(resolved.IPs, canonical)
	}
	if len(resolved.IPs) == 0 {
		return probeLinkResolvedTarget{}, fmt.Errorf("dns resolve returned no usable ip for host %s", trimmedHost)
	}
	resolved.DialHost = resolved.IPs[0]
	if reporter != nil {
		reporter.stage(probeLinkStageDNSSuccess, "初始化阶段 DNS 解析成功", strings.Join(resolved.IPs, ","))
	}
	return resolved, nil
}

func ensureControlPlaneReadyForOperation(service *networkAssistantService, reporter *probeLinkProgressReporter, operation string) error {
	if service == nil {
		return nil
	}
	if reporter != nil {
		reporter.stage(controlPlaneBypassStageName, "开始预热控制面绕行", strings.TrimSpace(operation))
	}
	startedAt := time.Now()
	err := service.ensureControlPlaneDialReady("")
	if reporter != nil {
		if err != nil {
			reporter.stage(controlPlaneBypassStageName, "控制面绕行预热失败", reporter.classifyError(err))
		} else {
			reporter.stage(controlPlaneBypassStageName, "控制面绕行预热完成", fmt.Sprintf("operation=%s elapsed=%s", strings.TrimSpace(operation), time.Since(startedAt)))
		}
	}
	return err
}

func buildProbeLinkURL(host, scheme string, port int, pathValue string) (string, error) {
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


	return ProbeChainPingResult{
		OK:        false,
		ChainID:   trimmedNodeID,
		LinkLayer: "ws",
		Message:   "连接失败: 本地无可复用链路",
	}, nil
}
