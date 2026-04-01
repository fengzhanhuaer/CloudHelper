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

func (s *networkAssistantService) applyTUNSystemRouting(controllerBaseURL string) error {
	chainHosts := s.collectProbeChainDirectHosts()
	targets := resolveTUNControlPlaneTargets(controllerBaseURL, chainHosts)
	controllerHost := strings.TrimSpace(targets.ControllerHost)

	s.setControlPlaneDirectTargets(targets.Hosts, targets.IPs)
	if err := s.applyPlatformTUNSystemRouting(targets); err != nil {
		s.clearControlPlaneDirectTargets()
		return err
	}
	if err := s.startInternalDNSServer(); err != nil {
		_ = s.clearPlatformTUNSystemRouting()
		s.clearControlPlaneDirectTargets()
		s.clearDNSRouteHints()
		return err
	}
	s.mu.Lock()
	s.tunRouteSyncedAt = time.Now()
	s.tunRouteHost = controllerHost
	s.mu.Unlock()
	return nil
}

func (s *networkAssistantService) clearTUNSystemRouting() error {
	errDNS := s.stopInternalDNSServer()
	err := s.clearPlatformTUNSystemRouting()
	s.clearControlPlaneDirectTargets()
	s.clearDNSRouteHints()
	s.mu.Lock()
	s.tunRouteSyncedAt = time.Time{}
	s.tunRouteHost = ""
	s.tunRouteSyncing = false
	s.mu.Unlock()
	return errors.Join(errDNS, err)
}

func (s *networkAssistantService) ensureControlPlaneDialReady(controllerBaseURL string) error {
	baseURL := strings.TrimSpace(controllerBaseURL)
	if baseURL == "" {
		return nil
	}
	host := resolveControllerHostForProtection(baseURL)

	needRefresh := false
	s.mu.Lock()
	mode := s.mode
	tunEnabled := s.tunEnabled
	lastHost := strings.TrimSpace(s.tunRouteHost)
	lastSyncAt := s.tunRouteSyncedAt
	if mode == networkModeTUN && tunEnabled {
		if !(host != "" && strings.EqualFold(host, lastHost) && !lastSyncAt.IsZero() && time.Since(lastSyncAt) < tunRouteRefreshInterval) {
			if !s.tunRouteSyncing {
				s.tunRouteSyncing = true
				needRefresh = true
			}
		}
	}
	s.mu.Unlock()

	if !needRefresh {
		return nil
	}
	err := s.applyTUNSystemRouting(baseURL)
	s.mu.Lock()
	s.tunRouteSyncing = false
	mode = s.mode
	tunEnabled = s.tunEnabled
	s.mu.Unlock()
	if err != nil && mode == networkModeTUN && tunEnabled {
		return s.fallbackToDirectModeOnTUNRoutingFailure("refresh tun direct routes failed", err)
	}
	return err
}

func (s *networkAssistantService) setControlPlaneDirectTargets(hosts map[string]struct{}, ips map[string]struct{}) {
	copyHosts := make(map[string]struct{}, len(hosts))
	for host := range hosts {
		clean := normalizeControlPlaneHost(host)
		if clean == "" {
			continue
		}
		copyHosts[clean] = struct{}{}
	}

	copyIPs := make(map[string]struct{}, len(ips))
	for ipValue := range ips {
		clean := normalizeControlPlaneIP(ipValue)
		if clean == "" {
			continue
		}
		copyIPs[clean] = struct{}{}
	}

	s.mu.Lock()
	s.controlPlaneHosts = copyHosts
	s.controlPlaneIPs = copyIPs
	s.mu.Unlock()
}

func (s *networkAssistantService) clearControlPlaneDirectTargets() {
	s.mu.Lock()
	s.controlPlaneHosts = make(map[string]struct{})
	s.controlPlaneIPs = make(map[string]struct{})
	s.mu.Unlock()
}

func (s *networkAssistantService) isControlPlaneDirectTarget(targetHost string) bool {
	cleanHost := normalizeControlPlaneHost(targetHost)
	cleanIP := normalizeControlPlaneIP(targetHost)

	s.mu.RLock()
	_, hostMatched := s.controlPlaneHosts[cleanHost]
	_, ipMatched := s.controlPlaneIPs[cleanIP]
	s.mu.RUnlock()

	return hostMatched || ipMatched
}

func (s *networkAssistantService) collectProbeChainDirectHosts() []string {
	s.mu.RLock()
	targets := copyProbeChainTargets(s.chainTargets)
	s.mu.RUnlock()
	hosts := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, endpoint := range targets {
		host := normalizeControlPlaneHost(endpoint.EntryHost)
		if host == "" {
			continue
		}
		if _, exists := seen[host]; exists {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

func resolveTUNControlPlaneTargets(controllerBaseURL string, additionalHosts []string) tunControlPlaneTargets {
	targets := tunControlPlaneTargets{
		Hosts:     make(map[string]struct{}),
		IPs:       make(map[string]struct{}),
		IPv4Addrs: make([]string, 0),
	}
	host := resolveControllerHostForProtection(controllerBaseURL)
	if host != "" {
		targets.ControllerHost = host
		// 如果配置了主控 IP，直接注入该 IP，跳过 DNS 解析，避免 TUN 模式下解析超时。
		if overrideIP := loadManagerControllerIP(); overrideIP != "" {
			addProtectedHostToTUNTargets(&targets, overrideIP)
		} else {
			addProtectedHostToTUNTargets(&targets, host)
		}
	}
	for _, extraHost := range additionalHosts {
		addProtectedHostToTUNTargets(&targets, extraHost)
	}
	sort.Strings(targets.IPv4Addrs)
	return targets
}

func addProtectedHostToTUNTargets(targets *tunControlPlaneTargets, host string) {
	if targets == nil {
		return
	}
	cleanHost := normalizeControlPlaneHost(host)
	if cleanHost == "" {
		return
	}
	targets.Hosts[cleanHost] = struct{}{}

	if parsedIP := net.ParseIP(cleanHost); parsedIP != nil {
		canonical := canonicalIP(parsedIP)
		if canonical == "" {
			return
		}
		targets.IPs[canonical] = struct{}{}
		if parsedIP.To4() != nil {
			if !containsString(targets.IPv4Addrs, canonical) {
				targets.IPv4Addrs = append(targets.IPv4Addrs, canonical)
			}
		}
		return
	}

	addIP := func(ipValue net.IP) {
		if ipValue == nil {
			return
		}
		canonical := canonicalIP(ipValue)
		if canonical == "" {
			return
		}
		targets.IPs[canonical] = struct{}{}
		if ipValue.To4() != nil {
			if !containsString(targets.IPv4Addrs, canonical) {
				targets.IPv4Addrs = append(targets.IPv4Addrs, canonical)
			}
		}
	}

	// 优先查直连 DNS 缓存（与 buildAdminWSDialer 共用同一缓存）。
	// TUN 激活后系统 DNS 被接管，net.LookupIP 会 i/o timeout，
	// 而缓存中已有主控/链路节点的 IP，直接使用即可避免超时。
	if cachedIP, ok := getProbeDNSCachedIP(cleanHost); ok {
		if parsed := net.ParseIP(cachedIP); parsed != nil {
			addIP(parsed)
			return
		}
	}

	// 缓存未命中时走系统 DNS（TUN 未激活时仍可正常解析）。
	ipList, err := net.LookupIP(cleanHost)
	if err != nil {
		// TUN 已激活时系统 DNS 不可用，跳过解析；不阻塞后续流程。
		return
	}
	for _, ipValue := range ipList {
		addIP(ipValue)
	}
	// 回填缓存，供后续 TUN 激活期间直接命中。
	if len(ipList) > 0 {
		for _, ipValue := range ipList {
			if ipValue.To4() != nil {
				_ = setProbeDNSCachedIP(cleanHost, ipValue.String())
				break
			}
		}
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
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
