//go:build windows

package main

import (
	"errors"
	"fmt"
	"time"
)

const (
	probeLocalTUNAdapterName        = "Maple"
	probeLocalTUNAdapterDescription = "Maple Virtual Network Adapter"
	probeLocalTUNTunnelType         = "Maple"
)

var (
	probeLocalEnsureWintunLibrary = ensureProbeEmbeddedWintunLibrary
	probeLocalResolveWintunPath   = resolveProbeWintunPath
	probeLocalDetectWintunAdapter = detectProbeLocalWintunAdapter
	probeLocalCreateWintunAdapter = createProbeLocalWintunAdapter
	probeLocalCloseWintunAdapter  = closeProbeLocalWintunAdapter
	probeLocalTUNInstallSleep     = time.Sleep
)

func installProbeLocalTUNDriver() error {
	if err := probeLocalEnsureWintunLibrary(); err != nil {
		return fmt.Errorf("prepare wintun library: %w", err)
	}

	if exists, err := probeLocalDetectWintunAdapter(); err == nil && exists {
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

	var detectErr error
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
			return nil
		}
	}
	if detectErr != nil {
		return fmt.Errorf("verify wintun adapter after install: %w", detectErr)
	}
	return errors.New("wintun adapter is not detected after install")
}

func resetProbeLocalTUNInstallWindowsHooksForTest() {
	probeLocalEnsureWintunLibrary = ensureProbeEmbeddedWintunLibrary
	probeLocalResolveWintunPath = resolveProbeWintunPath
	probeLocalDetectWintunAdapter = detectProbeLocalWintunAdapter
	probeLocalCreateWintunAdapter = createProbeLocalWintunAdapter
	probeLocalCloseWintunAdapter = closeProbeLocalWintunAdapter
	probeLocalTUNInstallSleep = time.Sleep
}
