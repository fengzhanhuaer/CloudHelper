package backend

import (
	"errors"
	"net"
	"net/url"
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
	// 仅使用按连接动态 bypass（acquireTUNDirectBypassRoute），
	// 不再对主控/探针域名做软件内置独立直连保护。
	targets := tunControlPlaneTargets{IPv4Addrs: make([]string, 0)}
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
	s.tunRouteHost = ""
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
