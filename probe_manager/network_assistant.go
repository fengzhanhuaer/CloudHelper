package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	networkModeDirect = "direct"
	networkModeGlobal = "global"

	defaultNodeID      = "cloudserver"
	defaultSocksListen = "127.0.0.1:10808"
	tunnelRoutePath    = "/api/ws/tunnel/"
	maxTunnelFailures  = 3
)

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
}

type tunnelControlMessage struct {
	Type    string `json:"type"`
	Network string `json:"network,omitempty"`
	Address string `json:"address,omitempty"`
	Error   string `json:"error,omitempty"`
}

type tunnelNodesResponse struct {
	Nodes []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Online bool   `json:"online"`
	} `json:"nodes"`
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
}

func newNetworkAssistantService() *networkAssistantService {
	return &networkAssistantService{
		mode:                networkModeDirect,
		nodeID:              defaultNodeID,
		availableNodes:      []string{defaultNodeID},
		socks5ListenAddr:    defaultSocksListen,
		tunnelStatusMessage: "未启用",
		systemProxyMessage:  "未设置",
		tunnelOpenFailures:  0,
	}
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
	defer s.mu.RUnlock()
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

	normalizedBase := strings.TrimSpace(controllerBaseURL)
	normalizedToken := strings.TrimSpace(sessionToken)

	s.mu.Lock()
	s.controllerBaseURL = normalizedBase
	s.sessionToken = normalizedToken
	s.lastError = ""
	s.mu.Unlock()

	if normalizedBase != "" && normalizedToken != "" {
		if err := s.refreshAvailableNodes(); err != nil {
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
			s.setLastError(err)
			return err
		}
		s.mu.Lock()
		s.mode = networkModeDirect
		s.tunnelStatusMessage = "直连模式"
		s.systemProxyMessage = "已恢复"
		s.tunnelOpenFailures = 0
		s.mu.Unlock()
		return nil
	}

	if normalizedBase == "" || normalizedToken == "" {
		err := errors.New("controller url and session token are required for global mode")
		s.setLastError(err)
		return err
	}

	if err := s.ensureSocksServer(); err != nil {
		s.setLastError(err)
		return err
	}

	if err := s.applySystemProxy(); err != nil {
		s.setLastError(err)
		_ = s.stopSocksServerOnly()
		return err
	}

	s.mu.Lock()
	s.mode = networkModeGlobal
	s.tunnelStatusMessage = "隧道待命"
	s.systemProxyMessage = "已设置"
	s.tunnelOpenFailures = 0
	s.mu.Unlock()

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
			log.Printf("network assistant: failed to accept socks5 conn: %v", err)
			continue
		}
		go s.handleSocksConn(conn)
	}
}

func (s *networkAssistantService) handleSocksConn(conn net.Conn) {
	defer conn.Close()

	br := bufio.NewReader(conn)
	if err := socks5Handshake(br, conn); err != nil {
		return
	}

	targetAddr, err := socks5ReadConnectRequest(br, conn)
	if err != nil {
		return
	}

	tunnelConn, err := s.openTunnel(targetAddr)
	if err != nil {
		log.Printf("network assistant: failed to open tunnel %s: %v", targetAddr, err)
		socks5Reply(conn, 0x01)
		s.setTunnelStatus("隧道异常")
		s.recordTunnelOpenFailure(err)
		return
	}
	defer tunnelConn.Close()
	s.resetTunnelOpenFailures()

	if err := socks5Reply(conn, 0x00); err != nil {
		return
	}
	s.setTunnelStatus("隧道已连接")

	errCh := make(chan error, 2)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := br.Read(buf)
			if n > 0 {
				_ = tunnelConn.SetWriteDeadline(time.Now().Add(30 * time.Second))
				if writeErr := tunnelConn.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
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
			msgType, payload, readErr := tunnelConn.ReadMessage()
			if readErr != nil {
				errCh <- readErr
				return
			}
			switch msgType {
			case websocket.BinaryMessage:
				if _, writeErr := conn.Write(payload); writeErr != nil {
					errCh <- writeErr
					return
				}
			case websocket.TextMessage:
				var msg tunnelControlMessage
				if err := json.Unmarshal(payload, &msg); err == nil && msg.Type == "close" {
					errCh <- io.EOF
					return
				}
			}
		}
	}()

	<-errCh
	s.setTunnelStatus("隧道已断开")
}

func (s *networkAssistantService) openTunnel(targetAddr string) (*websocket.Conn, error) {
	s.mu.RLock()
	baseURL := s.controllerBaseURL
	token := s.sessionToken
	nodeID := s.nodeID
	s.mu.RUnlock()

	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(token) == "" {
		return nil, errors.New("missing controller url or session token")
	}

	tunnelURL, err := buildTunnelWSURL(baseURL, nodeID, token)
	if err != nil {
		return nil, err
	}

	header := http.Header{}
	header.Set("X-Forwarded-Proto", "https")
	wsConn, _, err := websocket.DefaultDialer.Dial(tunnelURL, header)
	if err != nil {
		return nil, err
	}

	connectMsg := tunnelControlMessage{
		Type:    "connect",
		Network: "tcp",
		Address: targetAddr,
	}
	if err := wsConn.WriteJSON(connectMsg); err != nil {
		wsConn.Close()
		return nil, err
	}

	_ = wsConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, payload, err := wsConn.ReadMessage()
	if err != nil {
		wsConn.Close()
		return nil, err
	}
	_ = wsConn.SetReadDeadline(time.Time{})

	var resp tunnelControlMessage
	if err := json.Unmarshal(payload, &resp); err != nil {
		wsConn.Close()
		return nil, err
	}
	if resp.Type != "connected" {
		wsConn.Close()
		if strings.TrimSpace(resp.Error) == "" {
			return nil, errors.New("tunnel connect failed")
		}
		return nil, errors.New(resp.Error)
	}

	return wsConn, nil
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
	}

	if err := applySocks5SystemProxy(s.socks5ListenAddr); err != nil {
		return err
	}
	s.hasAppliedSysProxy = true
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
	if err := restoreSystemProxy(s.proxySnapshot); err != nil {
		return err
	}
	s.hasAppliedSysProxy = false
	return nil
}

func (s *networkAssistantService) stopSocksServerOnly() error {
	s.mu.Lock()
	ln := s.listener
	s.listener = nil
	s.stopping.Store(true)
	s.mu.Unlock()

	if ln == nil {
		return nil
	}
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

	if rollbackErr := s.ApplyMode("", "", networkModeDirect, ""); rollbackErr != nil {
		log.Printf("network assistant: failed to fallback to direct mode: %v", rollbackErr)
		s.setLastError(rollbackErr)
		return
	}
	if err != nil {
		log.Printf("network assistant: fallback to direct mode after repeated tunnel failures: %v", err)
	}
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

func socks5ReadConnectRequest(br *bufio.Reader, conn net.Conn) (string, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(br, head); err != nil {
		return "", err
	}
	if head[0] != 0x05 {
		return "", errors.New("invalid socks version")
	}
	if head[1] != 0x01 {
		_ = socks5Reply(conn, 0x07)
		return "", errors.New("only CONNECT is supported")
	}

	atyp := head[3]
	host := ""
	switch atyp {
	case 0x01:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(br, ip); err != nil {
			return "", err
		}
		host = net.IP(ip).String()
	case 0x03:
		sizeByte, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		domain := make([]byte, int(sizeByte))
		if _, err := io.ReadFull(br, domain); err != nil {
			return "", err
		}
		host = string(domain)
	case 0x04:
		ip := make([]byte, 16)
		if _, err := io.ReadFull(br, ip); err != nil {
			return "", err
		}
		host = net.IP(ip).String()
	default:
		_ = socks5Reply(conn, 0x08)
		return "", errors.New("unsupported address type")
	}

	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(br, portBytes); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBytes)
	if port == 0 {
		_ = socks5Reply(conn, 0x01)
		return "", errors.New("invalid port")
	}

	return fmt.Sprintf("%s:%d", host, port), nil
}

func socks5Reply(conn net.Conn, rep byte) error {
	resp := []byte{0x05, rep, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	_, err := conn.Write(resp)
	return err
}
