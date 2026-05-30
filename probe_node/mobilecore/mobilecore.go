package mobilecore

import (
	"bufio"
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
const androidLogMaxEntries = 300
const mobileWebSocketWriteBatchBytes = 1024 * 1024
const mobileWebSocketWriteQueueDepth = 64
const mobileRelayTCPSocketBufferBytes = 8 * 1024 * 1024
const mobileRelayTCPKeepAlivePeriod = 30 * time.Second
const mobileRelayIOCopyBufferBytes = 1024 * 1024

var manager = &coreManager{}
var androidLogStore = &androidLogBuffer{}

var mobileRelayCopyBufferPool = sync.Pool{
	New: func() any {
		return make([]byte, mobileRelayIOCopyBufferBytes)
	},
}

type coreManager struct {
	mu             sync.Mutex
	cancel         chan struct{}
	status         string
	version        string
	injectedIPv4   []string
	injectedIPv6   []string
	injectedAt     string
	controllerHost string
	controllerPort string
}

type reportPayload struct {
	Type      string       `json:"type"`
	NodeID    string       `json:"node_id"`
	Platform  string       `json:"platform,omitempty"`
	OS        string       `json:"os,omitempty"`
	Arch      string       `json:"arch,omitempty"`
	IPv4      []string     `json:"ipv4,omitempty"`
	IPv6      []string     `json:"ipv6,omitempty"`
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

type cpuSnapshot struct {
	total uint64
	idle  uint64
}

type cpuSampler struct {
	hasPrev bool
	prev    cpuSnapshot
}

type configRefreshSummary struct {
	ProxyGroupUpdated bool
	SelfChains        int
	ProxyEntries      int
	ConfigDir         string
}

var reportCPUSampler cpuSampler

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

type logsControlMessage struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id"`
	Lines        int    `json:"lines"`
	SinceMinutes int    `json:"since_minutes"`
	MinLevel     string `json:"min_level,omitempty"`
}

type logsControlResult struct {
	Type         string            `json:"type"`
	RequestID    string            `json:"request_id"`
	NodeID       string            `json:"node_id"`
	OK           bool              `json:"ok"`
	Source       string            `json:"source,omitempty"`
	FilePath     string            `json:"file_path,omitempty"`
	Lines        int               `json:"lines"`
	SinceMinutes int               `json:"since_minutes"`
	MinLevel     string            `json:"min_level,omitempty"`
	Content      string            `json:"content,omitempty"`
	Entries      []androidLogEntry `json:"entries,omitempty"`
	Error        string            `json:"error,omitempty"`
	Timestamp    string            `json:"timestamp"`
}

type androidLogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Source  string `json:"source,omitempty"`
	Message string `json:"message"`
	Line    string `json:"line"`
}

type androidLogBuffer struct {
	mu      sync.Mutex
	entries []androidLogEntry
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
	setControllerDirectTarget(controllerURL)

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
	setControllerDirectTarget(controllerURL)
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

func SetVersion(version string) string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.version = strings.TrimSpace(version)
	return currentVersionLocked()
}

func SetNativeIPs(ipv4JSON string, ipv6JSON string) string {
	ipv4 := parseInjectedIPList(ipv4JSON, true)
	ipv6 := parseInjectedIPList(ipv6JSON, false)
	manager.mu.Lock()
	manager.injectedIPv4 = ipv4
	manager.injectedIPv6 = ipv6
	manager.injectedAt = time.Now().UTC().Format(time.RFC3339)
	manager.mu.Unlock()
	return fmt.Sprintf("native ips set: ipv4=%d ipv6=%d", len(ipv4), len(ipv6))
}

func SetControllerURL(controllerURL string) string {
	if setControllerDirectTarget(controllerURL) {
		return "controller direct target set"
	}
	return "controller direct target unavailable"
}

func AppendAppLog(source string, level string, message string) string {
	androidLogStore.add(source, level, message)
	return "app log appended"
}

func Status() string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if strings.TrimSpace(manager.status) == "" {
		return "stopped"
	}
	return manager.status
}

func currentVersion() string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return currentVersionLocked()
}

func currentVersionLocked() string {
	if strings.TrimSpace(manager.version) != "" {
		return strings.TrimSpace(manager.version)
	}
	return "android"
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

func setControllerDirectTarget(controllerURL string) bool {
	host, port, ok := parseControllerHostPort(controllerURL)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !ok {
		manager.controllerHost = ""
		manager.controllerPort = ""
		return false
	}
	manager.controllerHost = host
	manager.controllerPort = port
	return true
}

func currentControllerDirectTarget() (string, string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.controllerHost, manager.controllerPort
}

func parseControllerHostPort(controllerURL string) (string, string, bool) {
	u, err := url.Parse(strings.TrimSpace(controllerURL))
	if err != nil {
		return "", "", false
	}
	host := normalizeDirectTargetHost(u.Hostname())
	if host == "" {
		return "", "", false
	}
	port := strings.TrimSpace(u.Port())
	if port == "" {
		switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
		case "https", "wss":
			port = "443"
		case "http", "ws":
			port = "80"
		default:
			return "", "", false
		}
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", "", false
	}
	return host, port, true
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
	ipv4, ipv6 := collectIPs()
	payload := reportPayload{
		Type:      "report",
		NodeID:    nodeID,
		Platform:  "android",
		OS:        "android",
		Arch:      runtime.GOARCH,
		IPv4:      ipv4,
		IPv6:      ipv6,
		System:    collectSystemStatus(&reportCPUSampler),
		Version:   currentVersion(),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := encoder.Encode(payload)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
}

func collectIPs() ([]string, []string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	seen4 := map[string]struct{}{}
	seen6 := map[string]struct{}{}
	ipv4 := make([]string, 0)
	ipv6 := make([]string, 0)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			default:
				continue
			}
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				value := ip4.String()
				if _, ok := seen4[value]; !ok {
					seen4[value] = struct{}{}
					ipv4 = append(ipv4, value)
				}
				continue
			}
			if ip.To16() != nil {
				value := ip.String()
				if _, ok := seen6[value]; !ok {
					seen6[value] = struct{}{}
					ipv6 = append(ipv6, value)
				}
			}
		}
	}
	addIPv4, addIPv6 := collectCommandIPs()
	for _, value := range addIPv4 {
		if _, ok := seen4[value]; ok {
			continue
		}
		seen4[value] = struct{}{}
		ipv4 = append(ipv4, value)
	}
	for _, value := range addIPv6 {
		if _, ok := seen6[value]; ok {
			continue
		}
		seen6[value] = struct{}{}
		ipv6 = append(ipv6, value)
	}
	publicIPv4, publicIPv6 := collectPublicIPs()
	for _, value := range publicIPv4 {
		if _, ok := seen4[value]; ok {
			continue
		}
		seen4[value] = struct{}{}
		ipv4 = append(ipv4, value)
	}
	for _, value := range publicIPv6 {
		if _, ok := seen6[value]; ok {
			continue
		}
		seen6[value] = struct{}{}
		ipv6 = append(ipv6, value)
	}
	injectedIPv4, injectedIPv6 := currentInjectedIPs()
	for _, value := range injectedIPv4 {
		if _, ok := seen4[value]; ok {
			continue
		}
		seen4[value] = struct{}{}
		ipv4 = append(ipv4, value)
	}
	for _, value := range injectedIPv6 {
		if _, ok := seen6[value]; ok {
			continue
		}
		seen6[value] = struct{}{}
		ipv6 = append(ipv6, value)
	}
	return ipv4, ipv6
}

func currentInjectedIPs() ([]string, []string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return append([]string{}, manager.injectedIPv4...), append([]string{}, manager.injectedIPv6...)
}

func parseInjectedIPList(raw string, wantIPv4 bool) []string {
	values := []string{}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return values
	}
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		values = strings.Split(trimmed, ",")
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		ip := net.ParseIP(strings.TrimSpace(strings.Trim(value, "[]")))
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		normalized := ""
		if wantIPv4 {
			if ip4 := ip.To4(); ip4 != nil {
				normalized = ip4.String()
			}
		} else if ip.To16() != nil && ip.To4() == nil {
			normalized = ip.String()
		}
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func collectSystemStatus(sampler *cpuSampler) systemStatus {
	memoryTotal, memoryUsed, swapTotal, swapUsed := readLinuxMemInfo()
	diskTotal, diskUsed := readDiskUsageRoot()
	return systemStatus{
		CPUPercent:        sampler.usagePercent(),
		MemoryTotalBytes:  memoryTotal,
		MemoryUsedBytes:   memoryUsed,
		MemoryUsedPercent: percentFromUsed(memoryUsed, memoryTotal),
		SwapTotalBytes:    swapTotal,
		SwapUsedBytes:     swapUsed,
		SwapUsedPercent:   percentFromUsed(swapUsed, swapTotal),
		DiskTotalBytes:    diskTotal,
		DiskUsedBytes:     diskUsed,
		DiskUsedPercent:   percentFromUsed(diskUsed, diskTotal),
	}
}

func (s *cpuSampler) usagePercent() float64 {
	if s == nil {
		return 0
	}
	snapshot, ok := readCPUSnapshot()
	if !ok {
		return 0
	}
	if !s.hasPrev {
		s.prev = snapshot
		s.hasPrev = true
		return 0
	}
	deltaTotal := snapshot.total - s.prev.total
	deltaIdle := snapshot.idle - s.prev.idle
	s.prev = snapshot
	if deltaTotal == 0 || deltaIdle > deltaTotal {
		return 0
	}
	used := deltaTotal - deltaIdle
	return (float64(used) / float64(deltaTotal)) * 100
}

func readCPUSnapshot() (cpuSnapshot, bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuSnapshot{}, false
	}
	defer f.Close()
	return parseCPUSnapshot(f)
}

func parseCPUSnapshot(r io.Reader) (cpuSnapshot, bool) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		return cpuSnapshot{}, false
	}
	fields := strings.Fields(strings.TrimSpace(scanner.Text()))
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuSnapshot{}, false
	}
	values := make([]uint64, 0, len(fields)-1)
	for _, field := range fields[1:] {
		v, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuSnapshot{}, false
		}
		values = append(values, v)
	}
	total := uint64(0)
	for _, v := range values {
		total += v
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return cpuSnapshot{total: total, idle: idle}, true
}

func readLinuxMemInfo() (memoryTotal uint64, memoryUsed uint64, swapTotal uint64, swapUsed uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, 0
	}
	defer f.Close()
	return parseLinuxMemInfo(f)
}

func parseLinuxMemInfo(r io.Reader) (memoryTotal uint64, memoryUsed uint64, swapTotal uint64, swapUsed uint64) {
	values := map[string]uint64{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		parts := strings.SplitN(strings.TrimSpace(scanner.Text()), ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		fields := strings.Fields(strings.TrimSpace(parts[1]))
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		values[key] = v * 1024
	}
	memoryTotal = values["MemTotal"]
	memAvailable := values["MemAvailable"]
	if memoryTotal >= memAvailable {
		memoryUsed = memoryTotal - memAvailable
	}
	swapTotal = values["SwapTotal"]
	swapFree := values["SwapFree"]
	if swapTotal >= swapFree {
		swapUsed = swapTotal - swapFree
	}
	return memoryTotal, memoryUsed, swapTotal, swapUsed
}

func percentFromUsed(used uint64, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return (float64(used) / float64(total)) * 100
}

func processControlMessage(raw json.RawMessage, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, nodeID string) {
	var envelope controlEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(envelope.Type)) {
	case "logs_get":
		var msg logsControlMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return
		}
		sendLogsControlResult(stream, encoder, writeMu, buildLogsControlResult(msg, nodeID))
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

func buildLogsControlResult(msg logsControlMessage, nodeID string) logsControlResult {
	lines := normalizeAndroidLogLines(msg.Lines)
	sinceMinutes := normalizeAndroidLogSinceMinutes(msg.SinceMinutes)
	minLevel := strings.TrimSpace(msg.MinLevel)
	content, entries := androidLogStore.tail(lines, sinceMinutes, minLevel)
	return logsControlResult{
		Type:         "logs_result",
		RequestID:    strings.TrimSpace(msg.RequestID),
		NodeID:       strings.TrimSpace(nodeID),
		OK:           true,
		Source:       "android",
		FilePath:     "memory://android_app",
		Lines:        lines,
		SinceMinutes: sinceMinutes,
		MinLevel:     minLevel,
		Content:      content,
		Entries:      entries,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
	}
}

func sendLogsControlResult(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, result logsControlResult) {
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = encoder.Encode(result)
	_ = stream.SetWriteDeadline(time.Time{})
}

func sendChainLinkControlResult(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, result chainLinkControlResult) {
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = encoder.Encode(result)
	_ = stream.SetWriteDeadline(time.Time{})
}

func (b *androidLogBuffer) add(source string, level string, message string) {
	if b == nil {
		return
	}
	text := strings.TrimSpace(message)
	if text == "" {
		return
	}
	cleanLevel := normalizeAndroidLogLevel(level)
	cleanSource := firstNonEmptyString(strings.TrimSpace(source), "android")
	now := time.Now().UTC().Format(time.RFC3339)
	line := fmt.Sprintf("%s [%s] [%s] %s", now, cleanLevel, cleanSource, text)
	entry := androidLogEntry{
		Time:    now,
		Level:   cleanLevel,
		Source:  cleanSource,
		Message: text,
		Line:    line,
	}
	b.mu.Lock()
	b.entries = append(b.entries, entry)
	if len(b.entries) > androidLogMaxEntries {
		b.entries = append([]androidLogEntry(nil), b.entries[len(b.entries)-androidLogMaxEntries:]...)
	}
	b.mu.Unlock()
}

func (b *androidLogBuffer) tail(lines int, sinceMinutes int, minLevel string) (string, []androidLogEntry) {
	if b == nil {
		return "", nil
	}
	limit := normalizeAndroidLogLines(lines)
	threshold := androidLogLevelRank(normalizeAndroidLogLevel(minLevel))
	cutoffEnabled := sinceMinutes > 0
	cutoff := time.Now().UTC().Add(-time.Duration(normalizeAndroidLogSinceMinutes(sinceMinutes)) * time.Minute)

	b.mu.Lock()
	defer b.mu.Unlock()
	filtered := make([]androidLogEntry, 0, len(b.entries))
	for _, entry := range b.entries {
		if cutoffEnabled {
			ts, err := time.Parse(time.RFC3339, strings.TrimSpace(entry.Time))
			if err == nil && ts.Before(cutoff) {
				continue
			}
		}
		if androidLogLevelRank(entry.Level) < threshold {
			continue
		}
		filtered = append(filtered, entry)
	}
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	linesOut := make([]string, 0, len(filtered))
	for _, entry := range filtered {
		linesOut = append(linesOut, entry.Line)
	}
	return strings.Join(linesOut, "\n"), filtered
}

func normalizeAndroidLogLines(lines int) int {
	if lines <= 0 {
		return 200
	}
	if lines > androidLogMaxEntries {
		return androidLogMaxEntries
	}
	return lines
}

func normalizeAndroidLogSinceMinutes(sinceMinutes int) int {
	if sinceMinutes <= 0 {
		return 0
	}
	if sinceMinutes > 2000 {
		return 2000
	}
	return sinceMinutes
}

func normalizeAndroidLogLevel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "realtime", "debug", "trace":
		return "realtime"
	case "warning", "warn":
		return "warning"
	case "error", "err":
		return "error"
	default:
		return "normal"
	}
}

func androidLogLevelRank(level string) int {
	switch normalizeAndroidLogLevel(level) {
	case "realtime":
		return 0
	case "warning":
		return 2
	case "error":
		return 3
	default:
		return 1
	}
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
	ws *websocket.Conn

	readMu sync.Mutex
	reader netReader

	writeCh        chan *webSocketWriteRequest
	writeDoneCh    chan struct{}
	writeCloseCh   chan struct{}
	writeCloseOnce sync.Once
}

type webSocketWriteRequest struct {
	payload []byte
	done    chan webSocketWriteResult
}

type webSocketWriteResult struct {
	n   int
	err error
}

type netReader interface {
	Read([]byte) (int, error)
}

func newWebSocketNetConn(ws *websocket.Conn) net.Conn {
	configureMobileWebSocketConn(ws)
	conn := &webSocketNetConn{
		ws:           ws,
		writeCh:      make(chan *webSocketWriteRequest, mobileWebSocketWriteQueueDepth),
		writeDoneCh:  make(chan struct{}),
		writeCloseCh: make(chan struct{}),
	}
	go conn.runWriteLoop()
	return conn
}

func configureMobileWebSocketConn(ws *websocket.Conn) {
	if ws == nil {
		return
	}
	ws.EnableWriteCompression(false)
	tuneMobileRelayNetConn(ws.UnderlyingConn())
}

func tuneMobileRelayNetConn(conn net.Conn) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcpConn.SetNoDelay(true)
	_ = tcpConn.SetKeepAlive(true)
	_ = tcpConn.SetKeepAlivePeriod(mobileRelayTCPKeepAlivePeriod)
	_ = tcpConn.SetReadBuffer(mobileRelayTCPSocketBufferBytes)
	_ = tcpConn.SetWriteBuffer(mobileRelayTCPSocketBufferBytes)
}

func mobileRelayCopy(dst io.Writer, src io.Reader) (int64, error) {
	buf, _ := mobileRelayCopyBufferPool.Get().([]byte)
	if len(buf) == 0 {
		buf = make([]byte, mobileRelayIOCopyBufferBytes)
	}
	defer mobileRelayCopyBufferPool.Put(buf)
	return io.CopyBuffer(dst, src, buf)
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
	if c == nil || c.ws == nil {
		return 0, net.ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	req := &webSocketWriteRequest{
		payload: p,
		done:    make(chan webSocketWriteResult, 1),
	}
	select {
	case c.writeCh <- req:
	case <-c.writeCloseCh:
		return 0, net.ErrClosed
	case <-c.writeDoneCh:
		return 0, net.ErrClosed
	}
	select {
	case result := <-req.done:
		return result.n, result.err
	case <-c.writeDoneCh:
		return 0, net.ErrClosed
	}
}

func (c *webSocketNetConn) runWriteLoop() {
	defer close(c.writeDoneCh)
	for {
		select {
		case req := <-c.writeCh:
			if req == nil {
				continue
			}
			if err := c.writeBatch(req); err != nil {
				c.failPendingWrites(err)
				return
			}
		case <-c.writeCloseCh:
			c.failPendingWrites(net.ErrClosed)
			return
		}
	}
}

func (c *webSocketNetConn) writeBatch(first *webSocketWriteRequest) error {
	batch := []*webSocketWriteRequest{first}
	total := len(first.payload)
	if total < mobileWebSocketWriteBatchBytes {
	collect:
		for total < mobileWebSocketWriteBatchBytes {
			select {
			case req := <-c.writeCh:
				if req == nil {
					continue
				}
				batch = append(batch, req)
				total += len(req.payload)
			case <-c.writeCloseCh:
				for _, req := range batch {
					req.done <- webSocketWriteResult{err: net.ErrClosed}
				}
				return net.ErrClosed
			default:
				break collect
			}
		}
	}
	writer, err := c.ws.NextWriter(websocket.BinaryMessage)
	if err != nil {
		for _, req := range batch {
			req.done <- webSocketWriteResult{err: err}
		}
		return err
	}
	results := make([]webSocketWriteResult, len(batch))
	var writeErr error
	for i, req := range batch {
		n, err := writer.Write(req.payload)
		results[i] = webSocketWriteResult{n: n, err: err}
		if err != nil {
			writeErr = err
			for j := i + 1; j < len(batch); j++ {
				results[j] = webSocketWriteResult{err: err}
			}
			break
		}
	}
	closeErr := writer.Close()
	resultErr := writeErr
	if resultErr == nil {
		resultErr = closeErr
	}
	for i, req := range batch {
		result := results[i]
		if result.err == nil && resultErr != nil {
			result.err = resultErr
		}
		req.done <- result
	}
	return resultErr
}

func (c *webSocketNetConn) failPendingWrites(err error) {
	if err == nil {
		err = net.ErrClosed
	}
	for {
		select {
		case req := <-c.writeCh:
			if req != nil {
				req.done <- webSocketWriteResult{err: err}
			}
		default:
			return
		}
	}
}

func (c *webSocketNetConn) Close() error {
	c.writeCloseOnce.Do(func() {
		close(c.writeCloseCh)
	})
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
