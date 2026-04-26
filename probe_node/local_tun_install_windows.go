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
	probeLocalFindWintunAdapter        = findProbeLocalWintunAdapter
	probeLocalCreateWintunAdapter      = createProbeLocalWintunAdapter
	probeLocalCloseWintunAdapter       = closeProbeLocalWintunAdapter
	probeLocalEnsureWindowsRouteTarget = ensureProbeLocalWindowsRouteTargetConfigured
	probeLocalIsWindowsAdmin           = isWindowsAdmin
	probeLocalRelaunchAsAdminInstall   = relaunchAsAdminForProbeLocalTUNInstall
	probeLocalTUNInstallSleep          = time.Sleep
)

func installProbeLocalTUNDriver() error {
	if err := probeLocalEnsureWintunLibrary(); err != nil {
		return fmt.Errorf("prepare wintun library: %w", err)
	}
	if !probeLocalIsWindowsAdmin() {
		if elevatedErr := installProbeLocalTUNDriverViaElevation(); elevatedErr != nil {
			return elevatedErr
		}
		return nil
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

	createdOrOpened := handle != 0
	var (
		detectErr error
		detected  bool
	)
	for _, delay := range []time.Duration{0, 200 * time.Millisecond, 450 * time.Millisecond, 800 * time.Millisecond, 1200 * time.Millisecond, 1800 * time.Millisecond} {
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
	if detected {
		if ensureErr := probeLocalEnsureWindowsRouteTarget(); ensureErr != nil {
			return fmt.Errorf("configure wintun adapter route target: %w", ensureErr)
		}
		return nil
	}
	if detectErr != nil {
		if createdOrOpened {
			logProbeWarnf("verify wintun adapter after install got transient errors, continue: %v", detectErr)
			return nil
		}
		return fmt.Errorf("verify wintun adapter after install: %w", detectErr)
	}
	if createdOrOpened {
		logProbeWarnf("wintun adapter is not detected after install, treat as success because adapter handle was created/opened")
		return nil
	}
	return errors.New("wintun adapter is not detected after install")
}

func installProbeLocalTUNDriverViaElevation() error {
	err := probeLocalRelaunchAsAdminInstall()
	if err != nil && !errors.Is(err, ErrProbeLocalRelaunchAsAdmin) {
		return fmt.Errorf("request administrator elevation failed: %w", err)
	}
	var detectErr error
	for _, delay := range []time.Duration{300 * time.Millisecond, 500 * time.Millisecond, 800 * time.Millisecond, 1200 * time.Millisecond, 1800 * time.Millisecond, 2500 * time.Millisecond, 3500 * time.Millisecond} {
		if delay > 0 {
			probeLocalTUNInstallSleep(delay)
		}
		adapter, exists, findErr := probeLocalFindWintunAdapter()
		if findErr != nil {
			detectErr = findErr
			continue
		}
		if !exists {
			continue
		}
		if adapter.InterfaceIndex > 0 {
			setProbeLocalWindowsRouteTargetEnv(adapter.InterfaceIndex)
		}
		return nil
	}
	if detectErr != nil {
		return fmt.Errorf("wait elevated tun install result failed: %w", detectErr)
	}
	return errors.New("wait elevated tun install result timeout")
}

func setProbeLocalWindowsRouteTargetEnv(interfaceIndex int) {
	_ = os.Setenv("PROBE_LOCAL_TUN_GATEWAY", probeLocalTUNRouteGatewayIPv4)
	if interfaceIndex > 0 {
		_ = os.Setenv("PROBE_LOCAL_TUN_IF_INDEX", strconv.Itoa(interfaceIndex))
	}
}

func ensureProbeLocalWindowsRouteTargetConfigured() error {
	adapter, exists, err := probeLocalFindWintunAdapter()
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("wintun adapter is not detected after install")
	}
	if adapter.InterfaceIndex <= 0 {
		if adapterLUID, luidExists, luidErr := findProbeLocalWintunAdapterLUID(); luidErr == nil && luidExists {
			ifIndex, convertErr := convertProbeLocalInterfaceLUIDToIndex(adapterLUID)
			if convertErr == nil && ifIndex > 0 {
				adapter.InterfaceIndex = ifIndex
			}
		}
	}
	if adapter.InterfaceIndex <= 0 {
		return errors.New("invalid wintun adapter interface index")
	}
	if err := ensureProbeLocalWindowsInterfaceIPv4Address(adapter.InterfaceIndex, probeLocalTUNRouteGatewayIPv4, probeLocalTUNRouteIPv4PrefixLen); err != nil {
		return err
	}
	setProbeLocalWindowsRouteTargetEnv(adapter.InterfaceIndex)
	return nil
}

func resetProbeLocalTUNInstallWindowsHooksForTest() {
	probeLocalEnsureWintunLibrary = ensureProbeEmbeddedWintunLibrary
	probeLocalResolveWintunPath = resolveProbeWintunPath
	probeLocalDetectWintunAdapter = detectProbeLocalWintunAdapter
	probeLocalFindWintunAdapter = findProbeLocalWintunAdapter
	probeLocalCreateWintunAdapter = createProbeLocalWintunAdapter
	probeLocalCloseWintunAdapter = closeProbeLocalWintunAdapter
	probeLocalEnsureWindowsRouteTarget = ensureProbeLocalWindowsRouteTargetConfigured
	probeLocalIsWindowsAdmin = isWindowsAdmin
	probeLocalRelaunchAsAdminInstall = relaunchAsAdminForProbeLocalTUNInstall
	probeLocalTUNInstallSleep = time.Sleep
}
