//go:build windows

package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func forceProbeLocalInstallAsAdminForTest() {
	probeLocalIsWindowsAdmin = func() bool { return true }
	probeLocalRelaunchAsAdminInstall = func() error { return nil }
}

func TestProbeLocalWintunAdapterMatches(t *testing.T) {
	if !probeLocalWintunAdapterMatches("Maple", "") {
		t.Fatal("expected exact adapter name to match")
	}
	if !probeLocalWintunAdapterMatches("maple 3", "") {
		t.Fatal("expected prefixed adapter name to match")
	}
	if !probeLocalWintunAdapterMatches("other", "Maple Virtual Network Adapter") {
		t.Fatal("expected adapter description to match")
	}
	if probeLocalWintunAdapterMatches("other", "other") {
		t.Fatal("unexpected adapter match")
	}
}

func TestInstallProbeLocalTUNDriverSkipsCreateWhenAdapterExists(t *testing.T) {
	forceProbeLocalInstallAsAdminForTest()
	probeLocalEnsureWintunLibrary = func() error { return nil }
	probeLocalDetectWintunAdapter = func() (bool, error) { return true, nil }
	createCalled := 0
	probeLocalCreateWintunAdapter = func(_, _, _ string) (uintptr, error) {
		createCalled++
		return 0, errors.New("should not create")
	}
	routeTargetCalls := 0
	probeLocalEnsureWindowsRouteTarget = func() error {
		routeTargetCalls++
		return nil
	}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := installProbeLocalTUNDriver(); err != nil {
		t.Fatalf("installProbeLocalTUNDriver returned error: %v", err)
	}
	if createCalled != 0 {
		t.Fatalf("create called=%d, want 0", createCalled)
	}
	if routeTargetCalls != 1 {
		t.Fatalf("route target calls=%d, want 1", routeTargetCalls)
	}
}

func TestInstallProbeLocalTUNDriverCreateAndVerify(t *testing.T) {
	forceProbeLocalInstallAsAdminForTest()
	probeLocalEnsureWintunLibrary = func() error { return nil }
	probeLocalResolveWintunPath = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	detectSeq := 0
	probeLocalDetectWintunAdapter = func() (bool, error) {
		detectSeq++
		if detectSeq >= 3 {
			return true, nil
		}
		return false, nil
	}
	createCalled := 0
	probeLocalCreateWintunAdapter = func(_, _, _ string) (uintptr, error) {
		createCalled++
		return uintptr(1), nil
	}
	closeCalled := 0
	probeLocalCloseWintunAdapter = func(_ string, _ uintptr) error {
		closeCalled++
		return nil
	}
	routeTargetCalls := 0
	probeLocalEnsureWindowsRouteTarget = func() error {
		routeTargetCalls++
		return nil
	}
	probeLocalTUNInstallSleep = func(_Duration time.Duration) {}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := installProbeLocalTUNDriver(); err != nil {
		t.Fatalf("installProbeLocalTUNDriver returned error: %v", err)
	}
	if createCalled != 1 {
		t.Fatalf("create called=%d, want 1", createCalled)
	}
	if closeCalled != 1 {
		t.Fatalf("close called=%d, want 1", closeCalled)
	}
	if routeTargetCalls != 1 {
		t.Fatalf("route target calls=%d, want 1", routeTargetCalls)
	}
}

func TestInstallProbeLocalTUNDriverCreateFailure(t *testing.T) {
	forceProbeLocalInstallAsAdminForTest()
	probeLocalEnsureWintunLibrary = func() error { return nil }
	probeLocalResolveWintunPath = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalDetectWintunAdapter = func() (bool, error) { return false, nil }
	probeLocalCreateWintunAdapter = func(_, _, _ string) (uintptr, error) {
		return 0, errors.New("access denied")
	}
	routeTargetCalls := 0
	probeLocalEnsureWindowsRouteTarget = func() error {
		routeTargetCalls++
		return nil
	}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	err := installProbeLocalTUNDriver()
	if err == nil {
		t.Fatal("expected installProbeLocalTUNDriver error")
	}
	if !strings.Contains(err.Error(), "create/open wintun adapter") {
		t.Fatalf("unexpected error: %v", err)
	}
	if routeTargetCalls != 0 {
		t.Fatalf("route target calls=%d, want 0", routeTargetCalls)
	}
}

func TestInstallProbeLocalTUNDriverVerifyFailure(t *testing.T) {
	forceProbeLocalInstallAsAdminForTest()
	probeLocalEnsureWintunLibrary = func() error { return nil }
	probeLocalResolveWintunPath = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalDetectWintunAdapter = func() (bool, error) { return false, nil }
	probeLocalCreateWintunAdapter = func(_, _, _ string) (uintptr, error) {
		return uintptr(1), nil
	}
	probeLocalCloseWintunAdapter = func(_ string, _ uintptr) error { return nil }
	probeLocalTUNInstallSleep = func(_Duration time.Duration) {}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	err := installProbeLocalTUNDriver()
	if err == nil {
		t.Fatal("expected installProbeLocalTUNDriver error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not detectable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInstallProbeLocalTUNDriverVerifyFailureWithoutAdapterHandle(t *testing.T) {
	forceProbeLocalInstallAsAdminForTest()
	probeLocalEnsureWintunLibrary = func() error { return nil }
	probeLocalResolveWintunPath = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	detectCalls := 0
	probeLocalDetectWintunAdapter = func() (bool, error) {
		detectCalls++
		return false, errors.New("adapter enumerate failed")
	}
	probeLocalCreateWintunAdapter = func(_, _, _ string) (uintptr, error) {
		return uintptr(0), nil
	}
	probeLocalCloseWintunAdapter = func(_ string, _ uintptr) error { return nil }
	probeLocalTUNInstallSleep = func(_Duration time.Duration) {}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	err := installProbeLocalTUNDriver()
	if err == nil {
		t.Fatal("expected installProbeLocalTUNDriver error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "verify wintun adapter") {
		t.Fatalf("unexpected error: %v", err)
	}
	if detectCalls < 2 {
		t.Fatalf("detect calls=%d, want >=2", detectCalls)
	}
}
