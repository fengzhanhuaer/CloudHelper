package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSaveProbeVirtualRouterLocalSettingsDisableKeepsBaseTransportAndPreservesProxy(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterLocalSettingsForTest()
	resetProbeLocalControlStateForTest()
	oldCleanup := probeVirtualRouterCleanupTakeoverRoutesForSettings
	oldEnsure := probeVirtualRouterEnsureBaseTransportForSettings
	oldRestoreDNS := probeVirtualRouterRestoreSystemDNS
	cleanupCalls := 0
	ensureCalls := 0
	restoreDNSCalls := 0
	probeVirtualRouterCleanupTakeoverRoutesForSettings = func() error {
		cleanupCalls++
		return nil
	}
	probeVirtualRouterEnsureBaseTransportForSettings = func() (string, error) {
		ensureCalls++
		return "198.18.0.7", nil
	}
	probeVirtualRouterRestoreSystemDNS = func() error {
		restoreDNSCalls++
		return nil
	}
	t.Cleanup(func() {
		probeVirtualRouterCleanupTakeoverRoutesForSettings = oldCleanup
		probeVirtualRouterEnsureBaseTransportForSettings = oldEnsure
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
	persistProbeLocalTUNStateBestEffort(true, true)

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
	if cleanupCalls != 1 || ensureCalls != 1 {
		t.Fatalf("takeover cleanup calls=%d base transport ensure calls=%d, want 1 each", cleanupCalls, ensureCalls)
	}
	if restoreDNSCalls != 1 {
		t.Fatalf("system dns restore calls=%d want 1", restoreDNSCalls)
	}
	probeLocalControl.mu.Lock()
	tunState := probeLocalControl.tun
	probeLocalControl.mu.Unlock()
	if !tunState.Enabled || !tunState.DataPlane {
		t.Fatalf("tun runtime state not preserved: %+v", tunState)
	}
	persisted, err := loadProbeLocalTUNStateFile()
	if err != nil {
		t.Fatalf("load persisted tun state: %v", err)
	}
	if !persisted.TUN.Enabled {
		t.Fatalf("persisted tun state not preserved: %+v", persisted.TUN)
	}
}

func TestSaveProbeVirtualRouterLocalSettingsDisableReportsDNSRestoreFailure(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterLocalSettingsForTest()
	oldCleanup := probeVirtualRouterCleanupTakeoverRoutesForSettings
	oldEnsure := probeVirtualRouterEnsureBaseTransportForSettings
	oldRestoreDNS := probeVirtualRouterRestoreSystemDNS
	probeVirtualRouterCleanupTakeoverRoutesForSettings = func() error { return nil }
	probeVirtualRouterEnsureBaseTransportForSettings = func() (string, error) { return "198.18.0.7", nil }
	probeVirtualRouterRestoreSystemDNS = func() error { return errors.New("dns restore failed") }
	t.Cleanup(func() {
		probeVirtualRouterCleanupTakeoverRoutesForSettings = oldCleanup
		probeVirtualRouterEnsureBaseTransportForSettings = oldEnsure
		probeVirtualRouterRestoreSystemDNS = oldRestoreDNS
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	settings := defaultProbeVirtualRouterLocalSettings()
	settings.VirtualRouterEnabled = true
	settings.VirtualDNSEnabled = true
	probeVirtualRouterLocalSettingsState.mu.Lock()
	probeVirtualRouterLocalSettingsState.loaded = true
	probeVirtualRouterLocalSettingsState.settings = settings
	probeVirtualRouterLocalSettingsState.mu.Unlock()

	settings.VirtualRouterEnabled = false
	settings.VirtualDNSEnabled = false
	_, err := saveProbeVirtualRouterLocalSettings(settings)
	if err == nil || !strings.Contains(err.Error(), "dns restore failed") {
		t.Fatalf("disable virtual router dns error=%v", err)
	}
}

func TestProbeVirtualRouterBaseTransportEnabledWhenLocalEntryDisabled(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeVirtualRouterLocalSettingsForTest)

	probeVirtualRouterLocalSettingsState.mu.Lock()
	probeVirtualRouterLocalSettingsState.loaded = true
	probeVirtualRouterLocalSettingsState.settings = probeVirtualRouterLocalSettings{VirtualRouterEnabled: false}
	probeVirtualRouterLocalSettingsState.mu.Unlock()
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = probeVirtualRouterConfig{Enabled: true}
	probeVirtualRouterState.localNodeID = "9"
	probeVirtualRouterState.localIP = "198.18.0.7"
	probeVirtualRouterState.mu.Unlock()

	if !probeVirtualRouterBaseTransportEnabled() {
		t.Fatal("base transport should remain enabled when only local global interception is disabled")
	}
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config.Enabled = false
	probeVirtualRouterState.mu.Unlock()
	if probeVirtualRouterBaseTransportEnabled() {
		t.Fatal("base transport should be disabled when controller virtual router config is disabled")
	}
}

func TestReconcileProbeVirtualRouterLocalEntryRuntimeCancelsPendingRetry(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	oldDelays := probeVirtualRouterLocalInterfaceRetryDelays
	oldCleanup := probeVirtualRouterCleanupTakeoverRoutesForSettings
	oldEnsure := probeVirtualRouterEnsureBaseTransportForSettings
	probeVirtualRouterLocalInterfaceRetryDelays = []time.Duration{time.Hour}
	probeVirtualRouterCleanupTakeoverRoutesForSettings = func() error { return nil }
	probeVirtualRouterEnsureBaseTransportForSettings = func() (string, error) { return "198.18.0.7", nil }
	t.Cleanup(func() {
		cancelAndWaitProbeVirtualRouterLocalInterfaceIPRetry()
		probeVirtualRouterLocalInterfaceRetryDelays = oldDelays
		probeVirtualRouterCleanupTakeoverRoutesForSettings = oldCleanup
		probeVirtualRouterEnsureBaseTransportForSettings = oldEnsure
		resetProbeVirtualRouterStateForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	enableProbeVirtualRouterLocalSettingsForTest(true, false)
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = probeVirtualRouterConfig{Enabled: true}
	probeVirtualRouterState.localNodeID = "9"
	probeVirtualRouterState.localIP = "198.18.0.7"
	probeVirtualRouterState.mu.Unlock()
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
