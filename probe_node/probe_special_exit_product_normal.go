//go:build !mihomo_exit && !linux_router

package main

func applyProbeProductRouteConfig(snapshot *probeSpecialExitSnapshot, nodeID string) error {
	return nil
}

func probeProductSpecialExitReport() probeSpecialExitRuntimeReport {
	return probeSpecialExitRuntimeReport{}
}

func startProbeProductRuntime(nodeID string) error { return nil }

func stopProbeProductRuntime() {}
