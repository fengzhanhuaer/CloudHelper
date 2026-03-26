package backend

import (
	"net"
	"net/url"
	"sort"
	"strings"
	"time"
)

const tunRouteRefreshInterval = 30 * time.Second

type tunSystemRouteState struct {
	AdapterIndex         int
	AdapterDNSServers    []string
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
	targets := resolveTUNControlPlaneTargets(controllerBaseURL)
	controllerHost := strings.TrimSpace(targets.ControllerHost)

	s.setControlPlaneDirectTargets(targets.Hosts, targets.IPs)
	if err := s.applyPlatformTUNSystemRouting(targets); err != nil {
		s.clearControlPlaneDirectTargets()
		return err
	}
	s.mu.Lock()
	s.tunRouteSyncedAt = time.Now()
	s.tunRouteHost = controllerHost
	s.mu.Unlock()
	return nil
}

func (s *networkAssistantService) clearTUNSystemRouting() error {
	err := s.clearPlatformTUNSystemRouting()
	s.clearControlPlaneDirectTargets()
	s.mu.Lock()
	s.tunRouteSyncedAt = time.Time{}
	s.tunRouteHost = ""
	s.mu.Unlock()
	return err
}

func (s *networkAssistantService) ensureControlPlaneDialReady(controllerBaseURL string) error {
	baseURL := strings.TrimSpace(controllerBaseURL)
	if baseURL == "" {
		return nil
	}
	host := resolveControllerHostForProtection(baseURL)

	s.mu.RLock()
	mode := s.mode
	lastHost := strings.TrimSpace(s.tunRouteHost)
	lastSyncAt := s.tunRouteSyncedAt
	s.mu.RUnlock()

	if mode != networkModeTUN {
		return nil
	}
	if host != "" && strings.EqualFold(host, lastHost) && !lastSyncAt.IsZero() && time.Since(lastSyncAt) < tunRouteRefreshInterval {
		return nil
	}
	return s.applyTUNSystemRouting(baseURL)
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

func resolveTUNControlPlaneTargets(controllerBaseURL string) tunControlPlaneTargets {
	targets := tunControlPlaneTargets{
		Hosts:     make(map[string]struct{}),
		IPs:       make(map[string]struct{}),
		IPv4Addrs: make([]string, 0),
	}
	host := resolveControllerHostForProtection(controllerBaseURL)
	if host == "" {
		return targets
	}
	targets.ControllerHost = host
	targets.Hosts[host] = struct{}{}

	if parsedIP := net.ParseIP(host); parsedIP != nil {
		canonical := canonicalIP(parsedIP)
		targets.IPs[canonical] = struct{}{}
		if parsedIP.To4() != nil {
			targets.IPv4Addrs = append(targets.IPv4Addrs, canonical)
		}
		return targets
	}

	ipList, err := net.LookupIP(host)
	if err != nil {
		return targets
	}
	seenIPv4 := make(map[string]struct{})
	for _, ipValue := range ipList {
		if ipValue == nil {
			continue
		}
		canonical := canonicalIP(ipValue)
		if canonical == "" {
			continue
		}
		targets.IPs[canonical] = struct{}{}
		if ipValue.To4() != nil {
			if _, exists := seenIPv4[canonical]; exists {
				continue
			}
			seenIPv4[canonical] = struct{}{}
			targets.IPv4Addrs = append(targets.IPv4Addrs, canonical)
		}
	}
	sort.Strings(targets.IPv4Addrs)
	return targets
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
