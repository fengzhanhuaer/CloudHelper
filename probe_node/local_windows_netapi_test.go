//go:build windows

package main

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
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

func TestSetProbeLocalWindowsInterfaceDNSFallsBackToQWords(t *testing.T) {
	ptrCalls := 0
	qwordCalls := 0
	dwordCalls := 0
	probeLocalCallSetInterfaceDNSSettingsByPtr = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		ptrCalls++
		return 87, nil
	}
	probeLocalCallSetInterfaceDNSSettingsByQW = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		qwordCalls++
		if guid == nil || settings == nil {
			t.Fatal("guid/settings nil")
		}
		if settings.Flags != probeLocalDNSSettingNameServer|probeLocalDNSSettingSearchList {
			t.Fatalf("flags=%d", settings.Flags)
		}
		return 0, nil
	}
	probeLocalCallSetInterfaceDNSSettingsByDW = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		dwordCalls++
		return 0, nil
	}
	t.Cleanup(func() {
		probeLocalCallSetInterfaceDNSSettingsByPtr = probeLocalCallSetInterfaceDNSSettingsByPtrDefault
		probeLocalCallSetInterfaceDNSSettingsByQW = probeLocalCallSetInterfaceDNSSettingsByQWordsDefault
		probeLocalCallSetInterfaceDNSSettingsByDW = probeLocalCallSetInterfaceDNSSettingsByDWordsDefault
	})

	err := setProbeLocalWindowsInterfaceDNS("{6BA2B7A3-1C2D-4E63-9E3C-6F7A8B9C0D21}", []string{"198.18.0.2"})
	if err != nil {
		t.Fatalf("setProbeLocalWindowsInterfaceDNS returned error: %v", err)
	}
	if ptrCalls != 1 || qwordCalls != 1 || dwordCalls != 0 {
		t.Fatalf("unexpected call count ptr=%d qwords=%d dwords=%d", ptrCalls, qwordCalls, dwordCalls)
	}
}

func TestSetProbeLocalWindowsInterfaceDNSUsesDWordsAsLastFallback(t *testing.T) {
	ptrCalls := 0
	qwordCalls := 0
	dwordCalls := 0
	probeLocalCallSetInterfaceDNSSettingsByPtr = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		ptrCalls++
		return 87, nil
	}
	probeLocalCallSetInterfaceDNSSettingsByQW = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		qwordCalls++
		return 87, nil
	}
	probeLocalCallSetInterfaceDNSSettingsByDW = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		dwordCalls++
		return 0, nil
	}
	t.Cleanup(func() {
		probeLocalCallSetInterfaceDNSSettingsByPtr = probeLocalCallSetInterfaceDNSSettingsByPtrDefault
		probeLocalCallSetInterfaceDNSSettingsByQW = probeLocalCallSetInterfaceDNSSettingsByQWordsDefault
		probeLocalCallSetInterfaceDNSSettingsByDW = probeLocalCallSetInterfaceDNSSettingsByDWordsDefault
	})

	err := setProbeLocalWindowsInterfaceDNS("{6BA2B7A3-1C2D-4E63-9E3C-6F7A8B9C0D21}", []string{"198.18.0.2"})
	if err != nil {
		t.Fatalf("setProbeLocalWindowsInterfaceDNS returned error: %v", err)
	}
	if ptrCalls != 1 || qwordCalls != 1 || dwordCalls != 1 {
		t.Fatalf("unexpected call count ptr=%d qwords=%d dwords=%d", ptrCalls, qwordCalls, dwordCalls)
	}
}

func TestSetProbeLocalWindowsInterfaceDNSReturnsErrorWhenAllCallsFail(t *testing.T) {
	probeLocalCallSetInterfaceDNSSettingsByPtr = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		return 87, nil
	}
	probeLocalCallSetInterfaceDNSSettingsByQW = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		return 87, nil
	}
	probeLocalCallSetInterfaceDNSSettingsByDW = func(guid *windows.GUID, settings *probeLocalDNSInterfaceSettings) (uintptr, error) {
		return 1168, nil
	}
	t.Cleanup(func() {
		probeLocalCallSetInterfaceDNSSettingsByPtr = probeLocalCallSetInterfaceDNSSettingsByPtrDefault
		probeLocalCallSetInterfaceDNSSettingsByQW = probeLocalCallSetInterfaceDNSSettingsByQWordsDefault
		probeLocalCallSetInterfaceDNSSettingsByDW = probeLocalCallSetInterfaceDNSSettingsByDWordsDefault
	})

	err := setProbeLocalWindowsInterfaceDNS("{6BA2B7A3-1C2D-4E63-9E3C-6F7A8B9C0D21}", []string{"198.18.0.2"})
	if err == nil || !strings.Contains(err.Error(), "SetInterfaceDnsSettings(dwords)") {
		t.Fatalf("unexpected error: %v", err)
	}
}
