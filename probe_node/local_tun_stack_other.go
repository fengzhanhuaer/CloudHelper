//go:build !windows

package main

import "net"

type probeLocalTUNUDPBridgeMonitorStats struct {
	Active int64                               `json:"active"`
	Direct int64                               `json:"direct"`
	Tunnel int64                               `json:"tunnel"`
	Opened int64                               `json:"opened"`
	Closed int64                               `json:"closed"`
	Items  []probeLocalTUNUDPBridgeMonitorItem `json:"items,omitempty"`
}

type probeLocalTUNUDPBridgeMonitorItem struct {
	ID          string `json:"id"`
	Target      string `json:"target,omitempty"`
	RouteTarget string `json:"route_target,omitempty"`
	Group       string `json:"group,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	Direct      bool   `json:"direct"`
	TimeoutMS   int64  `json:"timeout_ms"`
	OpenedAt    string `json:"opened_at,omitempty"`
	LastActive  string `json:"last_active,omitempty"`
	AgeMS       int64  `json:"age_ms"`
	IdleMS      int64  `json:"idle_ms"`
	BytesUp     int64  `json:"bytes_up,omitempty"`
	BytesDown   int64  `json:"bytes_down,omitempty"`
}

type probeLocalTUNTCPDirectFailureCacheStats struct {
	Active int   `json:"active"`
	Hits   int64 `json:"hits"`
	Stored int64 `json:"stored"`
}

func startProbeLocalTUNPacketStack() error { return nil }
func stopProbeLocalTUNPacketStack() error  { return nil }

func ensureProbeLocalExplicitDirectBypassForTarget(string) error { return nil }
func ensureProbeLocalFallbackDirectBypassForTarget(string) error { return nil }
func releaseProbeLocalAllDirectBypassRoutes()                    {}
func releaseProbeLocalManagedDirectBypassRoutes()                {}

func isProbeLocalTUNLocalOrDiscoveryIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		case ip4[0] == 169 && ip4[1] == 254:
			return true
		default:
			return false
		}
	}
	return ip.IsPrivate()
}

func snapshotProbeLocalTUNUDPBridgeMonitorStats() probeLocalTUNUDPBridgeMonitorStats {
	return probeLocalTUNUDPBridgeMonitorStats{}
}

func snapshotProbeLocalTUNTCPDirectFailureCacheStats() probeLocalTUNTCPDirectFailureCacheStats {
	return probeLocalTUNTCPDirectFailureCacheStats{}
}
