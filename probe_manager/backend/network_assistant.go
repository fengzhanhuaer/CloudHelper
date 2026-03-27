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
	tunPreferenceFile     = "network_tun_preference.json"
	tunnelRoutePath       = "/api/ws/tunnel/"
	maxTunnelFailures     = 20

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
	TunnelRoute       string   `json:"tunnel_route"`
	TunnelStatus      string   `json:"tunnel_status"`
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
	Name           string   `json:"name"`
	ChainID        string   `json:"chain_id"`
	Secret         string   `json:"secret"`
	EntryNodeID    string   `json:"entry_node_id"`
	ExitNodeID     string   `json:"exit_node_id"`
	CascadeNodeIDs []string `json:"cascade_node_ids"`
	LinkLayer      string   `json:"link_layer"`
	HopConfigs     []struct {
		NodeNo       int    `json:"node_no"`
		ListenPort   int    `json:"listen_port,omitempty"`
		ExternalPort int    `json:"external_port,omitempty"`
		LinkLayer    string `json:"link_layer"`
		DialMode     string `json:"dial_mode,omitempty"`
		RelayHost    string `json:"relay_host,omitempty"`
	} `json:"hop_configs"`

	PortForwards []struct {
		ID         string `json:"id,omitempty"`
		Name       string `json:"name,omitempty"`
		ListenHost string `json:"listen_host,omitempty"`
		ListenPort int    `json:"listen_port"`
		TargetHost string `json:"target_host"`
		TargetPort int    `json:"target_port"`
		Network    string `json:"network,omitempty"`
		Enabled    bool   `json:"enabled"`
	} `json:"port_forwards"`
}

type probeNodeAdminItem struct {
	NodeNo      int    `json:"node_no"`
	DDNS        string `json:"ddns"`
	ServiceHost string `json:"service_host"`
}

type probeChainEndpoint struct {
	TargetID    string
	ChainName   string
	ChainID     string
	EntryNode   string
	EntryHost   string // public-facing host of the entry hop (DDNS or ip)
	EntryPort   int    // public-facing port of the entry hop (external_port, fallback to listen_port)
	LinkLayer   string
	ChainSecret string
	PortForwards []probeChainPortForward
}

type probeChainPortForward struct {
	ID         string
	Name       string
	ListenHost string
	ListenPort int
	TargetHost string
	TargetPort int
	Network    string
	Enabled    bool
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

type tunPreferenceState struct {
	EverEnabled  bool `json:"ever_enabled"`
	ManualClosed bool `json:"manual_closed"`
}

type dnsRouteHintEntry struct {
	Direct  bool
	NodeID  string
	Group   string
	Expires time.Time
}

type tunnelDNSResolveResponse struct {
	Addrs []string `json:"addrs"`
	TTL   int      `json:"ttl"`
	Error string   `json:"error,omitempty"`
}

type networkAssistantService struct {
	mu sync.RWMutex

	mode             string
	nodeID           string
	availableNodes   []string

	controllerBaseURL string
	sessionToken      string

	tunnelStatusMessage string
	systemProxyMessage  string
	lastError           string
	tunnelOpenFailures  int
	tunnelMuxClient     *tunnelMuxClient
	chainTargets        map[string]probeChainEndpoint
	serverProbeNodes    []probeNodeAdminItem // 从服务器同步的探针节点列表（静态配置，非实时状态）
	muxReconnects       int64

	tunSupported     bool
	tunInstalled     bool
	tunEnabled       bool
	tunLibraryPath   string
	tunStatus        string
	tunAdapterHandle uintptr
	tunLastSyncAt    time.Time
	tunDataPlane     localTUNDataPlane
	tunPacketStack   localTUNPacketStack
	tunUDPHandler    localTUNUDPHandlerCloser
	tunIPIDSeq       uint32
	tunRouteState    tunSystemRouteState
	tunDynamicBypass map[string]int
	tunRouteSyncedAt time.Time
	tunRouteHost     string
	tunRouteSyncing  bool
	tunEverEnabled   bool
	tunManualClosed  bool

	directWhitelist *socksDirectWhitelist
	logStore        *networkAssistantLogStore
	ruleRouting     tunnelRuleRouting
	ruleDNSCache    map[string]dnsCacheEntry
	ruleDNSQuerySeq uint32
	ruleMuxClients  map[string]*tunnelMuxClient
	tunUDPRelays    map[string]*localTUNUDPRelay
	dnsRouteHints   map[string]dnsRouteHintEntry
	internalDNS     *localInternalDNSServer
	logRateState    map[string]time.Time

	lastChainRefreshAt map[string]time.Time
	controlPlaneHosts  map[string]struct{}
	controlPlaneIPs    map[string]struct{}
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
		tunnelStatusMessage: "未启用",
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
		ruleDNSCache:        loadRuleDNSFromDisk(), // 从 dns_cache.json 恢复（24h TTL）
		ruleMuxClients:      make(map[string]*tunnelMuxClient),
		tunUDPRelays:        make(map[string]*localTUNUDPRelay),
		tunDynamicBypass:    make(map[string]int),
		dnsRouteHints:       make(map[string]dnsRouteHintEntry),
		logRateState:        make(map[string]time.Time),
		controlPlaneHosts:   make(map[string]struct{}),
		controlPlaneIPs:     make(map[string]struct{}),
	}
	if _, err := getDNSUpstreamConfig(); err != nil {
		logStore.Appendf(logSourceManager, "init", "failed to load dns upstream config, fallback to defaults: %v", err)
	}
	if pref, err := loadTUNPreferenceState(); err == nil {
		service.tunEverEnabled = pref.EverEnabled
		service.tunManualClosed = pref.ManualClosed
	} else {
		logStore.Appendf(logSourceManager, "init", "failed to load tun preference state: %v", err)
	}
	// 从本地缓存恢复节点列表，重启后无需联网即可切换模式
	if cachedNodes, cachedTargets, cachedProbeNodes, err := loadNodesCacheFromFile(); err == nil && len(cachedNodes) > 0 {
		service.availableNodes = cachedNodes
		if cachedTargets != nil {
			service.chainTargets = cachedTargets
		}
		if len(cachedProbeNodes) > 0 {
			service.serverProbeNodes = cachedProbeNodes
		}
		logStore.Appendf(logSourceManager, "init", "loaded %d nodes, %d probe nodes from cache file", len(cachedNodes), len(cachedProbeNodes))
	} else if err != nil {
		logStore.Appendf(logSourceManager, "init", "failed to load nodes cache: %v", err)
	}
	service.syncTUNInstallState()
	service.logf("service initialized, mode=%s", service.mode)
	globalNetworkAssistantService = service
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
	a.networkAssistant.MarkTUNManualClosed()
	if err := a.networkAssistant.ApplyMode("", "", networkModeDirect, ""); err != nil {
		return a.networkAssistant.Status(), err
	}
	return a.networkAssistant.Status(), nil
}

func (a *App) ForceRefreshProbeDNSCache(controllerBaseURL, sessionToken string) (string, error) {
	if a.networkAssistant == nil {
		return "", errors.New("network assistant service is not initialized")
	}
	return a.networkAssistant.ForceRefreshProbeDNSCache(controllerBaseURL, sessionToken)
}

func (s *networkAssistantService) MarkTUNManualClosed() {
	s.mu.Lock()
	shouldPersist := s.tunEverEnabled && !s.tunManualClosed
	if shouldPersist {
		s.tunManualClosed = true
	}
	pref := tunPreferenceState{EverEnabled: s.tunEverEnabled, ManualClosed: s.tunManualClosed}
	s.mu.Unlock()
	if shouldPersist {
		if err := saveTUNPreferenceState(pref); err != nil {
			s.logf("failed to persist tun preference state: %v", err)
		}
	}
}

func (s *networkAssistantService) ForceRefreshProbeDNSCache(controllerBaseURL, sessionToken string) (string, error) {
	baseURLInput := strings.TrimSpace(controllerBaseURL)
	tokenInput := strings.TrimSpace(sessionToken)
	if baseURLInput != "" || tokenInput != "" {
		s.UpdateSession(baseURLInput, tokenInput)
	}

	if err := clearDNSCacheFile(); err != nil {
		return "", err
	}

	s.mu.RLock()
	effectiveBase := strings.TrimSpace(s.controllerBaseURL)
	effectiveToken := strings.TrimSpace(s.sessionToken)
	s.mu.RUnlock()

	if effectiveBase != "" && effectiveToken != "" {
		if err := s.refreshAvailableNodes(false); err != nil {
			s.logf("force refresh dns cache: refresh available nodes failed: %v", err)
		}
	}

	hostSet := make(map[string]struct{})
	addHost := func(rawHost string) {
		host := normalizeDNSCacheHost(rawHost)
		if host == "" {
			return
		}
		hostSet[host] = struct{}{}
	}
	if controllerHost := resolveControllerHostForProtection(effectiveBase); controllerHost != "" {
		addHost(controllerHost)
	}

	targets := s.getChainTargetsSnapshot()
	for _, endpoint := range targets {
		addHost(endpoint.EntryHost)
	}

	hosts := make([]string, 0, len(hostSet))
	for host := range hostSet {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)

	resolved := 0
	failed := 0
	for _, host := range hosts {
		if _, _, err := resolveProbeChainDialIPHostFresh(host); err != nil {
			failed++
			s.logf("force refresh dns cache failed: host=%s err=%v", host, err)
			continue
		}
		resolved++
	}

	message := fmt.Sprintf("dns cache refreshed: hosts=%d resolved=%d failed=%d ttl=%s", len(hosts), resolved, failed, dnsCacheTTL)
	s.logf("%s", message)
	if failed > 0 {
		return message, fmt.Errorf("dns cache refresh partially failed: resolved=%d failed=%d", resolved, failed)
	}
	return message, nil
}

func (s *networkAssistantService) getChainTargetsSnapshot() map[string]probeChainEndpoint {
	s.mu.RLock()
	targets := copyProbeChainTargets(s.chainTargets)
	s.mu.RUnlock()
	return targets
}

func (s *networkAssistantService) UpdateSession(controllerBaseURL, sessionToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.controllerBaseURL = strings.TrimSpace(controllerBaseURL)
	s.sessionToken = strings.TrimSpace(sessionToken)
}

func (s *networkAssistantService) Sync(controllerBaseURL, sessionToken string) error {
	s.UpdateSession(controllerBaseURL, sessionToken)
	if err := s.refreshAvailableNodes(false); err != nil {
		s.setLastError(err)
		return err
	}
	// 刷新完节点列表后，预热当前节点的 mux 连接
	go s.warmupTunnelMux("")
	return nil
}

// warmupTunnelMux 在后台立即建立（或复用）指定节点的 mux 连接。
// nodeID 为空时使用当前已选节点。失败仅记日志，不影响主流程。
func (s *networkAssistantService) warmupTunnelMux(nodeID string) {
	s.mu.RLock()
	mode := s.mode
	base := strings.TrimSpace(s.controllerBaseURL)
	token := strings.TrimSpace(s.sessionToken)
	selectedNode := strings.TrimSpace(s.nodeID)
	s.mu.RUnlock()

	if mode == networkModeDirect || base == "" || token == "" {
		return
	}
	target := strings.TrimSpace(nodeID)
	if target == "" {
		target = selectedNode
	}
	if target == "" {
		target = defaultNodeID
	}
	if _, err := s.ensureTunnelMuxClientForNode(target); err != nil {
		s.logf("warmup tunnel mux failed (will retry on first use): node=%s err=%v", target, err)
	} else {
		s.logf("warmup tunnel mux ok: node=%s", target)
	}
}

// tryPingExistingMux 如果已有对 nodeID 的 mux 连接，直接用 yamux Ping 测延迟并返回。
// 返回 (rtt, true) 表示成功；返回 (0, false) 表示没有可用连接（调用方应回退新建）。
func (s *networkAssistantService) tryPingExistingMux(nodeID string) (time.Duration, bool) {
	s.mu.RLock()
	selectedNode := strings.TrimSpace(s.nodeID)
	primary := s.tunnelMuxClient
	ruleMuxClients := s.ruleMuxClients
	s.mu.RUnlock()

	target := strings.TrimSpace(nodeID)
	if target == "" {
		target = selectedNode
	}
	if target == "" {
		target = defaultNodeID
	}

	var candidate *tunnelMuxClient
	if strings.EqualFold(target, selectedNode) && primary != nil && !primary.isClosed() {
		candidate = primary
	} else if ruleMuxClients != nil {
		if c, ok := ruleMuxClients[target]; ok && c != nil && !c.isClosed() {
			candidate = c
		}
	}
	if candidate == nil {
		return 0, false
	}
	rtt, err := candidate.ping()
	if err != nil {
		return 0, false
	}
	return rtt, true
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
		TunnelRoute:       buildNetworkAssistantTunnelRoute(s.nodeID),
		TunnelStatus:      s.tunnelStatusMessage,
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
	wasUsingTUNCapture := s.tunEnabled
	s.lastError = ""
	s.mu.Unlock()

	if effectiveBase != "" && effectiveToken != "" {
		s.mu.RLock()
		hasCachedNodes := len(s.availableNodes) > 0
		s.mu.RUnlock()
		if hasCachedNodes {
			s.logf("apply mode: using cached available nodes (skip server fetch)")
		} else {
			s.logf("apply mode: no cached nodes, fetching from controller: %s", effectiveBase)
			if err := s.refreshAvailableNodes(false); err != nil {
				s.logf("refresh available nodes failed: %v", err)
				s.setLastError(err)
				return err
			}
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
		errTunRouting := s.clearTUNSystemRouting()
		s.closeAllMuxClients()
		errDirectProxy := applyDirectSystemProxy()
		if err := errors.Join(errStopTUN, errTunRouting, errDirectProxy); err != nil {
			s.logf("failed to switch to direct mode: %v", err)
			s.setLastError(err)
			return err
		}
		s.mu.Lock()
		s.mode = networkModeDirect
		s.tunnelStatusMessage = "直连模式"
		s.tunnelOpenFailures = 0
		s.tunEnabled = false
		s.tunStatus = tunStatusAfterDisable(s.tunSupported, s.tunInstalled)
		s.mu.Unlock()
		if wasUsingTUNCapture {
			s.logf("switched from tun capture to direct mode")
		}
		s.logf("switched mode to direct and restored system dns/proxy, node=%s", normalizedNode)
		return nil
	}

	routing, err := loadOrCreateTunnelRuleRouting()
	if err != nil {
		s.logf("failed to load rule routing config: %v", err)
		s.setLastError(err)
		return err
	}
	if err := s.applyRuleModeViaTUN(routing, normalizedNode, effectiveBase); err != nil {
		s.logf("failed to enable rule mode via tun: %v", err)
		s.setLastError(err)
		return err
	}
	// 模式切换成功后立即预热 mux 连接
	go s.warmupTunnelMux(normalizedNode)
	return nil
}

func (s *networkAssistantService) shouldUseTUNCaptureForRuleMode() bool {
	return true
}

func loadTUNPreferenceState() (tunPreferenceState, error) {
	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return tunPreferenceState{}, err
	}
	path := filepath.Join(dataDir, tunPreferenceFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tunPreferenceState{}, nil
		}
		return tunPreferenceState{}, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return tunPreferenceState{}, nil
	}
	var out tunPreferenceState
	if err := json.Unmarshal(raw, &out); err != nil {
		return tunPreferenceState{}, err
	}
	return out, nil
}

func saveTUNPreferenceState(state tunPreferenceState) error {
	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, tunPreferenceFile)
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return err
	}
	return nil
}

func (s *networkAssistantService) Shutdown() error {
	errStopTUN := s.stopLocalTUNDataPlane()
	errTunRouting := s.clearTUNSystemRouting()
	s.closeAllMuxClients()
	errDirect := applyDirectSystemProxy()

	s.mu.Lock()
	tunAdapterHandle := s.tunAdapterHandle
	tunLibraryPath := s.tunLibraryPath
	s.mode = networkModeDirect
	s.tunnelStatusMessage = "直连模式"
	s.tunnelOpenFailures = 0
	s.tunEnabled = false
	s.tunStatus = tunStatusAfterDisable(s.tunSupported, s.tunInstalled)
	s.tunAdapterHandle = 0
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
	if errStopTUN != nil {
		s.logf("shutdown tun dataplane cleanup returned error: %v", errStopTUN)
	}
	return errors.Join(errStopTUN, errTunRouting, errDirect, errCloseAdapter)
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
	if s.isControlPlaneDirectTarget(host) {
		return tunnelRouteDecision{Direct: true, TargetAddr: normalizedTarget, NodeID: nodeID}, nil
	}

	useRuleRouting := mode == networkModeRule || mode == networkModeTUN
	if !useRuleRouting {
		if s.shouldDialDirect(normalizedTarget) {
			return tunnelRouteDecision{Direct: true, TargetAddr: normalizedTarget, NodeID: nodeID}, nil
		}
		return tunnelRouteDecision{Direct: false, TargetAddr: normalizedTarget, NodeID: nodeID}, nil
	}

	if s.shouldDialDirect(normalizedTarget) {
		return tunnelRouteDecision{Direct: true, TargetAddr: normalizedTarget, NodeID: nodeID}, nil
	}

	if parsedIP := net.ParseIP(host); parsedIP != nil {
		if hint, ok := s.loadDNSRouteHint(canonicalIP(parsedIP)); ok {
			hintNodeID := strings.TrimSpace(hint.NodeID)
			if hintNodeID == "" {
				hintNodeID = nodeID
			}
			if hintNodeID == "" {
				hintNodeID = defaultNodeID
			}
			return tunnelRouteDecision{
				Direct:     hint.Direct,
				TargetAddr: net.JoinHostPort(canonicalIP(parsedIP), port),
				NodeID:     hintNodeID,
				Group:      strings.TrimSpace(hint.Group),
			}, nil
		}
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
			routedTarget := normalizedTarget
			if ip := net.ParseIP(host); ip != nil {
				routedTarget = net.JoinHostPort(canonicalIP(ip), port)
			}
			return tunnelRouteDecision{
				Direct:     false,
				TargetAddr: routedTarget,
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
		routedTarget := normalizedTarget
		if ip := net.ParseIP(host); ip != nil {
			routedTarget = net.JoinHostPort(canonicalIP(ip), port)
		}
		return tunnelRouteDecision{
			Direct:     false,
			TargetAddr: routedTarget,
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

func (s *networkAssistantService) storeRuleDNSCache(cacheKey string, addresses []string, _ int) {
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
	// 统一使用 24h TTL（内存 + 磁盘一致）
	expires := dnsCacheExpiresAt()
	s.mu.Lock()
	if s.ruleDNSCache == nil {
		s.ruleDNSCache = make(map[string]dnsCacheEntry)
	}
	s.ruleDNSCache[cacheKey] = dnsCacheEntry{Addrs: normalized, Expires: expires}
	s.mu.Unlock()
	// 异步落盘，不阻塞 DNS 响应路径
	go persistRuleDNSEntry(cacheKey, normalized)
}

func (s *networkAssistantService) queryRuleDomainViaTunnel(nodeID string, domain string, qType uint16) ([]string, int, error) {
	normalizedDomain := normalizeRuleDomain(domain)
	if normalizedDomain == "" {
		return nil, 0, errors.New("invalid domain")
	}
	normalizedNodeID := strings.TrimSpace(nodeID)
	if normalizedNodeID == "" {
		normalizedNodeID = defaultNodeID
	}

	stream, err := s.openTunnelStreamForNode("dns", buildTunnelDNSResolveAddress(normalizedDomain, qType), normalizedNodeID)
	if err != nil {
		return nil, 0, err
	}
	defer stream.close()

	deadline := time.Now().Add(ruleDNSResolveTimeout)
	responsePayload := make([]byte, 0, 1024)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, 0, errors.New("dns resolve timeout")
		}
		waitTimeout := ruleDNSResolveReadTimeout
		if remaining < waitTimeout {
			waitTimeout = remaining
		}
		select {
		case payload := <-stream.readCh:
			if len(payload) == 0 {
				continue
			}
			responsePayload = append(responsePayload, payload...)
			if len(responsePayload) > 64*1024 {
				return nil, 0, errors.New("dns resolve payload too large")
			}
		case streamErr := <-stream.errCh:
			if len(responsePayload) == 0 {
				if streamErr == nil || errors.Is(streamErr, io.EOF) {
					return nil, 0, errors.New("dns resolve returned empty response")
				}
				return nil, 0, streamErr
			}
			if streamErr != nil && !errors.Is(streamErr, io.EOF) {
				return nil, 0, streamErr
			}
			addrs, ttl, decodeErr := decodeTunnelDNSResolveResponse(responsePayload, qType)
			if decodeErr != nil {
				return nil, 0, decodeErr
			}
			return addrs, ttl, nil
		case <-time.After(waitTimeout):
			return nil, 0, errors.New("dns tunnel read timeout")
		}
	}
}

func buildTunnelDNSResolveAddress(domain string, qType uint16) string {
	return strconv.Itoa(int(qType)) + "|" + normalizeRuleDomain(domain)
}

func decodeTunnelDNSResolveResponse(payload []byte, qType uint16) ([]string, int, error) {
	if len(payload) == 0 {
		return nil, 0, errors.New("empty dns resolve response")
	}
	var response tunnelDNSResolveResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(response.Error) != "" {
		return nil, 0, errors.New(strings.TrimSpace(response.Error))
	}
	addresses := make([]string, 0, len(response.Addrs))
	for _, rawAddr := range response.Addrs {
		ip := net.ParseIP(strings.TrimSpace(rawAddr))
		if ip == nil {
			continue
		}
		if qType == 1 && ip.To4() == nil {
			continue
		}
		if qType == 28 && ip.To4() != nil {
			continue
		}
		addresses = append(addresses, canonicalIP(ip))
	}
	if len(addresses) == 0 {
		return nil, 0, errors.New("dns resolve returned empty result")
	}
	return addresses, clampRuleDNSTTL(response.TTL), nil
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

func (s *networkAssistantService) closeAllMuxClients() {
	s.mu.Lock()
	muxClient := s.tunnelMuxClient
	extraMuxClients := make([]*tunnelMuxClient, 0, len(s.ruleMuxClients))
	for _, client := range s.ruleMuxClients {
		if client != nil {
			extraMuxClients = append(extraMuxClients, client)
		}
	}
	s.ruleMuxClients = make(map[string]*tunnelMuxClient)
	s.tunnelMuxClient = nil
	s.mu.Unlock()

	if muxClient != nil {
		muxClient.close()
	}
	for _, client := range extraMuxClients {
		client.close()
	}
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

func (s *networkAssistantService) logfRateLimited(rateKey string, interval time.Duration, format string, args ...any) {
	if s == nil || s.logStore == nil {
		return
	}
	key := strings.TrimSpace(rateKey)
	if key == "" || interval <= 0 {
		s.logf(format, args...)
		return
	}

	now := time.Now()
	s.mu.Lock()
	if s.logRateState == nil {
		s.logRateState = make(map[string]time.Time)
	}
	if lastAt, ok := s.logRateState[key]; ok {
		if now.Sub(lastAt) < interval {
			s.mu.Unlock()
			return
		}
	}
	s.logRateState[key] = now
	s.mu.Unlock()

	s.logf(format, args...)
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

func (s *networkAssistantService) refreshAvailableNodes(forceFetch bool) error {
	s.mu.RLock()
	baseURL := strings.TrimSpace(s.controllerBaseURL)
	token := strings.TrimSpace(s.sessionToken)
	s.mu.RUnlock()

	if !forceFetch {
		nodes, targets, probeNodes, loadErr := loadNodesCacheFromFile()
		if loadErr == nil && len(nodes) > 0 {
			s.mu.Lock()
			s.availableNodes = nodes
			s.chainTargets = targets
			s.serverProbeNodes = probeNodes
			if !containsNodeID(nodes, s.nodeID) {
				s.nodeID = nodes[0]
			}
			s.mu.Unlock()
			s.logf("loaded %d nodes and %d chain targets from local cache", len(nodes), len(targets))
			return nil
		}
	}

	if baseURL == "" || token == "" {
		return errors.New("controller url and session token are required")
	}
	if err := s.ensureControlPlaneDialReady(baseURL); err != nil {
		return err
	}

	chainTargets, chainNodes, probeNodes, chainErr := fetchProbeChainTargetsViaAdminWS(baseURL, token, s.logf)
	if chainErr != nil {
		s.logf("warning: fetch probe chain targets failed: %v, using cached endpoints", chainErr)
		s.mu.RLock()
		if len(s.chainTargets) > 0 {
			chainTargets = s.chainTargets
			for targetID := range s.chainTargets {
				chainNodes = append(chainNodes, targetID)
			}
		}
		if len(s.serverProbeNodes) > 0 {
			probeNodes = s.serverProbeNodes
		}
		s.mu.RUnlock()
	}

	nodes := make([]string, 0, len(chainNodes)+1)
	if len(chainNodes) > 0 {
		nodes = append(nodes, chainNodes...)
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
	}

	if !containsNodeID(nodes, defaultNodeID) {
		nodes = append(nodes, defaultNodeID)
	}

	s.mu.Lock()
	s.availableNodes = nodes
	s.chainTargets = chainTargets
	if len(probeNodes) > 0 || chainErr != nil {
		s.serverProbeNodes = probeNodes
	} else if chainErr == nil {
		s.serverProbeNodes = nil
	}
	if !containsNodeID(nodes, s.nodeID) {
		s.nodeID = nodes[0]
	}
	s.mu.Unlock()

	// 异步落盘，不阻塞主流程
	go func() {
		if err := saveNodesCacheToFile(nodes, chainTargets, probeNodes); err != nil {
			s.logf("warning: failed to save nodes cache to file: %v", err)
		}
	}()
	return nil
}

func fetchTunnelNodesViaAdminWS(baseURL, token string) (tunnelNodesResponse, error) {
	wsURL, err := buildAdminWSURL(baseURL)
	if err != nil {
		return tunnelNodesResponse{}, err
	}

	dialer := buildAdminWSDialer(baseURL)
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

// buildAdminWSDialer 构造一个 WebSocket Dialer，其 NetDialContext 使用
// newCachedDNSDialContext（与全局 HTTP transport 共享同一 DNS 缓存逻辑）：
// 缓存命中时直接用 IP 建连，绕过系统 DNS；未命中时走系统 DNS 并自动写缓存，
// 后续（含 TUN 接管 DNS 后）实现"自愈"。
func buildAdminWSDialer(baseURL string) websocket.Dialer {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	if globalNetworkAssistantService != nil {
		dialer.NetDialContext = globalNetworkAssistantService.newCachedDNSDialContext(nil)
	} else {
		dialer.NetDialContext = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	}
	_ = baseURL
	return dialer
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

func selectProbeChainEntryHost(node probeNodeAdminItem) string {
	ddns := normalizeHostValue(node.DDNS)
	var candidates []string
	// If DDNS is a plain domain (not api.*), construct api.<ddns> as the preferred relay host.
	if ddns != "" && isDomainHostValue(ddns) && !isLikelyAPIDomainHostValue(ddns) {
		candidates = append(candidates, "api."+ddns)
	}
	candidates = append(candidates,
		ddns,
		normalizeHostValue(node.ServiceHost),
	)
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

// relay_port at node level is deprecated; port is always resolved from hop_config.external_port.

func fetchProbeChainTargetsViaAdminWS(baseURL, token string, warnf func(string, ...any)) (map[string]probeChainEndpoint, []string, []probeNodeAdminItem, error) {
	wsURL, err := buildAdminWSURL(baseURL)
	if err != nil {
		return map[string]probeChainEndpoint{}, nil, nil, err
	}

	dialer := buildAdminWSDialer(baseURL)
	headers := http.Header{}
	headers.Set("X-Forwarded-Proto", "https")
	conn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return map[string]probeChainEndpoint{}, nil, nil, fmt.Errorf("admin ws handshake failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return map[string]probeChainEndpoint{}, nil, nil, err
	}
	defer conn.Close()

	deadline := time.Now().Add(12 * time.Second)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return map[string]probeChainEndpoint{}, nil, nil, err
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return map[string]probeChainEndpoint{}, nil, nil, err
	}

	authID := fmt.Sprintf("na-chain-auth-%d", time.Now().UnixNano())
	authReq := adminWSRequest{ID: authID, Action: "auth.session", Payload: map[string]string{"token": strings.TrimSpace(token)}}
	if err := conn.WriteJSON(authReq); err != nil {
		return map[string]probeChainEndpoint{}, nil, nil, err
	}
	authResp, err := readAdminWSResponseByID(conn, authID)
	if err != nil {
		return map[string]probeChainEndpoint{}, nil, nil, err
	}
	if !authResp.OK {
		return map[string]probeChainEndpoint{}, nil, nil, fmt.Errorf("admin ws auth failed: %s", strings.TrimSpace(authResp.Error))
	}

	chainsID := fmt.Sprintf("na-chain-items-%d", time.Now().UnixNano())
	if err := conn.WriteJSON(adminWSRequest{ID: chainsID, Action: "admin.probe.link.chains.get"}); err != nil {
		return map[string]probeChainEndpoint{}, nil, nil, err
	}
	chainsResp, err := readAdminWSResponseByID(conn, chainsID)
	if err != nil {
		return map[string]probeChainEndpoint{}, nil, nil, err
	}
	if !chainsResp.OK {
		return map[string]probeChainEndpoint{}, nil, nil, fmt.Errorf("fetch chain list failed: %s", strings.TrimSpace(chainsResp.Error))
	}

	nodesID := fmt.Sprintf("na-chain-nodes-%d", time.Now().UnixNano())
	if err := conn.WriteJSON(adminWSRequest{ID: nodesID, Action: "admin.probe.nodes.get"}); err != nil {
		return map[string]probeChainEndpoint{}, nil, nil, err
	}
	nodesResp, err := readAdminWSResponseByID(conn, nodesID)
	if err != nil {
		return map[string]probeChainEndpoint{}, nil, nil, err
	}
	if !nodesResp.OK {
		return map[string]probeChainEndpoint{}, nil, nil, fmt.Errorf("fetch probe nodes failed: %s", strings.TrimSpace(nodesResp.Error))
	}

	chainsPayload := probeLinkChainsResponse{}
	if len(chainsResp.Data) > 0 {
		if err := json.Unmarshal(chainsResp.Data, &chainsPayload); err != nil {
			return map[string]probeChainEndpoint{}, nil, nil, err
		}
	}
	nodesPayload := probeNodesResponse{}
	if len(nodesResp.Data) > 0 {
		if err := json.Unmarshal(nodesResp.Data, &nodesPayload); err != nil {
			return map[string]probeChainEndpoint{}, nil, nil, err
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
	seenIDs := make(map[string]struct{}, len(chainsPayload.Items))
	for _, chain := range chainsPayload.Items {
		chainID := strings.TrimSpace(chain.ChainID)
		if chainID == "" {
			continue
		}
		targetID := buildChainTargetNodeID(chainID)
		if targetID != "" {
			if _, exists := seenIDs[targetID]; !exists {
				seenIDs[targetID] = struct{}{}
				ids = append(ids, targetID)
			}
		}

		route := buildProbeChainRouteNodesForManager(chain)
		if len(route) == 0 {
			warnf("chain has no valid route (entry/exit node not configured): chain_id=%s", chainID)
			continue
		}
		entryNodeID := route[0]

		// Resolve entry host: prefer relay_host from hop_config (auto-filled by server
		// from Cloudflare DDNS), fall back to probe node DDNS/service_host.
		entryHost := ""
		entryPort := 0
		for _, hop := range chain.HopConfigs {
			if hop.NodeNo > 0 && strconv.Itoa(hop.NodeNo) == entryNodeID {
				entryHost = strings.TrimSpace(hop.RelayHost)
				// ExternalPort is the canonical public-facing port (auto-filled by server to listen_port when 0).
				// Fall back to listen_port for legacy records saved before external_port was introduced.
				if hop.ExternalPort > 0 {
					entryPort = hop.ExternalPort
				} else if hop.ListenPort > 0 {
					entryPort = hop.ListenPort
				}
				break
			}
		}
		if entryHost == "" {
			if node, ok := nodeByID[entryNodeID]; ok {
				entryHost = selectProbeChainEntryHost(node)
			}
		}
		// relay_port at node level is deprecated; entryPort must come from hop_config.

		if entryHost == "" {
			warnf("chain entry node has no host (hop relay_host/ddns all empty): chain_id=%s entry_node=%s", chainID, entryNodeID)
			continue
		}
		if entryPort <= 0 {
			warnf("chain entry node has no port (hop external_port/listen_port all 0): chain_id=%s entry_node=%s", chainID, entryNodeID)
			continue
		}
		if targetID == "" {
			continue
		}
		var pfs []probeChainPortForward
		if len(chain.PortForwards) > 0 {
			pfs = make([]probeChainPortForward, len(chain.PortForwards))
			for i, pf := range chain.PortForwards {
				pfs[i] = probeChainPortForward{
					ID:         pf.ID,
					Name:       pf.Name,
					ListenHost: pf.ListenHost,
					ListenPort: pf.ListenPort,
					TargetHost: pf.TargetHost,
					TargetPort: pf.TargetPort,
					Network:    pf.Network,
					Enabled:    pf.Enabled,
				}
			}
		}

		targets[targetID] = probeChainEndpoint{
			TargetID:    targetID,
			ChainName:   strings.TrimSpace(chain.Name),
			ChainID:     chainID,
			EntryNode:   entryNodeID,
			EntryHost:   entryHost,
			EntryPort:   entryPort,
			LinkLayer:   resolveProbeChainEntryLinkLayer(chain, entryNodeID),
			ChainSecret: strings.TrimSpace(chain.Secret),
			PortForwards: pfs,
		}
	}
	sort.Strings(ids)
	return targets, ids, nodesPayload.Nodes, nil
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

		hostRule := normalizeDirectWhitelistHostRule(rule)
		if hostRule == "" {
			continue
		}
		whitelist.hosts[hostRule] = struct{}{}
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

func normalizeDirectWhitelistHostRule(rawRule string) string {
	rule := strings.ToLower(strings.TrimSpace(rawRule))
	if rule == "" {
		return ""
	}
	if strings.Contains(rule, " ") || strings.Contains(rule, ",") {
		return ""
	}
	rule = strings.TrimPrefix(rule, "*.")
	rule = strings.TrimPrefix(rule, ".")
	rule = strings.Trim(rule, ".")
	if rule == "" {
		return ""
	}
	return rule
}

func directWhitelistHostSuffixMatch(host string, rules map[string]struct{}) bool {
	if host == "" || len(rules) == 0 {
		return false
	}
	if _, ok := rules[host]; ok {
		return true
	}
	for suffix := range rules {
		if suffix == "" {
			continue
		}
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
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

	if directWhitelistHostSuffixMatch(host, w.hosts) {
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


