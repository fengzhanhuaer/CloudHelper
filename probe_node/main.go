package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var BuildVersion = "dev"

const defaultReportIntervalSec = 60

var reportIntervalSec atomic.Int64

type nodeStatus struct {
	Service   string `json:"service"`
	NodeID    string `json:"node_id"`
	HasSecret bool   `json:"has_secret"`
	Version   string `json:"version"`
	Timestamp string `json:"timestamp"`
}

type nodeIdentity struct {
	NodeID    string `json:"node_id"`
	Secret    string `json:"secret"`
	UpdatedAt string `json:"updated_at"`
}

type probeReportPayload struct {
	Type      string       `json:"type"`
	NodeID    string       `json:"node_id"`
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

type probeNonceResponse struct {
	Nonce     string `json:"nonce"`
	ExpiresAt string `json:"expires_at"`
}

type probeControlMessage struct {
	Type              string `json:"type"`
	Mode              string `json:"mode"`
	ReleaseRepo       string `json:"release_repo"`
	ControllerBaseURL string `json:"controller_base_url"`
	IntervalSec       int    `json:"interval_sec"`
	Timestamp         string `json:"timestamp"`
}

func main() {
	reportIntervalSec.Store(defaultReportIntervalSec)
	listenAddr := firstNonEmpty(os.Getenv("PROBE_NODE_LISTEN"), ":16030")
	identity, err := resolveNodeIdentity()
	if err != nil {
		log.Fatalf("failed to load node identity: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, nodeStatus{
			Service:   "probe_node",
			NodeID:    identity.NodeID,
			HasSecret: strings.TrimSpace(identity.Secret) != "",
			Version:   BuildVersion,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	})

	mux.HandleFunc("/api/node/info", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, nodeStatus{
			Service:   "probe_node",
			NodeID:    identity.NodeID,
			HasSecret: strings.TrimSpace(identity.Secret) != "",
			Version:   BuildVersion,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	})

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if wsURL, nonceURL := resolveProbeEndpoints(); wsURL != "" && nonceURL != "" {
		go startProbeReporter(wsURL, nonceURL, identity)
	} else {
		log.Printf("probe reporter disabled: set PROBE_CONTROLLER_URL or PROBE_CONTROLLER_WS")
	}

	log.Printf("probe node started: node_id=%s listen=%s version=%s", identity.NodeID, listenAddr, BuildVersion)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("probe node exited unexpectedly: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func detectHostName() string {
	hostname, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(hostname)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func resolveNodeIdentity() (nodeIdentity, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return nodeIdentity{}, err
	}

	identityPath := filepath.Join(dataDir, "node_identity.json")
	existing := nodeIdentity{}
	raw, err := os.ReadFile(identityPath)
	if err == nil {
		_ = json.Unmarshal(raw, &existing)
	} else if !os.IsNotExist(err) {
		return nodeIdentity{}, fmt.Errorf("read node identity: %w", err)
	}

	nodeID := firstNonEmpty(
		strings.TrimSpace(os.Getenv("PROBE_NODE_ID")),
		strings.TrimSpace(existing.NodeID),
		detectHostName(),
		"probe-node",
	)
	secret := firstNonEmpty(
		strings.TrimSpace(os.Getenv("PROBE_NODE_SECRET")),
		strings.TrimSpace(existing.Secret),
		randomSecret(32),
	)

	identity := nodeIdentity{
		NodeID:    nodeID,
		Secret:    secret,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	payload, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return nodeIdentity{}, fmt.Errorf("marshal node identity: %w", err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(identityPath, payload, 0o600); err != nil {
		return nodeIdentity{}, fmt.Errorf("write node identity: %w", err)
	}

	return identity, nil
}

func resolveDataDir() (string, error) {
	candidates := []string{filepath.Join(".", "data")}
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		candidates = append(candidates,
			filepath.Join(exeDir, "data"),
			filepath.Join(exeDir, "..", "data"),
		)
	}

	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		absPath, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if _, ok := seen[absPath]; ok {
			continue
		}
		seen[absPath] = struct{}{}

		if err := os.MkdirAll(absPath, 0o755); err == nil {
			return absPath, nil
		}
	}

	return "", fmt.Errorf("failed to resolve data directory")
}

func randomSecret(length int) string {
	if length <= 0 {
		return ""
	}
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("node-secret-%d", time.Now().UnixNano())
	}
	out := make([]byte, length)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}

func resolveProbeEndpoints() (wsURL string, nonceURL string) {
	rawWS := strings.TrimSpace(os.Getenv("PROBE_CONTROLLER_WS"))
	if rawWS != "" {
		u, err := url.Parse(rawWS)
		if err == nil && (strings.EqualFold(u.Scheme, "ws") || strings.EqualFold(u.Scheme, "wss")) {
			if strings.TrimSpace(u.Path) == "" || strings.TrimSpace(u.Path) == "/" {
				u.Path = "/api/probe"
			}
			wsURL = u.String()

			nonceBase := *u
			if strings.EqualFold(nonceBase.Scheme, "wss") {
				nonceBase.Scheme = "https"
			} else {
				nonceBase.Scheme = "http"
			}
			nonceBase.Path = "/api/probe/nonce"
			nonceBase.RawQuery = ""
			nonceBase.Fragment = ""
			return wsURL, nonceBase.String()
		}
		log.Printf("warning: invalid PROBE_CONTROLLER_WS=%q", rawWS)
	}

	rawController := strings.TrimSpace(os.Getenv("PROBE_CONTROLLER_URL"))
	if rawController == "" {
		return "", ""
	}

	u, err := url.Parse(rawController)
	if err != nil {
		log.Printf("warning: invalid PROBE_CONTROLLER_URL=%q: %v", rawController, err)
		return "", ""
	}

	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme == "https" {
		u.Scheme = "wss"
	} else if scheme == "http" {
		u.Scheme = "ws"
	} else {
		log.Printf("warning: unsupported PROBE_CONTROLLER_URL scheme=%q", u.Scheme)
		return "", ""
	}

	u.Path = "/api/probe"
	u.RawQuery = ""
	u.Fragment = ""
	wsURL = u.String()

	nonceBase, _ := url.Parse(rawController)
	nonceBase.Path = "/api/probe/nonce"
	nonceBase.RawQuery = ""
	nonceBase.Fragment = ""
	return wsURL, nonceBase.String()
}

func startProbeReporter(wsURL, nonceURL string, identity nodeIdentity) {
	sampler := &cpuSampler{}
	for {
		if err := runProbeReporterSession(wsURL, nonceURL, identity, sampler); err != nil {
			log.Printf("probe reporter disconnected: %v", err)
		}
		time.Sleep(5 * time.Second)
	}
}

func runProbeReporterSession(wsURL, nonceURL string, identity nodeIdentity, sampler *cpuSampler) error {
	nonce, err := requestProbeNonce(nonceURL, identity.NodeID)
	if err != nil {
		return err
	}
	signature := signProbeNonce(identity.Secret, nonce)

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	headers := http.Header{}
	headers.Set("X-Probe-Node-Id", identity.NodeID)
	headers.Set("X-Probe-Nonce", nonce)
	headers.Set("X-Probe-Signature", signature)
	conn, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Printf("probe reporter connected: %s", wsURL)

	if err := sendProbeReport(conn, identity, sampler); err != nil {
		return err
	}

	readErrCh := make(chan error, 1)
	go func() {
		for {
			_, raw, readErr := conn.ReadMessage()
			if readErr != nil {
				readErrCh <- readErr
				return
			}
			processProbeControlMessage(raw, nonceURL, identity)
		}
	}()

	for {
		wait := currentReportIntervalDuration()
		select {
		case err := <-readErrCh:
			return err
		case <-time.After(wait):
			if err := sendProbeReport(conn, identity, sampler); err != nil {
				return err
			}
		}
	}
}

func sendProbeReport(conn *websocket.Conn, identity nodeIdentity, sampler *cpuSampler) error {
	ipv4, ipv6 := collectIPs()
	system := collectSystemStatus(sampler)
	payload := probeReportPayload{
		Type:      "report",
		NodeID:    identity.NodeID,
		IPv4:      ipv4,
		IPv6:      ipv6,
		System:    system,
		Version:   BuildVersion,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(payload); err != nil {
		return err
	}
	log.Printf(
		"probe report sent: node_id=%s ipv4=%d ipv6=%d cpu=%.2f%% mem=%.2f%% disk=%.2f%% swap=%.2f%%",
		identity.NodeID,
		len(ipv4),
		len(ipv6),
		system.CPUPercent,
		system.MemoryUsedPercent,
		system.DiskUsedPercent,
		system.SwapUsedPercent,
	)
	return nil
}

func requestProbeNonce(nonceURL, nodeID string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, nonceURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Probe-Node-Id", strings.TrimSpace(nodeID))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("nonce request failed: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var payload probeNonceResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Nonce) == "" {
		return "", fmt.Errorf("nonce response is empty")
	}
	return strings.TrimSpace(payload.Nonce), nil
}

func signProbeNonce(secret, nonce string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

func processProbeControlMessage(raw []byte, nonceURL string, identity nodeIdentity) {
	var msg probeControlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	typeName := strings.TrimSpace(strings.ToLower(msg.Type))
	if typeName == "report_interval" {
		if sec := normalizeReportInterval(msg.IntervalSec); sec > 0 {
			reportIntervalSec.Store(int64(sec))
			log.Printf("probe reporter interval updated: %ds", sec)
		}
		return
	}
	if typeName != "upgrade" {
		return
	}
	go runProbeUpgrade(msg, nonceURL, identity)
}

func currentReportIntervalDuration() time.Duration {
	sec := normalizeReportInterval(int(reportIntervalSec.Load()))
	return time.Duration(sec) * time.Second
}

func normalizeReportInterval(sec int) int {
	if sec <= 0 {
		return defaultReportIntervalSec
	}
	if sec < 5 {
		return 5
	}
	if sec > 3600 {
		return 3600
	}
	return sec
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
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
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

	return ipv4, ipv6
}

func collectSystemStatus(sampler *cpuSampler) systemStatus {
	memoryTotal, memoryUsed, swapTotal, swapUsed := readLinuxMemInfo()
	diskTotal, diskUsed := readDiskUsageRoot()

	memoryPercent := percentFromUsed(memoryUsed, memoryTotal)
	swapPercent := percentFromUsed(swapUsed, swapTotal)
	diskPercent := percentFromUsed(diskUsed, diskTotal)
	cpuPercent := sampler.usagePercent()

	return systemStatus{
		CPUPercent:        cpuPercent,
		MemoryTotalBytes:  memoryTotal,
		MemoryUsedBytes:   memoryUsed,
		MemoryUsedPercent: memoryPercent,
		SwapTotalBytes:    swapTotal,
		SwapUsedBytes:     swapUsed,
		SwapUsedPercent:   swapPercent,
		DiskTotalBytes:    diskTotal,
		DiskUsedBytes:     diskUsed,
		DiskUsedPercent:   diskPercent,
	}
}

func (s *cpuSampler) usagePercent() float64 {
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
	if deltaTotal == 0 {
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

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return cpuSnapshot{}, false
	}
	line := strings.TrimSpace(scanner.Text())
	fields := strings.Fields(line)
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

	values := map[string]uint64{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		right := strings.Fields(strings.TrimSpace(parts[1]))
		if len(right) == 0 {
			continue
		}
		v, err := strconv.ParseUint(right[0], 10, 64)
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
