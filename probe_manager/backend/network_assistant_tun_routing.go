package backend

import (
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"
)

const tunRouteRefreshInterval = 5 * time.Minute

type tunSystemRouteState struct {
	AdapterIndex         int
	AdapterDNSServers    []string
	DirectDNSServers     []string
	BypassInterfaceIndex int
	BypassNextHop        string
	BypassRoutePrefixes  []string
}

type tunControlPlaneTargets struct {
	ControllerHost string
	Hosts          map[string]struct{}
	IPs            map[string]struct{}
	IPv4Addrs      []string
}

func (s *networkAssistantService) clearTUNDynamicBypassRoutes() error {
	err := s.clearPlatformTUNDynamicBypassRoutes()
	s.mu.Lock()
	s.tunRouteSyncedAt = time.Time{}
	s.tunRouteHost = ""
	s.tunRouteSyncing = false
	s.mu.Unlock()
	return err
}

func (s *networkAssistantService) applyTUNSystemRouting(_ string) error {
	startedAt := time.Now()
	s.logf("tun routing apply start")

	targets, collectErr := s.collectTUNControlPlaneTargets()
	if collectErr != nil {
		s.logf("collect control-plane bypass targets partially failed: %v", collectErr)
	}
	if len(targets.IPv4Addrs) > 0 {
		s.logf("tun routing control-plane bypass targets prepared: controller=%s ipv4=%s", targets.ControllerHost, strings.Join(targets.IPv4Addrs, ","))
	} else {
		s.logf("tun routing control-plane bypass targets prepared: controller=%s ipv4=none", targets.ControllerHost)
	}

	if err := s.applyPlatformTUNSystemRouting(targets); err != nil {
		s.logf("tun routing apply failed in platform routing stage: err=%v elapsed=%s", err, time.Since(startedAt))
		return err
	}
	s.logf("tun routing stage success: platform-routing elapsed=%s", time.Since(startedAt))
	s.seedStaticDNSRouteHints()
	s.logf("tun routing stage success: seed-static-dns elapsed=%s", time.Since(startedAt))
	s.seedControlPlaneRouteHints(targets)
	s.logf("tun routing stage success: seed-control-plane-dns elapsed=%s", time.Since(startedAt))
	s.mu.Lock()
	s.tunRouteSyncedAt = time.Now()
	s.tunRouteHost = targets.ControllerHost
	s.mu.Unlock()
	s.logf("tun routing apply success: elapsed=%s", time.Since(startedAt))
	return nil
}

func (s *networkAssistantService) clearTUNSystemRouting() error {
	err := s.clearPlatformTUNSystemRouting()
	s.clearDNSRouteHints()
	s.mu.Lock()
	s.tunRouteSyncedAt = time.Time{}
	s.tunRouteHost = ""
	s.tunRouteSyncing = false
	s.mu.Unlock()
	return err
}

func (s *networkAssistantService) forceRefreshDNSOnModeSwitch(_ string) {
	s.clearDNSCache()
}

func (s *networkAssistantService) seedStaticDNSRouteHints() {
	s.mu.RLock()
	routing := s.ruleRouting
	nodeID := strings.TrimSpace(s.nodeID)
	availableNodes := append([]string(nil), s.availableNodes...)
	s.mu.RUnlock()
	if nodeID == "" {
		nodeID = defaultNodeID
	}
	tunnelOptions := buildRuleTunnelOptions(availableNodes, nodeID)

	seeded := 0
	for _, rule := range routing.RuleSet.Rules {
		if rule.Kind != ruleMatcherDomainStaticIP {
			continue
		}
		ipValue := canonicalIP(net.ParseIP(strings.TrimSpace(rule.IP)))
		domain := normalizeRuleDomain(rule.Domain)
		if ipValue == "" || domain == "" {
			continue
		}
		group := normalizeRuleGroupName(rule.Group)
		policy, err := readRulePolicyForGroup(routing, group, nodeID, tunnelOptions)
		if err != nil {
			continue
		}
		decision := tunnelRouteDecision{Group: group}
		switch policy.Action {
		case rulePolicyActionReject:
			continue
		case rulePolicyActionDirect:
			decision.Direct = true
			decision.BypassTUN = isDirectRuleGroupKey(group)
		case rulePolicyActionTunnel:
			if isDirectRuleGroupKey(group) {
				decision.Direct = true
				decision.BypassTUN = true
			} else {
				decision.Direct = false
				targetNodeID := strings.TrimSpace(policy.TunnelNodeID)
				if targetNodeID == "" {
					targetNodeID = nodeID
				}
				if targetNodeID == "" {
					targetNodeID = defaultNodeID
				}
				decision.NodeID = targetNodeID
			}
		default:
			decision.Direct = true
		}
		s.storeDNSRouteHint([]string{ipValue}, domain, decision, internalDNSDefaultTTLSeconds)
		seeded++
	}
	if seeded > 0 {
		s.logf("seeded static dns route hints: count=%d", seeded)
	}
}

func (s *networkAssistantService) seedControlPlaneRouteHints(targets tunControlPlaneTargets) {
	decision := tunnelRouteDecision{Direct: true, BypassTUN: true, Group: "direct"}
	seeded := 0

	for host := range targets.Hosts {
		if host == "" {
			continue
		}
		if ipHost := normalizeControlPlaneIP(host); ipHost != "" {
			s.storeDNSRouteHint([]string{ipHost}, host, decision, internalDNSDefaultTTLSeconds)
			seeded++
			continue
		}
		if cachedIP, ok := getProbeDNSCachedIP(host); ok {
			s.storeDNSRouteHint([]string{cachedIP}, host, decision, internalDNSDefaultTTLSeconds)
			seeded++
		}
	}

	if controllerIP := normalizeControlPlaneIP(targets.ControllerHost); controllerIP != "" {
		s.storeDNSRouteHint([]string{controllerIP}, targets.ControllerHost, decision, internalDNSDefaultTTLSeconds)
		seeded++
	}

	fallbackDomain := strings.TrimSpace(targets.ControllerHost)
	for _, ipValue := range targets.IPv4Addrs {
		canonical := canonicalIP(net.ParseIP(ipValue))
		if canonical == "" {
			continue
		}
		domain := fallbackDomain
		if domain == "" {
			domain = canonical
		}
		s.storeDNSRouteHint([]string{canonical}, domain, decision, internalDNSDefaultTTLSeconds)
		seeded++
	}

	if seeded > 0 {
		s.logf("seeded control-plane dns route hints: count=%d", seeded)
	}
}

func (s *networkAssistantService) isControlPlaneHost(host string) bool {
	normalizedHost := normalizeControlPlaneHost(host)
	if normalizedHost == "" {
		return false
	}

	s.mu.RLock()
	baseURL := strings.TrimSpace(s.controllerBaseURL)
	chainTargets := copyProbeChainTargets(s.chainTargets)
	s.mu.RUnlock()

	controllerHost := normalizeControlPlaneHost(resolveControllerHostForProtection(baseURL))
	if controllerHost != "" && normalizedHost == controllerHost {
		return true
	}
	preferredTarget, err := resolvePreferredControllerDialTarget(baseURL)
	if err == nil {
		preferredIP := normalizeControlPlaneHost(preferredTarget.PreferredIP)
		if preferredIP != "" && normalizedHost == preferredIP {
			return true
		}
	}
	for _, endpoint := range chainTargets {
		if normalizedHost == normalizeControlPlaneHost(endpoint.EntryHost) {
			return true
		}
	}
	return false
}

func (s *networkAssistantService) ensureControlPlaneDialReady(_ string) error {
	needRefresh := false
	s.mu.Lock()
	mode := s.mode
	tunEnabled := s.tunEnabled
	lastSyncAt := s.tunRouteSyncedAt
	if mode == networkModeTUN && tunEnabled {
		// 仅在本次进入 TUN 后首次建立控制面路由，后续持续复用已缓存的 bypass 出口。
		if lastSyncAt.IsZero() && !s.tunRouteSyncing {
			s.tunRouteSyncing = true
			needRefresh = true
		}
	}
	s.mu.Unlock()

	if !needRefresh {
		return nil
	}
	startedAt := time.Now()
	s.logf("tun routing refresh start: last_sync_at=%s", lastSyncAt.Format(time.RFC3339))
	err := s.applyTUNSystemRouting("")
	s.mu.Lock()
	s.tunRouteSyncing = false
	mode = s.mode
	tunEnabled = s.tunEnabled
	s.mu.Unlock()
	if err != nil {
		s.logf("tun routing refresh failed: err=%v elapsed=%s mode=%s tun_enabled=%t", err, time.Since(startedAt), mode, tunEnabled)
	} else {
		s.logf("tun routing refresh success: elapsed=%s", time.Since(startedAt))
	}
	if err != nil && mode == networkModeTUN && tunEnabled {
		return s.fallbackToDirectModeOnTUNRoutingFailure("refresh tun direct routes failed", err)
	}
	return err
}

func (s *networkAssistantService) collectTUNControlPlaneTargets() (tunControlPlaneTargets, error) {
	targets := tunControlPlaneTargets{
		Hosts:     make(map[string]struct{}),
		IPs:       make(map[string]struct{}),
		IPv4Addrs: make([]string, 0),
	}

	addIP := func(raw string) {
		ip := normalizeControlPlaneIP(raw)
		if ip == "" {
			return
		}
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.To4() == nil {
			return
		}
		targets.IPs[ip] = struct{}{}
	}
	addHost := func(raw string) {
		host := normalizeControlPlaneHost(raw)
		if host == "" {
			return
		}
		if normalizeControlPlaneIP(host) != "" {
			addIP(host)
			return
		}
		targets.Hosts[host] = struct{}{}
	}

	s.mu.RLock()
	baseURL := strings.TrimSpace(s.controllerBaseURL)
	s.mu.RUnlock()
	controllerHost := resolveControllerHostForProtection(baseURL)
	targets.ControllerHost = controllerHost
	addHost(controllerHost)
	preferredTarget, preferredErr := resolvePreferredControllerDialTarget(baseURL)
	if preferredTarget.Enabled {
		addIP(preferredTarget.PreferredIP)
	}

	chainTargets, chainErr := s.getOrLoadChainTargetsSnapshot()
	if chainErr == nil {
		for _, endpoint := range chainTargets {
			addHost(endpoint.EntryHost)
		}
	}

	var allErr error
	if chainErr != nil {
		allErr = errors.Join(allErr, chainErr)
	}
	if preferredErr != nil {
		allErr = errors.Join(allErr, preferredErr)
	}

	for host := range targets.Hosts {
		if cachedIP, ok := getProbeDNSCachedIP(host); ok {
			addIP(cachedIP)
		}
		if preferredTarget.Enabled && controllerHost != "" && strings.EqualFold(host, controllerHost) {
			addIP(preferredTarget.PreferredIP)
			continue
		}
		dialHost, _, err := resolveProbeChainDialIPHostFresh(host)
		if err != nil {
			allErr = errors.Join(allErr, err)
			continue
		}
		addIP(dialHost)
	}

	for ip := range targets.IPs {
		targets.IPv4Addrs = append(targets.IPv4Addrs, ip)
	}
	sort.Strings(targets.IPv4Addrs)
	return targets, allErr
}

func resolveControllerHostForProtection(rawBaseURL string) string {
	value := strings.TrimSpace(rawBaseURL)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || strings.TrimSpace(parsed.Host) == "" {
		if !strings.Contains(value, "://") {
			parsed, err = url.Parse("https://" + value)
		}
		if err != nil || parsed == nil {
			return ""
		}
	}
	host := strings.TrimSpace(parsed.Host)
	if host == "" {
		return ""
	}
	if splitHost, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = splitHost
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return ""
	}
	if parsedIP := net.ParseIP(host); parsedIP != nil {
		return canonicalIP(parsedIP)
	}
	return strings.ToLower(host)
}

func normalizeControlPlaneHost(rawHost string) string {
	host := strings.TrimSpace(strings.Trim(rawHost, "[]"))
	if host == "" {
		return ""
	}
	if parsedIP := net.ParseIP(host); parsedIP != nil {
		return canonicalIP(parsedIP)
	}
	return strings.ToLower(host)
}

func normalizeControlPlaneIP(rawHost string) string {
	host := strings.TrimSpace(strings.Trim(rawHost, "[]"))
	if host == "" {
		return ""
	}
	if parsedIP := net.ParseIP(host); parsedIP != nil {
		return canonicalIP(parsedIP)
	}
	return ""
}
