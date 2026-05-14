//go:build windows

package main

import (
	"errors"
	"testing"
)

func TestProbeLocalRepairWindowsInterfaceIPv4AddressIgnoresDeleteInvalidParameter(t *testing.T) {
	deleteCalls := 0
	upsertCalls := 0
	probeLocalDeleteWindowsInterfaceIPv4 = func(interfaceIndex int, ipText string) error {
		deleteCalls++
		if interfaceIndex != 18 {
			t.Fatalf("interfaceIndex=%d, want 18", interfaceIndex)
		}
		if ipText != probeLocalTUNInterfaceIPv4 {
			t.Fatalf("ipText=%q, want %q", ipText, probeLocalTUNInterfaceIPv4)
		}
		return errors.New("DeleteUnicastIpAddressEntry failed: code=87")
	}
	probeLocalUpsertWindowsInterfaceIPv4 = func(interfaceIndex int, ipText string, prefixLength int) error {
		upsertCalls++
		if interfaceIndex != 18 {
			t.Fatalf("interfaceIndex=%d, want 18", interfaceIndex)
		}
		if ipText != probeLocalTUNInterfaceIPv4 {
			t.Fatalf("ipText=%q, want %q", ipText, probeLocalTUNInterfaceIPv4)
		}
		if prefixLength != probeLocalTUNRouteIPv4PrefixLen {
			t.Fatalf("prefixLength=%d, want %d", prefixLength, probeLocalTUNRouteIPv4PrefixLen)
		}
		return nil
	}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := probeLocalRepairWindowsInterfaceIPv4Address(18, probeLocalTUNInterfaceIPv4, probeLocalTUNRouteIPv4PrefixLen); err != nil {
		t.Fatalf("probeLocalRepairWindowsInterfaceIPv4Address returned error: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("deleteCalls=%d, want 1", deleteCalls)
	}
	if upsertCalls != 1 {
		t.Fatalf("upsertCalls=%d, want 1", upsertCalls)
	}
}

func TestProbeLocalRepairWindowsInterfaceIPv4AddressIgnoresDeleteError(t *testing.T) {
	deleteCalls := 0
	upsertCalls := 0
	probeLocalDeleteWindowsInterfaceIPv4 = func(interfaceIndex int, ipText string) error {
		deleteCalls++
		return errors.New("DeleteUnicastIpAddressEntry failed: code=5")
	}
	probeLocalUpsertWindowsInterfaceIPv4 = func(interfaceIndex int, ipText string, prefixLength int) error {
		upsertCalls++
		return nil
	}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := probeLocalRepairWindowsInterfaceIPv4Address(18, probeLocalTUNInterfaceIPv4, probeLocalTUNRouteIPv4PrefixLen); err != nil {
		t.Fatalf("probeLocalRepairWindowsInterfaceIPv4Address returned error: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("deleteCalls=%d, want 1", deleteCalls)
	}
	if upsertCalls != 1 {
		t.Fatalf("upsertCalls=%d, want 1", upsertCalls)
	}
}

func TestUpsertProbeLocalWindowsInterfaceIPv4AddressCreateOnly(t *testing.T) {
	createCalls := 0
	probeLocalCallCreateUnicastIPAddressEntry = func(row *probeLocalMIBUnicastIPAddressRow) (uintptr, error) {
		createCalls++
		if row == nil {
			t.Fatal("row is nil")
		}
		return 0, nil
	}
	t.Cleanup(func() {
		probeLocalCallCreateUnicastIPAddressEntry = probeLocalCallCreateUnicastIPAddressEntryDefault
	})

	if err := upsertProbeLocalWindowsInterfaceIPv4Address(19, "198.18.0.2", 15); err != nil {
		t.Fatalf("upsertProbeLocalWindowsInterfaceIPv4Address returned error: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("createCalls=%d, want 1", createCalls)
	}
}
