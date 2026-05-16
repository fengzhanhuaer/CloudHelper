//go:build !windows

package main

type probeLocalTUNUDPBridgeMonitorStats struct {
	Active int64 `json:"active"`
	Opened int64 `json:"opened"`
	Closed int64 `json:"closed"`
}

func startProbeLocalTUNPacketStack() error { return nil }
func stopProbeLocalTUNPacketStack() error  { return nil }

func ensureProbeLocalExplicitDirectBypassForTarget(string) error { return nil }
func releaseProbeLocalAllDirectBypassRoutes()                    {}
func releaseProbeLocalManagedDirectBypassRoutes()                {}

func snapshotProbeLocalTUNUDPBridgeMonitorStats() probeLocalTUNUDPBridgeMonitorStats {
	return probeLocalTUNUDPBridgeMonitorStats{}
}
