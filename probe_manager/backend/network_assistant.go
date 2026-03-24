package backend

import (
	"bufio"
	"bytes"
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	networkModeDirect = "direct"
	networkModeGlobal = "global"
	networkModeTUN    = "tun"
	networkModeRule   = "rule"

	defaultNodeID         = "cloudserver"
	chainTargetNodePrefix = "chain:"
	defaultSocksListen    = "127.0.0.1:10808"
	directWhitelistFile   = "direct_whitelist.txt"
	ruleRouteFile         = "rule_routes.txt"
	ruleGroupFile         = "rule_groups.txt"
	tunnelRoutePath       = "/api/ws/tunnel/"
	maxTunnelFailures     = 20

	ruleDNSDefaultServerA      = "1.1.1.1:53"
	ruleDNSDefaultServerB      = "8.8.8.8:53"
	ruleDNSCacheMinTTLSeconds  = 15
	ruleDNSCacheMaxTTLSeconds  = 600
	ruleDNSResolveTimeout      = 8 * time.Second
	ruleDNSResolveReadTimeout  = 5 * time.Second
	ruleDNSResolveServerTrials = 2
)

var defaultDirectWhitelistRules = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"localhost",
	"127.0.0.1",
}

var defaultRuleRoutes = []string{
	"# example.com,default",
	"# 1.2.3.4,default",
	"# 10.10.0.0/16,default",
}

var defaultRuleGroups = []string{
	"default,cloudserver",
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
	TUNSupported      bool     `json:"tun_supported"`
	TUNInstalled      bool     `json:"tun_installed"`
	TUNEnabled        bool     `json:"tun_enabled"`
	TUNLibraryPath    string   `json:"tun_library_path"`
	TUNStatus         string   `json:"tun_status"`
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

type probeLinkChainsResponse struct {
	Items []probeLinkChainAdminItem `json:"items"`
}

type probeNodesResponse struct {
	Nodes []probeNodeAdminItem `json:"nodes"`
}

type probeLinkChainAdminItem struct {
	ChainID        string   `json:"chain_id"`
	EntryNodeID    string   `json:"entry_node_id"`
	ExitNodeID     string   `json:"exit_node_id"`
	CascadeNodeIDs []string `json:"cascade_node_ids"`
	LinkLayer      string   `json:"link_layer"`
	HopConfigs     []struct {
		NodeNo     int    `json:"node_no"`
		ListenPort int    `json:"listen_port"`
		LinkLayer  string `json:"link_layer"`
	} `json:"hop_configs"`
}

type probeNodeAdminItem struct {
	NodeNo      int    `json:"node_no"`
	DDNS        string `json:"ddns"`
	ServiceHost string `json:"service_host"`
	ServicePort int    `json:"service_port"`
	PublicHost  string `json:"public_host"`
	PublicPort  int    `json:"public_port"`
}

type probeChainEndpoint struct {
	TargetID  string
	ChainID   string
	EntryNode string
	RelayHost string
	RelayPort int
	LinkLayer string
}

type adminWSRequest struct {
	ID      string      `json:"id"`
	Action  string      `json:"action"`
	Payload interface{} `json:"payload,omitempty"`
}

type adminWSResponse struct {
	ID    string          `json:"id"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
	Type  string          `json:"type"`
}

type socksDirectWhitelist struct {
	hosts map[string]struct{}
	ips   map[string]struct{}
	cidrs []*net.IPNet
}

type ruleMatcherKind string

const (
	ruleMatcherDomainSuffix ruleMatcherKind = "domain_suffix"
	ruleMatcherIP           ruleMatcherKind = "ip"
	ruleMatcherCIDR         ruleMatcherKind = "cidr"
)

type tunnelRule struct {
	RawPattern string
	Group      string
	Kind       ruleMatcherKind
	Suffix     string
	IP         string
	CIDR       *net.IPNet
}

type tunnelRuleSet struct {
	Rules []tunnelRule
}

type tunnelRuleRouting struct {
	RuleSet       tunnelRuleSet
	GroupNodeMap  map[string]string
	RuleFilePath  string
	GroupFilePath string
}

type tunnelRouteDecision struct {
	Direct     bool
	TargetAddr string
	NodeID     string
	Group      string
}

type dnsCacheEntry struct {
	Addrs   []string
	Expires time.Time
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
	chainTargets        map[string]probeChainEndpoint
	muxReconnects       int64

	tunSupported     bool
	tunInstalled     bool
	tunEnabled       bool
	tunLibraryPath   string
	tunStatus        string
	tunAdapterHandle uintptr
	tunDataPlane     localTUNDataPlane
	tunPacketStack   localTUNPacketStack
	tunUDPHandler    localTUNUDPHandlerCloser
	tunIPIDSeq       uint32

	directWhitelist *socksDirectWhitelist
	logStore        *networkAssistantLogStore
	ruleRouting     tunnelRuleRouting
	ruleDNSCache    map[string]dnsCacheEntry
	ruleDNSQuerySeq uint32
	ruleMuxClients  map[string]*tunnelMuxClient
	tunUDPRelays    map[string]*localTUNUDPRelay
}

func newNetworkAssistantService() *networkAssistantService {
	logStore := newNetworkAssistantLogStore()

	directWhitelist, _, err := loadOrCreateSocksDirectWhitelist()
	if err != nil {
		logStore.Appendf(logSourceManager, "init", "failed to load direct whitelist, using defaults: %v", err)
		directWhitelist = mustBuildDefaultDirectWhitelist()
	}

	ruleRouting, ruleErr := loadOrCreateTunnelRuleRouting()
	if ruleErr != nil {
		logStore.Appendf(logSourceManager, "init", "failed to load rule routing config, using empty rules: %v", ruleErr)
		ruleRouting = tunnelRuleRouting{
			RuleSet:      tunnelRuleSet{Rules: []tunnelRule{}},
			GroupNodeMap: map[string]string{},
		}
	}

	service := &networkAssistantService{
		mode:                networkModeDirect,
		nodeID:              defaultNodeID,
		availableNodes:      []string{defaultNodeID},
		socks5ListenAddr:    defaultSocksListen,
		tunnelStatusMessage: "未启用",
		systemProxyMessage:  "未设置",
		tunnelOpenFailures:  0,
		chainTargets:        make(map[string]probeChainEndpoint),
		tunSupported:        false,
		tunInstalled:        false,
		tunEnabled:          false,
		tunLibraryPath:      "",
		tunStatus:           "未安装",
		directWhitelist:     directWhitelist,
		logStore:            logStore,
		ruleRouting:         ruleRouting,
		ruleDNSCache:        make(map[string]dnsCacheEntry),
		ruleMuxClients:      make(map[string]*tunnelMuxClient),
		tunUDPRelays:        make(map[string]*localTUNUDPRelay),
	}
	service.syncTUNInstallState()
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
	s.syncTUNInstallState()
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
		Enabled:           s.mode == networkModeTUN || s.mode == networkModeRule,
		Mode:              s.mode,
		NodeID:            s.nodeID,
		AvailableNodes:    append([]string(nil), s.availableNodes...),
		Socks5Listen:      s.socks5ListenAddr,
		TunnelRoute:       buildNetworkAssistantTunnelRoute(s.nodeID),
		TunnelStatus:      s.tunnelStatusMessage,
		SystemProxyStatus: s.systemProxyMessage,
		LastError:         s.lastError,
		MuxConnected:      muxConnected,
		MuxActiveStreams:  muxActiveStreams,
		MuxReconnects:     muxReconnects,
		MuxLastRecv:       muxLastRecv,
		MuxLastPong:       muxLastPong,
		TUNSupported:      s.tunSupported,
		TUNInstalled:      s.tunInstalled,
		TUNEnabled:        s.tunEnabled,
		TUNLibraryPath:    s.tunLibraryPath,
		TUNStatus:         s.tunStatus,
	}
}

func (s *networkAssistantService) ApplyMode(controllerBaseURL, sessionToken, mode, nodeID string) error {
	normalizedMode := strings.ToLower(strings.TrimSpace(mode))
	if normalizedMode == "" {
		normalizedMode = networkModeDirect
	}
	if normalizedMode == networkModeTUN {
		return s.EnableTUN()
	}
	if normalizedMode == networkModeGlobal {
		err := errors.New("global proxy mode has been removed")
		s.logf("switch global aborted: %v", err)
		s.setLastError(err)
		return err
	}
	if normalizedMode != networkModeDirect && normalizedMode != networkModeRule {
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
		errStopTUN := s.stopLocalTUNDataPlane()
		errServer := s.stopSocksServerOnly()
		errDirectProxy := applyDirectSystemProxy()
		if err := errors.Join(errStopTUN, errServer, errDirectProxy); err != nil {
			s.logf("failed to switch to direct mode: %v", err)
			s.setLastError(err)
			return err
		}
		s.mu.Lock()
		s.mode = networkModeDirect
		s.tunnelStatusMessage = "直连模式"
		s.systemProxyMessage = "已清除系统代理（直连）"
		s.tunnelOpenFailures = 0
		s.hasAppliedSysProxy = false
		s.hasProxySnapshot = false
		s.proxySnapshot = systemProxySnapshot{}
		s.tunEnabled = false
		s.tunStatus = tunStatusAfterDisable(s.tunSupported, s.tunInstalled)
		s.mu.Unlock()
		s.logf("switched mode to direct and cleared system proxy, node=%s", normalizedNode)
		return nil
	}

	routing, err := loadOrCreateTunnelRuleRouting()
	if err != nil {
		s.logf("failed to load rule routing config: %v", err)
		s.setLastError(err)
		return err
	}
	if err := s.stopLocalTUNDataPlane(); err != nil {
		s.logf("failed to stop tun data plane before rule mode: %v", err)
		s.setLastError(err)
		return err
	}
	if err := s.ensureSocksServer(); err != nil {
		s.logf("failed to enable rule mode: start socks server: %v", err)
		s.setLastError(err)
		return err
	}
	if err := s.applySystemProxy(); err != nil {
		s.logf("failed to enable rule mode: apply system proxy: %v", err)
		s.setLastError(err)
		return err
	}

	s.mu.Lock()
	s.mode = networkModeRule
	s.tunnelStatusMessage = "规则模式（命中规则走隧道）"
	s.systemProxyMessage = "规则模式已启用"
	s.tunnelOpenFailures = 0
	s.lastError = ""
	s.ruleRouting = routing
	s.ruleDNSCache = make(map[string]dnsCacheEntry)
	s.tunEnabled = false
	s.tunStatus = tunStatusAfterDisable(s.tunSupported, s.tunInstalled)
	s.mu.Unlock()

	s.logf(
		"switched mode to rule, node=%s rules=%d groups=%d rule_file=%s group_file=%s",
		normalizedNode,
		len(routing.RuleSet.Rules),
		len(routing.GroupNodeMap),
		strings.TrimSpace(routing.RuleFilePath),
		strings.TrimSpace(routing.GroupFilePath),
	)

	return nil
}

func (s *networkAssistantService) Shutdown() error {
	errStopTUN := s.stopLocalTUNDataPlane()
	errStop := s.stopProxyAndServer()
	errDirect := applyDirectSystemProxy()

	s.mu.Lock()
	tunAdapterHandle := s.tunAdapterHandle
	tunLibraryPath := s.tunLibraryPath
	s.mode = networkModeDirect
	s.tunnelStatusMessage = "直连模式"
	s.systemProxyMessage = "已恢复为直连"
	s.tunnelOpenFailures = 0
	s.tunEnabled = false
	s.tunStatus = tunStatusAfterDisable(s.tunSupported, s.tunInstalled)
	s.tunAdapterHandle = 0
	s.hasAppliedSysProxy = false
	s.hasProxySnapshot = false
	s.proxySnapshot = systemProxySnapshot{}
	s.mu.Unlock()

	var errCloseAdapter error
	if tunAdapterHandle != 0 {
		errCloseAdapter = closeConfiguredTUNAdapter(tunLibraryPath, tunAdapterHandle)
		if errCloseAdapter == nil {
			s.logf("released tun adapter handle during shutdown")
		} else {
			s.logf("failed to release tun adapter handle during shutdown: %v", errCloseAdapter)
		}
	}

	if errDirect == nil {
		s.logf("forced direct system proxy during shutdown")
	} else {
		s.logf("failed to force direct system proxy during shutdown: %v", errDirect)
	}
	if errStop != nil {
		s.logf("shutdown cleanup returned error: %v", errStop)
	}
	if errStopTUN != nil {
		s.logf("shutdown tun dataplane cleanup returned error: %v", errStopTUN)
	}
	return errors.Join(errStopTUN, errStop, errDirect, errCloseAdapter)
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
			s.logf("failed to accept proxy conn: %v", err)
			continue
		}
		go s.handleProxyConn(conn)
	}
}

func (s *networkAssistantService) handleProxyConn(conn net.Conn) {
	defer conn.Close()
	remoteAddr := strings.TrimSpace(conn.RemoteAddr().String())
	s.logf("proxy connection opened from %s", remoteAddr)
	defer s.logf("proxy connection closed from %s", remoteAddr)

	br := bufio.NewReader(conn)
	peek, err := br.Peek(1)
	if err != nil {
		s.logf("proxy request parse failed from %s: %v", remoteAddr, err)
		return
	}

	if peek[0] == 0x04 || peek[0] == 0x05 {
		s.handleSocksConn(conn, br, remoteAddr)
		return
	}
	s.handleHTTPProxyConn(conn, br, remoteAddr)
}

func (s *networkAssistantService) handleSocksConn(conn net.Conn, br *bufio.Reader, remoteAddr string) {
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

	route, err := s.decideRouteForTarget(targetAddr)
	if err != nil {
		if isRuleRouteRejectErr(err) {
			s.logf("proxy target rejected by rule target=%s err=%v", targetAddr, err)
			s.setTunnelStatus("规则拒绝")
		} else {
			s.logf("proxy route decision failed target=%s err=%v", targetAddr, err)
			s.setTunnelStatus("规则解析失败")
		}
		replyProxyFailure(conn, version)
		return
	}
	if route.Direct {
		directConn, err := net.DialTimeout("tcp", route.TargetAddr, 10*time.Second)
		if err != nil {
			s.logf("direct dial failed %s: %v", route.TargetAddr, err)
			replyProxyFailure(conn, version)
			s.setTunnelStatus("直连失败")
			return
		}
		defer directConn.Close()

		if err := replyProxySuccess(conn, version); err != nil {
			return
		}
		s.logf("proxy direct dial connected target=%s routed=%s", targetAddr, route.TargetAddr)
		if s.currentMode() == networkModeRule {
			s.setTunnelStatus("规则未命中，直连")
		} else {
			s.setTunnelStatus("白名单直连")
		}

		errCh := make(chan relayResult, 2)
		go func() {
			_, copyErr := io.Copy(directConn, br)
			errCh <- relayResult{Side: "client->direct", Err: copyErr}
		}()
		go func() {
			_, copyErr := io.Copy(conn, directConn)
			errCh <- relayResult{Side: "direct->client", Err: copyErr}
		}()

		s.logRelayClosed("direct", route.TargetAddr, <-errCh)
		return
	}

	tunnelStream, err := s.openTunnelStreamForNode("tcp", route.TargetAddr, route.NodeID)
	if err != nil {
		if !isCredentialMissingErr(err) || s.currentMode() == networkModeGlobal {
			s.logf("failed to open tunnel target=%s routed=%s node=%s group=%s err=%v", targetAddr, route.TargetAddr, route.NodeID, route.Group, err)
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
	s.logf("proxy tunnel connected target=%s routed=%s node=%s group=%s", targetAddr, route.TargetAddr, route.NodeID, route.Group)
	if s.currentMode() == networkModeRule && strings.TrimSpace(route.Group) != "" {
		s.setTunnelStatus("规则命中组：" + strings.TrimSpace(route.Group))
	} else {
		s.setTunnelStatus("隧道已连接")
	}

	errCh := make(chan relayResult, 2)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := br.Read(buf)
			if n > 0 {
				if writeErr := tunnelStream.write(buf[:n]); writeErr != nil {
					errCh <- relayResult{Side: "client->tunnel", Err: writeErr}
					return
				}
			}
			if readErr != nil {
				errCh <- relayResult{Side: "client->tunnel", Err: readErr}
				return
			}
		}
	}()

	go func() {
		for {
			select {
			case payload := <-tunnelStream.readCh:
				if _, writeErr := conn.Write(payload); writeErr != nil {
					errCh <- relayResult{Side: "tunnel->client", Err: writeErr}
					return
				}
			case readErr := <-tunnelStream.errCh:
				errCh <- relayResult{Side: "tunnel->client", Err: readErr}
				return
			}
		}
	}()

	s.logRelayClosed("tunnel", route.TargetAddr, <-errCh)
	s.setTunnelStatus("隧道已断开")
}

func (s *networkAssistantService) handleHTTPProxyConn(conn net.Conn, br *bufio.Reader, remoteAddr string) {
	req, err := http.ReadRequest(br)
	if err != nil {
		s.logf("http proxy request parse failed from %s: %v", remoteAddr, err)
		_ = writeHTTPProxyStatus(conn, http.StatusBadRequest, "invalid proxy request")
		return
	}
	defer req.Body.Close()

	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method != http.MethodConnect {
		targetAddr, payload, buildErr := buildHTTPProxyForwardRequest(req)
		if buildErr != nil {
			s.logf("http proxy request parse failed from %s: %v", remoteAddr, buildErr)
			_ = writeHTTPProxyStatus(conn, http.StatusBadRequest, "invalid proxy request")
			return
		}
		s.logf("http proxy request from %s: method=%s target=%s", remoteAddr, method, targetAddr)

		route, routeErr := s.decideRouteForTarget(targetAddr)
		if routeErr != nil {
			if isRuleRouteRejectErr(routeErr) {
				s.logf("http proxy target rejected by rule target=%s err=%v", targetAddr, routeErr)
				s.setTunnelStatus("规则拒绝")
				_ = writeHTTPProxyStatus(conn, http.StatusForbidden, "blocked by rule policy")
			} else {
				s.logf("http proxy route decision failed target=%s err=%v", targetAddr, routeErr)
				s.setTunnelStatus("规则解析失败")
				_ = writeHTTPProxyStatus(conn, http.StatusBadGateway, "failed to route target")
			}
			return
		}

		if route.Direct {
			directConn, dialErr := net.DialTimeout("tcp", route.TargetAddr, 10*time.Second)
			if dialErr != nil {
				s.logf("http proxy direct dial failed target=%s routed=%s err=%v", targetAddr, route.TargetAddr, dialErr)
				s.setTunnelStatus("直连失败")
				_ = writeHTTPProxyStatus(conn, http.StatusBadGateway, "failed to connect target")
				return
			}
			defer directConn.Close()

			if _, writeErr := directConn.Write(payload); writeErr != nil {
				s.logRelayClosed("http-forward-direct", route.TargetAddr, relayResult{Side: "proxy->direct", Err: writeErr})
				s.setTunnelStatus("直连已断开")
				_ = writeHTTPProxyStatus(conn, http.StatusBadGateway, "failed to send request")
				return
			}
			if s.currentMode() == networkModeRule {
				s.setTunnelStatus("规则未命中，直连")
			} else {
				s.setTunnelStatus("白名单直连")
			}
			_, copyErr := io.Copy(conn, directConn)
			s.logRelayClosed("http-forward-direct", route.TargetAddr, relayResult{Side: "direct->proxy", Err: copyErr})
			s.setTunnelStatus("直连已断开")
			return
		}

		tunnelStream, openErr := s.openTunnelStreamForNode("tcp", route.TargetAddr, route.NodeID)
		if openErr != nil {
			s.logf("failed to open tunnel target=%s routed=%s node=%s group=%s err=%v", targetAddr, route.TargetAddr, route.NodeID, route.Group, openErr)
			s.setTunnelStatus("隧道异常")
			s.recordTunnelOpenFailure(openErr)
			_ = writeHTTPProxyStatus(conn, http.StatusBadGateway, "failed to connect target")
			return
		}
		defer tunnelStream.close()
		s.resetTunnelOpenFailures()
		s.logf("proxy tunnel connected target=%s routed=%s node=%s group=%s", targetAddr, route.TargetAddr, route.NodeID, route.Group)
		if s.currentMode() == networkModeRule && strings.TrimSpace(route.Group) != "" {
			s.setTunnelStatus("规则命中组：" + strings.TrimSpace(route.Group))
		} else {
			s.setTunnelStatus("隧道已连接")
		}

		if writeErr := tunnelStream.write(payload); writeErr != nil {
			s.logRelayClosed("http-forward", route.TargetAddr, relayResult{Side: "proxy->tunnel", Err: writeErr})
			s.setTunnelStatus("隧道已断开")
			_ = writeHTTPProxyStatus(conn, http.StatusBadGateway, "failed to send request")
			return
		}

		for {
			select {
			case data := <-tunnelStream.readCh:
				if len(data) == 0 {
					continue
				}
				if _, writeErr := conn.Write(data); writeErr != nil {
					s.logRelayClosed("http-forward", route.TargetAddr, relayResult{Side: "tunnel->proxy", Err: writeErr})
					s.setTunnelStatus("隧道已断开")
					return
				}
			case readErr := <-tunnelStream.errCh:
				s.logRelayClosed("http-forward", route.TargetAddr, relayResult{Side: "tunnel->proxy", Err: readErr})
				s.setTunnelStatus("隧道已断开")
				return
			}
		}
	}

	targetAddr, err := normalizeProxyTargetAddress(req.Host, "443")
	if err != nil {
		s.logf("http proxy request parse failed from %s: %v", remoteAddr, err)
		_ = writeHTTPProxyStatus(conn, http.StatusBadRequest, "invalid CONNECT target")
		return
	}
	s.logf("http proxy request from %s: method=CONNECT target=%s", remoteAddr, targetAddr)

	route, routeErr := s.decideRouteForTarget(targetAddr)
	if routeErr != nil {
		if isRuleRouteRejectErr(routeErr) {
			s.logf("http connect target rejected by rule target=%s err=%v", targetAddr, routeErr)
			s.setTunnelStatus("规则拒绝")
			_ = writeHTTPProxyStatus(conn, http.StatusForbidden, "blocked by rule policy")
		} else {
			s.logf("http connect route decision failed target=%s err=%v", targetAddr, routeErr)
			s.setTunnelStatus("规则解析失败")
			_ = writeHTTPProxyStatus(conn, http.StatusBadGateway, "failed to route target")
		}
		return
	}

	if route.Direct {
		directConn, dialErr := net.DialTimeout("tcp", route.TargetAddr, 10*time.Second)
		if dialErr != nil {
			s.logf("http connect direct dial failed target=%s routed=%s err=%v", targetAddr, route.TargetAddr, dialErr)
			s.setTunnelStatus("直连失败")
			_ = writeHTTPProxyStatus(conn, http.StatusBadGateway, "failed to connect target")
			return
		}
		defer directConn.Close()

		if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\nProxy-Agent: CloudHelper\r\n\r\n")); err != nil {
			return
		}
		if s.currentMode() == networkModeRule {
			s.setTunnelStatus("规则未命中，直连")
		} else {
			s.setTunnelStatus("白名单直连")
		}

		if buffered := br.Buffered(); buffered > 0 {
			payload := make([]byte, buffered)
			n, readErr := io.ReadFull(br, payload)
			if n > 0 {
				if _, writeErr := directConn.Write(payload[:n]); writeErr != nil {
					s.logRelayClosed("direct", route.TargetAddr, relayResult{Side: "proxy->direct", Err: writeErr})
					return
				}
			}
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				s.logRelayClosed("direct", route.TargetAddr, relayResult{Side: "proxy->direct", Err: readErr})
				return
			}
		}

		errCh := make(chan relayResult, 2)
		go func() {
			_, copyErr := io.Copy(directConn, br)
			errCh <- relayResult{Side: "client->direct", Err: copyErr}
		}()
		go func() {
			_, copyErr := io.Copy(conn, directConn)
			errCh <- relayResult{Side: "direct->client", Err: copyErr}
		}()

		s.logRelayClosed("direct", route.TargetAddr, <-errCh)
		s.setTunnelStatus("直连已断开")
		return
	}

	tunnelStream, err := s.openTunnelStreamForNode("tcp", route.TargetAddr, route.NodeID)
	if err != nil {
		s.logf("failed to open tunnel target=%s routed=%s node=%s group=%s err=%v", targetAddr, route.TargetAddr, route.NodeID, route.Group, err)
		s.setTunnelStatus("隧道异常")
		s.recordTunnelOpenFailure(err)
		_ = writeHTTPProxyStatus(conn, http.StatusBadGateway, "failed to connect target")
		return
	}
	defer tunnelStream.close()
	s.resetTunnelOpenFailures()

	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\nProxy-Agent: CloudHelper\r\n\r\n")); err != nil {
		return
	}
	s.logf("proxy tunnel connected target=%s routed=%s node=%s group=%s", targetAddr, route.TargetAddr, route.NodeID, route.Group)
	if s.currentMode() == networkModeRule && strings.TrimSpace(route.Group) != "" {
		s.setTunnelStatus("规则命中组：" + strings.TrimSpace(route.Group))
	} else {
		s.setTunnelStatus("隧道已连接")
	}

	if buffered := br.Buffered(); buffered > 0 {
		payload := make([]byte, buffered)
		n, readErr := io.ReadFull(br, payload)
		if n > 0 {
			if writeErr := tunnelStream.write(payload[:n]); writeErr != nil {
				s.logf("tunnel relay closed with error %s: %v", route.TargetAddr, writeErr)
				return
			}
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			s.logf("tunnel relay closed with error %s: %v", route.TargetAddr, readErr)
			return
		}
	}

	errCh := make(chan relayResult, 2)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := br.Read(buf)
			if n > 0 {
				if writeErr := tunnelStream.write(buf[:n]); writeErr != nil {
					errCh <- relayResult{Side: "client->tunnel", Err: writeErr}
					return
				}
			}
			if readErr != nil {
				errCh <- relayResult{Side: "client->tunnel", Err: readErr}
				return
			}
		}
	}()

	go func() {
		for {
			select {
			case payload := <-tunnelStream.readCh:
				if _, writeErr := conn.Write(payload); writeErr != nil {
					errCh <- relayResult{Side: "tunnel->client", Err: writeErr}
					return
				}
			case readErr := <-tunnelStream.errCh:
				errCh <- relayResult{Side: "tunnel->client", Err: readErr}
				return
			}
		}
	}()

	s.logRelayClosed("tunnel", route.TargetAddr, <-errCh)
	s.setTunnelStatus("隧道已断开")
}

type relayResult struct {
	Side string
	Err  error
}

func (s *networkAssistantService) logRelayClosed(relayType string, targetAddr string, result relayResult) {
	side := strings.TrimSpace(result.Side)
	if side == "" {
		side = "unknown"
	}
	errText := relayErrorText(result.Err)
	s.logf("%s relay closed: target=%s side=%s err=%s", strings.TrimSpace(relayType), targetAddr, side, errText)
}

func relayErrorText(err error) string {
	if err == nil {
		return "nil"
	}
	if errors.Is(err, io.EOF) {
		return "eof"
	}
	if errors.Is(err, net.ErrClosed) {
		return "net_closed"
	}
	return strings.TrimSpace(err.Error())
}

func buildHTTPProxyForwardRequest(req *http.Request) (string, []byte, error) {
	targetHost := ""
	if req.URL != nil {
		targetHost = strings.TrimSpace(req.URL.Host)
	}
	if targetHost == "" {
		targetHost = strings.TrimSpace(req.Host)
	}
	if targetHost == "" {
		return "", nil, errors.New("missing target host")
	}

	defaultPort := "80"
	if req.URL != nil && strings.EqualFold(strings.TrimSpace(req.URL.Scheme), "https") {
		defaultPort = "443"
	}
	targetAddr, err := normalizeProxyTargetAddress(targetHost, defaultPort)
	if err != nil {
		return "", nil, err
	}

	outReq := req.Clone(req.Context())
	if outReq.URL == nil {
		outReq.URL = &url.URL{Path: "/"}
	}
	outReq.URL.Scheme = ""
	outReq.URL.Host = ""
	if strings.TrimSpace(outReq.URL.Path) == "" {
		outReq.URL.Path = "/"
	}
	outReq.RequestURI = ""
	outReq.Header = req.Header.Clone()
	removeHopByHopHeaders(outReq.Header)
	outReq.Header.Set("Connection", "close")
	outReq.Close = true
	outReq.Host = strings.TrimSpace(req.Host)
	if outReq.Host == "" {
		outReq.Host = hostWithoutPort(targetAddr)
	}

	buf := bytes.NewBuffer(nil)
	if err := outReq.Write(buf); err != nil {
		return "", nil, err
	}
	return targetAddr, buf.Bytes(), nil
}

func removeHopByHopHeaders(header http.Header) {
	if header == nil {
		return
	}
	connectionVals := header.Values("Connection")
	for _, value := range connectionVals {
		parts := strings.Split(value, ",")
		for _, part := range parts {
			key := strings.TrimSpace(part)
			if key != "" {
				header.Del(key)
			}
		}
	}

	header.Del("Connection")
	header.Del("Proxy-Connection")
	header.Del("Keep-Alive")
	header.Del("Proxy-Authenticate")
	header.Del("Proxy-Authorization")
	header.Del("Te")
	header.Del("Trailer")
	header.Del("Transfer-Encoding")
	header.Del("Upgrade")
}

func hostWithoutPort(targetAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return strings.TrimSpace(targetAddr)
	}
	return strings.Trim(host, "[]")
}

func normalizeProxyTargetAddress(rawHost string, defaultPort string) (string, error) {
	host := strings.TrimSpace(rawHost)
	if host == "" {
		return "", errors.New("missing target host")
	}
	if strings.Contains(host, "://") {
		parsed, err := url.Parse(host)
		if err != nil {
			return "", err
		}
		host = strings.TrimSpace(parsed.Host)
		if host == "" {
			return "", errors.New("missing target host")
		}
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host, nil
	}
	if strings.TrimSpace(defaultPort) == "" {
		defaultPort = "443"
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	}
	return net.JoinHostPort(host, defaultPort), nil
}

func writeHTTPProxyStatus(conn net.Conn, statusCode int, message string) error {
	statusText := strings.TrimSpace(http.StatusText(statusCode))
	if statusText == "" {
		statusText = "Error"
	}
	body := strings.TrimSpace(message)
	if body == "" {
		body = statusText
	}
	resp := fmt.Sprintf(
		"HTTP/1.1 %d %s\r\nConnection: close\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\n\r\n%s",
		statusCode,
		statusText,
		len(body),
		body,
	)
	_, err := conn.Write([]byte(resp))
	return err
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

func (s *networkAssistantService) decideRouteForTarget(targetAddr string) (tunnelRouteDecision, error) {
	host, port, err := splitTargetHostPort(targetAddr)
	if err != nil {
		return tunnelRouteDecision{}, err
	}
	normalizedTarget := net.JoinHostPort(host, port)

	s.mu.RLock()
	mode := strings.TrimSpace(s.mode)
	nodeID := strings.TrimSpace(s.nodeID)
	availableNodes := append([]string(nil), s.availableNodes...)
	routing := s.ruleRouting
	s.mu.RUnlock()
	if nodeID == "" {
		nodeID = defaultNodeID
	}
	tunnelOptions := buildRuleTunnelOptions(availableNodes, nodeID)

	if mode == networkModeDirect {
		return tunnelRouteDecision{Direct: true, TargetAddr: normalizedTarget, NodeID: nodeID}, nil
	}

	if mode != networkModeRule {
		if s.shouldDialDirect(normalizedTarget) {
			return tunnelRouteDecision{Direct: true, TargetAddr: normalizedTarget, NodeID: nodeID}, nil
		}
		return tunnelRouteDecision{Direct: false, TargetAddr: normalizedTarget, NodeID: nodeID}, nil
	}

	if s.shouldDialDirect(normalizedTarget) {
		return tunnelRouteDecision{Direct: true, TargetAddr: normalizedTarget, NodeID: nodeID}, nil
	}

	rule, matched := routing.RuleSet.matchHost(host)
	if matched {
		group := normalizeRuleGroupName(rule.Group)
		policy, policyErr := readRulePolicyForGroup(routing, group, nodeID, tunnelOptions)
		if policyErr != nil {
			return tunnelRouteDecision{}, policyErr
		}
		switch policy.Action {
		case rulePolicyActionDirect:
			return tunnelRouteDecision{Direct: true, TargetAddr: normalizedTarget, NodeID: nodeID, Group: group}, nil
		case rulePolicyActionReject:
			return tunnelRouteDecision{}, &ruleRouteRejectError{Group: group}
		default:
			targetNodeID := strings.TrimSpace(policy.TunnelNodeID)
			if targetNodeID == "" {
				targetNodeID = nodeID
			}
			if targetNodeID == "" {
				targetNodeID = defaultNodeID
			}

			if ip := net.ParseIP(host); ip != nil {
				return tunnelRouteDecision{
					Direct:     false,
					TargetAddr: net.JoinHostPort(canonicalIP(ip), port),
					NodeID:     targetNodeID,
					Group:      group,
				}, nil
			}

			resolvedAddr, resolveErr := s.resolveRuleDomainViaTunnel(targetNodeID, host)
			if resolveErr != nil {
				return tunnelRouteDecision{}, resolveErr
			}
			return tunnelRouteDecision{
				Direct:     false,
				TargetAddr: net.JoinHostPort(resolvedAddr, port),
				NodeID:     targetNodeID,
				Group:      group,
			}, nil
		}
	}

	fallbackPolicy, fallbackErr := readRulePolicyForGroup(routing, ruleFallbackGroupKey, nodeID, tunnelOptions)
	if fallbackErr != nil {
		return tunnelRouteDecision{}, fallbackErr
	}
	switch fallbackPolicy.Action {
	case rulePolicyActionReject:
		return tunnelRouteDecision{}, &ruleRouteRejectError{Group: ruleFallbackGroupKey}
	case rulePolicyActionTunnel:
		targetNodeID := strings.TrimSpace(fallbackPolicy.TunnelNodeID)
		if targetNodeID == "" {
			targetNodeID = nodeID
		}
		if targetNodeID == "" {
			targetNodeID = defaultNodeID
		}
		if ip := net.ParseIP(host); ip != nil {
			return tunnelRouteDecision{
				Direct:     false,
				TargetAddr: net.JoinHostPort(canonicalIP(ip), port),
				NodeID:     targetNodeID,
			}, nil
		}
		resolvedAddr, resolveErr := s.resolveRuleDomainViaTunnel(targetNodeID, host)
		if resolveErr != nil {
			return tunnelRouteDecision{}, resolveErr
		}
		return tunnelRouteDecision{
			Direct:     false,
			TargetAddr: net.JoinHostPort(resolvedAddr, port),
			NodeID:     targetNodeID,
		}, nil
	default:
		return tunnelRouteDecision{Direct: true, TargetAddr: normalizedTarget, NodeID: nodeID}, nil
	}
}

func splitTargetHostPort(targetAddr string) (string, string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return "", "", err
	}
	normalizedHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if normalizedHost == "" {
		return "", "", errors.New("missing target host")
	}
	normalizedPort := strings.TrimSpace(port)
	if normalizedPort == "" {
		return "", "", errors.New("missing target port")
	}
	return normalizedHost, normalizedPort, nil
}

func (set tunnelRuleSet) matchHost(host string) (tunnelRule, bool) {
	targetHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if targetHost == "" {
		return tunnelRule{}, false
	}
	targetHostLower := strings.ToLower(targetHost)
	targetIP := net.ParseIP(targetHostLower)
	targetIPCanonical := ""
	if targetIP != nil {
		targetIPCanonical = canonicalIP(targetIP)
	}

	for _, rule := range set.Rules {
		switch rule.Kind {
		case ruleMatcherDomainSuffix:
			if targetIP != nil {
				continue
			}
			if domainMatchesRuleSuffix(targetHostLower, rule.Suffix) {
				return rule, true
			}
		case ruleMatcherIP:
			if targetIP == nil {
				continue
			}
			if targetIPCanonical == rule.IP {
				return rule, true
			}
		case ruleMatcherCIDR:
			if targetIP == nil || rule.CIDR == nil {
				continue
			}
			if rule.CIDR.Contains(targetIP) {
				return rule, true
			}
		}
	}
	return tunnelRule{}, false
}

func domainMatchesRuleSuffix(host string, suffix string) bool {
	normalizedHost := strings.TrimSpace(strings.ToLower(host))
	normalizedSuffix := strings.TrimSpace(strings.ToLower(strings.TrimPrefix(suffix, ".")))
	if normalizedHost == "" || normalizedSuffix == "" {
		return false
	}
	if normalizedHost == normalizedSuffix {
		return true
	}
	return strings.HasSuffix(normalizedHost, "."+normalizedSuffix)
}

func normalizeRuleGroupName(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func (s *networkAssistantService) resolveRuleDomainViaTunnel(nodeID string, domain string) (string, error) {
	normalizedDomain := normalizeRuleDomain(domain)
	if normalizedDomain == "" {
		return "", errors.New("invalid domain")
	}
	cacheKey := normalizeRuleDNSCacheKey(nodeID, normalizedDomain)
	if cached, ok := s.loadRuleDNSCache(cacheKey); ok && len(cached) > 0 {
		return choosePreferredResolvedAddress(cached), nil
	}

	addresses, ttlSeconds, err := s.queryRuleDomainViaTunnel(nodeID, normalizedDomain, 1)
	if (err != nil || len(addresses) == 0) && strings.Contains(normalizedDomain, ".") {
		v6Addresses, v6TTL, v6Err := s.queryRuleDomainViaTunnel(nodeID, normalizedDomain, 28)
		if v6Err == nil && len(v6Addresses) > 0 {
			addresses = v6Addresses
			ttlSeconds = v6TTL
			err = nil
		}
	}
	if err != nil {
		return "", err
	}
	if len(addresses) == 0 {
		return "", errors.New("dns resolve returned empty result")
	}

	s.storeRuleDNSCache(cacheKey, addresses, ttlSeconds)
	return choosePreferredResolvedAddress(addresses), nil
}

func normalizeRuleDomain(domain string) string {
	value := strings.TrimSpace(strings.Trim(domain, "."))
	if value == "" {
		return ""
	}
	if strings.Contains(value, " ") {
		return ""
	}
	return strings.ToLower(value)
}

func normalizeRuleDNSCacheKey(nodeID string, domain string) string {
	node := strings.TrimSpace(strings.ToLower(nodeID))
	if node == "" {
		node = strings.ToLower(defaultNodeID)
	}
	return node + "|" + strings.ToLower(strings.TrimSpace(domain))
}

func choosePreferredResolvedAddress(addresses []string) string {
	for _, addr := range addresses {
		ip := net.ParseIP(strings.TrimSpace(addr))
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			return canonicalIP(ip)
		}
	}
	for _, addr := range addresses {
		ip := net.ParseIP(strings.TrimSpace(addr))
		if ip == nil {
			continue
		}
		return canonicalIP(ip)
	}
	return ""
}

func clampRuleDNSTTL(ttlSeconds int) int {
	if ttlSeconds < ruleDNSCacheMinTTLSeconds {
		return ruleDNSCacheMinTTLSeconds
	}
	if ttlSeconds > ruleDNSCacheMaxTTLSeconds {
		return ruleDNSCacheMaxTTLSeconds
	}
	return ttlSeconds
}

func (s *networkAssistantService) loadRuleDNSCache(cacheKey string) ([]string, bool) {
	s.mu.RLock()
	entry, ok := s.ruleDNSCache[cacheKey]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.Expires) {
		s.mu.Lock()
		if latest, exists := s.ruleDNSCache[cacheKey]; exists && time.Now().After(latest.Expires) {
			delete(s.ruleDNSCache, cacheKey)
		}
		s.mu.Unlock()
		return nil, false
	}
	if len(entry.Addrs) == 0 {
		return nil, false
	}
	return append([]string(nil), entry.Addrs...), true
}

func (s *networkAssistantService) storeRuleDNSCache(cacheKey string, addresses []string, ttlSeconds int) {
	if len(addresses) == 0 {
		return
	}
	normalized := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		ip := net.ParseIP(strings.TrimSpace(addr))
		if ip == nil {
			continue
		}
		normalized = append(normalized, canonicalIP(ip))
	}
	if len(normalized) == 0 {
		return
	}
	ttl := clampRuleDNSTTL(ttlSeconds)
	s.mu.Lock()
	if s.ruleDNSCache == nil {
		s.ruleDNSCache = make(map[string]dnsCacheEntry)
	}
	s.ruleDNSCache[cacheKey] = dnsCacheEntry{
		Addrs:   normalized,
		Expires: time.Now().Add(time.Duration(ttl) * time.Second),
	}
	s.mu.Unlock()
}

func (s *networkAssistantService) queryRuleDomainViaTunnel(nodeID string, domain string, qType uint16) ([]string, int, error) {
	normalizedDomain := normalizeRuleDomain(domain)
	if normalizedDomain == "" {
		return nil, 0, errors.New("invalid domain")
	}
	queryID := uint16(atomic.AddUint32(&s.ruleDNSQuerySeq, 1))
	packet, err := buildDNSQueryPacket(normalizedDomain, qType, queryID)
	if err != nil {
		return nil, 0, err
	}

	servers := []string{ruleDNSDefaultServerA, ruleDNSDefaultServerB}
	deadline := time.Now().Add(ruleDNSResolveTimeout)
	trials := 0
	var lastErr error
	for _, server := range servers {
		if trials >= ruleDNSResolveServerTrials {
			break
		}
		trials++
		remaining := time.Until(deadline)
		if remaining <= 0 {
			lastErr = errors.New("dns resolve timeout")
			break
		}

		stream, openErr := s.openTunnelStreamForNode("udp", server, nodeID)
		if openErr != nil {
			lastErr = openErr
			continue
		}

		writeErr := stream.write(packet)
		if writeErr != nil {
			lastErr = writeErr
			stream.close()
			continue
		}

		waitTimeout := ruleDNSResolveReadTimeout
		if remaining < waitTimeout {
			waitTimeout = remaining
		}
		select {
		case payload := <-stream.readCh:
			stream.close()
			addrs, ttl, parseErr := parseDNSResponseAddrs(payload, queryID, qType)
			if parseErr != nil {
				lastErr = parseErr
				continue
			}
			return addrs, ttl, nil
		case streamErr := <-stream.errCh:
			lastErr = streamErr
			stream.close()
			continue
		case <-time.After(waitTimeout):
			lastErr = errors.New("dns tunnel read timeout")
			stream.close()
			continue
		}
	}
	if lastErr == nil {
		lastErr = errors.New("dns resolve failed")
	}
	return nil, 0, lastErr
}

func buildDNSQueryPacket(domain string, qType uint16, queryID uint16) ([]byte, error) {
	namePayload, err := encodeDNSName(domain)
	if err != nil {
		return nil, err
	}
	packet := make([]byte, 12+len(namePayload)+4)
	binary.BigEndian.PutUint16(packet[0:2], queryID)
	binary.BigEndian.PutUint16(packet[2:4], 0x0100)
	binary.BigEndian.PutUint16(packet[4:6], 1)
	copy(packet[12:], namePayload)
	questionOffset := 12 + len(namePayload)
	binary.BigEndian.PutUint16(packet[questionOffset:questionOffset+2], qType)
	binary.BigEndian.PutUint16(packet[questionOffset+2:questionOffset+4], 1)
	return packet, nil
}

func encodeDNSName(domain string) ([]byte, error) {
	normalized := normalizeRuleDomain(domain)
	if normalized == "" {
		return nil, errors.New("invalid dns name")
	}
	parts := strings.Split(normalized, ".")
	buf := bytes.NewBuffer(make([]byte, 0, len(normalized)+2))
	for _, part := range parts {
		label := strings.TrimSpace(part)
		if label == "" || len(label) > 63 {
			return nil, errors.New("invalid dns label")
		}
		buf.WriteByte(byte(len(label)))
		buf.WriteString(label)
	}
	buf.WriteByte(0x00)
	return buf.Bytes(), nil
}

func parseDNSResponseAddrs(payload []byte, expectedID uint16, qType uint16) ([]string, int, error) {
	if len(payload) < 12 {
		return nil, 0, errors.New("dns response too short")
	}
	respID := binary.BigEndian.Uint16(payload[0:2])
	if respID != expectedID {
		return nil, 0, errors.New("dns response id mismatch")
	}
	rcode := payload[3] & 0x0F
	if rcode != 0 {
		return nil, 0, fmt.Errorf("dns response rcode=%d", rcode)
	}
	questionCount := int(binary.BigEndian.Uint16(payload[4:6]))
	answerCount := int(binary.BigEndian.Uint16(payload[6:8]))

	offset := 12
	for i := 0; i < questionCount; i++ {
		nextOffset, err := skipDNSName(payload, offset)
		if err != nil {
			return nil, 0, err
		}
		if nextOffset+4 > len(payload) {
			return nil, 0, errors.New("dns question truncated")
		}
		offset = nextOffset + 4
	}

	addresses := make([]string, 0, answerCount)
	minTTL := 0
	for i := 0; i < answerCount; i++ {
		nextOffset, err := skipDNSName(payload, offset)
		if err != nil {
			return nil, 0, err
		}
		if nextOffset+10 > len(payload) {
			return nil, 0, errors.New("dns answer truncated")
		}
		recordType := binary.BigEndian.Uint16(payload[nextOffset : nextOffset+2])
		recordClass := binary.BigEndian.Uint16(payload[nextOffset+2 : nextOffset+4])
		recordTTL := int(binary.BigEndian.Uint32(payload[nextOffset+4 : nextOffset+8]))
		recordLength := int(binary.BigEndian.Uint16(payload[nextOffset+8 : nextOffset+10]))
		recordStart := nextOffset + 10
		recordEnd := recordStart + recordLength
		if recordEnd > len(payload) {
			return nil, 0, errors.New("dns answer payload truncated")
		}

		if recordClass == 1 && recordType == qType {
			switch qType {
			case 1:
				if recordLength == net.IPv4len {
					ip := net.IP(payload[recordStart:recordEnd])
					addresses = append(addresses, canonicalIP(ip))
					if minTTL == 0 || (recordTTL > 0 && recordTTL < minTTL) {
						minTTL = recordTTL
					}
				}
			case 28:
				if recordLength == net.IPv6len {
					ip := net.IP(payload[recordStart:recordEnd])
					addresses = append(addresses, canonicalIP(ip))
					if minTTL == 0 || (recordTTL > 0 && recordTTL < minTTL) {
						minTTL = recordTTL
					}
				}
			}
		}

		offset = recordEnd
	}

	if len(addresses) == 0 {
		return nil, 0, errors.New("dns response has no address records")
	}
	if minTTL <= 0 {
		minTTL = ruleDNSCacheMinTTLSeconds
	}
	return addresses, clampRuleDNSTTL(minTTL), nil
}

func skipDNSName(payload []byte, offset int) (int, error) {
	nextOffset := offset
	for i := 0; i < 128; i++ {
		if nextOffset >= len(payload) {
			return 0, errors.New("invalid dns name offset")
		}
		length := int(payload[nextOffset])
		if length == 0 {
			return nextOffset + 1, nil
		}
		if length&0xC0 == 0xC0 {
			if nextOffset+1 >= len(payload) {
				return 0, errors.New("invalid dns pointer")
			}
			return nextOffset + 2, nil
		}
		nextOffset++
		if nextOffset+length > len(payload) {
			return 0, errors.New("invalid dns label length")
		}
		nextOffset += length
	}
	return 0, errors.New("dns name exceeds max depth")
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
		route, routeErr := s.decideRouteForTarget(targetAddr)
		if routeErr != nil {
			if isRuleRouteRejectErr(routeErr) {
				s.logf("udp target rejected by rule target=%s err=%v", targetAddr, routeErr)
				s.setTunnelStatus("规则拒绝")
			} else {
				s.logf("udp route decision failed target=%s err=%v", targetAddr, routeErr)
			}
			continue
		}
		if route.Direct {
			respPayload, respAddr, err = dialUDPDirectPacket(route.TargetAddr, payload)
		} else {
			stream, openErr := s.openTunnelStreamForNode("udp", route.TargetAddr, route.NodeID)
			if openErr != nil {
				err = openErr
			} else {
				err = stream.write(payload)
				if err == nil {
					select {
					case respPayload = <-stream.readCh:
						respAddr = route.TargetAddr
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
			s.logf("udp relay failed target=%s err=%v", targetAddr, err)
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

	s.logf("applying system proxy to http/https/socks=%s", s.socks5ListenAddr)
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
	extraMuxClients := make([]*tunnelMuxClient, 0, len(s.ruleMuxClients))
	for _, client := range s.ruleMuxClients {
		if client != nil {
			extraMuxClients = append(extraMuxClients, client)
		}
	}
	s.ruleMuxClients = make(map[string]*tunnelMuxClient)
	s.tunnelMuxClient = nil
	s.listener = nil
	s.stopping.Store(true)
	s.mu.Unlock()

	if muxClient != nil {
		muxClient.close()
	}
	for _, client := range extraMuxClients {
		client.close()
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
	case strings.Contains(lower, "rule") || strings.Contains(lower, "规则"):
		return "rule"
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

	chainTargets, chainNodes, chainErr := fetchProbeChainTargetsViaAdminWS(baseURL, token)
	nodes := make([]string, 0, len(chainNodes)+1)
	if chainErr == nil && len(chainNodes) > 0 {
		nodes = append(nodes, chainNodes...)
	}
	if !containsNodeID(nodes, defaultNodeID) {
		nodes = append(nodes, defaultNodeID)
	}
	if len(nodes) == 0 {
		payload, err := fetchTunnelNodesViaAdminWS(baseURL, token)
		if err != nil {
			if chainErr != nil {
				return chainErr
			}
			return err
		}
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
	}

	s.mu.Lock()
	s.availableNodes = nodes
	s.chainTargets = chainTargets
	if !containsNodeID(nodes, s.nodeID) {
		s.nodeID = nodes[0]
	}
	s.mu.Unlock()
	return nil
}

func fetchTunnelNodesViaAdminWS(baseURL, token string) (tunnelNodesResponse, error) {
	wsURL, err := buildAdminWSURL(baseURL)
	if err != nil {
		return tunnelNodesResponse{}, err
	}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	headers := http.Header{}
	headers.Set("X-Forwarded-Proto", "https")
	conn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return tunnelNodesResponse{}, fmt.Errorf("admin ws handshake failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return tunnelNodesResponse{}, err
	}
	defer conn.Close()

	deadline := time.Now().Add(10 * time.Second)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return tunnelNodesResponse{}, err
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return tunnelNodesResponse{}, err
	}

	authID := fmt.Sprintf("na-auth-%d", time.Now().UnixNano())
	authReq := adminWSRequest{ID: authID, Action: "auth.session", Payload: map[string]string{"token": strings.TrimSpace(token)}}
	if err := conn.WriteJSON(authReq); err != nil {
		return tunnelNodesResponse{}, err
	}
	authResp, err := readAdminWSResponseByID(conn, authID)
	if err != nil {
		return tunnelNodesResponse{}, err
	}
	if !authResp.OK {
		return tunnelNodesResponse{}, fmt.Errorf("admin ws auth failed: %s", strings.TrimSpace(authResp.Error))
	}

	queryID := fmt.Sprintf("na-nodes-%d", time.Now().UnixNano())
	queryReq := adminWSRequest{ID: queryID, Action: "admin.tunnel.nodes"}
	if err := conn.WriteJSON(queryReq); err != nil {
		return tunnelNodesResponse{}, err
	}
	queryResp, err := readAdminWSResponseByID(conn, queryID)
	if err != nil {
		return tunnelNodesResponse{}, err
	}
	if !queryResp.OK {
		return tunnelNodesResponse{}, fmt.Errorf("fetch tunnel nodes failed: %s", strings.TrimSpace(queryResp.Error))
	}

	var payload tunnelNodesResponse
	if len(queryResp.Data) == 0 {
		return payload, nil
	}
	if err := json.Unmarshal(queryResp.Data, &payload); err != nil {
		return tunnelNodesResponse{}, err
	}
	return payload, nil
}

func readAdminWSResponseByID(conn *websocket.Conn, requestID string) (adminWSResponse, error) {
	for {
		var resp adminWSResponse
		if err := conn.ReadJSON(&resp); err != nil {
			return adminWSResponse{}, err
		}
		if strings.TrimSpace(resp.Type) != "" {
			continue
		}
		if strings.TrimSpace(resp.ID) != strings.TrimSpace(requestID) {
			continue
		}
		return resp, nil
	}
}

func buildAdminWSURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || strings.TrimSpace(parsed.Host) == "" {
		return "", errors.New("invalid controller url")
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		parsed.Scheme = "wss"
	} else if strings.EqualFold(parsed.Scheme, "http") {
		parsed.Scheme = "ws"
	} else if strings.EqualFold(parsed.Scheme, "wss") || strings.EqualFold(parsed.Scheme, "ws") {
		// keep
	} else {
		return "", errors.New("unsupported controller url scheme")
	}
	parsed.Path = "/api/admin/ws"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimSpace(parsed.String()), nil
}

func buildNetworkAssistantTunnelRoute(nodeID string) string {
	if chainID, ok := parseChainTargetNodeID(nodeID); ok {
		return "chain://" + chainID
	}
	return tunnelRoutePath + strings.TrimSpace(nodeID)
}

func buildChainTargetNodeID(chainID string) string {
	value := strings.TrimSpace(chainID)
	if value == "" {
		return ""
	}
	return chainTargetNodePrefix + value
}

func parseChainTargetNodeID(nodeID string) (string, bool) {
	value := strings.TrimSpace(nodeID)
	if !strings.HasPrefix(strings.ToLower(value), chainTargetNodePrefix) {
		return "", false
	}
	chainID := strings.TrimSpace(value[len(chainTargetNodePrefix):])
	if chainID == "" {
		return "", false
	}
	return chainID, true
}

func normalizeProbeNodeIDValue(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	number, err := strconv.Atoi(value)
	if err != nil || number <= 0 {
		return ""
	}
	return strconv.Itoa(number)
}

func buildProbeChainRouteNodesForManager(item probeLinkChainAdminItem) []string {
	route := make([]string, 0, 2+len(item.CascadeNodeIDs))
	entry := normalizeProbeNodeIDValue(item.EntryNodeID)
	exitNode := normalizeProbeNodeIDValue(item.ExitNodeID)
	if entry != "" {
		route = append(route, entry)
	}
	seen := map[string]struct{}{}
	if entry != "" {
		seen[entry] = struct{}{}
	}
	for _, cascadeRaw := range item.CascadeNodeIDs {
		cascade := normalizeProbeNodeIDValue(cascadeRaw)
		if cascade == "" || cascade == entry || cascade == exitNode {
			continue
		}
		if _, ok := seen[cascade]; ok {
			continue
		}
		seen[cascade] = struct{}{}
		route = append(route, cascade)
	}
	if exitNode != "" {
		if len(route) == 0 || route[len(route)-1] != exitNode {
			route = append(route, exitNode)
		}
	}
	return route
}

func normalizeChainLinkLayerValue(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "http":
		return "http"
	case "http2", "h2":
		return "http2"
	case "http3", "h3":
		return "http3"
	default:
		return "http"
	}
}

func resolveProbeChainEntryLinkLayer(item probeLinkChainAdminItem, entryNodeID string) string {
	layer := normalizeChainLinkLayerValue(item.LinkLayer)
	targetNodeID := normalizeProbeNodeIDValue(entryNodeID)
	if targetNodeID == "" {
		return layer
	}
	for _, hop := range item.HopConfigs {
		if hop.NodeNo <= 0 {
			continue
		}
		if strconv.Itoa(hop.NodeNo) != targetNodeID {
			continue
		}
		return normalizeChainLinkLayerValue(hop.LinkLayer)
	}
	return layer
}

func normalizeHostValue(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil {
			value = strings.TrimSpace(parsed.Host)
		}
	}
	value = strings.TrimSpace(strings.Split(value, "/")[0])
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		return strings.TrimSpace(value[1 : len(value)-1])
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return strings.TrimSpace(strings.Trim(host, "[]"))
	}
	return strings.TrimSpace(strings.Trim(value, "[]"))
}

func isIPv4HostValue(host string) bool {
	return regexp.MustCompile(`^(?:\d{1,3}\.){3}\d{1,3}$`).MatchString(host)
}

func isIPv6HostValue(host string) bool {
	if !strings.Contains(host, ":") {
		return false
	}
	if strings.Contains(host, ".") {
		return false
	}
	return regexp.MustCompile(`^[0-9a-fA-F:]+$`).MatchString(host)
}

func isDomainHostValue(host string) bool {
	value := normalizeHostValue(host)
	if value == "" {
		return false
	}
	if isIPv4HostValue(value) || isIPv6HostValue(value) {
		return false
	}
	return strings.Contains(value, ".")
}

func isLikelyAPIDomainHostValue(host string) bool {
	value := strings.ToLower(normalizeHostValue(host))
	if !isDomainHostValue(value) {
		return false
	}
	return strings.HasPrefix(value, "api.") || strings.Contains(value, ".api.")
}

func selectProbeChainRelayHost(node probeNodeAdminItem) string {
	candidates := []string{
		normalizeHostValue(node.PublicHost),
		normalizeHostValue(node.DDNS),
		normalizeHostValue(node.ServiceHost),
	}
	best := ""
	for _, host := range candidates {
		if host == "" {
			continue
		}
		if isLikelyAPIDomainHostValue(host) {
			return host
		}
		if best == "" {
			best = host
		}
	}
	return best
}

func selectProbeChainRelayPort(node probeNodeAdminItem) int {
	if node.PublicPort > 0 && node.PublicPort <= 65535 {
		return node.PublicPort
	}
	if node.ServicePort > 0 && node.ServicePort <= 65535 {
		return node.ServicePort
	}
	return 0
}

func fetchProbeChainTargetsViaAdminWS(baseURL, token string) (map[string]probeChainEndpoint, []string, error) {
	wsURL, err := buildAdminWSURL(baseURL)
	if err != nil {
		return map[string]probeChainEndpoint{}, nil, err
	}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	headers := http.Header{}
	headers.Set("X-Forwarded-Proto", "https")
	conn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return map[string]probeChainEndpoint{}, nil, fmt.Errorf("admin ws handshake failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return map[string]probeChainEndpoint{}, nil, err
	}
	defer conn.Close()

	deadline := time.Now().Add(12 * time.Second)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return map[string]probeChainEndpoint{}, nil, err
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return map[string]probeChainEndpoint{}, nil, err
	}

	authID := fmt.Sprintf("na-chain-auth-%d", time.Now().UnixNano())
	authReq := adminWSRequest{ID: authID, Action: "auth.session", Payload: map[string]string{"token": strings.TrimSpace(token)}}
	if err := conn.WriteJSON(authReq); err != nil {
		return map[string]probeChainEndpoint{}, nil, err
	}
	authResp, err := readAdminWSResponseByID(conn, authID)
	if err != nil {
		return map[string]probeChainEndpoint{}, nil, err
	}
	if !authResp.OK {
		return map[string]probeChainEndpoint{}, nil, fmt.Errorf("admin ws auth failed: %s", strings.TrimSpace(authResp.Error))
	}

	chainsID := fmt.Sprintf("na-chain-items-%d", time.Now().UnixNano())
	if err := conn.WriteJSON(adminWSRequest{ID: chainsID, Action: "admin.probe.link.chains.get"}); err != nil {
		return map[string]probeChainEndpoint{}, nil, err
	}
	chainsResp, err := readAdminWSResponseByID(conn, chainsID)
	if err != nil {
		return map[string]probeChainEndpoint{}, nil, err
	}
	if !chainsResp.OK {
		return map[string]probeChainEndpoint{}, nil, fmt.Errorf("fetch chain list failed: %s", strings.TrimSpace(chainsResp.Error))
	}

	nodesID := fmt.Sprintf("na-chain-nodes-%d", time.Now().UnixNano())
	if err := conn.WriteJSON(adminWSRequest{ID: nodesID, Action: "admin.probe.nodes.get"}); err != nil {
		return map[string]probeChainEndpoint{}, nil, err
	}
	nodesResp, err := readAdminWSResponseByID(conn, nodesID)
	if err != nil {
		return map[string]probeChainEndpoint{}, nil, err
	}
	if !nodesResp.OK {
		return map[string]probeChainEndpoint{}, nil, fmt.Errorf("fetch probe nodes failed: %s", strings.TrimSpace(nodesResp.Error))
	}

	chainsPayload := probeLinkChainsResponse{}
	if len(chainsResp.Data) > 0 {
		if err := json.Unmarshal(chainsResp.Data, &chainsPayload); err != nil {
			return map[string]probeChainEndpoint{}, nil, err
		}
	}
	nodesPayload := probeNodesResponse{}
	if len(nodesResp.Data) > 0 {
		if err := json.Unmarshal(nodesResp.Data, &nodesPayload); err != nil {
			return map[string]probeChainEndpoint{}, nil, err
		}
	}

	nodeByID := make(map[string]probeNodeAdminItem, len(nodesPayload.Nodes))
	for _, item := range nodesPayload.Nodes {
		if item.NodeNo <= 0 {
			continue
		}
		nodeByID[strconv.Itoa(item.NodeNo)] = item
	}

	targets := make(map[string]probeChainEndpoint)
	ids := make([]string, 0, len(chainsPayload.Items))
	for _, chain := range chainsPayload.Items {
		chainID := strings.TrimSpace(chain.ChainID)
		if chainID == "" {
			continue
		}
		route := buildProbeChainRouteNodesForManager(chain)
		if len(route) == 0 {
			continue
		}
		entryNodeID := route[0]
		node, ok := nodeByID[entryNodeID]
		if !ok {
			continue
		}
		host := selectProbeChainRelayHost(node)
		port := selectProbeChainRelayPort(node)
		if host == "" || port <= 0 {
			continue
		}
		targetID := buildChainTargetNodeID(chainID)
		if targetID == "" {
			continue
		}
		targets[targetID] = probeChainEndpoint{
			TargetID:  targetID,
			ChainID:   chainID,
			EntryNode: entryNodeID,
			RelayHost: host,
			RelayPort: port,
			LinkLayer: resolveProbeChainEntryLinkLayer(chain, entryNodeID),
		}
		ids = append(ids, targetID)
	}
	sort.Strings(ids)
	return targets, ids, nil
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

func loadOrCreateTunnelRuleRouting() (tunnelRuleRouting, error) {
	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return tunnelRuleRouting{}, err
	}

	routePath := filepath.Join(dataDir, ruleRouteFile)
	policyPath, legacyGroupPath := resolveRulePolicyPaths(dataDir)
	if err := ensureTunnelRuleRouteFile(routePath); err != nil {
		return tunnelRuleRouting{}, err
	}
	if err := ensureTunnelRulePolicyFile(policyPath); err != nil {
		return tunnelRuleRouting{}, err
	}

	ruleSet, err := parseTunnelRuleFile(routePath)
	if err != nil {
		return tunnelRuleRouting{}, err
	}

	policyMap, err := parseTunnelRulePolicyFile(policyPath)
	if err != nil {
		return tunnelRuleRouting{}, fmt.Errorf("parse rule policies failed: %w", err)
	}

	legacyMap, legacyErr := loadLegacyRuleGroupPolicyMap(legacyGroupPath)
	if legacyErr == nil {
		for group, value := range legacyMap {
			if strings.TrimSpace(policyMap[group]) == "" {
				policyMap[group] = value
			}
		}
	}
	policyMap = buildCanonicalRulePolicyMap(ruleSet, policyMap, defaultNodeID)
	if err := saveTunnelRulePolicyFile(policyPath, ruleSet, policyMap); err != nil {
		return tunnelRuleRouting{}, err
	}

	return tunnelRuleRouting{
		RuleSet:       ruleSet,
		GroupNodeMap:  policyMap,
		RuleFilePath:  routePath,
		GroupFilePath: policyPath,
	}, nil
}

func ensureTunnelRuleRouteFile(routePath string) error {
	_, err := os.Stat(routePath)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	content := "# CloudHelper rule routes\n" +
		"# each line: <domain suffix|ip|cidr>,<proxy_group>\n" +
		"# examples:\n" +
		strings.Join(defaultRuleRoutes, "\n") + "\n"
	if err := os.WriteFile(routePath, []byte(content), 0o644); err != nil {
		return err
	}
	return autoBackupManagerData()
}

func ensureTunnelRuleGroupFile(groupPath string) error {
	_, err := os.Stat(groupPath)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	content := "# CloudHelper rule proxy groups\n" +
		"# each line: <proxy_group>,<node_id_or_chain_id>\n" +
		"# chain id example: chain:<chain_id>\n" +
		strings.Join(defaultRuleGroups, "\n") + "\n"
	if err := os.WriteFile(groupPath, []byte(content), 0o644); err != nil {
		return err
	}
	return autoBackupManagerData()
}

func parseTunnelRuleFile(path string) (tunnelRuleSet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return tunnelRuleSet{}, err
	}

	rules := make([]tunnelRule, 0)
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		rule, parseErr := parseTunnelRuleLine(line)
		if parseErr != nil {
			return tunnelRuleSet{}, fmt.Errorf("invalid rule_routes line %d: %w", lineNo, parseErr)
		}
		rules = append(rules, rule)
	}
	if err := scanner.Err(); err != nil {
		return tunnelRuleSet{}, err
	}
	return tunnelRuleSet{Rules: rules}, nil
}

func parseTunnelRuleLine(line string) (tunnelRule, error) {
	parts := strings.SplitN(strings.TrimSpace(line), ",", 2)
	if len(parts) != 2 {
		return tunnelRule{}, errors.New("expected <pattern>,<proxy_group>")
	}
	pattern := strings.TrimSpace(parts[0])
	group := normalizeRuleGroupName(parts[1])
	if pattern == "" {
		return tunnelRule{}, errors.New("rule pattern is required")
	}
	if group == "" {
		return tunnelRule{}, errors.New("proxy group is required")
	}

	patternLower := strings.ToLower(pattern)
	if _, cidr, err := net.ParseCIDR(patternLower); err == nil {
		return tunnelRule{
			RawPattern: pattern,
			Group:      group,
			Kind:       ruleMatcherCIDR,
			CIDR:       cidr,
		}, nil
	}
	if ip := net.ParseIP(patternLower); ip != nil {
		return tunnelRule{
			RawPattern: pattern,
			Group:      group,
			Kind:       ruleMatcherIP,
			IP:         canonicalIP(ip),
		}, nil
	}

	suffix := strings.TrimSpace(strings.TrimPrefix(patternLower, "*."))
	suffix = strings.TrimPrefix(suffix, ".")
	if suffix == "" {
		return tunnelRule{}, errors.New("domain suffix is required")
	}
	if strings.Contains(suffix, " ") || strings.Contains(suffix, ",") {
		return tunnelRule{}, errors.New("invalid domain suffix")
	}
	return tunnelRule{
		RawPattern: pattern,
		Group:      group,
		Kind:       ruleMatcherDomainSuffix,
		Suffix:     suffix,
	}, nil
}

func parseTunnelRuleGroupFile(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	groupMap := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, ",", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid rule_groups line %d: expected <proxy_group>,<node_id_or_chain_id>", lineNo)
		}
		group := normalizeRuleGroupName(parts[0])
		nodeID := strings.TrimSpace(parts[1])
		if group == "" {
			return nil, fmt.Errorf("invalid rule_groups line %d: proxy group is required", lineNo)
		}
		if nodeID == "" {
			return nil, fmt.Errorf("invalid rule_groups line %d: node id is required", lineNo)
		}
		groupMap[group] = nodeID
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return groupMap, nil
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
	if err := os.WriteFile(whitelistPath, []byte(content), 0o644); err != nil {
		return err
	}
	if err := autoBackupManagerData(); err != nil {
		return err
	}
	return nil
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
