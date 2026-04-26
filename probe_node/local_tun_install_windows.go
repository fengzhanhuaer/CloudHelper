//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	probeLocalTUNAdapterName          = "Maple"
	probeLocalTUNAdapterDescription   = "Maple Virtual Network Adapter"
	probeLocalTUNTunnelType           = "Maple"
	probeLocalTUNAdapterRequestedGUID = "{6BA2B7A3-1C2D-4E63-9E3C-6F7A8B9C0D21}"
	probeLocalTUNRouteGatewayIPv4     = "198.18.0.1"
	probeLocalTUNRouteIPv4PrefixLen   = 15
)

var (
	probeLocalEnsureWintunLibrary      = ensureProbeEmbeddedWintunLibrary
	probeLocalResolveWintunPath        = resolveProbeWintunPath
	probeLocalDetectWintunAdapter      = detectProbeLocalWintunAdapter
	probeLocalCreateWintunAdapter      = createProbeLocalWintunAdapter
	probeLocalCloseWintunAdapter       = closeProbeLocalWintunAdapter
	probeLocalEnsureWindowsRouteTarget = ensureProbeLocalWindowsRouteTargetConfigured
	probeLocalTUNInstallSleep          = time.Sleep
)

func installProbeLocalTUNDriver() error {
	if err := probeLocalEnsureWintunLibrary(); err != nil {
		return fmt.Errorf("prepare wintun library: %w", err)
	}

	if exists, err := probeLocalDetectWintunAdapter(); err == nil && exists {
		if routeErr := probeLocalEnsureWindowsRouteTarget(); routeErr != nil {
			return fmt.Errorf("configure wintun adapter route target: %w", routeErr)
		}
		return nil
	}

	libraryPath, err := probeLocalResolveWintunPath()
	if err != nil {
		return fmt.Errorf("resolve wintun library path: %w", err)
	}

	handle, err := probeLocalCreateWintunAdapter(libraryPath, probeLocalTUNAdapterName, probeLocalTUNTunnelType)
	if err != nil {
		return fmt.Errorf("create/open wintun adapter: %w", err)
	}
	if handle != 0 {
		if closeErr := probeLocalCloseWintunAdapter(libraryPath, handle); closeErr != nil {
			logProbeWarnf("close wintun adapter handle failed: %v", closeErr)
		}
	}

	var (
		detectErr error
		detected  bool
	)
	for _, delay := range []time.Duration{0, 200 * time.Millisecond, 450 * time.Millisecond, 800 * time.Millisecond} {
		if delay > 0 {
			probeLocalTUNInstallSleep(delay)
		}
		exists, err := probeLocalDetectWintunAdapter()
		if err != nil {
			detectErr = err
			continue
		}
		if exists {
			detected = true
			break
		}
	}
	if !detected {
		if detectErr != nil {
			return fmt.Errorf("verify wintun adapter after install: %w", detectErr)
		}
		return errors.New("wintun adapter is not detected after install")
	}
	if ensureErr := probeLocalEnsureWindowsRouteTarget(); ensureErr != nil {
		return fmt.Errorf("configure wintun adapter route target: %w", ensureErr)
	}
	return nil
}

func ensureProbeLocalWindowsRouteTargetConfigured() error {
	adapter, exists, err := findProbeLocalWintunAdapter()
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("wintun adapter is not detected after install")
	}
	if adapter.InterfaceIndex <= 0 {
		return errors.New("invalid wintun adapter interface index")
	}
	if err := ensureProbeLocalWindowsInterfaceIPv4Address(adapter.InterfaceIndex, probeLocalTUNRouteGatewayIPv4, probeLocalTUNRouteIPv4PrefixLen); err != nil {
		return err
	}
	_ = os.Setenv("PROBE_LOCAL_TUN_GATEWAY", probeLocalTUNRouteGatewayIPv4)
	_ = os.Setenv("PROBE_LOCAL_TUN_IF_INDEX", strconv.Itoa(adapter.InterfaceIndex))
	return nil
}

func resetProbeLocalTUNInstallWindowsHooksForTest() {
	probeLocalEnsureWintunLibrary = ensureProbeEmbeddedWintunLibrary
	probeLocalResolveWintunPath = resolveProbeWintunPath
	probeLocalDetectWintunAdapter = detectProbeLocalWintunAdapter
	probeLocalCreateWintunAdapter = createProbeLocalWintunAdapter
	probeLocalCloseWintunAdapter = closeProbeLocalWintunAdapter
	probeLocalEnsureWindowsRouteTarget = ensureProbeLocalWindowsRouteTargetConfigured
	probeLocalTUNInstallSleep = time.Sleep
}
