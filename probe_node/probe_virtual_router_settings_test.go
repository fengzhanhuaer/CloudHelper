package main

import (
	"errors"
	"testing"
	"time"
)

func TestSaveProbeVirtualRouterLocalSettingsDisableStopsTUNAndPreservesProxy(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterLocalSettingsForTest()
	resetProbeLocalControlStateForTest()
	oldCleanup := probeVirtualRouterCleanupPlatformRoutesForSettings
	oldStop := probeVirtualRouterStopTUNDataPlaneForSettings
	oldRestoreDNS := probeVirtualRouterRestoreSystemDNS
	cleanupCalls := 0
	stopCalls := 0
	probeVirtualRouterCleanupPlatformRoutesForSettings = func() error {
		cleanupCalls++
		return nil
	}
	probeVirtualRouterStopTUNDataPlaneForSettings = func() error {
		stopCalls++
		return nil
	}
	probeVirtualRouterRestoreSystemDNS = func() error { return nil }
	t.Cleanup(func() {
		probeVirtualRouterCleanupPlatformRoutesForSettings = oldCleanup
		probeVirtualRouterStopTUNDataPlaneForSettings = oldStop
		probeVirtualRouterRestoreSystemDNS = oldRestoreDNS
		resetProbeVirtualRouterLocalSettingsForTest()
		resetProbeLocalControlStateForTest()
	})

	settings := defaultProbeVirtualRouterLocalSettings()
	settings.VirtualRouterEnabled = true
	settings.VirtualDNSEnabled = true
	settings.ProxyEnabled = true
	probeVirtualRouterLocalSettingsState.mu.Lock()
	probeVirtualRouterLocalSettingsState.loaded = true
	probeVirtualRouterLocalSettingsState.settings = settings
	probeVirtualRouterLocalSettingsState.mu.Unlock()
	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.tun.Enabled = true
	probeLocalControl.tun.DataPlane = true
	probeLocalControl.mu.Unlock()

	settings.VirtualRouterEnabled = false
	settings.VirtualDNSEnabled = false
	saved, err := saveProbeVirtualRouterLocalSettingsWithoutProxyReconcile(settings)
	if err != nil {
		t.Fatalf("disable virtual router settings: %v", err)
	}
	if saved.VirtualRouterEnabled || saved.VirtualDNSEnabled {
		t.Fatalf("virtual router settings not disabled: %+v", saved)
	}
	if !saved.ProxyEnabled {
		t.Fatalf("independent proxy switch was changed: %+v", saved)
	}
	if cleanupCalls != 1 || stopCalls != 1 {
		t.Fatalf("cleanup calls=%d stop calls=%d, want 1 each", cleanupCalls, stopCalls)
	}
	probeLocalControl.mu.Lock()
	tunState := probeLocalControl.tun
	probeLocalControl.mu.Unlock()
	if tunState.Enabled || tunState.DataPlane {
		t.Fatalf("tun runtime state not disabled: %+v", tunState)
	}
	persisted, err := loadProbeLocalTUNStateFile()
	if err != nil {
		t.Fatalf("load persisted tun state: %v", err)
	}
	if persisted.TUN.Enabled {
		t.Fatalf("persisted tun state still enabled: %+v", persisted.TUN)
	}
}

func TestEnsureProbeVirtualRouterLocalInterfaceIPOnceSkipsWhenEntryDisabled(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeVirtualRouterLocalSettingsForTest)

	probeVirtualRouterLocalSettingsState.mu.Lock()
	probeVirtualRouterLocalSettingsState.loaded = true
	probeVirtualRouterLocalSettingsState.settings = probeVirtualRouterLocalSettings{VirtualRouterEnabled: false}
	probeVirtualRouterLocalSettingsState.mu.Unlock()
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.localNodeID = "9"
	probeVirtualRouterState.localIP = "198.18.0.7"
	probeVirtualRouterState.mu.Unlock()

	localIP, err := ensureProbeVirtualRouterLocalInterfaceIPOnce()
	if err != nil {
		t.Fatalf("disabled ensure returned error: %v", err)
	}
	if localIP != "" {
		t.Fatalf("disabled ensure local ip=%q, want empty", localIP)
	}
}

func TestReconcileProbeVirtualRouterLocalEntryRuntimeCancelsPendingRetry(t *testing.T) {
	resetProbeVirtualRouterLocalSettingsForTest()
	oldDelays := probeVirtualRouterLocalInterfaceRetryDelays
	oldCleanup := probeVirtualRouterCleanupPlatformRoutesForSettings
	oldStop := probeVirtualRouterStopTUNDataPlaneForSettings
	probeVirtualRouterLocalInterfaceRetryDelays = []time.Duration{time.Hour}
	probeVirtualRouterCleanupPlatformRoutesForSettings = func() error { return nil }
	probeVirtualRouterStopTUNDataPlaneForSettings = func() error { return nil }
	t.Cleanup(func() {
		cancelAndWaitProbeVirtualRouterLocalInterfaceIPRetry()
		probeVirtualRouterLocalInterfaceRetryDelays = oldDelays
		probeVirtualRouterCleanupPlatformRoutesForSettings = oldCleanup
		probeVirtualRouterStopTUNDataPlaneForSettings = oldStop
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	enableProbeVirtualRouterLocalSettingsForTest(true, false)
	scheduleProbeVirtualRouterLocalInterfaceIPRetry("198.18.0.7", errors.New("test retry"))
	deadline := time.Now().Add(time.Second)
	for {
		probeVirtualRouterLocalInterfaceRetryState.mu.Lock()
		running := probeVirtualRouterLocalInterfaceRetryState.running
		probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry worker did not start")
		}
		time.Sleep(time.Millisecond)
	}

	enableProbeVirtualRouterLocalSettingsForTest(false, false)
	startedAt := time.Now()
	if err := reconcileProbeVirtualRouterLocalEntryRuntime(loadProbeVirtualRouterLocalSettings()); err != nil {
		t.Fatalf("reconcile disabled local entry: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("retry cancellation took too long: %s", elapsed)
	}
	probeVirtualRouterLocalInterfaceRetryState.mu.Lock()
	running := probeVirtualRouterLocalInterfaceRetryState.running
	probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()
	if running {
		t.Fatal("retry worker still running after disable")
	}
}
