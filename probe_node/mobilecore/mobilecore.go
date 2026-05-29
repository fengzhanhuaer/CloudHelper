package mobilecore

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

const defaultReportIntervalSec = 60
const configRefreshTimeout = 20 * time.Second

var manager = &coreManager{}

type coreManager struct {
	mu     sync.Mutex
	cancel chan struct{}
	status string
}

type reportPayload struct {
	Type      string       `json:"type"`
	NodeID    string       `json:"node_id"`
	Platform  string       `json:"platform,omitempty"`
	OS        string       `json:"os,omitempty"`
	Arch      string       `json:"arch,omitempty"`
	System    systemStatus `json:"system"`
	Version   string       `json:"version,omitempty"`
	Timestamp string       `json:"timestamp"`
}

type systemStatus struct {
	CPUPercent        float64 `json:"cpu_percent"`
	MemoryTotalBytes  uint64  `json:"memory_total_bytes"`
	MemoryUsedBytes   uint64  `json:"memory_used_bytes"`
	MemoryUsedPercent float64 `json:"memory_used_percent"`
	SwapTotalBytes    uint64  `json:"swap_total_bytes"`
	SwapUsedBytes     uint64  `json:"swap_used_bytes"`
	SwapUsedPercent   float64 `json:"swap_used_percent"`
	DiskTotalBytes    uint64  `json:"disk_total_bytes"`
	DiskUsedBytes     uint64  `json:"disk_used_bytes"`
	DiskUsedPercent   float64 `json:"disk_used_percent"`
}

type configRefreshSummary struct {
	ProxyGroupUpdated bool
	SelfChains        int
	ProxyEntries      int
	ConfigDir         string
}

type proxyGroupBackupResponse struct {
	OK            bool   `json:"ok"`
	NodeID        string `json:"node_id"`
	FileName      string `json:"file_name"`
	ContentBase64 string `json:"content_base64"`
	UpdatedAt     string `json:"updated_at"`
	Error         string `json:"error"`
}

type linkChainConfigResponse struct {
	NodeID                   string            `json:"node_id"`
	Chains                   []json.RawMessage `json:"chains"`
	SelfChains               []json.RawMessage `json:"self_chains"`
	PortForwardChains        []json.RawMessage `json:"port_forward_chains"`
	ProxyChains              []json.RawMessage `json:"proxy_chains"`
	GlobalProxyForwardChains []json.RawMessage `json:"global_proxy_forward_chains"`
}

type chainCacheFile struct {
	UpdatedAt string            `json:"updated_at"`
	Items     []json.RawMessage `json:"items"`
}

type controlEnvelope struct {
	Type string `json:"type"`
}

type chainLinkControlMessage struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	ChainID   string `json:"chain_id"`
	Role      string `json:"role"`
}

type chainLinkControlResult struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	NodeID    string `json:"node_id"`
	OK        bool   `json:"ok"`
	Action    string `json:"action,omitempty"`
	ChainID   string `json:"chain_id,omitempty"`
	Role      string `json:"role,omitempty"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
	Timestamp string `json:"timestamp"`
}

func Start(controllerURL string, nodeID string, nodeSecret string) string {
	return StartWithConfigDir(controllerURL, nodeID, nodeSecret, "")
}

func StartWithConfigDir(controllerURL string, nodeID string, nodeSecret string, configDir string) string {
	controllerURL = strings.TrimSpace(controllerURL)
	nodeID = strings.TrimSpace(nodeID)
	nodeSecret = strings.TrimSpace(nodeSecret)
	if controllerURL == "" || nodeID == "" || nodeSecret == "" {
		return "controller URL, node ID, and node secret are required"
	}
	wsURL, err := resolveWebSocketURL(controllerURL)
	if err != nil {
		return err.Error()
	}

	manager.mu.Lock()
	if manager.cancel != nil {
		close(manager.cancel)
	}
	cancel := make(chan struct{})
	manager.cancel = cancel
	manager.status = "starting"
	manager.mu.Unlock()

	go runLoop(cancel, wsURL, nodeID, nodeSecret)
	if strings.TrimSpace(configDir) != "" {
		go func() {
			if _, err := refreshConfigFiles(controllerURL, nodeID, nodeSecret, configDir); err != nil {
				setStatus("config refresh failed: " + err.Error())
			}
		}()
	}
	return "starting"
}

func RefreshConfig(controllerURL string, nodeID string, nodeSecret string, configDir string) string {
	summary, err := refreshConfigFiles(controllerURL, nodeID, nodeSecret, configDir)
	if err != nil {
		return "配置刷新失败：" + err.Error()
	}
	proxyGroupText := "未更新"
	if summary.ProxyGroupUpdated {
		proxyGroupText = "已更新"
	}
	return fmt.Sprintf("配置刷新完成：代理组=%s，本机链路=%d，代理入口=%d", proxyGroupText, summary.SelfChains, summary.ProxyEntries)
}

func Stop() string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.cancel != nil {
		close(manager.cancel)
		manager.cancel = nil
	}
	manager.status = "stopped"
	return manager.status
}

func Status() string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if strings.TrimSpace(manager.status) == "" {
		return "stopped"
	}
	return manager.status
}

func refreshConfigFiles(controllerURL string, nodeID string, nodeSecret string, configDir string) (configRefreshSummary, error) {
	baseURL, err := normalizeControllerBaseURL(controllerURL)
	if err != nil {
		return configRefreshSummary{}, err
	}
	nodeID = strings.TrimSpace(nodeID)
	nodeSecret = strings.TrimSpace(nodeSecret)
	configDir = strings.TrimSpace(configDir)
	if nodeID == "" || nodeSecret == "" {
		return configRefreshSummary{}, errors.New("node ID and node secret are required")
	}
	if configDir == "" {
		return configRefreshSummary{}, errors.New("config dir is required")
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return configRefreshSummary{}, fmt.Errorf("create config dir: %w", err)
	}

	client := &http.Client{Timeout: configRefreshTimeout}
	summary := configRefreshSummary{ConfigDir: configDir}
	proxyGroupUpdated, err := refreshProxyGroupFile(client, baseURL, nodeID, nodeSecret, configDir)
	if err != nil {
		return summary, err
	}
	summary.ProxyGroupUpdated = proxyGroupUpdated

	config, err := fetchLinkChainConfig(client, baseURL, nodeID, nodeSecret)
	if err != nil {
		return summary, err
	}
	updatedAt := time.Now().UTC().Format(time.RFC3339)
	if err := writeJSONFile(filepath.Join(configDir, "probe_link_chain_config.json"), chainCacheFile{
		UpdatedAt: updatedAt,
		Items:     append([]json.RawMessage(nil), config.SelfChains...),
	}); err != nil {
		return summary, err
	}
	if err := writeJSONFile(filepath.Join(configDir, "proxy_chain.json"), chainCacheFile{
		UpdatedAt: updatedAt,
		Items:     append([]json.RawMessage(nil), config.GlobalProxyForwardChains...),
	}); err != nil {
		return summary, err
	}
	if err := writeJSONFile(filepath.Join(configDir, "probe_link_config_grouped.json"), config); err != nil {
		return summary, err
	}
	summary.SelfChains = len(config.SelfChains)
	summary.ProxyEntries = len(config.GlobalProxyForwardChains)
	return summary, nil
}

func refreshProxyGroupFile(client *http.Client, baseURL string, nodeID string, nodeSecret string, configDir string) (bool, error) {
	requestURL := strings.TrimRight(baseURL, "/") + "/api/probe/proxy_group/backup"
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return false, err
	}
	applyAuthHeaders(req, nodeID, nodeSecret)
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("fetch proxy group: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, fmt.Errorf("fetch proxy group status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload proxyGroupBackupResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, fmt.Errorf("decode proxy group response: %w", err)
	}
	encoded := strings.TrimSpace(payload.ContentBase64)
	if encoded == "" {
		return false, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false, fmt.Errorf("decode proxy group content: %w", err)
	}
	if !json.Valid(decoded) {
		return false, errors.New("proxy group content is not valid json")
	}
	if err := os.WriteFile(filepath.Join(configDir, "proxy_group.json"), decoded, 0600); err != nil {
		return false, fmt.Errorf("write proxy_group.json: %w", err)
	}
	return true, nil
}

func fetchLinkChainConfig(client *http.Client, baseURL string, nodeID string, nodeSecret string) (linkChainConfigResponse, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/api/probe/link/config/grouped")
	if err != nil {
		return linkChainConfigResponse{}, err
	}
	query := u.Query()
	query.Set("node_id", strings.TrimSpace(nodeID))
	query.Set("secret", strings.TrimSpace(nodeSecret))
	u.RawQuery = query.Encode()
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return linkChainConfigResponse{}, err
	}
	applyAuthHeaders(req, nodeID, nodeSecret)
	resp, err := client.Do(req)
	if err != nil {
		return linkChainConfigResponse{}, fmt.Errorf("fetch link config: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return linkChainConfigResponse{}, fmt.Errorf("fetch link config status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var config linkChainConfigResponse
	if err := json.Unmarshal(body, &config); err != nil {
		return linkChainConfigResponse{}, fmt.Errorf("decode link config: %w", err)
	}
	return config, nil
}

func writeJSONFile(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}

func normalizeControllerBaseURL(controllerURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(controllerURL))
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
	case "http", "https":
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	default:
		return "", fmt.Errorf("unsupported controller scheme: %s", u.Scheme)
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func runLoop(cancel <-chan struct{}, wsURL string, nodeID string, nodeSecret string) {
	for {
		select {
		case <-cancel:
			setStatus("stopped")
			return
		default:
		}
		if err := runSession(cancel, wsURL, nodeID, nodeSecret); err != nil {
			setStatus("disconnected: " + err.Error())
		}
		select {
		case <-cancel:
			setStatus("stopped")
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func runSession(cancel <-chan struct{}, wsURL string, nodeID string, nodeSecret string) error {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	headers := buildAuthHeaders(nodeID, nodeSecret)
	wsConn, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		return err
	}
	defer wsConn.Close()

	session, err := yamux.Client(newWebSocketNetConn(wsConn), yamux.DefaultConfig())
	if err != nil {
		return err
	}
	defer session.Close()

	stream, err := session.Open()
	if err != nil {
		return err
	}
	defer stream.Close()

	encoder := json.NewEncoder(stream)
	decoder := json.NewDecoder(stream)
	writeMu := &sync.Mutex{}
	setStatus("connected")
	if err := sendReport(stream, encoder, writeMu, nodeID); err != nil {
		return err
	}

	readErrCh := make(chan error, 1)
	go func() {
		for {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				readErrCh <- err
				return
			}
			processControlMessage(raw, stream, encoder, writeMu, nodeID)
		}
	}()

	ticker := time.NewTicker(defaultReportIntervalSec * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			return nil
		case err := <-readErrCh:
			return err
		case <-ticker.C:
			if err := sendReport(stream, encoder, writeMu, nodeID); err != nil {
				return err
			}
		}
	}
}

func sendReport(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, nodeID string) error {
	payload := reportPayload{
		Type:      "report",
		NodeID:    nodeID,
		Platform:  "android",
		OS:        "android",
		Arch:      runtime.GOARCH,
		System:    systemStatus{},
		Version:   "android-mvp",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := encoder.Encode(payload)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
}

func processControlMessage(raw json.RawMessage, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, nodeID string) {
	var envelope controlEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(envelope.Type)) {
	case "chain_link_control":
		var msg chainLinkControlMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return
		}
		sendChainLinkControlResult(stream, encoder, writeMu, chainLinkControlResult{
			Type:      "chain_link_control_result",
			RequestID: strings.TrimSpace(msg.RequestID),
			NodeID:    strings.TrimSpace(nodeID),
			OK:        false,
			Action:    strings.TrimSpace(msg.Action),
			ChainID:   strings.TrimSpace(msg.ChainID),
			Role:      strings.TrimSpace(msg.Role),
			Error:     "android mobilecore link service runtime is not packaged yet; refresh config first and rebuild with shared chain runtime",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	}
}

func sendChainLinkControlResult(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, result chainLinkControlResult) {
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = encoder.Encode(result)
	_ = stream.SetWriteDeadline(time.Time{})
}

func setStatus(status string) {
	manager.mu.Lock()
	manager.status = strings.TrimSpace(status)
	manager.mu.Unlock()
}

func resolveWebSocketURL(controllerURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(controllerURL))
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported controller scheme: %s", u.Scheme)
	}
	u.Path = "/api/probe"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func buildAuthHeaders(nodeID string, secret string) http.Header {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	randomToken := randomHexToken(16)
	signature := signConnect(secret, nodeID, timestamp, randomToken)
	headers := http.Header{}
	headers.Set("X-Probe-Node-Id", strings.TrimSpace(nodeID))
	headers.Set("X-Probe-Timestamp", timestamp)
	headers.Set("X-Probe-Rand", randomToken)
	headers.Set("X-Probe-Signature", signature)
	return headers
}

func applyAuthHeaders(req *http.Request, nodeID string, secret string) {
	for key, values := range buildAuthHeaders(nodeID, secret) {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cloudhelper-probe-node-android")
	req.Header.Set("X-Forwarded-Proto", "https")
}

func signConnect(secret, nodeID, timestamp, randomToken string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(strings.TrimSpace(nodeID)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(timestamp)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(randomToken)))
	return hex.EncodeToString(mac.Sum(nil))
}

func randomHexToken(size int) string {
	if size <= 0 {
		size = 8
	}
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

type webSocketNetConn struct {
	ws      *websocket.Conn
	readMu  sync.Mutex
	writeMu sync.Mutex
	reader  netReader
}

type netReader interface {
	Read([]byte) (int, error)
}

func newWebSocketNetConn(ws *websocket.Conn) net.Conn {
	return &webSocketNetConn{ws: ws}
}

func (c *webSocketNetConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for {
		if c.reader == nil {
			mt, reader, err := c.ws.NextReader()
			if err != nil {
				return 0, err
			}
			if mt != websocket.BinaryMessage && mt != websocket.TextMessage {
				continue
			}
			c.reader = reader
		}
		n, err := c.reader.Read(p)
		if errors.Is(err, net.ErrClosed) {
			return n, err
		}
		if errors.Is(err, io.EOF) {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *webSocketNetConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	writer, err := c.ws.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	n, writeErr := writer.Write(p)
	closeErr := writer.Close()
	if writeErr != nil {
		return n, writeErr
	}
	return n, closeErr
}

func (c *webSocketNetConn) Close() error {
	return c.ws.Close()
}

func (c *webSocketNetConn) LocalAddr() net.Addr {
	if c.ws == nil || c.ws.UnderlyingConn() == nil {
		return dummyAddr("local")
	}
	return c.ws.UnderlyingConn().LocalAddr()
}

func (c *webSocketNetConn) RemoteAddr() net.Addr {
	if c.ws == nil || c.ws.UnderlyingConn() == nil {
		return dummyAddr("remote")
	}
	return c.ws.UnderlyingConn().RemoteAddr()
}

func (c *webSocketNetConn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}

func (c *webSocketNetConn) SetReadDeadline(t time.Time) error {
	return c.ws.SetReadDeadline(t)
}

func (c *webSocketNetConn) SetWriteDeadline(t time.Time) error {
	return c.ws.SetWriteDeadline(t)
}

type dummyAddr string

func (a dummyAddr) Network() string { return string(a) }
func (a dummyAddr) String() string  { return string(a) }
