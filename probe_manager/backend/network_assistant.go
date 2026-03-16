package backend

import (
	"bufio"
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	networkModeDirect = "direct"
	networkModeGlobal = "global"

	defaultNodeID       = "cloudserver"
	defaultSocksListen  = "127.0.0.1:10808"
	directWhitelistFile = "direct_whitelist.txt"
	tunnelRoutePath     = "/api/ws/tunnel/"
	maxTunnelFailures   = 20
)

var defaultDirectWhitelistRules = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"localhost",
	"127.0.0.1",
}

type NetworkAssistantStatus struct {
	Enabled           bool     `json:"enabled"`
	Mode              string   `json:"mode"`
	NodeID            string   `json:"node_id"`
	AvailableNodes    []string `json:"available_nodes"`
	Socks5Listen      string   `json:"socks5_listen"`
	TunnelRoute       string   `json:"tunnel_route"`
	TunnelStatus      string   `json:"tunnel_status"`
	SystemProxyStatus string   `json:"system_proxy_status"`
	LastError         string   `json:"last_error"`
	MuxConnected      bool     `json:"mux_connected"`
	MuxActiveStreams  int      `json:"mux_active_streams"`
	MuxReconnects     int64    `json:"mux_reconnects"`
	MuxLastRecv       string   `json:"mux_last_recv"`
	MuxLastPong       string   `json:"mux_last_pong"`
}

type tunnelControlMessage struct {
	Type    string `json:"type"`
	Network string `json:"network,omitempty"`
	Address string `json:"address,omitempty"`
	Error   string `json:"error,omitempty"`
	Payload []byte `json:"payload,omitempty"`
}

type socks5Request struct {
	Cmd     byte
	Address string
}

type tunnelNodesResponse struct {
	Nodes []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Online bool   `json:"online"`
	} `json:"nodes"`
}

type socksDirectWhitelist struct {
	hosts map[string]struct{}
	ips   map[string]struct{}
	cidrs []*net.IPNet
}

type networkAssistantService struct {
	mu sync.RWMutex

	mode             string
	nodeID           string
	availableNodes   []string
	socks5ListenAddr string

	controllerBaseURL string
	sessionToken      string

	listener net.Listener
	stopping atomic.Bool

	proxySnapshot       systemProxySnapshot
	hasProxySnapshot    bool
	hasAppliedSysProxy  bool
	tunnelStatusMessage string
	systemProxyMessage  string
	lastError           string
	tunnelOpenFailures  int
	tunnelMuxClient     *tunnelMuxClient
	muxReconnects       int64

	directWhitelist *socksDirectWhitelist
	logStore        *networkAssistantLogStore
}

func newNetworkAssistantService() *networkAssistantService {
	logStore := newNetworkAssistantLogStore()

	directWhitelist, _, err := loadOrCreateSocksDirectWhitelist()
	if err != nil {
		logStore.Appendf(logSourceManager, "init", "failed to load direct whitelist, using defaults: %v", err)
		directWhitelist = mustBuildDefaultDirectWhitelist()
	}

	service := &networkAssistantService{
		mode:                networkModeDirect,
		nodeID:              defaultNodeID,
		availableNodes:      []string{defaultNodeID},
		socks5ListenAddr:    defaultSocksListen,
		tunnelStatusMessage: "未启用",
		systemProxyMessage:  "未设置",
		tunnelOpenFailures:  0,
		directWhitelist:     directWhitelist,
		logStore:            logStore,
	}
	service.logf("service initialized, mode=%s, socks5=%s", service.mode, service.socks5ListenAddr)
	return service
}

func (a *App) GetNetworkAssistantStatus() NetworkAssistantStatus {
	if a.networkAssistant == nil {
		return NetworkAssistantStatus{}
	}
	return a.networkAssistant.Status()
}

func (a *App) SetNetworkAssistantMode(controllerBaseURL, sessionToken, mode, nodeID string) (NetworkAssistantStatus, error) {
	if a.networkAssistant == nil {
		return NetworkAssistantStatus{}, errors.New("network assistant service is not initialized")
	}
	if err := a.networkAssistant.ApplyMode(controllerBaseURL, sessionToken, mode, nodeID); err != nil {
		return a.networkAssistant.Status(), err
	}
	return a.networkAssistant.Status(), nil
}

func (a *App) SyncNetworkAssistant(controllerBaseURL, sessionToken string) (NetworkAssistantStatus, error) {
	if a.networkAssistant == nil {
		return NetworkAssistantStatus{}, errors.New("network assistant service is not initialized")
	}
	if err := a.networkAssistant.Sync(controllerBaseURL, sessionToken); err != nil {
		return a.networkAssistant.Status(), err
	}
	return a.networkAssistant.Status(), nil
}

func (a *App) RestoreNetworkAssistantDirect() (NetworkAssistantStatus, error) {
	if a.networkAssistant == nil {
		return NetworkAssistantStatus{}, errors.New("network assistant service is not initialized")
	}
	if err := a.networkAssistant.ApplyMode("", "", networkModeDirect, ""); err != nil {
		return a.networkAssistant.Status(), err
	}
	return a.networkAssistant.Status(), nil
}

func (s *networkAssistantService) UpdateSession(controllerBaseURL, sessionToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.controllerBaseURL = strings.TrimSpace(controllerBaseURL)
	s.sessionToken = strings.TrimSpace(sessionToken)
}

func (s *networkAssistantService) Sync(controllerBaseURL, sessionToken string) error {
	s.UpdateSession(controllerBaseURL, sessionToken)
	if err := s.refreshAvailableNodes(); err != nil {
		s.setLastError(err)
		return err
	}
	return nil
}

func (s *networkAssistantService) Status() NetworkAssistantStatus {
	s.mu.RLock()
	muxClient := s.tunnelMuxClient
	muxReconnects := s.muxReconnects
	defer s.mu.RUnlock()

	muxConnected := false
	muxActiveStreams := 0
	muxLastRecv := ""
	muxLastPong := ""
	if muxClient != nil {
		muxConnected, muxActiveStreams, muxLastRecv, muxLastPong = muxClient.snapshot()
	}

	return NetworkAssistantStatus{
		Enabled:           s.mode == networkModeGlobal,
		Mode:              s.mode,
		NodeID:            s.nodeID,
		AvailableNodes:    append([]string(nil), s.availableNodes...),
		Socks5Listen:      s.socks5ListenAddr,
		TunnelRoute:       tunnelRoutePath + s.nodeID,
		TunnelStatus:      s.tunnelStatusMessage,
		SystemProxyStatus: s.systemProxyMessage,
		LastError:         s.lastError,
		MuxConnected:      muxConnected,
		MuxActiveStreams:  muxActiveStreams,
		MuxReconnects:     muxReconnects,
		MuxLastRecv:       muxLastRecv,
		MuxLastPong:       muxLastPong,
	}
}

func (s *networkAssistantService) ApplyMode(controllerBaseURL, sessionToken, mode, nodeID string) error {
	normalizedMode := strings.ToLower(strings.TrimSpace(mode))
	if normalizedMode == "" {
		normalizedMode = networkModeDirect
	}
	if normalizedMode != networkModeDirect && normalizedMode != networkModeGlobal {
		return fmt.Errorf("unsupported mode: %s", mode)
	}

	normalizedNode := strings.TrimSpace(nodeID)
	if normalizedNode == "" {
		normalizedNode = defaultNodeID
	}
	s.logf("apply mode requested: mode=%s node=%s", normalizedMode, normalizedNode)

	normalizedBase := strings.TrimSpace(controllerBaseURL)
	normalizedToken := strings.TrimSpace(sessionToken)

	s.mu.Lock()
	if normalizedBase != "" {
		s.controllerBaseURL = normalizedBase
	}
	if normalizedToken != "" {
		s.sessionToken = normalizedToken
	}
	effectiveBase := strings.TrimSpace(s.controllerBaseURL)
	effectiveToken := strings.TrimSpace(s.sessionToken)
	s.lastError = ""
	s.mu.Unlock()

	if effectiveBase != "" && effectiveToken != "" {
		s.logf("refreshing available nodes from controller: %s", effectiveBase)
		if err := s.refreshAvailableNodes(); err != nil {
			s.logf("refresh available nodes failed: %v", err)
			s.setLastError(err)
			return err
		}
	}

	s.mu.Lock()
	if !containsNodeID(s.availableNodes, normalizedNode) {
		s.mu.Unlock()
		err := fmt.Errorf("selected node is unavailable: %s", normalizedNode)
		s.setLastError(err)
		return err
	}
	s.nodeID = normalizedNode
	s.mu.Unlock()

	if normalizedMode == networkModeDirect {
		if err := s.stopProxyAndServer(); err != nil {
			s.logf("failed to switch to direct mode: %v", err)
			s.setLastError(err)
			return err
		}
		s.mu.Lock()
		s.mode = networkModeDirect
		s.tunnelStatusMessage = "直连模式"
		s.systemProxyMessage = "已恢复"
		s.tunnelOpenFailures = 0
		s.mu.Unlock()
		s.logf("switched mode to direct, node=%s", normalizedNode)
		return nil
	}

	if effectiveBase == "" || effectiveToken == "" {
		err := errors.New("controller url and session token are required for global mode")
		s.logf("switch global aborted: %v", err)
		s.setLastError(err)
		return err
	}

	if whitelist, _, err := loadOrCreateSocksDirectWhitelist(); err != nil {
		s.logf("failed to refresh direct whitelist: %v", err)
	} else {
		s.mu.Lock()
		s.directWhitelist = whitelist
		s.mu.Unlock()
	}

	if err := s.ensureSocksServer(); err != nil {
		s.logf("failed to start socks server: %v", err)
		s.setLastError(err)
		return err
	}

	if err := s.applySystemProxy(); err != nil {
		s.logf("failed to apply system proxy: %v", err)
		s.setLastError(err)
		_ = s.stopSocksServerOnly()
		return err
	}

	s.mu.Lock()
	s.mode = networkModeGlobal
	s.tunnelStatusMessage = "隧道待命"
	s.systemProxyMessage = "已设置"
	s.tunnelOpenFailures = 0
	socksAddr := s.socks5ListenAddr
	s.mu.Unlock()
	s.logf("switched mode to global, node=%s, socks5=%s", normalizedNode, socksAddr)

	return nil
}

func (s *networkAssistantService) Shutdown() error {
	return s.stopProxyAndServer()
}

func (s *networkAssistantService) ensureSocksServer() error {
	s.mu.Lock()
	if s.listener != nil {
		s.mu.Unlock()
		return nil
	}
	listenAddr := s.socks5ListenAddr
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.listener = ln
	s.stopping.Store(false)
	s.mu.Unlock()
	s.logf("socks5 listener started at %s", listenAddr)

	go s.acceptLoop(ln)
	return nil
}

func (s *networkAssistantService) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if s.stopping.Load() {
				return
			}
			s.logf("failed to accept socks5 conn: %v", err)
			continue
		}
		go s.handleSocksConn(conn)
	}
}

func (s *networkAssistantService) handleSocksConn(conn net.Conn) {
	defer conn.Close()
	remoteAddr := strings.TrimSpace(conn.RemoteAddr().String())
	s.logf("proxy connection opened from %s", remoteAddr)
	defer s.logf("proxy connection closed from %s", remoteAddr)

	br := bufio.NewReader(conn)
	version, req, err := readProxyRequest(br, conn)
	if err != nil {
		s.logf("proxy request parse failed from %s: %v", remoteAddr, err)
		return
	}
	s.logf("proxy request from %s: version=%d cmd=%d target=%s", remoteAddr, version, req.Cmd, req.Address)

	if req.Cmd == 0x03 {
		s.handleSocksUDPAssociate(conn)
		return
	}
	targetAddr := req.Address

	if s.shouldDialDirect(targetAddr) {
		directConn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
		if err != nil {
			s.logf("direct dial failed %s: %v", targetAddr, err)
			replyProxyFailure(conn, version)
			s.setTunnelStatus("白名单直连失败")
			return
		}
		defer directConn.Close()

		if err := replyProxySuccess(conn, version); err != nil {
			return
		}
		s.logf("proxy direct dial connected %s", targetAddr)
		s.setTunnelStatus("白名单直连")

		errCh := make(chan error, 2)
		go func() {
			_, copyErr := io.Copy(directConn, br)
			errCh <- copyErr
		}()
		go func() {
			_, copyErr := io.Copy(conn, directConn)
			errCh <- copyErr
		}()

		transferErr := <-errCh
		if transferErr != nil && !errors.Is(transferErr, io.EOF) {
			s.logf("direct relay closed with error %s: %v", targetAddr, transferErr)
		}
		return
	}

	tunnelStream, err := s.openTunnelStream("tcp", targetAddr)
	if err != nil {
		if !isCredentialMissingErr(err) || s.currentMode() == networkModeGlobal {
			s.logf("failed to open tunnel %s: %v", targetAddr, err)
		}
		replyProxyFailure(conn, version)
		s.setTunnelStatus("隧道异常")
		s.recordTunnelOpenFailure(err)
		return
	}
	defer tunnelStream.close()
	s.resetTunnelOpenFailures()

	if err := replyProxySuccess(conn, version); err != nil {
		return
	}
	s.logf("proxy tunnel connected %s", targetAddr)
	s.setTunnelStatus("隧道已连接")

	errCh := make(chan error, 2)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := br.Read(buf)
			if n > 0 {
				if writeErr := tunnelStream.write(buf[:n]); writeErr != nil {
					errCh <- writeErr
					return
				}
			}
			if readErr != nil {
				errCh <- readErr
				return
			}
		}
	}()

	go func() {
		for {
			select {
			case payload := <-tunnelStream.readCh:
				if _, writeErr := conn.Write(payload); writeErr != nil {
					errCh <- writeErr
					return
				}
			case readErr := <-tunnelStream.errCh:
				errCh <- readErr
				return
			}
		}
	}()

	relayErr := <-errCh
	if relayErr != nil && !errors.Is(relayErr, io.EOF) {
		s.logf("tunnel relay closed with error %s: %v", targetAddr, relayErr)
	}
	s.setTunnelStatus("隧道已断开")
}

func (s *networkAssistantService) shouldDialDirect(targetAddr string) bool {
	s.mu.RLock()
	whitelist := s.directWhitelist
	s.mu.RUnlock()

	if whitelist == nil {
		return false
	}
	return whitelist.matchesTarget(targetAddr)
}

func (s *networkAssistantService) handleSocksUDPAssociate(tcpConn net.Conn) {
	udpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		_ = socks5ReplyWithAddr(tcpConn, 0x01, "0.0.0.0:0")
		return
	}
	defer udpConn.Close()

	_ = socks5ReplyWithAddr(tcpConn, 0x00, udpConn.LocalAddr().String())

	go func() {
		buf := make([]byte, 1)
		_, _ = tcpConn.Read(buf)
		_ = udpConn.Close()
	}()

	buf := make([]byte, 64*1024)
	for {
		n, src, err := udpConn.ReadFrom(buf)
		if err != nil {
			return
		}
		targetAddr, payload, err := parseSocks5UDPDatagram(buf[:n])
		if err != nil {
			continue
		}

		var respPayload []byte
		var respAddr string
		if s.shouldDialDirect(targetAddr) {
			respPayload, respAddr, err = dialUDPDirectPacket(targetAddr, payload)
		} else {
			stream, openErr := s.openTunnelStream("udp", targetAddr)
			if openErr != nil {
				err = openErr
			} else {
				err = stream.write(payload)
				if err == nil {
					select {
					case respPayload = <-stream.readCh:
						respAddr = targetAddr
					case readErr := <-stream.errCh:
						err = readErr
					case <-time.After(10 * time.Second):
						err = errors.New("udp tunnel timeout")
					}
				}
				stream.close()
			}
		}
		if err != nil {
			s.logf("failed to open udp tunnel %s: %v", targetAddr, err)
			continue
		}
		if strings.TrimSpace(respAddr) == "" {
			respAddr = targetAddr
		}
		packet, err := buildSocks5UDPDatagram(respAddr, respPayload)
		if err != nil {
			continue
		}
		_, _ = udpConn.WriteTo(packet, src)
	}
}

func dialUDPDirectPacket(targetAddr string, payload []byte) ([]byte, string, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", targetAddr)
	if err != nil {
		return nil, "", err
	}
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return nil, "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		return nil, "", err
	}
	buf := make([]byte, 65535)
	n, remote, err := conn.ReadFromUDP(buf)
	if err != nil {
		return nil, "", err
	}
	respAddr := targetAddr
	if remote != nil {
		respAddr = remote.String()
	}
	return append([]byte(nil), buf[:n]...), respAddr, nil
}

func buildTunnelWSURL(baseURL, nodeID, token string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || strings.TrimSpace(parsed.Host) == "" {
		return "", errors.New("invalid controller url")
	}

	scheme := parsed.Scheme
	switch scheme {
	case "https":
		scheme = "wss"
	case "http", "":
		scheme = "ws"
	case "wss", "ws":
	default:
		return "", fmt.Errorf("unsupported controller url scheme: %s", parsed.Scheme)
	}

	parsed.Scheme = scheme
	parsed.Path = tunnelRoutePath + strings.TrimSpace(nodeID)
	q := parsed.Query()
	q.Set("token", strings.TrimSpace(token))
	parsed.RawQuery = q.Encode()
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (s *networkAssistantService) applySystemProxy() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.hasProxySnapshot {
		snapshot, err := captureSystemProxySnapshot()
		if err != nil {
			return err
		}
		s.proxySnapshot = snapshot
		s.hasProxySnapshot = true
		s.logf("captured system proxy snapshot: %s", snapshot.Summary())
	}

	s.logf("applying system proxy to socks=%s", s.socks5ListenAddr)
	if err := applySocks5SystemProxy(s.socks5ListenAddr); err != nil {
		return err
	}
	s.hasAppliedSysProxy = true
	s.logf("system proxy applied")
	return nil
}

func (s *networkAssistantService) stopProxyAndServer() error {
	errProxy := s.restoreSystemProxyIfNeeded()
	errServer := s.stopSocksServerOnly()
	if errProxy != nil {
		return errProxy
	}
	return errServer
}

func (s *networkAssistantService) restoreSystemProxyIfNeeded() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.hasProxySnapshot || !s.hasAppliedSysProxy {
		return nil
	}
	s.logf("restoring system proxy: %s", s.proxySnapshot.Summary())
	if err := restoreSystemProxy(s.proxySnapshot); err != nil {
		return err
	}
	s.hasAppliedSysProxy = false
	s.logf("system proxy restored")
	return nil
}

func (s *networkAssistantService) stopSocksServerOnly() error {
	s.mu.Lock()
	ln := s.listener
	muxClient := s.tunnelMuxClient
	s.tunnelMuxClient = nil
	s.listener = nil
	s.stopping.Store(true)
	s.mu.Unlock()

	if muxClient != nil {
		muxClient.close()
	}

	if ln == nil {
		return nil
	}
	s.logf("stopping socks5 listener")
	return ln.Close()
}

func (s *networkAssistantService) setLastError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = err.Error()
}

func (s *networkAssistantService) setTunnelStatus(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tunnelStatusMessage = status
}

func (s *networkAssistantService) logf(format string, args ...any) {
	if s == nil || s.logStore == nil {
		return
	}
	message := strings.TrimSpace(fmt.Sprintf(format, args...))
	if message == "" {
		return
	}
	s.logStore.Append(logSourceManager, inferManagerLogCategory(message), message)
}

func (s *networkAssistantService) logController(category, message string) {
	if s == nil || s.logStore == nil {
		return
	}
	s.logStore.Append(logSourceController, category, message)
}

func inferManagerLogCategory(message string) string {
	lower := strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.Contains(lower, "mode") || strings.Contains(lower, "模式"):
		return "mode"
	case strings.Contains(lower, "system proxy") || strings.Contains(lower, "代理"):
		return "proxy"
	case strings.Contains(lower, "socks") || strings.Contains(lower, "proxy request") || strings.Contains(lower, "proxy connection"):
		return "socks"
	case strings.Contains(lower, "tunnel mux") || strings.Contains(lower, "mux"):
		return "mux"
	case strings.Contains(lower, "open tunnel") || strings.Contains(lower, "stream") || strings.Contains(lower, "relay") || strings.Contains(lower, "tunnel"):
		return "tunnel"
	case strings.Contains(lower, "node"):
		return "node"
	case strings.Contains(lower, "whitelist"):
		return "whitelist"
	case strings.Contains(lower, "failed") || strings.Contains(lower, "error"):
		return "error"
	default:
		return defaultLogCategory
	}
}

func (s *networkAssistantService) resetTunnelOpenFailures() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tunnelOpenFailures = 0
}

func (s *networkAssistantService) recordTunnelOpenFailure(err error) {
	s.mu.Lock()
	s.tunnelOpenFailures++
	failures := s.tunnelOpenFailures
	mode := s.mode
	s.mu.Unlock()

	if failures < maxTunnelFailures || mode != networkModeGlobal {
		return
	}
	s.setTunnelStatus("隧道持续异常")
	if err != nil {
		s.logf("tunnel open failures reached threshold (%d), keep global mode: %v", failures, err)
	}
}

func (s *networkAssistantService) currentMode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

func (s *networkAssistantService) currentControllerState() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.controllerBaseURL
}

func isCredentialMissingErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "missing controller url or session token")
}

func (s *networkAssistantService) refreshAvailableNodes() error {
	s.mu.RLock()
	baseURL := strings.TrimSpace(s.controllerBaseURL)
	token := strings.TrimSpace(s.sessionToken)
	s.mu.RUnlock()

	if baseURL == "" || token == "" {
		return errors.New("controller url and session token are required")
	}

	nodesURL, err := buildControllerNodesURL(baseURL)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, nodesURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Forwarded-Proto", "https")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("fetch tunnel nodes failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload tunnelNodesResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}

	nodes := make([]string, 0, len(payload.Nodes))
	for _, item := range payload.Nodes {
		if !item.Online {
			continue
		}
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		nodes = append(nodes, id)
	}
	if len(nodes) == 0 {
		nodes = []string{defaultNodeID}
	}

	s.mu.Lock()
	s.availableNodes = nodes
	if !containsNodeID(nodes, s.nodeID) {
		s.nodeID = nodes[0]
	}
	s.mu.Unlock()
	return nil
}

func buildControllerNodesURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || strings.TrimSpace(parsed.Host) == "" {
		return "", errors.New("invalid controller url")
	}
	parsed.Path = "/api/admin/tunnel/nodes"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func containsNodeID(nodes []string, target string) bool {
	needle := strings.TrimSpace(target)
	if needle == "" {
		return false
	}
	for _, item := range nodes {
		if strings.EqualFold(strings.TrimSpace(item), needle) {
			return true
		}
	}
	return false
}

func loadOrCreateSocksDirectWhitelist() (*socksDirectWhitelist, string, error) {
	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return nil, "", err
	}

	whitelistPath := filepath.Join(dataDir, directWhitelistFile)
	if err := ensureDirectWhitelistFile(whitelistPath); err != nil {
		return nil, whitelistPath, err
	}

	raw, err := os.ReadFile(whitelistPath)
	if err != nil {
		return nil, whitelistPath, err
	}

	rules := make([]string, 0)
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rules = append(rules, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, whitelistPath, err
	}

	whitelist, err := parseDirectWhitelistRules(rules)
	if err != nil {
		return nil, whitelistPath, err
	}
	return whitelist, whitelistPath, nil
}

func ensureDirectWhitelistFile(whitelistPath string) error {
	_, err := os.Stat(whitelistPath)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	content := "# CloudHelper direct whitelist\n" +
		"# one CIDR/IP/hostname per line\n" +
		strings.Join(defaultDirectWhitelistRules, "\n") + "\n"
	return os.WriteFile(whitelistPath, []byte(content), 0o644)
}

func ensureManagerDataDir() (string, error) {
	candidates := []string{filepath.Join(".", "data")}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "data"),
			filepath.Join(dir, "..", "data"),
		)
	}

	seen := map[string]struct{}{}
	var firstErr error
	for _, candidate := range candidates {
		absPath, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if _, ok := seen[absPath]; ok {
			continue
		}
		seen[absPath] = struct{}{}

		info, err := os.Stat(absPath)
		if err == nil {
			if info.IsDir() {
				return absPath, nil
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		if err := os.MkdirAll(absPath, 0o755); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		return absPath, nil
	}

	if firstErr != nil {
		return "", firstErr
	}
	return "", errors.New("failed to resolve manager data directory")
}

func parseDirectWhitelistRules(rules []string) (*socksDirectWhitelist, error) {
	whitelist := &socksDirectWhitelist{
		hosts: make(map[string]struct{}),
		ips:   make(map[string]struct{}),
		cidrs: make([]*net.IPNet, 0),
	}

	for _, rawRule := range rules {
		rule := strings.ToLower(strings.TrimSpace(rawRule))
		if rule == "" {
			continue
		}

		if _, cidr, err := net.ParseCIDR(rule); err == nil {
			whitelist.cidrs = append(whitelist.cidrs, cidr)
			continue
		}

		if ip := net.ParseIP(rule); ip != nil {
			whitelist.ips[canonicalIP(ip)] = struct{}{}
			continue
		}

		if strings.Contains(rule, " ") {
			continue
		}
		whitelist.hosts[rule] = struct{}{}
	}

	if len(whitelist.hosts) == 0 && len(whitelist.ips) == 0 && len(whitelist.cidrs) == 0 {
		return nil, errors.New("direct whitelist has no valid entries")
	}
	return whitelist, nil
}

func mustBuildDefaultDirectWhitelist() *socksDirectWhitelist {
	whitelist, err := parseDirectWhitelistRules(defaultDirectWhitelistRules)
	if err != nil {
		return &socksDirectWhitelist{
			hosts: make(map[string]struct{}),
			ips:   make(map[string]struct{}),
			cidrs: make([]*net.IPNet, 0),
		}
	}
	return whitelist
}

func canonicalIP(ip net.IP) string {
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4.String()
	}
	return ip.String()
}

func (w *socksDirectWhitelist) matchesTarget(targetAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return false
	}
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "" {
		return false
	}

	if _, ok := w.hosts[host]; ok {
		return true
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if _, ok := w.ips[canonicalIP(ip)]; ok {
		return true
	}
	for _, cidr := range w.cidrs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

func readProxyRequest(br *bufio.Reader, conn net.Conn) (byte, socks5Request, error) {
	peek, err := br.Peek(1)
	if err != nil {
		return 0, socks5Request{}, err
	}

	version := peek[0]
	switch version {
	case 0x05:
		if err := socks5Handshake(br, conn); err != nil {
			return version, socks5Request{}, err
		}
		req, err := socks5ReadRequest(br, conn)
		return version, req, err
	case 0x04:
		req, err := socks4ReadRequest(br, conn)
		return version, req, err
	default:
		return version, socks5Request{}, fmt.Errorf("unsupported socks version: %d", version)
	}
}

func replyProxySuccess(conn net.Conn, version byte) error {
	if version == 0x04 {
		return socks4Reply(conn, 0x5A)
	}
	return socks5Reply(conn, 0x00)
}

func replyProxyFailure(conn net.Conn, version byte) error {
	if version == 0x04 {
		return socks4Reply(conn, 0x5B)
	}
	return socks5Reply(conn, 0x01)
}

func socks5Handshake(br *bufio.Reader, conn net.Conn) error {
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil {
		return err
	}
	if head[0] != 0x05 {
		return errors.New("invalid socks version")
	}
	nMethods := int(head[1])
	if nMethods <= 0 {
		return errors.New("invalid socks auth methods")
	}

	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(br, methods); err != nil {
		return err
	}

	accepted := false
	for _, method := range methods {
		if method == 0x00 {
			accepted = true
			break
		}
	}
	if !accepted {
		_, _ = conn.Write([]byte{0x05, 0xFF})
		return errors.New("no supported auth method")
	}

	_, err := conn.Write([]byte{0x05, 0x00})
	return err
}

func socks4ReadRequest(br *bufio.Reader, conn net.Conn) (socks5Request, error) {
	head := make([]byte, 8)
	if _, err := io.ReadFull(br, head); err != nil {
		return socks5Request{}, err
	}
	if head[0] != 0x04 {
		return socks5Request{}, errors.New("invalid socks4 version")
	}
	if head[1] != 0x01 {
		_ = socks4Reply(conn, 0x5B)
		return socks5Request{}, errors.New("only CONNECT is supported for socks4")
	}

	port := binary.BigEndian.Uint16(head[2:4])
	if port == 0 {
		_ = socks4Reply(conn, 0x5B)
		return socks5Request{}, errors.New("invalid socks4 port")
	}

	if _, err := readNullTerminated(br, 512); err != nil {
		_ = socks4Reply(conn, 0x5B)
		return socks5Request{}, err
	}

	ipBytes := head[4:8]
	var host string
	if ipBytes[0] == 0x00 && ipBytes[1] == 0x00 && ipBytes[2] == 0x00 && ipBytes[3] != 0x00 {
		domain, err := readNullTerminated(br, 1024)
		if err != nil {
			_ = socks4Reply(conn, 0x5B)
			return socks5Request{}, err
		}
		host = strings.TrimSpace(domain)
		if host == "" {
			_ = socks4Reply(conn, 0x5B)
			return socks5Request{}, errors.New("invalid socks4a domain")
		}
	} else {
		host = net.IP(ipBytes).String()
		if strings.TrimSpace(host) == "" {
			_ = socks4Reply(conn, 0x5B)
			return socks5Request{}, errors.New("invalid socks4 address")
		}
	}

	return socks5Request{Cmd: 0x01, Address: net.JoinHostPort(host, strconv.Itoa(int(port)))}, nil
}

func readNullTerminated(br *bufio.Reader, maxLen int) (string, error) {
	if maxLen <= 0 {
		maxLen = 256
	}
	buf := make([]byte, 0, maxLen)
	for len(buf) < maxLen {
		b, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if b == 0x00 {
			return string(buf), nil
		}
		buf = append(buf, b)
	}
	return "", errors.New("null-terminated field exceeds max length")
}

func socks5ReadRequest(br *bufio.Reader, conn net.Conn) (socks5Request, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(br, head); err != nil {
		return socks5Request{}, err
	}
	if head[0] != 0x05 {
		return socks5Request{}, errors.New("invalid socks version")
	}
	cmd := head[1]
	if cmd != 0x01 && cmd != 0x03 {
		_ = socks5Reply(conn, 0x07)
		return socks5Request{}, errors.New("only CONNECT and UDP ASSOCIATE are supported")
	}

	atyp := head[3]
	host := ""
	switch atyp {
	case 0x01:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(br, ip); err != nil {
			return socks5Request{}, err
		}
		host = net.IP(ip).String()
	case 0x03:
		sizeByte, err := br.ReadByte()
		if err != nil {
			return socks5Request{}, err
		}
		domain := make([]byte, int(sizeByte))
		if _, err := io.ReadFull(br, domain); err != nil {
			return socks5Request{}, err
		}
		host = string(domain)
	case 0x04:
		ip := make([]byte, 16)
		if _, err := io.ReadFull(br, ip); err != nil {
			return socks5Request{}, err
		}
		host = net.IP(ip).String()
	default:
		_ = socks5Reply(conn, 0x08)
		return socks5Request{}, errors.New("unsupported address type")
	}

	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(br, portBytes); err != nil {
		return socks5Request{}, err
	}
	port := binary.BigEndian.Uint16(portBytes)
	if port == 0 {
		if cmd == 0x01 {
			_ = socks5Reply(conn, 0x01)
			return socks5Request{}, errors.New("invalid port")
		}
	}

	return socks5Request{Cmd: cmd, Address: net.JoinHostPort(host, strconv.Itoa(int(port)))}, nil
}

func socks5Reply(conn net.Conn, rep byte) error {
	return socks5ReplyWithAddr(conn, rep, "0.0.0.0:0")
}

func socks4Reply(conn net.Conn, rep byte) error {
	resp := []byte{0x00, rep, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	_, err := conn.Write(resp)
	return err
}

func socks5ReplyWithAddr(conn net.Conn, rep byte, bindAddr string) error {
	host, portStr, err := net.SplitHostPort(bindAddr)
	if err != nil {
		host = "0.0.0.0"
		portStr = "0"
	}
	port, _ := strconv.Atoi(portStr)
	if port < 0 || port > 65535 {
		port = 0
	}

	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip4 := ip.To4(); ip4 != nil {
		resp := []byte{0x05, rep, 0x00, 0x01, ip4[0], ip4[1], ip4[2], ip4[3], 0, 0}
		binary.BigEndian.PutUint16(resp[8:], uint16(port))
		_, err := conn.Write(resp)
		return err
	}
	if ip16 := ip.To16(); ip16 != nil {
		resp := make([]byte, 4+16+2)
		resp[0] = 0x05
		resp[1] = rep
		resp[2] = 0x00
		resp[3] = 0x04
		copy(resp[4:20], ip16)
		binary.BigEndian.PutUint16(resp[20:], uint16(port))
		_, err := conn.Write(resp)
		return err
	}

	hostBytes := []byte(host)
	if len(hostBytes) > 255 {
		hostBytes = hostBytes[:255]
	}
	resp := make([]byte, 5+len(hostBytes)+2)
	resp[0] = 0x05
	resp[1] = rep
	resp[2] = 0x00
	resp[3] = 0x03
	resp[4] = byte(len(hostBytes))
	copy(resp[5:5+len(hostBytes)], hostBytes)
	binary.BigEndian.PutUint16(resp[5+len(hostBytes):], uint16(port))
	_, err = conn.Write(resp)
	return err
}

func parseSocks5UDPDatagram(packet []byte) (targetAddr string, payload []byte, err error) {
	if len(packet) < 10 {
		return "", nil, errors.New("udp packet too short")
	}
	if packet[2] != 0x00 {
		return "", nil, errors.New("fragmented udp packet is not supported")
	}
	offset := 3
	var host string
	switch packet[offset] {
	case 0x01:
		offset++
		if len(packet) < offset+4+2 {
			return "", nil, errors.New("invalid ipv4 udp packet")
		}
		host = net.IP(packet[offset : offset+4]).String()
		offset += 4
	case 0x03:
		offset++
		if len(packet) < offset+1 {
			return "", nil, errors.New("invalid domain udp packet")
		}
		hLen := int(packet[offset])
		offset++
		if len(packet) < offset+hLen+2 {
			return "", nil, errors.New("invalid domain udp packet")
		}
		host = string(packet[offset : offset+hLen])
		offset += hLen
	case 0x04:
		offset++
		if len(packet) < offset+16+2 {
			return "", nil, errors.New("invalid ipv6 udp packet")
		}
		host = net.IP(packet[offset : offset+16]).String()
		offset += 16
	default:
		return "", nil, errors.New("unsupported udp atyp")
	}

	port := binary.BigEndian.Uint16(packet[offset : offset+2])
	offset += 2
	return net.JoinHostPort(host, strconv.Itoa(int(port))), append([]byte(nil), packet[offset:]...), nil
}

func buildSocks5UDPDatagram(addr string, payload []byte) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return nil, errors.New("invalid udp port")
	}

	ip := net.ParseIP(strings.Trim(host, "[]"))
	buf := make([]byte, 0, 64+len(payload))
	buf = append(buf, 0x00, 0x00, 0x00)
	if ip4 := ip.To4(); ip4 != nil {
		buf = append(buf, 0x01)
		buf = append(buf, ip4...)
	} else if ip16 := ip.To16(); ip16 != nil {
		buf = append(buf, 0x04)
		buf = append(buf, ip16...)
	} else {
		hostBytes := []byte(host)
		if len(hostBytes) > 255 {
			return nil, errors.New("udp host too long")
		}
		buf = append(buf, 0x03, byte(len(hostBytes)))
		buf = append(buf, hostBytes...)
	}
	buf = append(buf, 0x00, 0x00)
	binary.BigEndian.PutUint16(buf[len(buf)-2:], uint16(port))
	buf = append(buf, payload...)
	return buf, nil
}
