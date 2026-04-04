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
	}

	if err := s.applyPlatformTUNSystemRouting(targets); err != nil {
		s.logf("tun routing apply failed in platform routing stage: err=%v elapsed=%s", err, time.Since(startedAt))
		return err
	}
	if err := s.startInternalDNSServer(); err != nil {
		_ = s.clearPlatformTUNSystemRouting()
		s.clearDNSRouteHints()
		s.logf("tun routing apply failed in internal dns stage: err=%v elapsed=%s", err, time.Since(startedAt))
		return err
	}
	s.mu.Lock()
	s.tunRouteSyncedAt = time.Now()
	s.tunRouteHost = targets.ControllerHost
	s.mu.Unlock()
	s.logf("tun routing apply success: elapsed=%s", time.Since(startedAt))
	return nil
}

func (s *networkAssistantService) clearTUNSystemRouting() error {
	errDNS := s.stopInternalDNSServer()
	err := s.clearPlatformTUNSystemRouting()
	s.clearDNSRouteHints()
	s.mu.Lock()
	s.tunRouteSyncedAt = time.Time{}
	s.tunRouteHost = ""
	s.tunRouteSyncing = false
	s.mu.Unlock()
	return errors.Join(errDNS, err)
}

func (s *networkAssistantService) ensureControlPlaneDialReady(_ string) error {
	needRefresh := false
	s.mu.Lock()
	mode := s.mode
	tunEnabled := s.tunEnabled
	lastSyncAt := s.tunRouteSyncedAt
	if mode == networkModeTUN && tunEnabled {
		if (lastSyncAt.IsZero() || time.Since(lastSyncAt) >= tunRouteRefreshInterval) && !s.tunRouteSyncing {
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

	for host := range targets.Hosts {
		if cachedIP, ok := getProbeDNSCachedIP(host); ok {
			addIP(cachedIP)
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
