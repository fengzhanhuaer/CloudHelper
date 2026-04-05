package backend

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	dnsUpstreamConfigFileName      = "network_dns_servers.json"
	dnsUpstreamConfigCheckInterval = 5 * time.Second
	dnsUpstreamResolveTimeout      = 5 * time.Second
	dnsUpstreamDoHReadLimit        = 64 * 1024
	dnsUpstreamDefaultDoHPath      = "/dns-query"
)

var (
	defaultDNSUpstreamPlainServers = []string{"223.5.5.5", "119.29.29.29"}
	defaultDNSUpstreamDoTServers   = []string{"dns.alidns.com:853", "dot.pub:853"}
	defaultDNSUpstreamDoHServers   = []string{"https://dns.alidns.com/dns-query", "https://doh.pub/dns-query"}
)

type dnsUpstreamConfigFilePayload struct {
	Prefer          string   `json:"prefer"`
	DNSServers      []string `json:"dns_servers"`
	DoTServers      []string `json:"dot_servers"`
	DoHServers      []string `json:"doh_servers"`
	FakeIPCIDR      string   `json:"fake_ip_cidr,omitempty"`
	FakeIPWhitelist []string `json:"fake_ip_whitelist,omitempty"`
}

type dnsUpstreamConfig struct {
	Prefer          string
	DNSServers      []dnsPlainServer
	DoTServers      []dnsDoTServer
	DoHServers      []dnsDoHServer
	FakeIPCIDR      string
	FakeIPWhitelist []string
}

type dnsPlainServer struct {
	Host    string
	Port    string
	Address string
}

type dnsDoTServer struct {
	Host          string
	Port          string
	Address       string
	TLSServerName string
}

type dnsDoHServer struct {
	URL           string
	Host          string
	Port          string
	TLSServerName string
}

var dnsUpstreamConfigState = struct {
	mu         sync.Mutex
	loaded     bool
	path       string
	lastCheck  time.Time
	lastModUTC time.Time
	config     dnsUpstreamConfig
}{}

func defaultDNSUpstreamConfigFilePayload() dnsUpstreamConfigFilePayload {
	return dnsUpstreamConfigFilePayload{
		Prefer:          "doh",
		DNSServers:      append([]string(nil), defaultDNSUpstreamPlainServers...),
		DoTServers:      append([]string(nil), defaultDNSUpstreamDoTServers...),
		DoHServers:      append([]string(nil), defaultDNSUpstreamDoHServers...),
		FakeIPCIDR:      "198.18.0.0/15",
		FakeIPWhitelist: []string{},
	}
}

func defaultDNSUpstreamConfig() dnsUpstreamConfig {
	return normalizeDNSUpstreamConfig(defaultDNSUpstreamConfigFilePayload())
}

func cloneDNSUpstreamConfig(source dnsUpstreamConfig) dnsUpstreamConfig {
	return dnsUpstreamConfig{
		Prefer:          source.Prefer,
		DNSServers:      append([]dnsPlainServer(nil), source.DNSServers...),
		DoTServers:      append([]dnsDoTServer(nil), source.DoTServers...),
		DoHServers:      append([]dnsDoHServer(nil), source.DoHServers...),
		FakeIPCIDR:      source.FakeIPCIDR,
		FakeIPWhitelist: append([]string(nil), source.FakeIPWhitelist...),
	}
}

func getDNSUpstreamConfig() (dnsUpstreamConfig, error) {
	dnsUpstreamConfigState.mu.Lock()
	defer dnsUpstreamConfigState.mu.Unlock()

	path, err := ensureDNSUpstreamConfigPathLocked()
	if err != nil {
		return defaultDNSUpstreamConfig(), err
	}

	needReload := !dnsUpstreamConfigState.loaded
	if !needReload {
		now := time.Now()
		if now.Sub(dnsUpstreamConfigState.lastCheck) >= dnsUpstreamConfigCheckInterval {
			dnsUpstreamConfigState.lastCheck = now
			if info, statErr := os.Stat(path); statErr == nil {
				modUTC := info.ModTime().UTC()
				if modUTC.After(dnsUpstreamConfigState.lastModUTC) || modUTC.Before(dnsUpstreamConfigState.lastModUTC) {
					needReload = true
				}
			} else if errors.Is(statErr, os.ErrNotExist) {
				needReload = true
			} else {
				return cloneDNSUpstreamConfig(dnsUpstreamConfigState.config), statErr
			}
		}
	}

	if needReload {
		loaded, loadErr := loadDNSUpstreamConfigFromDiskLocked(path)
		if loadErr != nil {
			if dnsUpstreamConfigState.loaded {
				return cloneDNSUpstreamConfig(dnsUpstreamConfigState.config), loadErr
			}
			fallback := defaultDNSUpstreamConfig()
			dnsUpstreamConfigState.config = fallback
			dnsUpstreamConfigState.loaded = true
			dnsUpstreamConfigState.lastCheck = time.Now()
			return cloneDNSUpstreamConfig(fallback), loadErr
		}
		dnsUpstreamConfigState.config = loaded
		dnsUpstreamConfigState.loaded = true
	}

	return cloneDNSUpstreamConfig(dnsUpstreamConfigState.config), nil
}

func ensureDNSUpstreamConfigPathLocked() (string, error) {
	if strings.TrimSpace(dnsUpstreamConfigState.path) != "" {
		return dnsUpstreamConfigState.path, nil
	}
	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, dnsUpstreamConfigFileName)
	dnsUpstreamConfigState.path = path
	return path, nil
}

func loadDNSUpstreamConfigFromDiskLocked(path string) (dnsUpstreamConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			defaults := defaultDNSUpstreamConfigFilePayload()
			if writeErr := writeDNSUpstreamConfigFile(path, defaults); writeErr != nil {
				return defaultDNSUpstreamConfig(), writeErr
			}
			raw, err = os.ReadFile(path)
			if err != nil {
				return defaultDNSUpstreamConfig(), err
			}
		} else {
			return defaultDNSUpstreamConfig(), err
		}
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		defaults := defaultDNSUpstreamConfigFilePayload()
		if writeErr := writeDNSUpstreamConfigFile(path, defaults); writeErr != nil {
			return defaultDNSUpstreamConfig(), writeErr
		}
		return normalizeDNSUpstreamConfig(defaults), updateDNSUpstreamConfigFileModTimeLocked(path)
	}

	var payload dnsUpstreamConfigFilePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return defaultDNSUpstreamConfig(), fmt.Errorf("parse dns upstream config failed: %w", err)
	}

	config := normalizeDNSUpstreamConfig(payload)
	if err := updateDNSUpstreamConfigFileModTimeLocked(path); err != nil {
		return config, err
	}
	return config, nil
}

// ensureDNSUpstreamConfigPath 获取 DNS 上游配置文件路径（线程安全）
func ensureDNSUpstreamConfigPath() (string, error) {
	dnsUpstreamConfigState.mu.Lock()
	defer dnsUpstreamConfigState.mu.Unlock()
	return ensureDNSUpstreamConfigPathLocked()
}

// readDNSUpstreamConfigPayload 从磁盘读取并解析 DNS 上游配置原始 payload
func readDNSUpstreamConfigPayload(path string) (dnsUpstreamConfigFilePayload, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultDNSUpstreamConfigFilePayload(), nil
		}
		return dnsUpstreamConfigFilePayload{}, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return defaultDNSUpstreamConfigFilePayload(), nil
	}
	var payload dnsUpstreamConfigFilePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return dnsUpstreamConfigFilePayload{}, fmt.Errorf("parse dns upstream config failed: %w", err)
	}
	return payload, nil
}

// invalidateDNSUpstreamConfigCache 使内存缓存失效，强制下次读取时重新加载文件
func invalidateDNSUpstreamConfigCache() {
	dnsUpstreamConfigState.mu.Lock()
	dnsUpstreamConfigState.loaded = false
	dnsUpstreamConfigState.lastCheck = time.Time{}
	dnsUpstreamConfigState.mu.Unlock()
}

func updateDNSUpstreamConfigFileModTimeLocked(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	dnsUpstreamConfigState.lastCheck = time.Now()
	dnsUpstreamConfigState.lastModUTC = info.ModTime().UTC()
	return nil
}

func writeDNSUpstreamConfigFile(path string, payload dnsUpstreamConfigFilePayload) error {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return err
	}
	if err := autoBackupManagerData(); err != nil {
		return err
	}
	return nil
}

func normalizeDNSUpstreamConfig(payload dnsUpstreamConfigFilePayload) dnsUpstreamConfig {
	config := dnsUpstreamConfig{
		Prefer:          normalizeDNSUpstreamPrefer(payload.Prefer),
		FakeIPCIDR:      strings.TrimSpace(payload.FakeIPCIDR),
		FakeIPWhitelist: normalizeDNSUpstreamFakeIPWhitelist(payload.FakeIPWhitelist),
	}

	for _, rawServer := range payload.DNSServers {
		if item, ok := normalizeDNSPlainServer(rawServer); ok {
			config.DNSServers = append(config.DNSServers, item)
		}
	}
	for _, rawServer := range payload.DoTServers {
		if item, ok := normalizeDNSDoTServer(rawServer); ok {
			config.DoTServers = append(config.DoTServers, item)
		}
	}
	for _, rawServer := range payload.DoHServers {
		if item, ok := normalizeDNSDoHServer(rawServer); ok {
			config.DoHServers = append(config.DoHServers, item)
		}
	}

	config.DNSServers = dedupeDNSPlainServers(config.DNSServers)
	config.DoTServers = dedupeDNSDoTServers(config.DoTServers)
	config.DoHServers = dedupeDNSDoHServers(config.DoHServers)

	if len(config.DNSServers) == 0 {
		for _, rawServer := range defaultDNSUpstreamPlainServers {
			if item, ok := normalizeDNSPlainServer(rawServer); ok {
				config.DNSServers = append(config.DNSServers, item)
			}
		}
		config.DNSServers = dedupeDNSPlainServers(config.DNSServers)
	}
	if len(config.DoTServers) == 0 {
		for _, rawServer := range defaultDNSUpstreamDoTServers {
			if item, ok := normalizeDNSDoTServer(rawServer); ok {
				config.DoTServers = append(config.DoTServers, item)
			}
		}
		config.DoTServers = dedupeDNSDoTServers(config.DoTServers)
	}
	if len(config.DoHServers) == 0 {
		for _, rawServer := range defaultDNSUpstreamDoHServers {
			if item, ok := normalizeDNSDoHServer(rawServer); ok {
				config.DoHServers = append(config.DoHServers, item)
			}
		}
		config.DoHServers = dedupeDNSDoHServers(config.DoHServers)
	}

	return config
}

func normalizeDNSUpstreamFakeIPWhitelist(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" || strings.HasPrefix(item, "#") {
			continue
		}
		out = append(out, strings.ToLower(item))
	}
	return out
}

func normalizeDNSUpstreamPrefer(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "dns", "udp":
		return "dns"
	case "dot", "tls":
		return "dot"
	default:
		return "doh"
	}
}

func buildDNSUpstreamQueryOrder(prefer string) []string {
	switch normalizeDNSUpstreamPrefer(prefer) {
	case "dns":
		return []string{"dns", "doh", "dot"}
	case "dot":
		return []string{"dot", "doh", "dns"}
	default:
		return []string{"doh", "dot", "dns"}
	}
}

func normalizeDNSUpstreamHost(raw string) string {
	host := strings.TrimSpace(strings.Trim(raw, "[]"))
	if host == "" {
		return ""
	}
	if strings.Contains(host, " ") {
		return ""
	}
	if parsedIP := net.ParseIP(host); parsedIP != nil {
		return canonicalIP(parsedIP)
	}
	return strings.ToLower(host)
}

func normalizeDNSPlainServer(raw string) (dnsPlainServer, bool) {
	host, port, ok := normalizeDNSHostPort(raw, "53")
	if !ok {
		return dnsPlainServer{}, false
	}
	parsedIP := net.ParseIP(host)
	if parsedIP == nil || parsedIP.To4() == nil {
		return dnsPlainServer{}, false
	}
	host = parsedIP.To4().String()
	return dnsPlainServer{
		Host:    host,
		Port:    port,
		Address: net.JoinHostPort(host, port),
	}, true
}

func normalizeDNSDoTServer(raw string) (dnsDoTServer, bool) {
	host, port, ok := normalizeDNSHostPort(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "tls://")), "853")
	if !ok {
		return dnsDoTServer{}, false
	}
	return dnsDoTServer{
		Host:          host,
		Port:          port,
		Address:       net.JoinHostPort(host, port),
		TLSServerName: host,
	}, true
}

func normalizeDNSDoHServer(raw string) (dnsDoHServer, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return dnsDoHServer{}, false
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil {
		return dnsDoHServer{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(parsed.Scheme), "https") {
		return dnsDoHServer{}, false
	}
	host := normalizeDNSUpstreamHost(parsed.Hostname())
	if host == "" {
		return dnsDoHServer{}, false
	}
	port := strings.TrimSpace(parsed.Port())
	if port == "" {
		port = "443"
	}
	if !isValidPort(port) {
		return dnsDoHServer{}, false
	}
	path := strings.TrimSpace(parsed.Path)
	if path == "" {
		path = dnsUpstreamDefaultDoHPath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	rebuilt := &url.URL{
		Scheme:   "https",
		Host:     host,
		Path:     path,
		RawQuery: parsed.RawQuery,
	}
	if port != "443" {
		rebuilt.Host = net.JoinHostPort(host, port)
	}

	return dnsDoHServer{
		URL:           rebuilt.String(),
		Host:          host,
		Port:          port,
		TLSServerName: host,
	}, true
}

func normalizeDNSHostPort(raw string, defaultPort string) (host string, port string, ok bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", "", false
	}

	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed == nil {
			return "", "", false
		}
		host = normalizeDNSUpstreamHost(parsed.Hostname())
		port = strings.TrimSpace(parsed.Port())
	} else {
		if splitHost, splitPort, err := net.SplitHostPort(value); err == nil {
			host = normalizeDNSUpstreamHost(splitHost)
			port = strings.TrimSpace(splitPort)
		} else {
			host = normalizeDNSUpstreamHost(value)
			port = ""
		}
	}

	if host == "" {
		return "", "", false
	}
	if strings.TrimSpace(port) == "" {
		port = strings.TrimSpace(defaultPort)
	}
	if !isValidPort(port) {
		return "", "", false
	}
	return host, port, true
}

func isValidPort(port string) bool {
	value, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil {
		return false
	}
	return value > 0 && value <= 65535
}

func dedupeDNSPlainServers(items []dnsPlainServer) []dnsPlainServer {
	if len(items) == 0 {
		return []dnsPlainServer{}
	}
	out := make([]dnsPlainServer, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		key := strings.ToLower(strings.TrimSpace(item.Address))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func dedupeDNSDoTServers(items []dnsDoTServer) []dnsDoTServer {
	if len(items) == 0 {
		return []dnsDoTServer{}
	}
	out := make([]dnsDoTServer, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		key := strings.ToLower(strings.TrimSpace(item.Address))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func dedupeDNSDoHServers(items []dnsDoHServer) []dnsDoHServer {
	if len(items) == 0 {
		return []dnsDoHServer{}
	}
	out := make([]dnsDoHServer, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		key := strings.ToLower(strings.TrimSpace(item.URL))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func (s *networkAssistantService) collectConfiguredDNSBypassIPv4Addrs() []string {
	config, err := getDNSUpstreamConfig()
	if err != nil {
		s.logfRateLimited("dns-upstream-config-load", 30*time.Second, "load dns upstream config failed, fallback to defaults: %v", err)
	}

	ipSet := make(map[string]struct{})
	addIPv4 := func(rawIP string) {
		parsedIP := net.ParseIP(strings.TrimSpace(rawIP))
		if parsedIP == nil || parsedIP.To4() == nil {
			return
		}
		ipSet[parsedIP.To4().String()] = struct{}{}
	}

	for _, server := range config.DNSServers {
		addIPv4(server.Host)
	}
	for _, server := range config.DoTServers {
		addIPv4(server.Host)
		if cachedIP, ok := getProbeDNSCachedIP(server.Host); ok {
			addIPv4(cachedIP)
		}
	}
	for _, server := range config.DoHServers {
		addIPv4(server.Host)
		if cachedIP, ok := getProbeDNSCachedIP(server.Host); ok {
			addIPv4(cachedIP)
		}
	}

	out := make([]string, 0, len(ipSet))
	for ipValue := range ipSet {
		out = append(out, ipValue)
	}
	sort.Strings(out)
	return out
}

func resolveDNSUpstreamDialHost(service *networkAssistantService, rawHost string, bootstrapServers []dnsPlainServer, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = dnsUpstreamResolveTimeout
	}
	host := normalizeDNSUpstreamHost(rawHost)
	if host == "" {
		return "", errors.New("invalid dns upstream host")
	}
	if parsedIP := net.ParseIP(host); parsedIP != nil {
		return canonicalIP(parsedIP), nil
	}
	if cachedIP, ok := getProbeDNSCachedIP(host); ok {
		if parsedIP := net.ParseIP(cachedIP); parsedIP != nil {
			return canonicalIP(parsedIP), nil
		}
	}
	if len(bootstrapServers) == 0 {
		return "", errors.New("dns bootstrap servers are not configured")
	}

	queryID := uint16(time.Now().UnixNano())
	packet, err := buildDNSQueryPacket(host, 1, queryID)
	if err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	var lastErr error
	for _, server := range bootstrapServers {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			lastErr = errors.New("dns resolve timeout")
			break
		}
		payload, queryErr := service.queryRawDNSPacket(server.Address, packet, remaining)
		if queryErr != nil {
			lastErr = queryErr
			continue
		}
		addrs, _, parseErr := parseDNSResponseAddrs(payload, queryID, 1)
		if parseErr != nil {
			lastErr = parseErr
			continue
		}
		for _, addr := range addrs {
			parsedIP := net.ParseIP(strings.TrimSpace(addr))
			if parsedIP == nil {
				continue
			}
			resolved := canonicalIP(parsedIP)
			if resolved == "" {
				continue
			}
			_ = setProbeDNSCachedIP(host, resolved)
			return resolved, nil
		}
		lastErr = errors.New("resolve dns upstream host returned empty result")
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("resolve dns upstream host failed")
}

func (s *networkAssistantService) queryRawDNSPacketViaDoT(server dnsDoTServer, packet []byte, timeout time.Duration, bootstrapServers []dnsPlainServer) ([]byte, error) {
	if strings.TrimSpace(server.Host) == "" || strings.TrimSpace(server.Port) == "" {
		return nil, errors.New("invalid dot server")
	}
	if len(packet) <= 0 || len(packet) > 0xffff {
		return nil, errors.New("invalid dns query payload")
	}
	dialHost, err := resolveDNSUpstreamDialHost(s, server.Host, bootstrapServers, timeout)
	if err != nil {
		return nil, err
	}
	dialAddr := net.JoinHostPort(dialHost, server.Port)

	releaseBypass, bypassErr := s.acquireTUNDirectBypassRoute(dialAddr)
	if bypassErr != nil {
		return nil, bypassErr
	}
	defer releaseBypass()

	tlsServerName := strings.TrimSpace(server.TLSServerName)
	if tlsServerName == "" {
		tlsServerName = strings.TrimSpace(server.Host)
	}

	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", dialAddr, &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: tlsServerName,
	})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}

	lengthHeader := [2]byte{}
	binary.BigEndian.PutUint16(lengthHeader[:], uint16(len(packet)))
	if err := writeAll(conn, lengthHeader[:]); err != nil {
		return nil, err
	}
	if err := writeAll(conn, packet); err != nil {
		return nil, err
	}

	if _, err := io.ReadFull(conn, lengthHeader[:]); err != nil {
		return nil, err
	}
	responseLen := int(binary.BigEndian.Uint16(lengthHeader[:]))
	if responseLen <= 0 {
		return nil, errors.New("dns resolve returned empty payload")
	}
	if responseLen > 65535 {
		return nil, errors.New("dns resolve payload too large")
	}

	response := make([]byte, responseLen)
	if _, err := io.ReadFull(conn, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *networkAssistantService) queryRawDNSPacketViaDoH(server dnsDoHServer, packet []byte, timeout time.Duration, bootstrapServers []dnsPlainServer) ([]byte, error) {
	if strings.TrimSpace(server.URL) == "" || strings.TrimSpace(server.Host) == "" || strings.TrimSpace(server.Port) == "" {
		return nil, errors.New("invalid doh server")
	}
	dialHost, err := resolveDNSUpstreamDialHost(s, server.Host, bootstrapServers, timeout)
	if err != nil {
		return nil, err
	}
	dialAddr := net.JoinHostPort(dialHost, server.Port)

	releaseBypass, bypassErr := s.acquireTUNDirectBypassRoute(dialAddr)
	if bypassErr != nil {
		return nil, bypassErr
	}
	defer releaseBypass()

	tlsServerName := strings.TrimSpace(server.TLSServerName)
	if tlsServerName == "" {
		tlsServerName = strings.TrimSpace(server.Host)
	}

	transport := &http.Transport{
		Proxy:             nil,
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network string, _ string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: timeout}
			return dialer.DialContext(ctx, network, dialAddr)
		},
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: tlsServerName,
		},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{Transport: transport, Timeout: timeout}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewReader(packet))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/dns-message")
	request.Header.Set("Accept", "application/dns-message")

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return nil, fmt.Errorf("doh status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	payload, err := io.ReadAll(io.LimitReader(response.Body, dnsUpstreamDoHReadLimit))
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return nil, errors.New("dns resolve returned empty payload")
	}
	return payload, nil
}
