// probelink.go implements the probe link test capability migrated from probe_manager/backend.
// It handles HTTP/HTTPS/HTTP3 endpoint probing with DNS pre-resolution.
// PKG-W2-02 / RQ-004
package node

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/quic-go/quic-go/http3"
)

const (
	probeLinkTestPingPath = "/api/node/link-test/ping"
	probeLinkInfoPath     = "/api/node/info"
	probeLinkHealthPath   = "/healthz"
	probeLinkTimeout      = 8 * time.Second
)

// LinkConnectResult is the result of a probe link connectivity test.
type LinkConnectResult struct {
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

type probeLinkTestPingResponse struct {
	OK       bool   `json:"ok"`
	NodeID   string `json:"node_id"`
	Protocol string `json:"protocol"`
	Message  string `json:"message"`
}

type probeNodeInfoResponse struct {
	Service string `json:"service"`
	NodeID  string `json:"node_id"`
	Version string `json:"version"`
}

type resolvedTarget struct {
	OriginalHost string
	DialHost     string
	IPs          []string
}

// TestLink tests connectivity to a probe node endpoint.
// endpointType: "http" | "https" | "http3" | "service" | "public" (legacy)
func TestLink(ctx context.Context, nodeID, endpointType, scheme, host string, port int) (LinkConnectResult, error) {
	protocol := normalizeProtocol(endpointType)
	trimmedHost := strings.TrimSpace(host)
	if trimmedHost == "" {
		return LinkConnectResult{}, errors.New("host is required")
	}
	if port <= 0 || port > 65535 {
		return LinkConnectResult{}, errors.New("port must be between 1 and 65535")
	}

	target, err := resolveTarget(ctx, trimmedHost)
	if err != nil {
		return LinkConnectResult{}, err
	}

	if protocol != "" {
		return testByProtocol(nodeID, protocol, target, port)
	}

	// Legacy path: service / public via HTTP
	normalizedType := strings.ToLower(strings.TrimSpace(endpointType))
	if normalizedType != "public" {
		normalizedType = "service"
	}
	normalizedScheme := normalizeScheme(scheme)
	client := &http.Client{Timeout: probeLinkTimeout}
	paths := []string{probeLinkInfoPath, probeLinkHealthPath}
	var lastErr error
	for _, p := range paths {
		result, err := legacyRequest(client, strings.TrimSpace(nodeID), normalizedType, normalizedScheme, target, port, p)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("probe link test failed")
	}
	return LinkConnectResult{}, lastErr
}

func testByProtocol(nodeID, protocol string, target resolvedTarget, port int) (LinkConnectResult, error) {
	switch protocol {
	case "http", "https":
		return httpPing(nodeID, protocol, target, port)
	case "http3":
		return http3Ping(nodeID, target, port)
	default:
		return LinkConnectResult{}, fmt.Errorf("unsupported protocol: %s", protocol)
	}
}

func httpPing(nodeID, protocol string, target resolvedTarget, port int) (LinkConnectResult, error) {
	targetURL, nonce, err := buildTestURL(target.OriginalHost, port, protocol)
	if err != nil {
		return LinkConnectResult{}, err
	}
	transport := buildHTTPTransport(protocol, target)
	client := &http.Client{Timeout: probeLinkTimeout, Transport: transport}

	startedAt := time.Now()
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return LinkConnectResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return LinkConnectResult{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return LinkConnectResult{}, fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var data probeLinkTestPingResponse
	_ = json.Unmarshal(body, &data)
	message := strings.TrimSpace(data.Message)
	if message == "" {
		message = "probe link ping success"
	}
	if nonce != "" {
		message += ", nonce=" + nonce
	}
	expectedID := normalizeNodeID(nodeID)
	actualID := normalizeNodeID(data.NodeID)
	if expectedID != "" && actualID != "" && expectedID != actualID {
		message = fmt.Sprintf("%s, but node_id mismatch: expected=%s actual=%s", message, expectedID, actualID)
	}
	return LinkConnectResult{
		OK:          true,
		NodeID:      firstNonEmpty(actualID, expectedID),
		EndpointType: protocol,
		URL:         targetURL,
		StatusCode:  resp.StatusCode,
		Service:     "probe_link_test",
		Message:     message,
		ConnectedAt: time.Now().UTC().Format(time.RFC3339),
		DurationMS:  time.Since(startedAt).Milliseconds(),
	}, nil
}

func http3Ping(nodeID string, target resolvedTarget, port int) (LinkConnectResult, error) {
	targetURL, nonce, err := buildTestURL(target.OriginalHost, port, "http3")
	if err != nil {
		return LinkConnectResult{}, err
	}
	transport := &http3.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13,
			NextProtos: []string{"h3"},
		},
	}
	defer transport.Close()
	client := &http.Client{Timeout: probeLinkTimeout, Transport: transport}

	startedAt := time.Now()
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return LinkConnectResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return LinkConnectResult{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return LinkConnectResult{}, fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var data probeLinkTestPingResponse
	_ = json.Unmarshal(body, &data)
	message := strings.TrimSpace(data.Message)
	if message == "" {
		message = "probe link http3 ping success"
	}
	if nonce != "" {
		message += ", nonce=" + nonce
	}
	expectedID := normalizeNodeID(nodeID)
	actualID := normalizeNodeID(data.NodeID)
	if expectedID != "" && actualID != "" && expectedID != actualID {
		message = fmt.Sprintf("%s, but node_id mismatch: expected=%s actual=%s", message, expectedID, actualID)
	}
	return LinkConnectResult{
		OK:          true,
		NodeID:      firstNonEmpty(actualID, expectedID),
		EndpointType: "http3",
		URL:         targetURL,
		StatusCode:  resp.StatusCode,
		Service:     "probe_link_test",
		Message:     message,
		ConnectedAt: time.Now().UTC().Format(time.RFC3339),
		DurationMS:  time.Since(startedAt).Milliseconds(),
	}, nil
}

func legacyRequest(client *http.Client, nodeID, endpointType, scheme string, target resolvedTarget, port int, pathValue string) (LinkConnectResult, error) {
	rawURL, err := buildLegacyURL(target.OriginalHost, scheme, port, pathValue)
	if err != nil {
		return LinkConnectResult{}, err
	}
	transport := buildHTTPTransport(scheme, target)
	clientCopy := *client
	clientCopy.Transport = transport

	startedAt := time.Now()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return LinkConnectResult{}, err
	}
	resp, err := clientCopy.Do(req)
	if err != nil {
		return LinkConnectResult{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return LinkConnectResult{}, fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var info probeNodeInfoResponse
	_ = json.Unmarshal(body, &info)
	message := "probe link connected"
	expectedID := normalizeNodeID(nodeID)
	actualID := normalizeNodeID(info.NodeID)
	if expectedID != "" && actualID != "" && expectedID != actualID {
		message = fmt.Sprintf("probe link connected, but node_id mismatch: expected=%s actual=%s", expectedID, actualID)
	}
	return LinkConnectResult{
		OK:          true,
		NodeID:      firstNonEmpty(strings.TrimSpace(info.NodeID), expectedID),
		EndpointType: endpointType,
		URL:         rawURL,
		StatusCode:  resp.StatusCode,
		Service:     strings.TrimSpace(info.Service),
		Version:     strings.TrimSpace(info.Version),
		Message:     message,
		ConnectedAt: time.Now().UTC().Format(time.RFC3339),
		DurationMS:  time.Since(startedAt).Milliseconds(),
	}, nil
}

// ---- helpers ----

func resolveTarget(ctx context.Context, host string) (resolvedTarget, error) {
	target := resolvedTarget{OriginalHost: host, DialHost: host}
	if ip := net.ParseIP(host); ip != nil {
		canonical := ip.String()
		target.DialHost = canonical
		target.IPs = []string{canonical}
		return target, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return resolvedTarget{}, err
	}
	seen := map[string]struct{}{}
	for _, item := range ips {
		if item.IP == nil {
			continue
		}
		s := item.IP.String()
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		target.IPs = append(target.IPs, s)
	}
	if len(target.IPs) == 0 {
		return resolvedTarget{}, fmt.Errorf("dns resolve returned no usable ip for host %s", host)
	}
	target.DialHost = target.IPs[0]
	return target, nil
}

func buildHTTPTransport(protocol string, target resolvedTarget) *http.Transport {
	t := &http.Transport{}
	if strings.EqualFold(protocol, "https") {
		t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	dialHost := target.DialHost
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, _ := net.SplitHostPort(addr)
		resolvedAddr := addr
		if dialHost != "" && strings.TrimSpace(port) != "" {
			resolvedAddr = net.JoinHostPort(dialHost, port)
		}
		return (&net.Dialer{Timeout: probeLinkTimeout}).DialContext(ctx, network, resolvedAddr)
	}
	return t
}

func buildTestURL(host string, port int, protocol string) (string, string, error) {
	if host == "" {
		return "", "", errors.New("host is required")
	}
	nonce := strconv.FormatInt(time.Now().UnixNano(), 36)
	scheme := "https"
	if strings.EqualFold(protocol, "http") {
		scheme = "http"
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   probeLinkTestPingPath,
	}
	q := u.Query()
	q.Set("nonce", nonce)
	u.RawQuery = q.Encode()
	return u.String(), nonce, nil
}

func buildLegacyURL(host, scheme string, port int, path string) (string, error) {
	s := scheme
	if s != "https" {
		s = "http"
	}
	u := &url.URL{
		Scheme: s,
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   path,
	}
	return u.String(), nil
}

func normalizeProtocol(endpointType string) string {
	switch strings.ToLower(strings.TrimSpace(endpointType)) {
	case "http":
		return "http"
	case "https":
		return "https"
	case "http3":
		return "http3"
	}
	return ""
}

func normalizeScheme(scheme string) string {
	if strings.EqualFold(strings.TrimSpace(scheme), "https") {
		return "https"
	}
	return "http"
}

func normalizeNodeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func isConnectError(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && (opErr.Op == "dial" || opErr.Op == "read" || opErr.Op == "write") {
		return true
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
	return false
}

var _ = isConnectError // used in future diagnostic helpers
