package backend

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

const aiDebugDirectDialTimeout = 8 * time.Second

type aiDebugDNSStatePayload struct {
	Kind               string                            `json:"kind"`
	Status             NetworkAssistantStatus            `json:"status"`
	Upstream           NetworkAssistantDNSUpstreamConfig `json:"upstream"`
	FakeIPEnabled      bool                              `json:"fake_ip_enabled"`
	FakeIPCIDR         string                            `json:"fake_ip_cidr"`
	DNSCacheEntryCount int                               `json:"dns_cache_entry_count"`
	FakeIPEntryCount   int                               `json:"fake_ip_entry_count"`
	BiMapEntryCount    int                               `json:"bimap_entry_count"`
	FetchedAt          string                            `json:"fetched_at"`
}

type aiDebugDNSCachePayload struct {
	Kind      string                          `json:"kind"`
	Query     string                          `json:"query,omitempty"`
	Count     int                             `json:"count"`
	Items     []NetworkAssistantDNSCacheEntry `json:"items"`
	FetchedAt string                          `json:"fetched_at"`
}

type aiDebugFakeIPEntryPayload struct {
	IP        string `json:"ip"`
	Domain    string `json:"domain"`
	Direct    bool   `json:"direct"`
	BypassTUN bool   `json:"bypass_tun"`
	NodeID    string `json:"node_id,omitempty"`
	Group     string `json:"group,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type aiDebugFakeIPListPayload struct {
	Kind      string                      `json:"kind"`
	Enabled   bool                        `json:"enabled"`
	CIDR      string                      `json:"cidr,omitempty"`
	Count     int                         `json:"count"`
	Items     []aiDebugFakeIPEntryPayload `json:"items"`
	FetchedAt string                      `json:"fetched_at"`
}

type aiDebugFakeIPLookupPayload struct {
	Kind      string `json:"kind"`
	IP        string `json:"ip"`
	Found     bool   `json:"found"`
	Domain    string `json:"domain,omitempty"`
	Direct    bool   `json:"direct,omitempty"`
	BypassTUN bool   `json:"bypass_tun,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	Group     string `json:"group,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	FetchedAt string `json:"fetched_at"`
}

type aiDebugDNSBiMapEntryPayload struct {
	Domain    string `json:"domain"`
	IP        string `json:"ip"`
	Group     string `json:"group,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type aiDebugDNSBiMapPayload struct {
	Kind      string                        `json:"kind"`
	Query     string                        `json:"query,omitempty"`
	Loaded    bool                          `json:"loaded"`
	Path      string                        `json:"path,omitempty"`
	Count     int                           `json:"count"`
	Items     []aiDebugDNSBiMapEntryPayload `json:"items"`
	FetchedAt string                        `json:"fetched_at"`
}

type aiDebugRouteDecisionPayload struct {
	Kind         string `json:"kind"`
	Target       string `json:"target"`
	ResolvedHost string `json:"resolved_host,omitempty"`
	Port         string `json:"port,omitempty"`
	Direct       bool   `json:"direct"`
	BypassTUN    bool   `json:"bypass_tun"`
	NodeID       string `json:"node_id,omitempty"`
	Group        string `json:"group,omitempty"`
	TargetAddr   string `json:"target_addr,omitempty"`
	Error        string `json:"error,omitempty"`
	FetchedAt    string `json:"fetched_at"`
}

type aiDebugDNSResolvePayload struct {
	Kind        string   `json:"kind"`
	Domain      string   `json:"domain"`
	QType       string   `json:"qtype"`
	Mode        string   `json:"mode"`
	UsedFakeIP  bool     `json:"used_fake_ip"`
	Addrs       []string `json:"addrs,omitempty"`
	TTL         int      `json:"ttl,omitempty"`
	Direct      bool     `json:"direct,omitempty"`
	BypassTUN   bool     `json:"bypass_tun,omitempty"`
	NodeID      string   `json:"node_id,omitempty"`
	Group       string   `json:"group,omitempty"`
	Source      string   `json:"source,omitempty"`
	CacheKey    string   `json:"cache_key,omitempty"`
	FakeIPValue string   `json:"fake_ip_value,omitempty"`
	Error       string   `json:"error,omitempty"`
	FetchedAt   string   `json:"fetched_at"`
}

type aiDebugDirectDialAttemptPayload struct {
	Address    string `json:"address"`
	DurationMS int64  `json:"duration_ms"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
}

type aiDebugDirectDialPayload struct {
	Kind        string                            `json:"kind"`
	Target      string                            `json:"target"`
	Host        string                            `json:"host"`
	Port        string                            `json:"port"`
	TimeoutMS   int64                             `json:"timeout_ms"`
	Direct      bool                              `json:"direct"`
	BypassTUN   bool                              `json:"bypass_tun"`
	NodeID      string                            `json:"node_id,omitempty"`
	Group       string                            `json:"group,omitempty"`
	TargetAddr  string                            `json:"target_addr,omitempty"`
	ResolvedIPs []string                          `json:"resolved_ips,omitempty"`
	Attempts    []aiDebugDirectDialAttemptPayload `json:"attempts,omitempty"`
	Error       string                            `json:"error,omitempty"`
	FetchedAt   string                            `json:"fetched_at"`
}

type aiDebugDNSUpstreamProbeResult struct {
	Type          string   `json:"type"`
	Prefer        string   `json:"prefer,omitempty"`
	Address       string   `json:"address,omitempty"`
	URL           string   `json:"url,omitempty"`
	Host          string   `json:"host,omitempty"`
	Port          string   `json:"port,omitempty"`
	IP            string   `json:"ip,omitempty"`
	TLSServerName string   `json:"tls_server_name,omitempty"`
	DurationMS    int64    `json:"duration_ms"`
	Addrs         []string `json:"addrs,omitempty"`
	TTL           int      `json:"ttl,omitempty"`
	Error         string   `json:"error,omitempty"`
}

type aiDebugDNSSystemProbePayload struct {
	Kind      string                          `json:"kind"`
	Domain    string                          `json:"domain"`
	QType     string                          `json:"qtype"`
	Prefer    string                          `json:"prefer,omitempty"`
	Count     int                             `json:"count"`
	Results   []aiDebugDNSUpstreamProbeResult `json:"results"`
	FetchedAt string                          `json:"fetched_at"`
}

type aiDebugProcessEventsPayload struct {
	Kind      string                `json:"kind"`
	SinceMS   int64                 `json:"since_ms"`
	Count     int                   `json:"count"`
	Items     []NetworkProcessEvent `json:"items"`
	FetchedAt string                `json:"fetched_at"`
}

type aiDebugMuxClientSnapshotPayload struct {
	ID                uint64   `json:"id"`
	NodeID            string   `json:"node_id,omitempty"`
	ModeKey           string   `json:"mode_key,omitempty"`
	SessionID         string   `json:"session_id,omitempty"`
	Connected         bool     `json:"connected"`
	Closed            bool     `json:"closed"`
	SessionClosed     bool     `json:"session_closed"`
	KeepAliveFailures int32    `json:"keepalive_failures"`
	ActiveStreams     int      `json:"active_streams"`
	LastRecv          string   `json:"last_recv,omitempty"`
	LastPong          string   `json:"last_pong,omitempty"`
	LastPingRTTMS     int64    `json:"last_ping_rtt_ms,omitempty"`
	CloseSource       string   `json:"close_source,omitempty"`
	CloseReason       string   `json:"close_reason,omitempty"`
	CloseAt           string   `json:"close_at,omitempty"`
	Groups            []string `json:"groups,omitempty"`
}

type aiDebugMuxGroupItemPayload struct {
	Group         string                             `json:"group"`
	ResolvedGroup string                             `json:"resolved_group,omitempty"`
	PolicyAction  string                             `json:"policy_action,omitempty"`
	PolicyNodeID  string                             `json:"policy_node_id,omitempty"`
	FailureCount  int                                `json:"failure_count"`
	RetryAt       string                             `json:"retry_at,omitempty"`
	LastError     string                             `json:"last_error,omitempty"`
	Snapshot      NetworkAssistantGroupKeepaliveItem `json:"snapshot"`
	Client        *aiDebugMuxClientSnapshotPayload   `json:"client,omitempty"`
}

type aiDebugMuxGroupsPayload struct {
	Kind      string                       `json:"kind"`
	Group     string                       `json:"group,omitempty"`
	Count     int                          `json:"count"`
	Items     []aiDebugMuxGroupItemPayload `json:"items"`
	FetchedAt string                       `json:"fetched_at"`
}

type aiDebugMuxClientsPayload struct {
	Kind      string                            `json:"kind"`
	Group     string                            `json:"group,omitempty"`
	Count     int                               `json:"count"`
	Items     []aiDebugMuxClientSnapshotPayload `json:"items"`
	FetchedAt string                            `json:"fetched_at"`
}

type aiDebugTUNRouteStatePayload struct {
	AdapterIndex         int      `json:"adapter_index"`
	AdapterDNSServers    []string `json:"adapter_dns_servers,omitempty"`
	DirectDNSServers     []string `json:"direct_dns_servers,omitempty"`
	BypassInterfaceIndex int      `json:"bypass_interface_index"`
	BypassNextHop        string   `json:"bypass_next_hop,omitempty"`
	BypassRoutePrefixes  []string `json:"bypass_route_prefixes,omitempty"`
}

type aiDebugTUNDynamicBypassItemPayload struct {
	Prefix   string `json:"prefix"`
	RefCount int    `json:"ref_count"`
}

type aiDebugTUNControlPlaneTargetsPayload struct {
	ControllerHost string   `json:"controller_host,omitempty"`
	Hosts          []string `json:"hosts,omitempty"`
	IPv4Addrs      []string `json:"ipv4_addrs,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type aiDebugTUNStatusPayload struct {
	Kind                string                               `json:"kind"`
	Status              NetworkAssistantStatus               `json:"status"`
	LastError           string                               `json:"last_error,omitempty"`
	TunnelOpenFailures  int                                  `json:"tunnel_open_failures"`
	RouteSyncedAt       string                               `json:"route_synced_at,omitempty"`
	RouteHost           string                               `json:"route_host,omitempty"`
	RouteSyncing        bool                                 `json:"route_syncing"`
	EverEnabled         bool                                 `json:"ever_enabled"`
	ManualClosed        bool                                 `json:"manual_closed"`
	PacketStackActive   bool                                 `json:"packet_stack_active"`
	UDPHandlerActive    bool                                 `json:"udp_handler_active"`
	InternalDNSActive   bool                                 `json:"internal_dns_active"`
	DataPlane           localTUNDataPlaneStats               `json:"data_plane"`
	RouteState          aiDebugTUNRouteStatePayload          `json:"route_state"`
	DynamicBypass       []aiDebugTUNDynamicBypassItemPayload `json:"dynamic_bypass"`
	ControlPlaneTargets aiDebugTUNControlPlaneTargetsPayload `json:"control_plane_targets"`
	FetchedAt           string                               `json:"fetched_at"`
}

type aiDebugUDPPortConflictItemPayload struct {
	Target     string `json:"target,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Count      int    `json:"count"`
	LastSeen   string `json:"last_seen,omitempty"`
	Error      string `json:"error,omitempty"`
	SampleLine string `json:"sample_line,omitempty"`
}

type aiDebugUDPPortConflictsPayload struct {
	Kind         string                              `json:"kind"`
	SinceMinutes int                                 `json:"since_minutes"`
	Lines        int                                 `json:"lines"`
	LogPath      string                              `json:"log_path,omitempty"`
	Count        int                                 `json:"count"`
	TotalEvents  int                                 `json:"total_events"`
	Items        []aiDebugUDPPortConflictItemPayload `json:"items"`
	FetchedAt    string                              `json:"fetched_at"`
}

type aiDebugFailureEventPayload struct {
	Timestamp    string `json:"timestamp"`
	Scope        string `json:"scope,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Target       string `json:"target,omitempty"`
	RoutedTarget string `json:"routed_target,omitempty"`
	Group        string `json:"group,omitempty"`
	NodeID       string `json:"node_id,omitempty"`
	ModeKey      string `json:"mode_key,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Error        string `json:"error,omitempty"`
	Message      string `json:"message,omitempty"`
	Direct       bool   `json:"direct"`
}

type aiDebugFailureEventsPayload struct {
	Kind      string                       `json:"kind"`
	SinceMS   int64                        `json:"since_ms"`
	Filter    string                       `json:"filter,omitempty"`
	Limit     int                          `json:"limit"`
	Count     int                          `json:"count"`
	Items     []aiDebugFailureEventPayload `json:"items"`
	FetchedAt string                       `json:"fetched_at"`
}

type aiDebugFailureTargetItemPayload struct {
	Scope         string `json:"scope,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Target        string `json:"target,omitempty"`
	RoutedTarget  string `json:"routed_target,omitempty"`
	Group         string `json:"group,omitempty"`
	NodeID        string `json:"node_id,omitempty"`
	ModeKey       string `json:"mode_key,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Direct        bool   `json:"direct"`
	Count         int    `json:"count"`
	LastSeen      string `json:"last_seen,omitempty"`
	SampleError   string `json:"sample_error,omitempty"`
	SampleMessage string `json:"sample_message,omitempty"`
}

type aiDebugFailureTargetsPayload struct {
	Kind      string                            `json:"kind"`
	SinceMS   int64                             `json:"since_ms"`
	Filter    string                            `json:"filter,omitempty"`
	Limit     int                               `json:"limit"`
	Count     int                               `json:"count"`
	Items     []aiDebugFailureTargetItemPayload `json:"items"`
	FetchedAt string                            `json:"fetched_at"`
}

func aiDebugActiveNetworkAssistant() (*networkAssistantService, error) {
	if globalNetworkAssistantService != nil {
		return globalNetworkAssistantService, nil
	}
	return nil, errors.New("network assistant service is not initialized")
}

func buildAIDebugDNSStatePayload(service *networkAssistantService) (aiDebugDNSStatePayload, error) {
	if service == nil {
		return aiDebugDNSStatePayload{}, errors.New("network assistant service is not initialized")
	}
	status := service.Status()
	upstream, err := service.GetDNSUpstreamConfig()
	if err != nil {
		return aiDebugDNSStatePayload{}, err
	}
	cacheItems := service.QueryDNSCache("")
	fakeIPItems, fakeIPEnabled := snapshotAIDebugFakeIPEntries(service)
	biMapItems, _, _, err := snapshotAIDebugDNSBiMapEntries("")
	if err != nil {
		return aiDebugDNSStatePayload{}, err
	}
	return aiDebugDNSStatePayload{
		Kind:               "network_dns_state",
		Status:             status,
		Upstream:           upstream,
		FakeIPEnabled:      fakeIPEnabled,
		FakeIPCIDR:         strings.TrimSpace(upstream.FakeIPCIDR),
		DNSCacheEntryCount: len(cacheItems),
		FakeIPEntryCount:   len(fakeIPItems),
		BiMapEntryCount:    len(biMapItems),
		FetchedAt:          time.Now().Format(time.RFC3339),
	}, nil
}

func buildAIDebugDNSCachePayload(service *networkAssistantService, query string) (aiDebugDNSCachePayload, error) {
	if service == nil {
		return aiDebugDNSCachePayload{}, errors.New("network assistant service is not initialized")
	}
	items := service.QueryDNSCache(strings.TrimSpace(query))
	return aiDebugDNSCachePayload{
		Kind:      "network_dns_cache",
		Query:     strings.TrimSpace(query),
		Count:     len(items),
		Items:     items,
		FetchedAt: time.Now().Format(time.RFC3339),
	}, nil
}

func buildAIDebugFakeIPListPayload(service *networkAssistantService) (aiDebugFakeIPListPayload, error) {
	if service == nil {
		return aiDebugFakeIPListPayload{}, errors.New("network assistant service is not initialized")
	}
	upstream, err := service.GetDNSUpstreamConfig()
	if err != nil {
		return aiDebugFakeIPListPayload{}, err
	}
	items, enabled := snapshotAIDebugFakeIPEntries(service)
	return aiDebugFakeIPListPayload{
		Kind:      "network_fake_ip_list",
		Enabled:   enabled,
		CIDR:      strings.TrimSpace(upstream.FakeIPCIDR),
		Count:     len(items),
		Items:     items,
		FetchedAt: time.Now().Format(time.RFC3339),
	}, nil
}

func buildAIDebugFakeIPLookupPayload(service *networkAssistantService, ip string) (aiDebugFakeIPLookupPayload, error) {
	if service == nil {
		return aiDebugFakeIPLookupPayload{}, errors.New("network assistant service is not initialized")
	}
	lookupIP := normalizeDNSCacheIP(ip)
	if lookupIP == "" {
		return aiDebugFakeIPLookupPayload{}, errors.New("ip is required")
	}
	service.mu.RLock()
	pool := service.fakeIPPool
	service.mu.RUnlock()
	if pool == nil {
		return aiDebugFakeIPLookupPayload{
			Kind:      "network_fake_ip_lookup",
			IP:        lookupIP,
			Found:     false,
			FetchedAt: time.Now().Format(time.RFC3339),
		}, nil
	}
	items := pool.ListAll()
	entry, ok := items[lookupIP]
	payload := aiDebugFakeIPLookupPayload{
		Kind:      "network_fake_ip_lookup",
		IP:        lookupIP,
		Found:     ok,
		FetchedAt: time.Now().Format(time.RFC3339),
	}
	if !ok {
		return payload, nil
	}
	payload.Domain = strings.TrimSpace(entry.Domain)
	payload.Direct = entry.Route.Direct
	payload.BypassTUN = entry.Route.BypassTUN
	payload.NodeID = strings.TrimSpace(entry.Route.NodeID)
	payload.Group = strings.TrimSpace(entry.Route.Group)
	if !entry.Expires.IsZero() {
		payload.ExpiresAt = entry.Expires.Format(time.RFC3339)
	}
	return payload, nil
}

func buildAIDebugDNSBiMapPayload(query string) (aiDebugDNSBiMapPayload, error) {
	items, path, loaded, err := snapshotAIDebugDNSBiMapEntries(query)
	if err != nil {
		return aiDebugDNSBiMapPayload{}, err
	}
	return aiDebugDNSBiMapPayload{
		Kind:      "network_dns_bimap",
		Query:     strings.TrimSpace(query),
		Loaded:    loaded,
		Path:      path,
		Count:     len(items),
		Items:     items,
		FetchedAt: time.Now().Format(time.RFC3339),
	}, nil
}

func buildAIDebugRouteDecisionPayload(service *networkAssistantService, target string) (aiDebugRouteDecisionPayload, error) {
	if service == nil {
		return aiDebugRouteDecisionPayload{}, errors.New("network assistant service is not initialized")
	}
	cleanTarget := strings.TrimSpace(target)
	if cleanTarget == "" {
		return aiDebugRouteDecisionPayload{}, errors.New("target is required")
	}
	host, port, err := splitTargetHostPort(cleanTarget)
	if err != nil {
		return aiDebugRouteDecisionPayload{}, err
	}
	decision, err := service.decideRouteForTarget(net.JoinHostPort(host, port))
	payload := aiDebugRouteDecisionPayload{
		Kind:         "network_route_decide",
		Target:       cleanTarget,
		ResolvedHost: strings.TrimSpace(host),
		Port:         strings.TrimSpace(port),
		FetchedAt:    time.Now().Format(time.RFC3339),
	}
	if err != nil {
		payload.Error = err.Error()
		return payload, nil
	}
	payload.Direct = decision.Direct
	payload.BypassTUN = decision.BypassTUN
	payload.NodeID = strings.TrimSpace(decision.NodeID)
	payload.Group = strings.TrimSpace(decision.Group)
	payload.TargetAddr = strings.TrimSpace(decision.TargetAddr)
	return payload, nil
}

func buildAIDebugDNSResolvePayload(service *networkAssistantService, domain string, qType string, mode string) (aiDebugDNSResolvePayload, error) {
	if service == nil {
		return aiDebugDNSResolvePayload{}, errors.New("network assistant service is not initialized")
	}
	cleanDomain := normalizeRuleDomain(domain)
	if cleanDomain == "" {
		return aiDebugDNSResolvePayload{}, errors.New("domain is required")
	}
	resolvedQType, qTypeLabel, err := parseAIDebugDNSQType(qType)
	if err != nil {
		return aiDebugDNSResolvePayload{}, err
	}
	resolveMode := normalizeAIDebugDNSResolveMode(mode)
	payload := aiDebugDNSResolvePayload{
		Kind:      "network_dns_resolve",
		Domain:    cleanDomain,
		QType:     qTypeLabel,
		Mode:      resolveMode,
		FetchedAt: time.Now().Format(time.RFC3339),
	}

	switch resolveMode {
	case "system":
		addrs, ttl, resolveErr := service.queryRuleDomainViaSystemDNS(cleanDomain, resolvedQType)
		if resolveErr != nil {
			payload.Error = resolveErr.Error()
			return payload, nil
		}
		payload.Addrs = filterDNSResponseAddrs(addrs, resolvedQType)
		payload.TTL = ttl
		payload.Source = "system"
		return payload, nil
	case "internal":
		return buildAIDebugInternalDNSResolvePayload(service, cleanDomain, resolvedQType, qTypeLabel)
	default:
		internalPayload, internalErr := buildAIDebugInternalDNSResolvePayload(service, cleanDomain, resolvedQType, qTypeLabel)
		if internalErr != nil {
			return aiDebugDNSResolvePayload{}, internalErr
		}
		if internalPayload.Error == "" {
			internalPayload.Mode = resolveMode
			return internalPayload, nil
		}
		addrs, ttl, resolveErr := service.queryRuleDomainViaSystemDNS(cleanDomain, resolvedQType)
		if resolveErr != nil {
			internalPayload.Mode = resolveMode
			return internalPayload, nil
		}
		internalPayload.Mode = resolveMode
		internalPayload.Addrs = filterDNSResponseAddrs(addrs, resolvedQType)
		internalPayload.TTL = ttl
		internalPayload.Source = "system"
		internalPayload.Error = ""
		internalPayload.UsedFakeIP = false
		return internalPayload, nil
	}
}

func buildAIDebugInternalDNSResolvePayload(service *networkAssistantService, domain string, qType uint16, qTypeLabel string) (aiDebugDNSResolvePayload, error) {
	payload := aiDebugDNSResolvePayload{
		Kind:      "network_dns_resolve",
		Domain:    domain,
		QType:     qTypeLabel,
		Mode:      "internal",
		FetchedAt: time.Now().Format(time.RFC3339),
	}
	if qType == 1 && service.shouldUseFakeIP(domain) {
		fakeIP, fakeErr := service.assignFakeIP(domain)
		if fakeErr == nil && strings.TrimSpace(fakeIP) != "" {
			payload.UsedFakeIP = true
			payload.Addrs = []string{strings.TrimSpace(fakeIP)}
			payload.TTL = dnsSharedTTLSeconds
			payload.Source = "fake_ip"
			payload.FakeIPValue = strings.TrimSpace(fakeIP)
			if route, routeErr := service.decideRouteForTarget(net.JoinHostPort(domain, "53")); routeErr == nil {
				payload.Direct = route.Direct
				payload.BypassTUN = route.BypassTUN
				payload.NodeID = strings.TrimSpace(route.NodeID)
				payload.Group = strings.TrimSpace(route.Group)
				payload.CacheKey = buildInternalDNSCacheKey(route, domain, qType)
			}
			return payload, nil
		}
	}
	addrs, ttl, route, resolveErr := service.resolveDomainForInternalDNS(domain, qType)
	payload.Direct = route.Direct
	payload.BypassTUN = route.BypassTUN
	payload.NodeID = strings.TrimSpace(route.NodeID)
	payload.Group = strings.TrimSpace(route.Group)
	payload.CacheKey = buildInternalDNSCacheKey(route, domain, qType)
	if resolveErr != nil {
		payload.Error = resolveErr.Error()
		return payload, nil
	}
	payload.Addrs = filterDNSResponseAddrs(addrs, qType)
	payload.TTL = ttl
	if shouldUseTunnelDNSForRoute(route) {
		payload.Source = "tunnel_dns"
	} else {
		payload.Source = "system_dns"
	}
	return payload, nil
}

func buildAIDebugDirectDialPayload(service *networkAssistantService, target string) (aiDebugDirectDialPayload, error) {
	if service == nil {
		return aiDebugDirectDialPayload{}, errors.New("network assistant service is not initialized")
	}
	cleanTarget := strings.TrimSpace(target)
	if cleanTarget == "" {
		return aiDebugDirectDialPayload{}, errors.New("target is required")
	}
	host, port, err := splitTargetHostPort(cleanTarget)
	if err != nil {
		return aiDebugDirectDialPayload{}, err
	}
	route, err := service.decideRouteForTarget(net.JoinHostPort(host, port))
	payload := aiDebugDirectDialPayload{
		Kind:      "network_direct_dial",
		Target:    cleanTarget,
		Host:      strings.TrimSpace(host),
		Port:      strings.TrimSpace(port),
		TimeoutMS: aiDebugDirectDialTimeout.Milliseconds(),
		FetchedAt: time.Now().Format(time.RFC3339),
	}
	if err != nil {
		payload.Error = err.Error()
		return payload, nil
	}
	payload.Direct = route.Direct
	payload.BypassTUN = route.BypassTUN
	payload.NodeID = strings.TrimSpace(route.NodeID)
	payload.Group = strings.TrimSpace(route.Group)
	payload.TargetAddr = strings.TrimSpace(route.TargetAddr)
	if !route.Direct {
		payload.Error = "route is not direct"
		return payload, nil
	}

	resolvedIPs := make([]string, 0, 4)
	if parsedIP := net.ParseIP(host); parsedIP != nil {
		resolvedIPs = append(resolvedIPs, canonicalIP(parsedIP))
	} else {
		addrs, _, resolveErr := service.queryRuleDomainViaSystemDNS(host, 1)
		if resolveErr != nil || len(addrs) == 0 {
			lookupCtx, cancel := context.WithTimeout(context.Background(), aiDebugDirectDialTimeout)
			defer cancel()
			addrs, resolveErr = net.DefaultResolver.LookupHost(lookupCtx, host)
			if resolveErr != nil {
				payload.Error = resolveErr.Error()
				return payload, nil
			}
		}
		resolvedIPs = normalizeDNSCacheIPs(addrs)
	}
	payload.ResolvedIPs = resolvedIPs
	if len(resolvedIPs) == 0 {
		payload.Error = "no resolved ip for direct dial"
		return payload, nil
	}

	attempts := make([]aiDebugDirectDialAttemptPayload, 0, len(resolvedIPs))
	var lastErr error
	for _, ip := range resolvedIPs {
		addr := net.JoinHostPort(ip, port)
		release := func() {}
		if route.BypassTUN {
			releaseBypass, bypassErr := service.acquireTUNDirectBypassRoute(addr)
			if bypassErr != nil {
				attempts = append(attempts, aiDebugDirectDialAttemptPayload{
					Address:    addr,
					DurationMS: 0,
					Success:    false,
					Error:      bypassErr.Error(),
				})
				lastErr = bypassErr
				continue
			}
			release = releaseBypass
		}
		startedAt := time.Now()
		conn, dialErr := net.DialTimeout("tcp", addr, aiDebugDirectDialTimeout)
		duration := time.Since(startedAt).Milliseconds()
		release()
		attempt := aiDebugDirectDialAttemptPayload{
			Address:    addr,
			DurationMS: duration,
			Success:    dialErr == nil,
		}
		if dialErr != nil {
			attempt.Error = dialErr.Error()
			lastErr = dialErr
		} else {
			_ = conn.Close()
		}
		attempts = append(attempts, attempt)
	}
	payload.Attempts = attempts
	if len(attempts) > 0 {
		for _, attempt := range attempts {
			if attempt.Success {
				return payload, nil
			}
		}
	}
	if lastErr != nil {
		payload.Error = lastErr.Error()
	}
	return payload, nil
}

func buildAIDebugDNSSystemProbePayload(service *networkAssistantService, domain string, qType string) (aiDebugDNSSystemProbePayload, error) {
	if service == nil {
		return aiDebugDNSSystemProbePayload{}, errors.New("network assistant service is not initialized")
	}
	cleanDomain := normalizeRuleDomain(domain)
	if cleanDomain == "" {
		return aiDebugDNSSystemProbePayload{}, errors.New("domain is required")
	}
	resolvedQType, qTypeLabel, err := parseAIDebugDNSQType(qType)
	if err != nil {
		return aiDebugDNSSystemProbePayload{}, err
	}
	config, err := getDNSUpstreamConfig()
	if err != nil {
		return aiDebugDNSSystemProbePayload{}, err
	}
	queryID := uint16(time.Now().UnixNano())
	packet, err := buildDNSQueryPacket(cleanDomain, resolvedQType, queryID)
	if err != nil {
		return aiDebugDNSSystemProbePayload{}, err
	}

	results := make([]aiDebugDNSUpstreamProbeResult, 0, len(config.DNSServers)+len(config.DoTServers)+len(config.DoHServers))
	for _, server := range config.DNSServers {
		results = append(results, aiDebugProbePlainDNSServer(service, cleanDomain, resolvedQType, qTypeLabel, queryID, packet, server))
	}
	for _, server := range config.DoTServers {
		results = append(results, aiDebugProbeDoTServer(service, cleanDomain, resolvedQType, qTypeLabel, queryID, packet, server, config.DNSServers))
	}
	for _, server := range config.DoHServers {
		results = append(results, aiDebugProbeDoHServer(service, cleanDomain, resolvedQType, qTypeLabel, queryID, packet, server, config.DNSServers))
	}

	return aiDebugDNSSystemProbePayload{
		Kind:      "network_dns_system_probe",
		Domain:    cleanDomain,
		QType:     qTypeLabel,
		Prefer:    strings.TrimSpace(config.Prefer),
		Count:     len(results),
		Results:   results,
		FetchedAt: time.Now().Format(time.RFC3339),
	}, nil
}

func buildAIDebugProcessEventsPayload(service *networkAssistantService, sinceMS int64) (aiDebugProcessEventsPayload, error) {
	if service == nil {
		return aiDebugProcessEventsPayload{}, errors.New("network assistant service is not initialized")
	}
	items := []NetworkProcessEvent{}
	if service.processMonitor != nil {
		items = service.processMonitor.QueryEvents(sinceMS)
	}
	return aiDebugProcessEventsPayload{
		Kind:      "network_process_events",
		SinceMS:   sinceMS,
		Count:     len(items),
		Items:     items,
		FetchedAt: time.Now().Format(time.RFC3339),
	}, nil
}

func buildAIDebugMuxGroupsPayload(service *networkAssistantService, group string) (aiDebugMuxGroupsPayload, error) {
	if service == nil {
		return aiDebugMuxGroupsPayload{}, errors.New("network assistant service is not initialized")
	}
	filter := strings.ToLower(strings.TrimSpace(group))
	service.mu.RLock()
	states := make(map[string]*ruleGroupRuntimeState, len(service.ruleRouting.GroupState))
	for key, state := range service.ruleRouting.GroupState {
		states[key] = state
	}
	service.mu.RUnlock()

	items := make([]aiDebugMuxGroupItemPayload, 0, len(states))
	for rawGroup, state := range states {
		groupName := strings.TrimSpace(rawGroup)
		if state != nil && strings.TrimSpace(state.ResolvedGroup) != "" {
			groupName = strings.TrimSpace(state.ResolvedGroup)
		}
		if groupName == "" {
			groupName = strings.TrimSpace(rawGroup)
		}
		if filter != "" && !strings.EqualFold(groupName, filter) && !strings.EqualFold(rawGroup, filter) {
			continue
		}
		item := aiDebugMuxGroupItemPayload{
			Group: groupName,
		}
		if state != nil {
			item.ResolvedGroup = strings.TrimSpace(state.ResolvedGroup)
			item.PolicyAction = strings.TrimSpace(state.PolicyAction)
			item.PolicyNodeID = strings.TrimSpace(state.PolicyNodeID)
			item.FailureCount = state.FailureCount
			item.RetryAt = formatMuxRuntimeTime(state.RetryAt)
			item.LastError = strings.TrimSpace(state.LastError)
			item.Snapshot = state.Snapshot
			if strings.TrimSpace(item.Snapshot.Group) == "" {
				item.Snapshot.Group = groupName
			}
			if state.Client != nil {
				clientSnapshot := snapshotAIDebugMuxClient(state.Client, []string{groupName})
				item.Client = &clientSnapshot
			}
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Group) < strings.ToLower(items[j].Group)
	})
	return aiDebugMuxGroupsPayload{
		Kind:      "network_mux_groups",
		Group:     strings.TrimSpace(group),
		Count:     len(items),
		Items:     items,
		FetchedAt: time.Now().Format(time.RFC3339),
	}, nil
}

func buildAIDebugMuxClientsPayload(service *networkAssistantService, group string) (aiDebugMuxClientsPayload, error) {
	if service == nil {
		return aiDebugMuxClientsPayload{}, errors.New("network assistant service is not initialized")
	}
	filter := strings.ToLower(strings.TrimSpace(group))
	service.mu.RLock()
	clientGroups := make(map[*tunnelMuxClient][]string)
	for rawGroup, state := range service.ruleRouting.GroupState {
		if state == nil || state.Client == nil {
			continue
		}
		groupName := strings.TrimSpace(rawGroup)
		if strings.TrimSpace(state.ResolvedGroup) != "" {
			groupName = strings.TrimSpace(state.ResolvedGroup)
		}
		if groupName == "" {
			groupName = strings.TrimSpace(rawGroup)
		}
		if filter != "" && !strings.EqualFold(groupName, filter) && !strings.EqualFold(rawGroup, filter) {
			continue
		}
		clientGroups[state.Client] = append(clientGroups[state.Client], groupName)
	}
	service.mu.RUnlock()

	items := make([]aiDebugMuxClientSnapshotPayload, 0, len(clientGroups))
	for client, groups := range clientGroups {
		sort.Strings(groups)
		items = append(items, snapshotAIDebugMuxClient(client, groups))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ID == items[j].ID {
			if items[i].NodeID == items[j].NodeID {
				return items[i].ModeKey < items[j].ModeKey
			}
			return items[i].NodeID < items[j].NodeID
		}
		return items[i].ID < items[j].ID
	})
	return aiDebugMuxClientsPayload{
		Kind:      "network_mux_clients",
		Group:     strings.TrimSpace(group),
		Count:     len(items),
		Items:     items,
		FetchedAt: time.Now().Format(time.RFC3339),
	}, nil
}

func buildAIDebugTUNStatusPayload(service *networkAssistantService) (aiDebugTUNStatusPayload, error) {
	if service == nil {
		return aiDebugTUNStatusPayload{}, errors.New("network assistant service is not initialized")
	}
	status := service.Status()
	service.mu.RLock()
	lastError := strings.TrimSpace(service.lastError)
	tunnelOpenFailures := service.tunnelOpenFailures
	routeSyncedAt := service.tunRouteSyncedAt
	routeHost := strings.TrimSpace(service.tunRouteHost)
	routeSyncing := service.tunRouteSyncing
	everEnabled := service.tunEverEnabled
	manualClosed := service.tunManualClosed
	packetStackActive := service.tunPacketStack != nil
	udpHandlerActive := service.tunUDPHandler != nil
	internalDNSActive := service.internalDNS != nil
	dataPlane := service.tunDataPlane
	routeState := service.tunRouteState
	dynamicBypass := make(map[string]int, len(service.tunDynamicBypass))
	for prefix, refCount := range service.tunDynamicBypass {
		dynamicBypass[prefix] = refCount
	}
	service.mu.RUnlock()

	stats := localTUNDataPlaneStats{}
	if dataPlane != nil {
		stats = dataPlane.Stats()
	}
	bypassItems := make([]aiDebugTUNDynamicBypassItemPayload, 0, len(dynamicBypass))
	for prefix, refCount := range dynamicBypass {
		bypassItems = append(bypassItems, aiDebugTUNDynamicBypassItemPayload{Prefix: prefix, RefCount: refCount})
	}
	sort.Slice(bypassItems, func(i, j int) bool {
		return bypassItems[i].Prefix < bypassItems[j].Prefix
	})

	targetsPayload := aiDebugTUNControlPlaneTargetsPayload{}
	targets, targetsErr := service.collectTUNControlPlaneTargets()
	targetsPayload.ControllerHost = strings.TrimSpace(targets.ControllerHost)
	if len(targets.Hosts) > 0 {
		hosts := make([]string, 0, len(targets.Hosts))
		for host := range targets.Hosts {
			hosts = append(hosts, host)
		}
		sort.Strings(hosts)
		targetsPayload.Hosts = hosts
	}
	if len(targets.IPv4Addrs) > 0 {
		targetsPayload.IPv4Addrs = append([]string(nil), targets.IPv4Addrs...)
	}
	if targetsErr != nil {
		targetsPayload.Error = targetsErr.Error()
	}

	payload := aiDebugTUNStatusPayload{
		Kind:               "network_tun_status",
		Status:             status,
		LastError:          lastError,
		TunnelOpenFailures: tunnelOpenFailures,
		RouteHost:          routeHost,
		RouteSyncing:       routeSyncing,
		EverEnabled:        everEnabled,
		ManualClosed:       manualClosed,
		PacketStackActive:  packetStackActive,
		UDPHandlerActive:   udpHandlerActive,
		InternalDNSActive:  internalDNSActive,
		DataPlane:          stats,
		RouteState: aiDebugTUNRouteStatePayload{
			AdapterIndex:         routeState.AdapterIndex,
			AdapterDNSServers:    append([]string(nil), routeState.AdapterDNSServers...),
			DirectDNSServers:     append([]string(nil), routeState.DirectDNSServers...),
			BypassInterfaceIndex: routeState.BypassInterfaceIndex,
			BypassNextHop:        strings.TrimSpace(routeState.BypassNextHop),
			BypassRoutePrefixes:  append([]string(nil), routeState.BypassRoutePrefixes...),
		},
		DynamicBypass:       bypassItems,
		ControlPlaneTargets: targetsPayload,
		FetchedAt:           time.Now().Format(time.RFC3339),
	}
	if !routeSyncedAt.IsZero() {
		payload.RouteSyncedAt = routeSyncedAt.UTC().Format(time.RFC3339)
	}
	return payload, nil
}

func buildAIDebugUDPPortConflictsPayload(sinceMinutes int, lines int) (aiDebugUDPPortConflictsPayload, error) {
	lineLimit := normalizeLogViewLines(lines)
	logPath, err := resolveManagerLogPath()
	if err != nil {
		return aiDebugUDPPortConflictsPayload{}, err
	}
	_, entries, err := readLogTailLines(logPath, lineLimit, sinceMinutes, "")
	if err != nil {
		return aiDebugUDPPortConflictsPayload{}, err
	}
	aggregated := make(map[string]*aiDebugUDPPortConflictItemPayload)
	totalEvents := 0
	for _, entry := range entries {
		message := strings.TrimSpace(entry.Message)
		if !strings.Contains(message, "local tun udp create endpoint failed") || !strings.Contains(message, "reason=local_port_in_use") {
			continue
		}
		totalEvents++
		target := aiDebugExtractLogField(message, "target")
		if target == "" {
			target = "unknown"
		}
		item, ok := aggregated[target]
		if !ok {
			item = &aiDebugUDPPortConflictItemPayload{
				Target: target,
				Reason: aiDebugExtractLogField(message, "reason"),
				Error:  aiDebugExtractLogField(message, "err"),
			}
			aggregated[target] = item
		}
		item.Count++
		if strings.TrimSpace(entry.Time) != "" {
			item.LastSeen = strings.TrimSpace(entry.Time)
		}
		if item.SampleLine == "" {
			item.SampleLine = strings.TrimSpace(entry.Line)
		}
		if item.Error == "" {
			item.Error = aiDebugExtractLogField(message, "err")
		}
	}
	items := make([]aiDebugUDPPortConflictItemPayload, 0, len(aggregated))
	for _, item := range aggregated {
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Target < items[j].Target
		}
		return items[i].Count > items[j].Count
	})
	return aiDebugUDPPortConflictsPayload{
		Kind:         "network_tun_udp_port_conflicts",
		SinceMinutes: sinceMinutes,
		Lines:        lineLimit,
		LogPath:      logPath,
		Count:        len(items),
		TotalEvents:  totalEvents,
		Items:        items,
		FetchedAt:    time.Now().Format(time.RFC3339),
	}, nil
}

func buildAIDebugFailureEventsPayload(sinceMS int64, kind string, limit int) (aiDebugFailureEventsPayload, error) {
	resolvedLimit := normalizeNetworkDebugFailureLimit(limit)
	events := queryNetworkDebugFailures(sinceMS, kind, resolvedLimit)
	items := make([]aiDebugFailureEventPayload, 0, len(events))
	for _, entry := range events {
		payload := aiDebugFailureEventPayload{
			Scope:        strings.TrimSpace(entry.Scope),
			Kind:         strings.TrimSpace(entry.Kind),
			Target:       strings.TrimSpace(entry.Target),
			RoutedTarget: strings.TrimSpace(entry.RoutedTarget),
			Group:        strings.TrimSpace(entry.Group),
			NodeID:       strings.TrimSpace(entry.NodeID),
			ModeKey:      strings.TrimSpace(entry.ModeKey),
			Reason:       strings.TrimSpace(entry.Reason),
			Error:        strings.TrimSpace(entry.Error),
			Message:      strings.TrimSpace(entry.Message),
			Direct:       entry.Direct,
		}
		if !entry.Timestamp.IsZero() {
			payload.Timestamp = entry.Timestamp.UTC().Format(time.RFC3339)
		}
		items = append(items, payload)
	}
	return aiDebugFailureEventsPayload{
		Kind:      "network_failure_events",
		SinceMS:   sinceMS,
		Filter:    strings.TrimSpace(kind),
		Limit:     resolvedLimit,
		Count:     len(items),
		Items:     items,
		FetchedAt: time.Now().Format(time.RFC3339),
	}, nil
}

func buildAIDebugFailureTargetsPayload(sinceMS int64, kind string, limit int) (aiDebugFailureTargetsPayload, error) {
	resolvedLimit := normalizeNetworkDebugFailureLimit(limit)
	events := queryNetworkDebugFailures(sinceMS, kind, maxFailureEventsQueryLimit)
	type aggregateKey struct {
		Scope        string
		Kind         string
		Target       string
		RoutedTarget string
		Group        string
		NodeID       string
		ModeKey      string
		Reason       string
		Direct       bool
	}
	aggregated := make(map[aggregateKey]*aiDebugFailureTargetItemPayload)
	for _, entry := range events {
		key := aggregateKey{
			Scope:        strings.TrimSpace(entry.Scope),
			Kind:         strings.TrimSpace(entry.Kind),
			Target:       strings.TrimSpace(entry.Target),
			RoutedTarget: strings.TrimSpace(entry.RoutedTarget),
			Group:        strings.TrimSpace(entry.Group),
			NodeID:       strings.TrimSpace(entry.NodeID),
			ModeKey:      strings.TrimSpace(entry.ModeKey),
			Reason:       strings.TrimSpace(entry.Reason),
			Direct:       entry.Direct,
		}
		item, ok := aggregated[key]
		if !ok {
			item = &aiDebugFailureTargetItemPayload{
				Scope:        key.Scope,
				Kind:         key.Kind,
				Target:       key.Target,
				RoutedTarget: key.RoutedTarget,
				Group:        key.Group,
				NodeID:       key.NodeID,
				ModeKey:      key.ModeKey,
				Reason:       key.Reason,
				Direct:       key.Direct,
			}
			aggregated[key] = item
		}
		item.Count++
		if !entry.Timestamp.IsZero() {
			seen := entry.Timestamp.UTC().Format(time.RFC3339)
			if seen > item.LastSeen {
				item.LastSeen = seen
			}
		}
		if item.SampleError == "" {
			item.SampleError = strings.TrimSpace(entry.Error)
		}
		if item.SampleMessage == "" {
			item.SampleMessage = strings.TrimSpace(entry.Message)
		}
	}
	items := make([]aiDebugFailureTargetItemPayload, 0, len(aggregated))
	for _, item := range aggregated {
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			if items[i].LastSeen == items[j].LastSeen {
				if items[i].Target == items[j].Target {
					return items[i].Reason < items[j].Reason
				}
				return items[i].Target < items[j].Target
			}
			return items[i].LastSeen > items[j].LastSeen
		}
		return items[i].Count > items[j].Count
	})
	if len(items) > resolvedLimit {
		items = append([]aiDebugFailureTargetItemPayload(nil), items[:resolvedLimit]...)
	}
	return aiDebugFailureTargetsPayload{
		Kind:      "network_failure_targets",
		SinceMS:   sinceMS,
		Filter:    strings.TrimSpace(kind),
		Limit:     resolvedLimit,
		Count:     len(items),
		Items:     items,
		FetchedAt: time.Now().Format(time.RFC3339),
	}, nil
}

func snapshotAIDebugMuxClient(client *tunnelMuxClient, groups []string) aiDebugMuxClientSnapshotPayload {
	payload := aiDebugMuxClientSnapshotPayload{}
	if client == nil {
		return payload
	}
	connected, activeStreams, lastRecv, lastPong, lastPingRTTMS := client.snapshot()
	sessionClosed := true
	if client.session != nil {
		sessionClosed = client.session.IsClosed()
	}
	client.mu.Lock()
	closeSource := strings.TrimSpace(client.closeSource)
	closeReason := strings.TrimSpace(client.closeReason)
	closeAt := client.closeAt
	client.mu.Unlock()
	payload = aiDebugMuxClientSnapshotPayload{
		ID:                client.id,
		NodeID:            strings.TrimSpace(client.nodeID),
		ModeKey:           strings.TrimSpace(client.modeKey),
		SessionID:         strings.TrimSpace(client.sessionID),
		Connected:         connected,
		Closed:            client.closed.Load(),
		SessionClosed:     sessionClosed,
		KeepAliveFailures: client.keepAliveFailures.Load(),
		ActiveStreams:     activeStreams,
		LastRecv:          strings.TrimSpace(lastRecv),
		LastPong:          strings.TrimSpace(lastPong),
		LastPingRTTMS:     lastPingRTTMS,
		CloseSource:       closeSource,
		CloseReason:       closeReason,
		Groups:            append([]string(nil), groups...),
	}
	if !closeAt.IsZero() {
		payload.CloseAt = closeAt.UTC().Format(time.RFC3339)
	}
	return payload
}

func aiDebugExtractLogField(message string, key string) string {
	cleanMessage := strings.TrimSpace(message)
	cleanKey := strings.TrimSpace(key)
	if cleanMessage == "" || cleanKey == "" {
		return ""
	}
	token := cleanKey + "="
	idx := strings.Index(cleanMessage, token)
	if idx < 0 {
		return ""
	}
	value := strings.TrimSpace(cleanMessage[idx+len(token):])
	if value == "" {
		return ""
	}
	if cleanKey == "err" {
		return value
	}
	if space := strings.IndexByte(value, ' '); space >= 0 {
		return strings.TrimSpace(value[:space])
	}
	return value
}

func parseAIDebugDNSQType(raw string) (uint16, string, error) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "", "A", "1":
		return 1, "A", nil
	case "AAAA", "28":
		return 28, "AAAA", nil
	default:
		return 0, "", fmt.Errorf("unsupported qtype: %s", strings.TrimSpace(raw))
	}
}

func normalizeAIDebugDNSResolveMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto":
		return "auto"
	case "system":
		return "system"
	case "internal":
		return "internal"
	default:
		return "auto"
	}
}

func aiDebugProbePlainDNSServer(service *networkAssistantService, domain string, qType uint16, qTypeLabel string, queryID uint16, packet []byte, server dnsPlainServer) aiDebugDNSUpstreamProbeResult {
	startedAt := time.Now()
	result := aiDebugDNSUpstreamProbeResult{
		Type:       "plain_dns",
		Prefer:     qTypeLabel,
		Address:    strings.TrimSpace(server.Address),
		Host:       strings.TrimSpace(server.Host),
		Port:       strings.TrimSpace(server.Port),
		DurationMS: 0,
	}
	payload, err := service.queryRawDNSPacket(server.Address, packet, dnsUpstreamResolveTimeout)
	result.DurationMS = time.Since(startedAt).Milliseconds()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	addrs, ttl, parseErr := parseDNSResponseAddrs(payload, queryID, qType)
	if parseErr != nil {
		result.Error = parseErr.Error()
		return result
	}
	result.Addrs = filterDNSResponseAddrs(addrs, qType)
	result.TTL = ttl
	return result
}

func aiDebugProbeDoTServer(service *networkAssistantService, domain string, qType uint16, qTypeLabel string, queryID uint16, packet []byte, server dnsDoTServer, bootstrap []dnsPlainServer) aiDebugDNSUpstreamProbeResult {
	startedAt := time.Now()
	result := aiDebugDNSUpstreamProbeResult{
		Type:          "dot",
		Prefer:        qTypeLabel,
		Address:       strings.TrimSpace(server.Address),
		Host:          strings.TrimSpace(server.Host),
		Port:          strings.TrimSpace(server.Port),
		TLSServerName: strings.TrimSpace(server.TLSServerName),
		DurationMS:    0,
	}
	payload, err := service.queryRawDNSPacketViaDoT(server, packet, dnsUpstreamResolveTimeout, bootstrap)
	result.DurationMS = time.Since(startedAt).Milliseconds()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	addrs, ttl, parseErr := parseDNSResponseAddrs(payload, queryID, qType)
	if parseErr != nil {
		result.Error = parseErr.Error()
		return result
	}
	result.Addrs = filterDNSResponseAddrs(addrs, qType)
	result.TTL = ttl
	return result
}

func aiDebugProbeDoHServer(service *networkAssistantService, domain string, qType uint16, qTypeLabel string, queryID uint16, packet []byte, server dnsDoHServer, bootstrap []dnsPlainServer) aiDebugDNSUpstreamProbeResult {
	startedAt := time.Now()
	result := aiDebugDNSUpstreamProbeResult{
		Type:          "doh",
		Prefer:        qTypeLabel,
		URL:           strings.TrimSpace(server.URL),
		Host:          strings.TrimSpace(server.Host),
		Port:          strings.TrimSpace(server.Port),
		IP:            strings.TrimSpace(server.IP),
		TLSServerName: strings.TrimSpace(server.TLSServerName),
		DurationMS:    0,
	}
	payload, err := service.queryRawDNSPacketViaDoH(server, packet, dnsUpstreamResolveTimeout, bootstrap)
	result.DurationMS = time.Since(startedAt).Milliseconds()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	addrs, ttl, parseErr := parseDNSResponseAddrs(payload, queryID, qType)
	if parseErr != nil {
		result.Error = parseErr.Error()
		return result
	}
	result.Addrs = filterDNSResponseAddrs(addrs, qType)
	result.TTL = ttl
	return result
}

func snapshotAIDebugFakeIPEntries(service *networkAssistantService) ([]aiDebugFakeIPEntryPayload, bool) {
	if service == nil {
		return nil, false
	}
	service.mu.RLock()
	pool := service.fakeIPPool
	service.mu.RUnlock()
	if pool == nil {
		return []aiDebugFakeIPEntryPayload{}, false
	}
	itemsByIP := pool.ListAll()
	ips := make([]string, 0, len(itemsByIP))
	for ip := range itemsByIP {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	items := make([]aiDebugFakeIPEntryPayload, 0, len(ips))
	for _, ip := range ips {
		entry := itemsByIP[ip]
		payload := aiDebugFakeIPEntryPayload{
			IP:        ip,
			Domain:    strings.TrimSpace(entry.Domain),
			Direct:    entry.Route.Direct,
			BypassTUN: entry.Route.BypassTUN,
			NodeID:    strings.TrimSpace(entry.Route.NodeID),
			Group:     strings.TrimSpace(entry.Route.Group),
		}
		if !entry.Expires.IsZero() {
			payload.ExpiresAt = entry.Expires.Format(time.RFC3339)
		}
		items = append(items, payload)
	}
	return items, true
}

func snapshotAIDebugDNSBiMapEntries(query string) ([]aiDebugDNSBiMapEntryPayload, string, bool, error) {
	if err := ensureDNSBiMapCacheLoaded(); err != nil {
		return nil, "", false, err
	}
	filter := strings.ToLower(strings.TrimSpace(query))
	now := time.Now()
	dnsBiMapCache.mu.Lock()
	removed := pruneExpiredDNSBiMapLocked(now)
	path := strings.TrimSpace(dnsBiMapCache.path)
	loaded := dnsBiMapCache.loaded
	items := make([]aiDebugDNSBiMapEntryPayload, 0, len(dnsBiMapCache.entries))
	for _, entry := range dnsBiMapCache.entries {
		domain := strings.TrimSpace(entry.Domain)
		ip := strings.TrimSpace(entry.IP)
		if filter != "" && !strings.Contains(strings.ToLower(domain), filter) && !strings.Contains(strings.ToLower(ip), filter) {
			continue
		}
		payload := aiDebugDNSBiMapEntryPayload{
			Domain: domain,
			IP:     ip,
			Group:  strings.TrimSpace(entry.Group),
			NodeID: strings.TrimSpace(entry.NodeID),
		}
		if !entry.ExpiresAt.IsZero() {
			payload.ExpiresAt = entry.ExpiresAt.Format(time.RFC3339)
		}
		if !entry.UpdatedAt.IsZero() {
			payload.UpdatedAt = entry.UpdatedAt.Format(time.RFC3339)
		}
		items = append(items, payload)
	}
	if removed {
		_ = storeDNSBiMapToDiskLocked()
	}
	dnsBiMapCache.mu.Unlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].Domain != items[j].Domain {
			return items[i].Domain < items[j].Domain
		}
		return items[i].IP < items[j].IP
	})
	return items, path, loaded, nil
}
