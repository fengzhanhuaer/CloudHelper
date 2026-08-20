//go:build mihomo_exit

package main

func applyProbeProductRouteConfig(snapshot *probeSpecialExitSnapshot, nodeID string) error {
	return applyProbeMihomoRouteConfig(snapshot, nodeID, true)
}

func probeProductSpecialExitReport() probeSpecialExitRuntimeReport {
	return probeMihomoSpecialExitReport()
}

func startProbeProductRuntime(nodeID string) error {
	return startProbeMihomoRuntime(nodeID, true)
}

func stopProbeProductRuntime() {
	stopProbeMihomoRuntime()
}
