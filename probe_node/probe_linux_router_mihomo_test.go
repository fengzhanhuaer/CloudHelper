//go:build linux_router

package main

import "testing"

func TestLinuxRouterStartsWithoutMihomoAndDoesNotLeakConfiguredExit(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	if err := startProbeProductRuntime("7"); err != nil {
		t.Fatalf("router startup without Mihomo snapshot failed: %v", err)
	}
	t.Cleanup(stopProbeProductRuntime)
	if !probeLinuxRouterUsesDirectExit() || !probeProductAllowsPhysicalICMPExit() {
		t.Fatal("unconfigured router must preserve its direct exit behavior")
	}

	activeProbeMihomoRuntime.mu.Lock()
	activeProbeMihomoRuntime.runtime = probeMihomoExitRuntimeConfig{AppliedRevision: 1, Healthy: false}
	activeProbeMihomoRuntime.mu.Unlock()
	if probeLinuxRouterUsesDirectExit() || probeProductAllowsPhysicalICMPExit() {
		t.Fatal("configured but unhealthy Mihomo must fail closed instead of using direct exit")
	}
	if err := applyProbeProductRouteConfig(nil, "7"); err != nil {
		t.Fatalf("disabling optional Mihomo failed: %v", err)
	}
	if !probeLinuxRouterUsesDirectExit() {
		t.Fatal("router direct exit was not restored after Mihomo configuration removal")
	}
}
