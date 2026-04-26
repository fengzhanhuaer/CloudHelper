package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
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
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

var BuildVersion = "dev"

const defaultReportIntervalSec = 60

var reportIntervalSec atomic.Int64

type nodeStatus struct {
	Status    string `json:"status"`
	NodeID    string `json:"node_id,omitempty"`
	Version   string `json:"version,omitempty"`
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

type probeChainPortForwardMessage struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	EntrySide  string `json:"entry_side"`
	ListenHost string `json:"listen_host"`
	ListenPort int    `json:"listen_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Network    string `json:"network"`
	Enabled    bool   `json:"enabled"`
}

type probeControlMessage struct {
	Type              string                         `json:"type"`
	Mode              string                         `json:"mode"`
	Action            string                         `json:"action"`
	Protocol          string                         `json:"protocol"`
	ChainID           string                         `json:"chain_id"`
	ChainType         string                         `json:"chain_type"`
	Name              string                         `json:"name"`
	UserID            string                         `json:"user_id"`
	UserPublicKey     string                         `json:"user_public_key"`
	LinkSecret        string                         `json:"link_secret"`
	Role              string                         `json:"role"`
	ListenHost        string                         `json:"listen_host"`
	ListenPort        int                            `json:"listen_port"`
	LinkLayer         string                         `json:"link_layer"`
	NextLinkLayer     string                         `json:"next_link_layer"`
	NextDialMode      string                         `json:"next_dial_mode"`
	InternalPort      int                            `json:"internal_port"`
	NextHost          string                         `json:"next_host"`
	NextPort          int                            `json:"next_port"`
	PrevHost          string                         `json:"prev_host"`
	PrevPort          int                            `json:"prev_port"`
	PrevLinkLayer     string                         `json:"prev_link_layer"`
	PrevDialMode      string                         `json:"prev_dial_mode"`
	PortForwards      []probeChainPortForwardMessage `json:"port_forwards"`
	RequireUserAuth   bool                           `json:"require_user_auth"`
	NextAuthMode      string                         `json:"next_auth_mode"`
	SessionID         string                         `json:"session_id"`
	Command           string                         `json:"command"`
	TimeoutSec        int                            `json:"timeout_sec"`
	ReleaseRepo       string                         `json:"release_repo"`
	ControllerBaseURL string                         `json:"controller_base_url"`
	IntervalSec       int                            `json:"interval_sec"`
	RequestID         string                         `json:"request_id"`
	Lines             int                            `json:"lines"`
	SinceMinutes      int                            `json:"since_minutes"`
	MinLevel          string                         `json:"min_level"`
	Timestamp         string                         `json:"timestamp"`
}

type probeLaunchOptions struct {
	ListenAddr               string
	LocalListenAddr          string
	NodeID                   string
	NodeSecret               string
	ControllerURL            string
	ControllerWS             string
	ServiceName              string
	UpgradeVerify            bool
	UpgradeVerifyDurationSec int
	LocalTUNInstall          bool
}

func main() {
	initProbeLogger()
	reportIntervalSec.Store(defaultReportIntervalSec)
	options := parseProbeLaunchOptions()
	if options.LocalTUNInstall {
		if err := installProbeLocalTUNDriver(); err != nil {
			logProbeErrorf("probe local tun install mode failed: %v", err)
			log.Fatalf("probe local tun install mode failed: %v", err)
		}
		logProbeInfof("probe local tun install mode finished")
		return
	}

	if options.UpgradeVerify {
		if err := runProbeUpgradeVerifyMode(options); err != nil {
			logProbeErrorf("probe upgrade verification failed: %v", err)
			log.Fatalf("probe upgrade verification failed: %v", err)
		}
		return
	}

	if err := runProbeNodeEntry(options); err != nil {
		logProbeErrorf("probe node exited unexpectedly: %v", err)
		log.Fatalf("probe node exited unexpectedly: %v", err)
	}
}

func parseProbeLaunchOptions() probeLaunchOptions {
	options := probeLaunchOptions{}
	flag.StringVar(&options.ListenAddr, "listen", "", "probe listen address (fallback: PROBE_NODE_LISTEN or :16030)")
	flag.StringVar(&options.LocalListenAddr, "local-listen", "", "probe local console listen address (fallback: PROBE_LOCAL_LISTEN or 127.0.0.1:16032)")
	flag.StringVar(&options.NodeID, "node-id", "", "probe node id (fallback: PROBE_NODE_ID)")
	flag.StringVar(&options.NodeSecret, "node-secret", "", "probe node secret (fallback: PROBE_NODE_SECRET)")
	flag.StringVar(&options.ControllerURL, "controller-url", "", "controller base url, e.g. https://127.0.0.1:15030 (fallback: PROBE_CONTROLLER_URL)")
	flag.StringVar(&options.ControllerWS, "controller-ws", "", "controller websocket url, e.g. wss://127.0.0.1:15030/api/probe (fallback: PROBE_CONTROLLER_WS)")
	flag.StringVar(&options.ServiceName, "service-name", "", "windows service name")
	flag.BoolVar(&options.UpgradeVerify, "upgrade-verify", false, "internal: run upgrade verification mode")
	flag.IntVar(&options.UpgradeVerifyDurationSec, "upgrade-verify-duration", defaultUpgradeVerifyDurationSec, "internal: upgrade verification duration in seconds")
	flag.BoolVar(&options.LocalTUNInstall, "local-tun-install", false, "internal: run local tun install mode")
	flag.Parse()
	return options
}

func runProbeNode(options probeLaunchOptions) error {
	identity, err := resolveNodeIdentity(strings.TrimSpace(options.NodeID), strings.TrimSpace(options.NodeSecret))
	if err != nil {
		return fmt.Errorf("failed to load node identity: %w", err)
	}
	if _, err := ensureProbeLocalAuthManager(); err != nil {
		return fmt.Errorf("failed to initialize local console auth: %w", err)
	}
	if err := ensureProbeLocalProxyDefaultsInitialized(); err != nil {
		return fmt.Errorf("failed to initialize local proxy default files: %w", err)
	}
	ensureProbeLocalDNSServiceStarted()
	controllerBaseURL := resolveProbeControllerBaseURL(strings.TrimSpace(options.ControllerURL), strings.TrimSpace(options.ControllerWS))
	setProbeLocalProxyRuntimeContext(identity, controllerBaseURL)

	nodeMux := buildProbeNodeHTTPMux(identity)
	localMux := buildProbeLocalConsoleMux()
	if err := startProbeLocalConsoleServer(localMux, strings.TrimSpace(options.LocalListenAddr)); err != nil {
		return fmt.Errorf("failed to start local console: %w", err)
	}

	if wsURL := resolveProbeEndpoints(strings.TrimSpace(options.ControllerWS), strings.TrimSpace(options.ControllerURL)); wsURL != "" {
		go startProbeReporter(wsURL, identity)
	} else {
		logProbeWarnf("probe reporter disabled: set PROBE_CONTROLLER_URL or PROBE_CONTROLLER_WS")
	}
	restoreProbeChainRuntimesFromTopologyCache(identity, controllerBaseURL)
	startProbeLinkChainsSyncLoop(identity, controllerBaseURL)
	startProbeServiceRuntimeLoop(nodeMux, identity, controllerBaseURL)

	logProbeInfof("probe node started: node_id=%s version=%s", identity.NodeID, BuildVersion)
	select {}
}

func buildProbeNodeHTTPMux(identity nodeIdentity) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			writeProbeOpenAIStyleMethodNotAllowed(w, r.Method)
			return
		}
		sleepProbeOpenAIStyleJitter()
		writeJSON(w, http.StatusOK, map[string]any{
			"message":   "OpenAI-compatible API endpoint",
			"api_base":  "/v1",
			"version":   BuildVersion,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("/v1", func(w http.ResponseWriter, r *http.Request) {
		sleepProbeOpenAIStyleJitter()
		writeProbeOpenAIStyleUnauthorized(w)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		sleepProbeOpenAIStyleJitter()
		writeProbeOpenAIStyleUnauthorized(w)
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		sleepProbeOpenAIStyleJitter()
		writeProbeOpenAIStyleUnauthorized(w)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, nodeStatus{
			Status:    "ok",
			NodeID:    identity.NodeID,
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
			Status:    "ok",
			NodeID:    identity.NodeID,
			Version:   BuildVersion,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc(probeChainRelayAPIPath, handleProbeChainRelayHTTP)
	return mux
}

func buildProbeOpenAIStyleModelsListPayload() map[string]any {
	return map[string]any{
		"object": "list",
		"data": []any{
			map[string]any{
				"id":       "gpt-4o-mini",
				"object":   "model",
				"created":  1686935002,
				"owned_by": "openai",
			},
		},
	}
}

func writeProbeOpenAIStyleUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error": map[string]any{
			"message": "Incorrect API key provided.",
			"type":    "invalid_request_error",
			"param":   nil,
			"code":    "invalid_api_key",
		},
	})
}

func writeProbeOpenAIStyleMethodNotAllowed(w http.ResponseWriter, method string) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
		"error": map[string]any{
			"message": fmt.Sprintf("Method %s is not allowed for this endpoint.", strings.TrimSpace(method)),
			"type":    "invalid_request_error",
			"param":   nil,
			"code":    "method_not_allowed",
		},
	})
}

func probeOpenAIStyleJitterDuration() time.Duration {
	const minMs = int64(300)
	const spanMs = int64(701)
	offset := time.Now().UnixNano() % spanMs
	if offset < 0 {
		offset = -offset
	}
	return time.Duration(minMs+offset) * time.Millisecond
}

func sleepProbeOpenAIStyleJitter() {
	time.Sleep(probeOpenAIStyleJitterDuration())
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

func resolveNodeIdentity(explicitNodeID string, explicitSecret string) (nodeIdentity, error) {
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
		strings.TrimSpace(explicitNodeID),
		strings.TrimSpace(os.Getenv("PROBE_NODE_ID")),
		strings.TrimSpace(existing.NodeID),
		detectHostName(),
		"probe-node",
	)
	secret := firstNonEmpty(
		strings.TrimSpace(explicitSecret),
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
	candidates := make([]string, 0, 4)
	if envDir := strings.TrimSpace(os.Getenv("PROBE_NODE_DATA_DIR")); envDir != "" {
		candidates = append(candidates, envDir)
	}
	candidates = append(candidates, filepath.Join(".", "data"))
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

func resolveProbeEndpoints(explicitWSURL string, explicitControllerURL string) string {
	rawWS := firstNonEmpty(strings.TrimSpace(explicitWSURL), strings.TrimSpace(os.Getenv("PROBE_CONTROLLER_WS")))
	if rawWS != "" {
		u, err := url.Parse(rawWS)
		if err == nil && (strings.EqualFold(u.Scheme, "ws") || strings.EqualFold(u.Scheme, "wss")) {
			if strings.TrimSpace(u.Path) == "" || strings.TrimSpace(u.Path) == "/" {
				u.Path = "/api/probe"
			}
			u.RawQuery = ""
			u.Fragment = ""
			return u.String()
		}
		log.Printf("warning: invalid PROBE_CONTROLLER_WS=%q", rawWS)
	}

	rawController := firstNonEmpty(strings.TrimSpace(explicitControllerURL), strings.TrimSpace(os.Getenv("PROBE_CONTROLLER_URL")))
	if rawController == "" {
		return ""
	}

	u, err := url.Parse(rawController)
	if err != nil {
		log.Printf("warning: invalid PROBE_CONTROLLER_URL=%q: %v", rawController, err)
		return ""
	}

	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme == "https" {
		u.Scheme = "wss"
	} else if scheme == "http" {
		u.Scheme = "ws"
	} else {
		log.Printf("warning: unsupported PROBE_CONTROLLER_URL scheme=%q", u.Scheme)
		return ""
	}

	u.Path = "/api/probe"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func startProbeReporter(wsURL string, identity nodeIdentity) {
	sampler := &cpuSampler{}
	for {
		if err := runProbeReporterSession(wsURL, identity, sampler); err != nil {
			log.Printf("probe reporter disconnected: %v", err)
		}
		time.Sleep(5 * time.Second)
	}
}

func runProbeReporterSession(wsURL string, identity nodeIdentity, sampler *cpuSampler) error {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	headers := http.Header{}
	for key, value := range buildProbeAuthHeaders(identity) {
		headers.Set(key, value)
	}
	wsConn, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		return err
	}
	defer wsConn.Close()

	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	session, err := yamux.Client(newWebSocketNetConn(wsConn), cfg)
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

	log.Printf("probe reporter connected: %s", wsURL)

	if err := sendProbeReport(stream, encoder, identity, sampler, writeMu); err != nil {
		return err
	}

	readErrCh := make(chan error, 1)
	go func() {
		for {
			var msg probeControlMessage
			if readErr := decoder.Decode(&msg); readErr != nil {
				readErrCh <- readErr
				return
			}
			processProbeControlMessage(msg, identity, stream, encoder, writeMu)
		}
	}()

	for {
		wait := currentReportIntervalDuration()
		select {
		case err := <-readErrCh:
			return err
		case <-time.After(wait):
			if err := sendProbeReport(stream, encoder, identity, sampler, writeMu); err != nil {
				return err
			}
		}
	}
}

func sendProbeReport(stream net.Conn, encoder *json.Encoder, identity nodeIdentity, sampler *cpuSampler, writeMu *sync.Mutex) error {
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

	if err := writeProbeStreamJSON(stream, encoder, writeMu, payload); err != nil {
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

func writeProbeStreamJSON(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, payload any) error {
	if writeMu != nil {
		writeMu.Lock()
		defer writeMu.Unlock()
	}
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := encoder.Encode(payload)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
}

func signProbeConnect(secret, nodeID, timestamp, randomToken string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.TrimSpace(nodeID)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(timestamp)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(randomToken)))
	return hex.EncodeToString(mac.Sum(nil))
}

func buildProbeAuthHeaders(identity nodeIdentity) map[string]string {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	randomToken := randomHexToken(16)
	signature := signProbeConnect(identity.Secret, identity.NodeID, timestamp, randomToken)
	return map[string]string{
		"X-Probe-Node-Id":   strings.TrimSpace(identity.NodeID),
		"X-Probe-Timestamp": timestamp,
		"X-Probe-Rand":      randomToken,
		"X-Probe-Signature": signature,
	}
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

func processProbeControlMessage(msg probeControlMessage, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	typeName := strings.TrimSpace(strings.ToLower(msg.Type))
	if typeName == "report_interval" {
		if sec := normalizeReportInterval(msg.IntervalSec); sec > 0 {
			reportIntervalSec.Store(int64(sec))
			log.Printf("probe reporter interval updated: %ds", sec)
		}
		return
	}
	if typeName == "logs_get" {
		go runProbeLogFetch(msg, identity, stream, encoder, writeMu)
		return
	}
	if typeName == "udp_associations_get" {
		go runProbeUDPAssociationsFetch(msg, identity, stream, encoder, writeMu)
		return
	}
	if typeName == "tcp_debug_get" {
		go runProbeTCPDebugFetch(msg, identity, stream, encoder, writeMu)
		return
	}
	if typeName == "link_test_control" {
		go runProbeLinkTestControl(msg, identity, stream, encoder, writeMu)
		return
	}
	if typeName == "shell_exec" {
		go runProbeShellExec(msg, identity, stream, encoder, writeMu)
		return
	}
	if typeName == "shell_session_control" {
		go runProbeShellSessionControl(msg, identity, stream, encoder, writeMu)
		return
	}
	if typeName == "chain_link_control" {
		go runProbeChainLinkControl(msg, identity, stream, encoder, writeMu)
		return
	}
	if typeName != "upgrade" {
		return
	}
	go runProbeUpgrade(msg, identity)
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

type webSocketNetConn struct {
	ws *websocket.Conn

	readMu  sync.Mutex
	writeMu sync.Mutex
	reader  io.Reader
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
	if closeErr != nil {
		return n, closeErr
	}
	return n, nil
}

func (c *webSocketNetConn) Close() error {
	return c.ws.Close()
}

func (c *webSocketNetConn) LocalAddr() net.Addr {
	return c.ws.UnderlyingConn().LocalAddr()
}

func (c *webSocketNetConn) RemoteAddr() net.Addr {
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
