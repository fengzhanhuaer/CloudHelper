package backend

import (
	"bufio"
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
	networkModeTUN    = "tun"

	defaultNodeID         = networkModeDirect
	chainTargetNodePrefix = "chain:"
	ruleRouteFile         = "rule_routes.txt"
	ruleGroupFile         = "rule_groups.txt"
	tunPreferenceFile     = "network_tun_preference.json"
	tunnelRoutePath       = "/api/ws/tunnel/"
	maxTunnelFailures     = 20

	dnsSharedTTLSeconds        = int(dnsCacheTTL / time.Second)
	ruleDNSResolveTimeout      = 8 * time.Second
	ruleDNSResolveReadTimeout  = 5 * time.Second
	ruleDNSResolveServerTrials = 2
	udpDialRetryMaxAttempts    = 4
	udpDialRetryInitialBackoff = 40 * time.Millisecond
	udpDialRetryMaxBackoff     = 300 * time.Millisecond
)

var defaultRuleRoutes = []string{
	"default",
	"{",
	"# example.com",
	"# 1.2.3.4",
	"# 10.10.0.0/16",
	"}",
}

var defaultRuleGroups = []string{
	"default," + networkModeDirect,
}

var defaultDirectLANCIDRRules = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"100.64.0.0/10",
}

type NetworkAssistantStatus struct {
	Enabled            bool                                 `json:"enabled"`
	Mode               string                               `json:"mode"`
	NodeID             string                               `json:"node_id"`
	AvailableNodes     []string                             `json:"available_nodes"`
	Socks5Listen       string                               `json:"socks5_listen"`
	TunnelRoute        string                               `json:"tunnel_route"`
	TunnelStatus       string                               `json:"tunnel_status"`
	SystemProxyStatus  string                               `json:"system_proxy_status"`
	LastError          string                               `json:"last_error"`
	MuxConnected       bool                                 `json:"mux_connected"`
	MuxActiveStreams   int                                  `json:"mux_active_streams"`
	MuxReconnects      int64                                `json:"mux_reconnects"`
	MuxLastRecv        string                               `json:"mux_last_recv"`
	MuxLastPong        string                               `json:"mux_last_pong"`
	GroupKeepalive     []NetworkAssistantGroupKeepaliveItem `json:"group_keepalive"`
	TUNSupported       bool                                 `json:"tun_supported"`
	TUNInstalled       bool                                 `json:"tun_installed"`
	TUNEnabled         bool                                 `json:"tun_enabled"`
	TUNLibraryPath     string                               `json:"tun_library_path"`
	TUNStatus          string                               `json:"tun_status"`
}

// NetworkAssistantGroupKeepaliveItem 描述单个规则组当前生效链路的保活状态。
type NetworkAssistantGroupKeepaliveItem struct {
	Group         string `json:"group"`
	Action        string `json:"action"`
	TunnelNodeID  string `json:"tunnel_node_id,omitempty"`
	TunnelLabel   string `json:"tunnel_label,omitempty"`
	Connected     bool   `json:"connected"`
	ActiveStreams int    `json:"active_streams"`
	LastRecv      string `json:"last_recv"`
	LastPong      string `json:"last_pong"`
	Status        string `json:"status"`
}

// ruleGroupRuntimeState 是规则组的唯一运行时状态承载体。
// 连接对象、保活失败次数、重试时间等内部状态都跟随规则组存储，
// 不再依赖额外的 node->mux 私有映射作为最终事实来源。
type ruleGroupRuntimeState struct {
	Snapshot      NetworkAssistantGroupKeepaliveItem `json:"-"`
	Client        *tunnelMuxClient                   `json:"-"`
	RetryAt       time.Time                          `json:"-"`
	FailureCount  int                                `json:"-"`
	LastError     string                             `json:"-"`
	PolicyAction  string                             `json:"-"`
	PolicyNodeID  string                             `json:"-"`
	ResolvedGroup string                             `json:"-"`
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

type cloudflareDDNSRecordsResponse struct {
	Records []cloudflareDDNSRecordItem `json:"records"`
}

type cloudflareDDNSRecordItem struct {
	NodeNo      int    `json:"node_no"`
	RecordClass string `json:"record_class"`
	RecordName  string `json:"record_name"`
}

type probeLinkChainAdminItem struct {
	Name           string   `json:"name"`
	ChainID        string   `json:"chain_id"`
	UserID         string   `json:"user_id"`
	UserPublicKey  string   `json:"user_public_key"`
	Secret         string   `json:"secret"`
	EntryNodeID    string   `json:"entry_node_id"`
	ExitNodeID     string   `json:"exit_node_id"`
	CascadeNodeIDs []string `json:"cascade_node_ids"`
	ListenHost     string   `json:"listen_host"`
	ListenPort     int      `json:"listen_port"`
	LinkLayer      string   `json:"link_layer"`
	EgressHost     string   `json:"egress_host"`
	EgressPort     int      `json:"egress_port"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	HopConfigs     []struct {
		NodeNo       int    `json:"node_no"`
		ListenHost   string `json:"listen_host,omitempty"`
		ListenPort   int    `json:"listen_port,omitempty"`
		ExternalPort int    `json:"external_port,omitempty"`
		LinkLayer    string `json:"link_layer"`
		DialMode     string `json:"dial_mode,omitempty"`
		RelayHost    string `json:"relay_host,omitempty"`
	} `json:"hop_configs"`
	PortForwards []struct {
		ID         string `json:"id,omitempty"`
		Name       string `json:"name,omitempty"`
		EntrySide  string `json:"entry_side,omitempty"`
		ListenHost string `json:"listen_host"`
		ListenPort int    `json:"listen_port"`
		TargetHost string `json:"target_host"`
		TargetPort int    `json:"target_port"`
		Network    string `json:"network,omitempty"`
		Enabled    bool   `json:"enabled"`
	} `json:"port_forwards"`
}

type probeNodeAdminItem struct {
	NodeNo       int    `json:"node_no"`
	DDNS         string `json:"ddns"`
	ServiceHost  string `json:"service_host"`
	BusinessDDNS string `json:"business_ddns"`
}

type probeChainHopConfig struct {
	NodeNo       int
	ListenHost   string
	ListenPort   int
	ExternalPort int
	LinkLayer    string
	DialMode     string
	RelayHost    string
}

type probeChainPortForward struct {
	ID         string
	Name       string
	EntrySide  string
	ListenHost string
	ListenPort int
	TargetHost string
	TargetPort int
	Network    string
	Enabled    bool
}

type probeChainEndpoint struct {
	TargetID        string
	ChainName       string
	ChainID         string
	UserID          string
	UserPublicKey   string
	EntryNode       string
	ExitNode        string
	CascadeNodeIDs  []string
	ListenHost      string
	ListenPort      int
	EgressHost      string
	EgressPort      int
	CreatedAt       string
	UpdatedAt       string
	EntryHost       string // public-facing host of the entry hop (DDNS or ip)
	EntryPort       int    // public-facing port of the entry hop (external_port, fallback to listen_port)
	LinkLayer       string
	ChainSecret     string
	HopConfigs      []probeChainHopConfig
	PortForwards    []probeChainPortForward
}

type ProbeLinkChainCacheHopConfig struct {
	NodeNo       int    `json:"node_no"`
	ListenHost   string `json:"listen_host,omitempty"`
	ListenPort   int    `json:"listen_port,omitempty"`
	ExternalPort int    `json:"external_port,omitempty"`
	LinkLayer    string `json:"link_layer"`
	DialMode     string `json:"dial_mode,omitempty"`
	RelayHost    string `json:"relay_host,omitempty"`
}

type ProbeLinkChainCachePortForward struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	EntrySide  string `json:"entry_side,omitempty"`
	ListenHost string `json:"listen_host"`
	ListenPort int    `json:"listen_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Network    string `json:"network,omitempty"`
	Enabled    bool   `json:"enabled"`
}

type ProbeLinkChainCacheItem struct {
	ChainID        string                           `json:"chain_id"`
	Name           string                           `json:"name"`
	UserID         string                           `json:"user_id"`
	UserPublicKey  string                           `json:"user_public_key"`
	Secret         string                           `json:"secret"`
	EntryNodeID    string                           `json:"entry_node_id"`
	ExitNodeID     string                           `json:"exit_node_id"`
	CascadeNodeIDs []string                         `json:"cascade_node_ids"`
	NodeNameByID   map[string]string                `json:"node_name_by_id,omitempty"`
	ListenHost     string                           `json:"listen_host"`
	ListenPort     int                              `json:"listen_port"`
	LinkLayer      string                           `json:"link_layer,omitempty"`
	HopConfigs     []ProbeLinkChainCacheHopConfig   `json:"hop_configs,omitempty"`
	PortForwards   []ProbeLinkChainCachePortForward `json:"port_forwards,omitempty"`
	EgressHost     string                           `json:"egress_host"`
	EgressPort     int                              `json:"egress_port"`
	CreatedAt      string                           `json:"created_at,omitempty"`
	UpdatedAt      string                           `json:"updated_at,omitempty"`
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

type ruleMatcherKind string

const (
	ruleMatcherDomainExact    ruleMatcherKind = "domain_exact"
	ruleMatcherDomainSuffix   ruleMatcherKind = "domain_suffix"
	ruleMatcherDomainPrefix   ruleMatcherKind = "domain_prefix"
	ruleMatcherDomainContains ruleMatcherKind = "domain_contains"
	ruleMatcherDomainStaticIP ruleMatcherKind = "domain_static_ip"
	ruleMatcherIP             ruleMatcherKind = "ip"
	ruleMatcherCIDR           ruleMatcherKind = "cidr"
)

type tunnelRule struct {
	RawPattern string
	Group      string
	Kind       ruleMatcherKind
	Domain     string
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
	// GroupRuntime 仅用于对外导出当前组保活快照；内部唯一运行时真相在 GroupState。
	GroupRuntime map[string]NetworkAssistantGroupKeepaliveItem
	// GroupState 是组级连接/保活/重试状态的唯一内部事实来源。
	GroupState map[string]*ruleGroupRuntimeState
}

type tunnelRouteDecision struct {
	Direct     bool
	BypassTUN  bool
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
	Direct    bool
	BypassTUN bool
	NodeID    string
	Group     string
	Expires   time.Time
	Domain    string // non-empty when this is a fake IP entry
	FakeIP    bool
}

// NetworkAssistantDNSCacheEntry 是单条 DNS/路由缓存记录，供前端展示。
type NetworkAssistantDNSCacheEntry struct {
	Domain         string `json:"domain"`
	IP             string `json:"ip"`
	FakeIP         bool   `json:"fake_ip"`
	FakeIPValue    string `json:"fake_ip_value"`
	Direct         bool   `json:"direct"`
	NodeID         string `json:"node_id"`
	Group          string `json:"group"`
	Kind           string `json:"kind"`
	Source         string `json:"source"`
	DNSCount       int    `json:"dns_count"`
	IPConnectCount int    `json:"ip_connect_count"`
	TotalCount     int    `json:"total_count"`
	ExpiresAt      string `json:"expires_at"` // RFC3339
}

type tunnelDNSResolveResponse struct {
	Addrs []string `json:"addrs"`
	TTL   int      `json:"ttl"`
	Error string   `json:"error,omitempty"`
}

type networkAssistantService struct {
	mu sync.RWMutex

	mode           string
	nodeID         string
	availableNodes []string

	controllerBaseURL string
	sessionToken      string

	tunnelStatusMessage string
	systemProxyMessage  string
	lastError           string
	tunnelOpenFailures  int
	chainTargets        map[string]probeChainEndpoint
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

	logStore        *networkAssistantLogStore
	ruleRouting     tunnelRuleRouting
	ruleDNSCache    map[string]dnsCacheEntry
	ruleDNSQuerySeq uint32
	tunUDPRelays    map[string]*localTUNUDPRelay
	dnsRouteHints   map[string]dnsRouteHintEntry
	fakeIPPool      *fakeIPPool
	internalDNS     *localInternalDNSServer
	logRateState    map[string]time.Time

	lastChainRefreshAt map[string]time.Time
	muxMaintainerStop  chan struct{}
	muxMaintainerDone  chan struct{}
	muxMaintaining     bool

	processMonitor *processMonitor
}

func ensureRuleRoutingRuntimeMaps(routing *tunnelRuleRouting) {
	if routing == nil {
		return
	}
	if routing.GroupRuntime == nil {
		routing.GroupRuntime = map[string]NetworkAssistantGroupKeepaliveItem{}
	}
	if routing.GroupState == nil {
		routing.GroupState = map[string]*ruleGroupRuntimeState{}
	}
}

func newNetworkAssistantService() *networkAssistantService {
	logStore := newNetworkAssistantLogStore()

	ruleRouting, ruleErr := loadOrCreateTunnelRuleRouting()
	if ruleErr != nil {
		logStore.Appendf(logSourceManager, "init", "failed to load rule routing config, using empty rules: %v", ruleErr)
		ruleRouting = tunnelRuleRouting{
			RuleSet:       tunnelRuleSet{Rules: []tunnelRule{}},
			GroupNodeMap:  map[string]string{},
			GroupRuntime:  map[string]NetworkAssistantGroupKeepaliveItem{},
			GroupState:    map[string]*ruleGroupRuntimeState{},
		}
	}
	ensureRuleRoutingRuntimeMaps(&ruleRouting)

	service := &networkAssistantService{
		mode:                networkModeDirect,
		nodeID:              defaultNodeID,
		availableNodes:      []string{defaultNodeID},
		tunnelStatusMessage: "未启用",
		systemProxyMessage:  "未设置",
		tunnelOpenFailures:  0,
		chainTargets:        make(map[string]probeChainEndpoint),
		tunSupported:        false,
		tunInstalled:        false,
		tunEnabled:          false,
		tunLibraryPath:      "",
		tunStatus:           "未安装",
		logStore:            logStore,
		ruleRouting:         ruleRouting,
		ruleDNSCache:        make(map[string]dnsCacheEntry),
		tunUDPRelays:        make(map[string]*localTUNUDPRelay),
		tunDynamicBypass:    make(map[string]int),
		dnsRouteHints:       make(map[string]dnsRouteHintEntry),
		logRateState:        make(map[string]time.Time),
		processMonitor:      newProcessMonitor(),
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
	if err := service.refreshAvailableNodes(); err != nil {
		logStore.Appendf(logSourceManager, "init", "load local available nodes skipped: %v", err)
	}
	if _, err := service.getOrLoadChainTargetsSnapshot(); err != nil {
		logStore.Appendf(logSourceManager, "init", "load local chain targets skipped: %v", err)
	}
	service.syncTUNInstallState()
	if err := service.startInternalDNSServer(); err != nil {
		service.setLastError(err)
		service.logf("failed to auto-start internal dns service: %v", err)
	}
	service.startMuxAutoMaintainLoop()
	return service
}

func (a *App) GetNetworkAssistantStatus() NetworkAssistantStatus {
	if a.networkAssistant == nil {
		return NetworkAssistantStatus{}
	}
	return a.networkAssistant.Status()
}

func (a *App) GetNetworkAssistantDNSUpstreamConfig() (NetworkAssistantDNSUpstreamConfig, error) {
	if a.networkAssistant == nil {
		return NetworkAssistantDNSUpstreamConfig{}, errors.New("network assistant service is not initialized")
	}
	return a.networkAssistant.GetDNSUpstreamConfig()
}

func (a *App) SetNetworkAssistantDNSUpstreamConfig(cfg NetworkAssistantDNSUpstreamConfig) error {
	if a.networkAssistant == nil {
		return errors.New("network assistant service is not initialized")
	}
	return a.networkAssistant.SetDNSUpstreamConfig(cfg)
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

func (a *App) ListNetworkAssistantProcesses() ([]NetworkProcessInfo, error) {
	if a.networkAssistant == nil {
		return nil, errors.New("network assistant service is not initialized")
	}
	return listRunningProcesses(), nil
}

func (a *App) StartNetworkAssistantProcessMonitor() error {
	if a.networkAssistant == nil {
		return errors.New("network assistant service is not initialized")
	}
	a.networkAssistant.processMonitor.Start()
	return nil
}

func (a *App) StopNetworkAssistantProcessMonitor() error {
	if a.networkAssistant == nil {
		return errors.New("network assistant service is not initialized")
	}
	a.networkAssistant.processMonitor.Stop()
	return nil
}

func (a *App) ClearNetworkAssistantProcessEvents() error {
	if a.networkAssistant == nil {
		return errors.New("network assistant service is not initialized")
	}
	a.networkAssistant.processMonitor.ClearEvents()
	return nil
}

func (a *App) QueryNetworkAssistantProcessEvents(sinceMs int64) ([]NetworkProcessEvent, error) {
	if a.networkAssistant == nil {
		return nil, errors.New("network assistant service is not initialized")
	}
	return a.networkAssistant.processMonitor.QueryEvents(sinceMs), nil
}

func (a *App) QueryNetworkAssistantDNSCache(query string) ([]NetworkAssistantDNSCacheEntry, error) {
	if a.networkAssistant == nil {
		return nil, errors.New("network assistant service is not initialized")
	}
	return a.networkAssistant.QueryDNSCache(query), nil
}

// QueryDNSCache 返回匹配 query 的 DNS 路由缓存条目。
// query 为空时返回全部；query 为 IP 时按 IP 查；query 为域名时按域名查。
func (s *networkAssistantService) QueryDNSCache(query string) []NetworkAssistantDNSCacheEntry {
	return querySplitDNSCacheEntries(s, query)
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
	if err := s.ensureControlPlaneDialReady(baseURLInput); err != nil {
		s.logfRateLimited("dns-cache-refresh:control-plane-preheat-failed", 5*time.Second, "force refresh dns cache: control-plane preheat failed: %v", err)
		return "", err
	}

	s.clearDNSCache()

	s.mu.RLock()
	effectiveBase := strings.TrimSpace(s.controllerBaseURL)
	effectiveToken := strings.TrimSpace(s.sessionToken)
	s.mu.RUnlock()

	if effectiveBase != "" && effectiveToken != "" {
		if err := s.refreshAvailableNodes(); err != nil {
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

func (s *networkAssistantService) getOrLoadChainTargetsSnapshot() (map[string]probeChainEndpoint, error) {
	targets := s.getChainTargetsSnapshot()
	if len(targets) > 0 {
		return targets, nil
	}

	cachedNodes, cachedTargets, err := loadChainCacheFromFile()
	if err != nil {
		return nil, err
	}
	if len(cachedTargets) == 0 {
		return map[string]probeChainEndpoint{}, nil
	}

	nodes := mergeAvailableNodeIDs(cachedNodes, cachedTargets)
	s.mu.Lock()
	s.chainTargets = copyProbeChainTargets(cachedTargets)
	if len(nodes) > 0 {
		s.availableNodes = nodes
		if !containsNodeID(nodes, s.nodeID) {
			s.nodeID = nodes[0]
		}
	}
	targets = copyProbeChainTargets(s.chainTargets)
	s.mu.Unlock()
	return targets, nil
}

func mergeAvailableNodeIDs(nodeIDs []string, chainTargets map[string]probeChainEndpoint) []string {
	seen := make(map[string]struct{}, len(nodeIDs)+len(chainTargets)+1)
	out := make([]string, 0, len(nodeIDs)+len(chainTargets)+1)
	add := func(raw string) {
		nodeID := strings.TrimSpace(raw)
		if nodeID == "" {
			return
		}
		if _, ok := seen[strings.ToLower(nodeID)]; ok {
			return
		}
		seen[strings.ToLower(nodeID)] = struct{}{}
		out = append(out, nodeID)
	}

	for _, nodeID := range nodeIDs {
		if _, ok := parseChainTargetNodeID(nodeID); ok {
			add(nodeID)
		}
	}
	for nodeID := range chainTargets {
		add(nodeID)
	}
	add(defaultNodeID)
	return out
}

func mustLoadProbeNodes() []ProbeNode {
	nodes, _, err := loadProbeNodes()
	if err != nil {
		return nil
	}
	return nodes
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
	s.triggerMuxAutoMaintainNow()
	return nil
}

func (s *networkAssistantService) syncAvailableNodesFromController() error {
	readWaitStartedAt := time.Now()
	s.logf("sync available nodes read lock wait begin")
	s.mu.RLock()
	s.logf("sync available nodes read lock acquired: elapsed=%s", time.Since(readWaitStartedAt))
	baseURL := strings.TrimSpace(s.controllerBaseURL)
	token := strings.TrimSpace(s.sessionToken)
	s.mu.RUnlock()

	if baseURL == "" || token == "" {
		return errors.New("controller url and session token are required")
	}

	if err := s.ensureControlPlaneDialReady(baseURL); err != nil {
		return err
	}

	chainTargets, chainNodes, _, chainErr := fetchProbeChainTargetsViaAdminWSWithNodes(baseURL, token, s.logf)
	if chainErr != nil {
		return chainErr
	}
	nodes := mergeAvailableNodeIDs(chainNodes, chainTargets)

	commitWaitStartedAt := time.Now()
	s.logf("sync available nodes commit lock wait begin")
	s.mu.Lock()
	s.logf("sync available nodes commit lock acquired: elapsed=%s", time.Since(commitWaitStartedAt))
	if chainErr == nil {
		s.chainTargets = chainTargets
	} else {
		// Keep prior chain cache on transient controller failures to avoid UI option loss.
		s.logf("refresh chain targets failed, keep cached chain targets: %v", chainErr)
		for nodeID := range s.chainTargets {
			if _, exists := chainTargets[nodeID]; !exists {
				chainTargets[nodeID] = s.chainTargets[nodeID]
			}
		}
		nodes = mergeAvailableNodeIDs(nodes, chainTargets)
	}
	s.availableNodes = nodes
	if !containsNodeID(nodes, s.nodeID) {
		s.nodeID = nodes[0]
	}
	s.mu.Unlock()
	if saveErr := saveChainCacheToFile(nodes, s.getChainTargetsSnapshot()); saveErr != nil {
		s.logf("save local chain cache failed: %v", saveErr)
	}
	return nil
}

func (s *networkAssistantService) GetProbeLinkChainsCache() ([]ProbeLinkChainCacheItem, error) {
	targets, err := s.getOrLoadChainTargetsSnapshot()
	if err != nil {
		return nil, err
	}
	nodeNameByID := loadProbeNodeNameByID()
	items := make([]ProbeLinkChainCacheItem, 0, len(targets))
	for _, endpoint := range targets {
		items = append(items, buildProbeLinkChainCacheItem(endpoint, nodeNameByID))
	}
	sort.Slice(items, func(i, j int) bool {
		leftKey := strings.TrimSpace(items[i].UpdatedAt)
		if leftKey == "" {
			leftKey = strings.TrimSpace(items[i].CreatedAt)
		}
		rightKey := strings.TrimSpace(items[j].UpdatedAt)
		if rightKey == "" {
			rightKey = strings.TrimSpace(items[j].CreatedAt)
		}
		if leftKey == rightKey {
			return strings.TrimSpace(items[i].ChainID) < strings.TrimSpace(items[j].ChainID)
		}
		return leftKey > rightKey
	})
	return items, nil
}

func buildProbeLinkChainCacheItem(endpoint probeChainEndpoint, nodeNameByID map[string]string) ProbeLinkChainCacheItem {
	hops := make([]ProbeLinkChainCacheHopConfig, 0, len(endpoint.HopConfigs))
	for _, hop := range endpoint.HopConfigs {
		hops = append(hops, ProbeLinkChainCacheHopConfig{
			NodeNo:       hop.NodeNo,
			ListenHost:   strings.TrimSpace(hop.ListenHost),
			ListenPort:   hop.ListenPort,
			ExternalPort: hop.ExternalPort,
			LinkLayer:    strings.TrimSpace(hop.LinkLayer),
			DialMode:     strings.TrimSpace(hop.DialMode),
			RelayHost:    strings.TrimSpace(hop.RelayHost),
		})
	}
	portForwards := make([]ProbeLinkChainCachePortForward, 0, len(endpoint.PortForwards))
	for _, pf := range endpoint.PortForwards {
		portForwards = append(portForwards, ProbeLinkChainCachePortForward{
			ID:         strings.TrimSpace(pf.ID),
			Name:       strings.TrimSpace(pf.Name),
			EntrySide:  strings.TrimSpace(pf.EntrySide),
			ListenHost: strings.TrimSpace(pf.ListenHost),
			ListenPort: pf.ListenPort,
			TargetHost: strings.TrimSpace(pf.TargetHost),
			TargetPort: pf.TargetPort,
			Network:    strings.TrimSpace(pf.Network),
			Enabled:    pf.Enabled,
		})
	}
	return ProbeLinkChainCacheItem{
		ChainID:        strings.TrimSpace(endpoint.ChainID),
		Name:           strings.TrimSpace(endpoint.ChainName),
		UserID:         strings.TrimSpace(endpoint.UserID),
		UserPublicKey:  strings.TrimSpace(endpoint.UserPublicKey),
		Secret:         strings.TrimSpace(endpoint.ChainSecret),
		EntryNodeID:    strings.TrimSpace(endpoint.EntryNode),
		ExitNodeID:     strings.TrimSpace(endpoint.ExitNode),
		CascadeNodeIDs: append([]string(nil), endpoint.CascadeNodeIDs...),
		NodeNameByID:   buildProbeChainNodeNameMap(endpoint, nodeNameByID),
		ListenHost:     strings.TrimSpace(endpoint.ListenHost),
		ListenPort:     endpoint.ListenPort,
		LinkLayer:      strings.TrimSpace(endpoint.LinkLayer),
		HopConfigs:     hops,
		PortForwards:   portForwards,
		EgressHost:     strings.TrimSpace(endpoint.EgressHost),
		EgressPort:     endpoint.EgressPort,
		CreatedAt:      strings.TrimSpace(endpoint.CreatedAt),
		UpdatedAt:      strings.TrimSpace(endpoint.UpdatedAt),
	}
}

func loadProbeNodeNameByID() map[string]string {
	nodes, _, err := loadProbeNodes()
	if err != nil || len(nodes) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(nodes))
	for _, item := range nodes {
		if item.NodeNo <= 0 {
			continue
		}
		name := strings.TrimSpace(item.NodeName)
		if name == "" {
			continue
		}
		out[strconv.Itoa(item.NodeNo)] = name
	}
	return out
}

func buildProbeChainNodeNameMap(endpoint probeChainEndpoint, allNodeNameByID map[string]string) map[string]string {
	out := make(map[string]string)
	add := func(raw string) {
		nodeID := strings.TrimSpace(raw)
		if nodeID == "" {
			return
		}
		name := strings.TrimSpace(allNodeNameByID[nodeID])
		if name == "" {
			return
		}
		out[nodeID] = name
	}
	add(endpoint.EntryNode)
	add(endpoint.ExitNode)
	for _, nodeID := range endpoint.CascadeNodeIDs {
		add(nodeID)
	}
	for _, hop := range endpoint.HopConfigs {
		if hop.NodeNo <= 0 {
			continue
		}
		add(strconv.Itoa(hop.NodeNo))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *networkAssistantService) Status() NetworkAssistantStatus {
	s.syncTUNInstallState()
	s.mu.RLock()
	mode := s.mode
	nodeID := s.nodeID
	availableNodes := append([]string(nil), s.availableNodes...)
	tunnelStatusMessage := s.tunnelStatusMessage
	systemProxyMessage := s.systemProxyMessage
	lastError := s.lastError
	tunSupported := s.tunSupported
	tunInstalled := s.tunInstalled
	tunEnabled := s.tunEnabled
	tunLibraryPath := s.tunLibraryPath
	tunStatus := s.tunStatus
	muxReconnects := s.muxReconnects
	ruleRouting := s.ruleRouting
	s.mu.RUnlock()

	groupRuntime := s.buildGroupKeepaliveSnapshot()
	muxConnected, muxActiveStreams, muxLastRecv, muxLastPong := summarizeGroupKeepaliveSnapshot(groupRuntime)
	groupKeepalive := orderedGroupKeepaliveItems(ruleRouting, groupRuntime)

	return NetworkAssistantStatus{
		Enabled:           mode == networkModeTUN,
		Mode:              mode,
		NodeID:            nodeID,
		AvailableNodes:    availableNodes,
		Socks5Listen:      "",
		TunnelRoute:       buildNetworkAssistantTunnelRoute(nodeID),
		TunnelStatus:      tunnelStatusMessage,
		SystemProxyStatus: systemProxyMessage,
		LastError:         lastError,
		MuxConnected:      muxConnected,
		MuxActiveStreams:  muxActiveStreams,
		MuxReconnects:     muxReconnects,
		MuxLastRecv:       muxLastRecv,
		MuxLastPong:       muxLastPong,
		GroupKeepalive:    groupKeepalive,
		TUNSupported:      tunSupported,
		TUNInstalled:      tunInstalled,
		TUNEnabled:        tunEnabled,
		TUNLibraryPath:    tunLibraryPath,
		TUNStatus:         tunStatus,
	}
}


func copyGroupKeepaliveSnapshot(source map[string]NetworkAssistantGroupKeepaliveItem) map[string]NetworkAssistantGroupKeepaliveItem {
	if len(source) == 0 {
		return map[string]NetworkAssistantGroupKeepaliveItem{}
	}
	target := make(map[string]NetworkAssistantGroupKeepaliveItem, len(source))
	for key, item := range source {
		target[key] = item
	}
	return target
}

func indexGroupKeepaliveSnapshotFromState(state map[string]*ruleGroupRuntimeState) map[string]NetworkAssistantGroupKeepaliveItem {
	if len(state) == 0 {
		return map[string]NetworkAssistantGroupKeepaliveItem{}
	}
	indexed := make(map[string]NetworkAssistantGroupKeepaliveItem, len(state))
	for rawGroup, runtimeState := range state {
		if runtimeState == nil {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(rawGroup))
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(runtimeState.ResolvedGroup))
		}
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(runtimeState.Snapshot.Group))
		}
		if key == "" {
			continue
		}
		snapshot := runtimeState.Snapshot
		if strings.TrimSpace(snapshot.Group) == "" {
			snapshot.Group = strings.TrimSpace(runtimeState.ResolvedGroup)
		}
		indexed[key] = snapshot
	}
	return indexed
}

func orderedGroupKeepaliveItems(routing tunnelRuleRouting, groupRuntime map[string]NetworkAssistantGroupKeepaliveItem) []NetworkAssistantGroupKeepaliveItem {
	groups := extractRuleGroupsFromRuleSet(routing.RuleSet)
	groups = append(groups, ruleFallbackGroupKey)
	items := make([]NetworkAssistantGroupKeepaliveItem, 0, len(groups))
	for _, group := range groups {
		key := strings.ToLower(strings.TrimSpace(group))
		if key == "" {
			continue
		}
		if item, ok := groupRuntime[key]; ok {
			items = append(items, item)
		}
	}
	return items
}

func (s *networkAssistantService) buildGroupKeepaliveSnapshot() map[string]NetworkAssistantGroupKeepaliveItem {
	if s == nil {
		return map[string]NetworkAssistantGroupKeepaliveItem{}
	}

	s.mu.RLock()
	stateIndexed := indexGroupKeepaliveSnapshotFromState(s.ruleRouting.GroupState)
	s.mu.RUnlock()

	s.mu.Lock()
	ensureRuleRoutingRuntimeMaps(&s.ruleRouting)
	s.ruleRouting.GroupRuntime = copyGroupKeepaliveSnapshot(stateIndexed)
	s.mu.Unlock()
	return copyGroupKeepaliveSnapshot(stateIndexed)
}

func summarizeGroupKeepaliveSnapshot(groupRuntime map[string]NetworkAssistantGroupKeepaliveItem) (bool, int, string, string) {
	muxConnected := false
	muxActiveStreams := 0
	muxLastRecv := ""
	muxLastPong := ""
	for _, item := range groupRuntime {
		if item.Connected {
			muxConnected = true
		}
		muxActiveStreams += item.ActiveStreams
		lastRecv := strings.TrimSpace(item.LastRecv)
		lastPong := strings.TrimSpace(item.LastPong)
		if lastRecv > muxLastRecv {
			muxLastRecv = lastRecv
		}
		if lastPong > muxLastPong {
			muxLastPong = lastPong
		}
	}
	return muxConnected, muxActiveStreams, muxLastRecv, muxLastPong
}

func (s *networkAssistantService) ApplyMode(controllerBaseURL, sessionToken, mode, nodeID string) error {
	normalizedMode := strings.ToLower(strings.TrimSpace(mode))
	if normalizedMode == "" {
		normalizedMode = networkModeDirect
	}
	if normalizedMode == networkModeTUN {
		return s.EnableTUN()
	}
	if normalizedMode != networkModeDirect && normalizedMode != networkModeTUN {
		return fmt.Errorf("unsupported mode: %s", mode)
	}

	normalizedNode := strings.TrimSpace(nodeID)
	if normalizedNode == "" {
		normalizedNode = defaultNodeID
	}
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
		if err := s.refreshAvailableNodes(); err != nil {
			s.logf("refresh available nodes failed: %v", err)
			s.setLastError(err)
			return err
		}
	}

	s.mu.Lock()
	isValidSelectedNode := strings.EqualFold(normalizedNode, defaultNodeID)
	if !isValidSelectedNode {
		_, isValidSelectedNode = parseChainTargetNodeID(normalizedNode)
	}
	availableNodesSnapshot := append([]string(nil), s.availableNodes...)
	previousNodeID := strings.TrimSpace(s.nodeID)
	if !isValidSelectedNode || !containsNodeID(s.availableNodes, normalizedNode) {
		s.mu.Unlock()
		s.logf("apply mode rejected selected node: requested=%s previous=%s available=%v", normalizedNode, previousNodeID, availableNodesSnapshot)
		err := fmt.Errorf("selected node is unavailable: %s", normalizedNode)
		s.setLastError(err)
		return err
	}
	s.nodeID = normalizedNode
	s.mu.Unlock()
	s.triggerMuxAutoMaintainNow()

	if normalizedMode == networkModeDirect {
		errStopTUN := s.stopLocalTUNDataPlane()
		errTunRouting := s.clearTUNSystemRouting()
		errDirectProxy := applyDirectSystemProxy()
		if err := errors.Join(errStopTUN, errTunRouting, errDirectProxy); err != nil {
			s.logf("failed to switch to direct mode: %v", err)
			s.setLastError(err)
			return err
		}
		s.forceRefreshDNSOnModeSwitch("after_switch_direct")
		s.mu.Lock()
		s.mode = networkModeDirect
		s.tunnelStatusMessage = "直连模式"
		s.systemProxyMessage = "已清除系统代理并恢复系统 DNS（直连）"
		s.tunnelOpenFailures = 0
		s.tunEnabled = false
		s.tunStatus = tunStatusAfterDisable(s.tunSupported, s.tunInstalled)
		s.mu.Unlock()
		if wasUsingTUNCapture {
		}
		return nil
	}

	return fmt.Errorf("unsupported mode: %s", mode)
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
	s.stopMuxAutoMaintainLoop()
	errStopTUN := s.stopLocalTUNDataPlane()
	errTunRouting := s.clearTUNSystemRouting()
	errStopDNS := s.stopInternalDNSServer()
	errStopMux := s.stopTunnelMuxClients()
	errDirect := applyDirectSystemProxy()

	s.mu.Lock()
	tunAdapterHandle := s.tunAdapterHandle
	tunLibraryPath := s.tunLibraryPath
	s.mode = networkModeDirect
	s.tunnelStatusMessage = "直连模式"
	s.systemProxyMessage = "已恢复为直连（系统 DNS 已恢复）"
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
	if errStopMux != nil {
		s.logf("shutdown mux cleanup returned error: %v", errStopMux)
	}
	if errStopTUN != nil {
		s.logf("shutdown tun dataplane cleanup returned error: %v", errStopTUN)
	}
	if errStopDNS != nil {
		s.logf("shutdown internal dns cleanup returned error: %v", errStopDNS)
	}
	return errors.Join(errStopTUN, errTunRouting, errStopDNS, errStopMux, errDirect, errCloseAdapter)
}
func (s *networkAssistantService) stopTunnelMuxClients() error {
	s.mu.Lock()
	groupMuxClients := make([]*tunnelMuxClient, 0, len(s.ruleRouting.GroupState))
	for _, state := range s.ruleRouting.GroupState {
		if state == nil || state.Client == nil {
			continue
		}
		groupMuxClients = append(groupMuxClients, state.Client)
		state.Client = nil
		state.RetryAt = time.Time{}
		state.FailureCount = 0
		state.LastError = ""
		snapshot := state.Snapshot
		if strings.TrimSpace(snapshot.Status) != "" {
			snapshot.Status = "未建立"
		}
		snapshot.Connected = false
		snapshot.ActiveStreams = 0
		snapshot.LastRecv = ""
		snapshot.LastPong = ""
		state.Snapshot = snapshot
	}
	ensureRuleRoutingRuntimeMaps(&s.ruleRouting)
	s.ruleRouting.GroupRuntime = indexGroupKeepaliveSnapshotFromState(s.ruleRouting.GroupState)
	s.mu.Unlock()

	closed := make(map[*tunnelMuxClient]struct{}, len(groupMuxClients))
	for _, client := range groupMuxClients {
		if client == nil {
			continue
		}
		if _, ok := closed[client]; ok {
			continue
		}
		closed[client] = struct{}{}
		client.close()
	}
	return nil
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
		return tunnelRouteDecision{Direct: true, BypassTUN: false, TargetAddr: normalizedTarget, NodeID: ""}, nil
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
			resolvedNodeID := hintNodeID
			if hint.Direct {
				resolvedNodeID = ""
			}
			return tunnelRouteDecision{
				Direct:     hint.Direct,
				BypassTUN:  hint.BypassTUN,
				TargetAddr: net.JoinHostPort(canonicalIP(parsedIP), port),
				NodeID:     resolvedNodeID,
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
			return tunnelRouteDecision{Direct: true, BypassTUN: true, TargetAddr: normalizedTarget, NodeID: "", Group: group}, nil
		case rulePolicyActionReject:
			return tunnelRouteDecision{}, &ruleRouteRejectError{Group: group}
		default:
			if isDirectRuleGroupKey(group) {
				return tunnelRouteDecision{Direct: true, BypassTUN: true, TargetAddr: normalizedTarget, NodeID: "", Group: group}, nil
			}
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

	if s.isControlPlaneHost(host) {
		return tunnelRouteDecision{Direct: true, BypassTUN: true, TargetAddr: normalizedTarget, NodeID: "", Group: "direct"}, nil
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
			Group:      ruleFallbackGroupKey,
		}, nil
	default:
		return tunnelRouteDecision{Direct: true, BypassTUN: true, TargetAddr: normalizedTarget, NodeID: "", Group: ruleFallbackGroupKey}, nil
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
	if targetIP != nil {
		targetIPCanonical := canonicalIP(targetIP)
		for _, rule := range set.Rules {
			switch rule.Kind {
			case ruleMatcherIP:
				if targetIPCanonical == rule.IP {
					return rule, true
				}
			case ruleMatcherCIDR:
				if rule.CIDR != nil && rule.CIDR.Contains(targetIP) {
					return rule, true
				}
			}
		}
		return tunnelRule{}, false
	}

	domainPriorities := []ruleMatcherKind{
		ruleMatcherDomainExact,
		ruleMatcherDomainSuffix,
		ruleMatcherDomainPrefix,
		ruleMatcherDomainContains,
	}
	for _, kind := range domainPriorities {
		for _, rule := range set.Rules {
			if rule.Kind != kind {
				continue
			}
			if domainMatchesRule(targetHostLower, rule) {
				return rule, true
			}
		}
	}
	return tunnelRule{}, false
}

func (set tunnelRuleSet) matchStaticDomainIP(domain string, qType uint16) ([]string, bool) {
	normalizedDomain := normalizeRuleDomain(domain)
	if normalizedDomain == "" {
		return nil, false
	}
	for _, rule := range set.Rules {
		if rule.Kind != ruleMatcherDomainStaticIP {
			continue
		}
		if normalizedDomain != domainPatternFromRule(rule) {
			continue
		}
		addrs := filterDNSResponseAddrs([]string{rule.IP}, qType)
		if len(addrs) == 0 {
			continue
		}
		return addrs, true
	}
	return nil, false
}

func domainMatchesRule(host string, rule tunnelRule) bool {
	normalizedHost := strings.TrimSpace(strings.ToLower(host))
	normalizedDomain := domainPatternFromRule(rule)
	if normalizedHost == "" || normalizedDomain == "" {
		return false
	}
	switch rule.Kind {
	case ruleMatcherDomainExact:
		return normalizedHost == normalizedDomain
	case ruleMatcherDomainSuffix:
		if normalizedHost == normalizedDomain {
			return true
		}
		return strings.HasSuffix(normalizedHost, "."+normalizedDomain)
	case ruleMatcherDomainPrefix:
		return strings.HasPrefix(normalizedHost, normalizedDomain)
	case ruleMatcherDomainContains:
		return strings.Contains(normalizedHost, normalizedDomain)
	default:
		return false
	}
}

func domainPatternFromRule(rule tunnelRule) string {
	if normalized := normalizeRuleDomain(rule.Domain); normalized != "" {
		return normalized
	}
	return normalizeRuleDomain(rule.Suffix)
}

func normalizeRuleGroupName(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
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
	_ = ttlSeconds
	return dnsSharedTTLSeconds
}

func (s *networkAssistantService) loadRuleDNSCache(cacheKey string) ([]string, bool) {
	normalizedKey := strings.ToLower(strings.TrimSpace(cacheKey))
	if normalizedKey == "" {
		return nil, false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ruleDNSCache == nil {
		s.ruleDNSCache = make(map[string]dnsCacheEntry)
	}
	entry, ok := s.ruleDNSCache[normalizedKey]
	if !ok {
		return nil, false
	}
	if now.After(entry.Expires) {
		delete(s.ruleDNSCache, normalizedKey)
		return nil, false
	}
	if len(entry.Addrs) == 0 {
		return nil, false
	}
	return append([]string(nil), entry.Addrs...), true
}

func (s *networkAssistantService) storeRuleDNSCache(cacheKey string, addresses []string, ttlSeconds int) {
	normalizedKey := strings.ToLower(strings.TrimSpace(cacheKey))
	normalizedAddrs := normalizeDNSCacheIPs(addresses)
	if normalizedKey == "" || len(normalizedAddrs) == 0 {
		return
	}
	expires := time.Now().Add(time.Duration(clampRuleDNSTTL(ttlSeconds)) * time.Second)
	s.mu.Lock()
	if s.ruleDNSCache == nil {
		s.ruleDNSCache = make(map[string]dnsCacheEntry)
	}
	s.ruleDNSCache[normalizedKey] = dnsCacheEntry{Addrs: normalizedAddrs, Expires: expires}
	s.mu.Unlock()
}


func (s *networkAssistantService) queryRuleDomainViaTunnelGroup(group string, domain string, qType uint16) ([]string, int, error) {
	normalizedDomain := normalizeRuleDomain(domain)
	if normalizedDomain == "" {
		return nil, 0, errors.New("invalid domain")
	}
	normalizedGroup := strings.TrimSpace(group)
	if normalizedGroup == "" {
		return nil, 0, errors.New("group is required")
	}

	queryID := uint16(atomic.AddUint32(&s.ruleDNSQuerySeq, 1))
	packet, err := buildDNSQueryPacket(normalizedDomain, qType, queryID)
	if err != nil {
		return nil, 0, err
	}
	config, configErr := getDNSUpstreamConfig()
	if configErr != nil {
		s.logfRateLimited("dns-upstream-config-load", 30*time.Second, "load dns upstream config failed, fallback to defaults: %v", configErr)
	}

	deadline := time.Now().Add(ruleDNSResolveTimeout)
	var lastErr error
	tryParseResponse := func(payload []byte) ([]string, int, error) {
		addrs, ttlSeconds, parseErr := parseDNSResponseAddrs(payload, queryID, qType)
		if parseErr != nil {
			return nil, 0, parseErr
		}
		if len(addrs) == 0 {
			return nil, 0, errors.New("dns resolve returned empty result")
		}
		return addrs, ttlSeconds, nil
	}
	queryByDoH := func() ([]string, int, bool) {
		trials := 0
		for _, server := range config.TUN.DoHServers {
			if trials >= ruleDNSResolveServerTrials {
				break
			}
			trials++
			remaining := time.Until(deadline)
			if remaining <= 0 {
				lastErr = errors.New("dns resolve timeout")
				return nil, 0, false
			}
			payload, queryErr := s.queryRawDNSPacketViaTunnelDoHProxy(server, packet, remaining, normalizedGroup)
			if queryErr != nil {
				lastErr = queryErr
				continue
			}
			addrs, ttlSeconds, parseErr := tryParseResponse(payload)
			if parseErr != nil {
				lastErr = parseErr
				continue
			}
			return addrs, ttlSeconds, true
		}
		return nil, 0, false
	}
	queryByDoT := func() ([]string, int, bool) {
		trials := 0
		for _, server := range config.TUN.DoTServers {
			if trials >= ruleDNSResolveServerTrials {
				break
			}
			trials++
			remaining := time.Until(deadline)
			if remaining <= 0 {
				lastErr = errors.New("dns resolve timeout")
				return nil, 0, false
			}
			payload, queryErr := s.queryRawDNSPacketViaTunnelDoT(server, packet, remaining, normalizedGroup)
			if queryErr != nil {
				lastErr = queryErr
				continue
			}
			addrs, ttlSeconds, parseErr := tryParseResponse(payload)
			if parseErr != nil {
				lastErr = parseErr
				continue
			}
			return addrs, ttlSeconds, true
		}
		return nil, 0, false
	}
	queryByPlainDNS := func() ([]string, int, bool) {
		trials := 0
		for _, server := range config.TUN.DNSServers {
			if trials >= ruleDNSResolveServerTrials {
				break
			}
			trials++
			remaining := time.Until(deadline)
			if remaining <= 0 {
				lastErr = errors.New("dns resolve timeout")
				return nil, 0, false
			}
			payload, queryErr := s.queryRawDNSPacketViaTunnelUDP(server, packet, remaining, normalizedGroup)
			if queryErr != nil {
				lastErr = queryErr
				continue
			}
			addrs, ttlSeconds, parseErr := tryParseResponse(payload)
			if parseErr != nil {
				lastErr = parseErr
				continue
			}
			return addrs, ttlSeconds, true
		}
		return nil, 0, false
	}
	for _, tier := range buildDNSUpstreamQueryOrder(config.TUN.Prefer) {
		switch tier {
		case "doh":
			if addrs, ttlSeconds, ok := queryByDoH(); ok {
				return addrs, ttlSeconds, nil
			}
		case "dot":
			if addrs, ttlSeconds, ok := queryByDoT(); ok {
				return addrs, ttlSeconds, nil
			}
		default:
			if addrs, ttlSeconds, ok := queryByPlainDNS(); ok {
				return addrs, ttlSeconds, nil
			}
		}
	}
	if lastErr != nil {
		return nil, 0, lastErr
	}
	return nil, 0, errors.New("dns resolve failed")
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
		minTTL = dnsSharedTTLSeconds
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

func dialUDPWithRetry(network string, laddr, raddr *net.UDPAddr) (*net.UDPConn, error) {
	attempts := udpDialRetryMaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	backoff := udpDialRetryInitialBackoff
	if backoff <= 0 {
		backoff = 20 * time.Millisecond
	}
	maxBackoff := udpDialRetryMaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 300 * time.Millisecond
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		conn, err := net.DialUDP(network, laddr, raddr)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if !isRetryableUDPSocketErr(err) || attempt == attempts {
			return nil, err
		}
		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
	return nil, lastErr
}

func isRetryableUDPSocketErr(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		err = opErr.Err
	}

	var sysErr *os.SyscallError
	if errors.As(err, &sysErr) {
		err = sysErr.Err
	}

	errText := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(errText, "queue was full") ||
		strings.Contains(errText, "no buffer") ||
		strings.Contains(errText, "sufficient buffer space") ||
		strings.Contains(errText, "no space left") ||
		strings.Contains(errText, "wsaenobufs")
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

	if failures < maxTunnelFailures || mode != networkModeTUN {
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

func (s *networkAssistantService) logRuntimeNodeState(reason string) {
	s.mu.RLock()
	mode := strings.TrimSpace(s.mode)
	selectedNodeID := strings.TrimSpace(s.nodeID)
	availableNodes := append([]string(nil), s.availableNodes...)
	targetCount := len(s.chainTargets)
	controllerConfigured := strings.TrimSpace(s.controllerBaseURL) != ""
	tokenConfigured := strings.TrimSpace(s.sessionToken) != ""
	groupStates := make([]string, 0, len(s.ruleRouting.GroupState))
	for rawGroup, state := range s.ruleRouting.GroupState {
		if state == nil {
			continue
		}
		groupName := strings.TrimSpace(state.ResolvedGroup)
		if groupName == "" {
			groupName = strings.TrimSpace(rawGroup)
		}
		groupStates = append(
			groupStates,
			fmt.Sprintf(
				"%s(action=%s,node=%s,status=%s,connected=%v,retry_at=%s,failures=%d)",
				groupName,
				strings.TrimSpace(state.PolicyAction),
				strings.TrimSpace(state.PolicyNodeID),
				strings.TrimSpace(state.Snapshot.Status),
				state.Snapshot.Connected,
				strings.TrimSpace(state.RetryAt.Format(time.RFC3339)),
				state.FailureCount,
			),
		)
	}
	s.mu.RUnlock()
	if selectedNodeID == "" {
		selectedNodeID = defaultNodeID
	}
	s.logfRateLimited(
		"runtime-node-state:"+strings.ToLower(strings.TrimSpace(reason)),
		10*time.Second,
		"runtime node state: reason=%s mode=%s selected=%s available=%v chain_targets=%d controller=%v token=%v group_state=%v",
		reason,
		mode,
		selectedNodeID,
		availableNodes,
		targetCount,
		controllerConfigured,
		tokenConfigured,
		groupStates,
	)
}

func (s *networkAssistantService) refreshAvailableNodes() error {
	cachedNodes, cachedTargets, cacheErr := loadChainCacheFromFile()
	if cacheErr == nil && (len(cachedNodes) > 0 || len(cachedTargets) > 0) {
		nodes := mergeAvailableNodeIDs(cachedNodes, cachedTargets)
		s.mu.Lock()
		s.availableNodes = nodes
		s.chainTargets = copyProbeChainTargets(cachedTargets)
		if !containsNodeID(nodes, s.nodeID) {
			s.nodeID = nodes[0]
		}
		s.mu.Unlock()
		return nil
	}
	if cacheErr != nil {
		s.logf("load local chain cache failed: %v", cacheErr)
	}
	return errors.New("local chain cache is empty, please click from-controller fetch")
}

func fetchTunnelNodesViaAdminWS(baseURL, token string) (tunnelNodesResponse, error) {
	wsURL, err := buildAdminWSURL(baseURL)
	if err != nil {
		return tunnelNodesResponse{}, err
	}

	dialer := buildControllerWSDialer(baseURL)
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

type controllerPreferredDialTarget struct {
	Enabled       bool
	PreferredIP   string
	Address       string
	TLSServerName string
}

func resolvePreferredControllerDialTarget(baseURL string) (controllerPreferredDialTarget, error) {
	config, _, err := loadManagerGlobalConfig()
	if err != nil {
		return controllerPreferredDialTarget{}, err
	}
	return resolveControllerPreferredDialTargetForConfig(baseURL, config.ControllerIP)
}

func resolveControllerPreferredDialTargetForConfig(baseURL, configuredIP string) (controllerPreferredDialTarget, error) {
	parsed, err := parseControllerBaseURLForDial(baseURL)
	if err != nil {
		return controllerPreferredDialTarget{}, err
	}
	preferredIP := sanitizeControllerIP(configuredIP)
	if preferredIP == "" {
		return controllerPreferredDialTarget{}, nil
	}
	originalHost := strings.TrimSpace(parsed.Hostname())
	if originalHost == "" || net.ParseIP(originalHost) != nil {
		return controllerPreferredDialTarget{}, nil
	}
	port := strings.TrimSpace(parsed.Port())
	if port == "" {
		port = controllerDefaultPortForScheme(parsed.Scheme)
	}
	if port == "" {
		port = "80"
	}
	target := controllerPreferredDialTarget{
		Enabled:     true,
		PreferredIP: preferredIP,
		Address:     net.JoinHostPort(preferredIP, port),
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Scheme)) {
	case "https", "wss":
		target.TLSServerName = originalHost
	}
	return target, nil
}

func parseControllerBaseURLForDial(rawBaseURL string) (*url.URL, error) {
	value := strings.TrimSpace(rawBaseURL)
	if value == "" {
		return nil, errors.New("controller base url is required")
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || strings.TrimSpace(parsed.Host) == "" {
		return nil, errors.New("invalid controller base url")
	}
	return parsed, nil
}

func controllerDefaultPortForScheme(rawScheme string) string {
	switch strings.ToLower(strings.TrimSpace(rawScheme)) {
	case "https", "wss":
		return "443"
	case "http", "ws":
		return "80"
	default:
		return ""
	}
}

func cloneDefaultHTTPTransport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		return base.Clone()
	}
	return &http.Transport{}
}

func cloneDefaultWSDialer() websocket.Dialer {
	if websocket.DefaultDialer == nil {
		return websocket.Dialer{}
	}
	cloned := *websocket.DefaultDialer
	if cloned.TLSClientConfig != nil {
		cloned.TLSClientConfig = cloned.TLSClientConfig.Clone()
	}
	return cloned
}

func buildControllerHTTPTransport(baseURL string) *http.Transport {
	transport := cloneDefaultHTTPTransport()
	target, err := resolvePreferredControllerDialTarget(baseURL)
	if err != nil || !target.Enabled || strings.TrimSpace(target.Address) == "" {
		return transport
	}
	netDialer := &net.Dialer{KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return netDialer.DialContext(ctx, network, target.Address)
	}
	if target.TLSServerName != "" {
		cfg := transport.TLSClientConfig
		if cfg != nil {
			cfg = cfg.Clone()
		} else {
			cfg = &tls.Config{}
		}
		cfg.ServerName = target.TLSServerName
		transport.TLSClientConfig = cfg
	}
	return transport
}

func buildControllerHTTPClient(baseURL string) *http.Client {
	return &http.Client{Transport: buildControllerHTTPTransport(baseURL)}
}

// buildControllerWSDialer 构造用于连接主控的 WebSocket Dialer。
func buildControllerWSDialer(baseURL string) websocket.Dialer {
	dialer := cloneDefaultWSDialer()
	dialer.HandshakeTimeout = 10 * time.Second
	target, err := resolvePreferredControllerDialTarget(baseURL)
	if err != nil || !target.Enabled || strings.TrimSpace(target.Address) == "" {
		return dialer
	}
	netDialer := &net.Dialer{KeepAlive: 30 * time.Second}
	dialer.NetDialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return netDialer.DialContext(ctx, network, target.Address)
	}
	if target.TLSServerName != "" {
		cfg := dialer.TLSClientConfig
		if cfg != nil {
			cfg = cfg.Clone()
		} else {
			cfg = &tls.Config{}
		}
		cfg.ServerName = target.TLSServerName
		dialer.TLSClientConfig = cfg
	}
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
	business := normalizeHostValue(node.BusinessDDNS)
	var candidates []string
	if business != "" && isDomainHostValue(business) && !isLikelyAPIDomainHostValue(business) {
		candidates = append(candidates, "api."+business)
	}
	// If DDNS is a plain domain (not api.*), construct api.<ddns> as the preferred relay host.
	if ddns != "" && isDomainHostValue(ddns) && !isLikelyAPIDomainHostValue(ddns) {
		candidates = append(candidates, "api."+ddns)
	}
	candidates = append(candidates,
		business,
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

func fetchProbeChainTargetsViaAdminWS(baseURL, token string, warnf func(string, ...any)) (map[string]probeChainEndpoint, []string, error) {
	targets, ids, _, err := fetchProbeChainTargetsViaAdminWSWithNodes(baseURL, token, warnf)
	return targets, ids, err
}

func normalizeBusinessDomainFromRelayHost(relayHost string) string {
	host := strings.ToLower(normalizeHostValue(relayHost))
	if !isDomainHostValue(host) {
		return ""
	}
	if strings.HasPrefix(host, "api.") {
		host = strings.TrimSpace(host[len("api."):])
	}
	if !isDomainHostValue(host) {
		return ""
	}
	return host
}

func buildBusinessDomainByNodeIDFromCloudflareRecords(records []cloudflareDDNSRecordItem) map[string]string {
	out := make(map[string]string)
	for _, rec := range records {
		if rec.NodeNo <= 0 {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(rec.RecordClass), "business") {
			continue
		}
		nodeID := strconv.Itoa(rec.NodeNo)
		if strings.TrimSpace(out[nodeID]) != "" {
			continue
		}
		recordName := strings.TrimSpace(strings.ToLower(rec.RecordName))
		if strings.HasPrefix(recordName, "api.") {
			recordName = strings.TrimSpace(recordName[len("api."):])
		}
		domain := normalizeHostValue(recordName)
		if !isDomainHostValue(domain) {
			continue
		}
		out[nodeID] = domain
	}
	return out
}

func backfillProbeNodeDomainsFromChains(nodes []probeNodeAdminItem, businessDomainByNodeID map[string]string, chains []probeLinkChainAdminItem) []probeNodeAdminItem {
	if len(nodes) == 0 {
		return nodes
	}
	relayDomainByNodeID := make(map[string]string)
	for _, chain := range chains {
		for _, hop := range chain.HopConfigs {
			if hop.NodeNo <= 0 {
				continue
			}
			nodeID := strconv.Itoa(hop.NodeNo)
			if strings.TrimSpace(nodeID) == "" {
				continue
			}
			domain := normalizeBusinessDomainFromRelayHost(hop.RelayHost)
			if domain == "" {
				continue
			}
			if strings.TrimSpace(relayDomainByNodeID[nodeID]) == "" {
				relayDomainByNodeID[nodeID] = domain
			}
		}
	}

	out := append([]probeNodeAdminItem(nil), nodes...)
	for i := range out {
		if out[i].NodeNo <= 0 {
			continue
		}
		nodeID := strconv.Itoa(out[i].NodeNo)
		domain := strings.TrimSpace(businessDomainByNodeID[nodeID])
		if domain == "" {
			domain = strings.TrimSpace(relayDomainByNodeID[nodeID])
		}
		if domain == "" {
			continue
		}
		if strings.TrimSpace(out[i].BusinessDDNS) == "" {
			out[i].BusinessDDNS = domain
		}
		if strings.TrimSpace(out[i].DDNS) == "" {
			out[i].DDNS = domain
		}
	}
	return out
}

func buildProbeChainHopConfigsForManager(chain probeLinkChainAdminItem) []probeChainHopConfig {
	if len(chain.HopConfigs) == 0 {
		return nil
	}
	out := make([]probeChainHopConfig, 0, len(chain.HopConfigs))
	for _, item := range chain.HopConfigs {
		out = append(out, probeChainHopConfig{
			NodeNo:       item.NodeNo,
			ListenHost:   strings.TrimSpace(item.ListenHost),
			ListenPort:   item.ListenPort,
			ExternalPort: item.ExternalPort,
			LinkLayer:    normalizeChainLinkLayerValue(item.LinkLayer),
			DialMode:     strings.TrimSpace(item.DialMode),
			RelayHost:    strings.TrimSpace(item.RelayHost),
		})
	}
	return out
}

func buildProbeChainPortForwardsForManager(chain probeLinkChainAdminItem) []probeChainPortForward {
	if len(chain.PortForwards) == 0 {
		return nil
	}
	out := make([]probeChainPortForward, 0, len(chain.PortForwards))
	for _, item := range chain.PortForwards {
		out = append(out, probeChainPortForward{
			ID:         strings.TrimSpace(item.ID),
			Name:       strings.TrimSpace(item.Name),
			EntrySide:  strings.TrimSpace(item.EntrySide),
			ListenHost: strings.TrimSpace(item.ListenHost),
			ListenPort: item.ListenPort,
			TargetHost: strings.TrimSpace(item.TargetHost),
			TargetPort: item.TargetPort,
			Network:    strings.TrimSpace(item.Network),
			Enabled:    item.Enabled,
		})
	}
	return out
}

func fetchProbeChainTargetsViaAdminWSWithNodes(baseURL, token string, warnf func(string, ...any)) (map[string]probeChainEndpoint, []string, []probeNodeAdminItem, error) {
	wsURL, err := buildAdminWSURL(baseURL)
	if err != nil {
		return map[string]probeChainEndpoint{}, nil, nil, err
	}

	dialer := buildControllerWSDialer(baseURL)
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
	nodesPayload.Nodes = backfillProbeNodeDomainsFromChains(nodesPayload.Nodes, map[string]string{}, chainsPayload.Items)

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
		targets[targetID] = probeChainEndpoint{
			TargetID:       targetID,
			ChainName:      strings.TrimSpace(chain.Name),
			ChainID:        chainID,
			UserID:         strings.TrimSpace(chain.UserID),
			UserPublicKey:  strings.TrimSpace(chain.UserPublicKey),
			EntryNode:      entryNodeID,
			ExitNode:       normalizeProbeNodeIDValue(chain.ExitNodeID),
			CascadeNodeIDs: append([]string(nil), chain.CascadeNodeIDs...),
			ListenHost:     strings.TrimSpace(chain.ListenHost),
			ListenPort:     chain.ListenPort,
			EgressHost:     strings.TrimSpace(chain.EgressHost),
			EgressPort:     chain.EgressPort,
			CreatedAt:      strings.TrimSpace(chain.CreatedAt),
			UpdatedAt:      strings.TrimSpace(chain.UpdatedAt),
			EntryHost:      entryHost,
			EntryPort:      entryPort,
			LinkLayer:      resolveProbeChainEntryLinkLayer(chain, entryNodeID),
			ChainSecret:    strings.TrimSpace(chain.Secret),
			HopConfigs:     buildProbeChainHopConfigsForManager(chain),
			PortForwards:   buildProbeChainPortForwardsForManager(chain),
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
		"# format:\n" +
		"# <proxy_group>\n" +
		"# {\n" +
		"#   <pattern>                 # route rule (exact/suffix/prefix/contains/ip/cidr)\n" +
		"#   <domain>,<ip>             # static dns (exact domain only, no wildcard)\n" +
		"# }\n" +
		"# wildcard priority: exact > suffix > prefix > contains\n" +
		"# wildcard syntax: *domain (suffix), domain* (prefix), *domain* (contains)\n" +
		"# no '*' means exact domain match\n" +
		"# braces must be on their own lines\n" +
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
	currentGroup := ""
	inGroupBlock := false
	currentGroupRuleCount := 0
	directGroupDeclared := false
	directGroupEmpty := false
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if !inGroupBlock {
			if strings.Contains(line, ",") {
				return tunnelRuleSet{}, fmt.Errorf("invalid rule_routes line %d: legacy '<pattern>,<group>' format is no longer supported", lineNo)
			}
			group := normalizeRuleGroupName(line)
			if group == "" {
				return tunnelRuleSet{}, fmt.Errorf("invalid rule_routes line %d: proxy group is required", lineNo)
			}
			if isDirectRuleGroupKey(group) {
				directGroupDeclared = true
			}
			currentGroup = group
			currentGroupRuleCount = 0
			inGroupBlock = true
			continue
		}

		if line == "{" {
			continue
		}
		if line == "}" {
			if isDirectRuleGroupKey(currentGroup) && currentGroupRuleCount == 0 {
				directGroupEmpty = true
			}
			currentGroup = ""
			currentGroupRuleCount = 0
			inGroupBlock = false
			continue
		}

		var (
			rule     tunnelRule
			parseErr error
		)
		if strings.Contains(line, ",") {
			rule, parseErr = parseTunnelStaticRuleLine(line, currentGroup)
		} else {
			rule, parseErr = parseTunnelRuleLine(line + "," + currentGroup)
		}
		if parseErr != nil {
			return tunnelRuleSet{}, fmt.Errorf("invalid rule_routes line %d: %w", lineNo, parseErr)
		}
		rules = append(rules, rule)
		currentGroupRuleCount++
	}
	if err := scanner.Err(); err != nil {
		return tunnelRuleSet{}, err
	}
	if inGroupBlock {
		return tunnelRuleSet{}, fmt.Errorf("invalid rule_routes: group %q missing closing brace '}'", currentGroup)
	}
	if directGroupDeclared && directGroupEmpty {
		rules = append(rules, buildDefaultDirectLANRules()...)
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

	wildcardCount := strings.Count(patternLower, "*")
	if wildcardCount == 0 {
		domain := normalizeRuleDomain(patternLower)
		if domain == "" || strings.Contains(domain, ",") {
			return tunnelRule{}, errors.New("invalid exact domain pattern")
		}
		return tunnelRule{
			RawPattern: pattern,
			Group:      group,
			Kind:       ruleMatcherDomainExact,
			Domain:     domain,
		}, nil
	}

	if wildcardCount == 1 && strings.HasPrefix(patternLower, "*") {
		suffix := normalizeRuleDomain(strings.TrimPrefix(patternLower, "*"))
		suffix = strings.TrimPrefix(suffix, ".")
		if suffix == "" || strings.Contains(suffix, ",") {
			return tunnelRule{}, errors.New("invalid suffix domain pattern")
		}
		return tunnelRule{
			RawPattern: pattern,
			Group:      group,
			Kind:       ruleMatcherDomainSuffix,
			Domain:     suffix,
		}, nil
	}
	if wildcardCount == 1 && strings.HasSuffix(patternLower, "*") {
		prefix := normalizeRuleDomain(strings.TrimSuffix(patternLower, "*"))
		if prefix == "" || strings.Contains(prefix, ",") {
			return tunnelRule{}, errors.New("invalid prefix domain pattern")
		}
		return tunnelRule{
			RawPattern: pattern,
			Group:      group,
			Kind:       ruleMatcherDomainPrefix,
			Domain:     prefix,
		}, nil
	}
	if wildcardCount == 2 && strings.HasPrefix(patternLower, "*") && strings.HasSuffix(patternLower, "*") {
		contains := normalizeRuleDomain(strings.TrimSuffix(strings.TrimPrefix(patternLower, "*"), "*"))
		if contains == "" || strings.Contains(contains, ",") {
			return tunnelRule{}, errors.New("invalid contains domain pattern")
		}
		return tunnelRule{
			RawPattern: pattern,
			Group:      group,
			Kind:       ruleMatcherDomainContains,
			Domain:     contains,
		}, nil
	}
	return tunnelRule{}, errors.New("unsupported wildcard domain pattern")
}

func parseTunnelStaticRuleLine(line string, group string) (tunnelRule, error) {
	parts := strings.SplitN(strings.TrimSpace(line), ",", 2)
	if len(parts) != 2 {
		return tunnelRule{}, errors.New("expected <domain>,<ip> for static dns rule")
	}
	pattern := strings.TrimSpace(parts[0])
	ipText := strings.TrimSpace(parts[1])
	if pattern == "" {
		return tunnelRule{}, errors.New("static dns domain pattern is required")
	}
	if ipText == "" {
		return tunnelRule{}, errors.New("static dns ip is required")
	}
	if strings.Contains(pattern, "*") {
		return tunnelRule{}, errors.New("static dns domain pattern does not support wildcard '*'")
	}
	patternLower := strings.ToLower(pattern)
	if _, _, err := net.ParseCIDR(patternLower); err == nil {
		return tunnelRule{}, errors.New("static dns pattern must be a domain, cidr is not allowed")
	}
	if ip := net.ParseIP(patternLower); ip != nil {
		return tunnelRule{}, errors.New("static dns pattern must be a domain, ip is not allowed")
	}
	domain := normalizeRuleDomain(patternLower)
	if domain == "" || strings.Contains(domain, ",") {
		return tunnelRule{}, errors.New("invalid static dns domain pattern")
	}
	ipValue := net.ParseIP(ipText)
	if ipValue == nil {
		return tunnelRule{}, errors.New("invalid static dns ip")
	}
	return tunnelRule{
		RawPattern: pattern,
		Group:      normalizeRuleGroupName(group),
		Kind:       ruleMatcherDomainStaticIP,
		Domain:     domain,
		IP:         canonicalIP(ipValue),
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

func buildDefaultDirectLANRules() []tunnelRule {
	rules := make([]tunnelRule, 0, len(defaultDirectLANCIDRRules))
	for _, rawCIDR := range defaultDirectLANCIDRRules {
		_, cidr, err := net.ParseCIDR(strings.TrimSpace(rawCIDR))
		if err != nil || cidr == nil {
			continue
		}
		rules = append(rules, tunnelRule{
			RawPattern: strings.TrimSpace(rawCIDR),
			Group:      "direct",
			Kind:       ruleMatcherCIDR,
			CIDR:       cidr,
		})
	}
	return rules
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

func canonicalIP(ip net.IP) string {
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4.String()
	}
	return ip.String()
}

