//go:build windows

package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func forceProbeLocalInstallAsAdminForTest() {
	probeLocalIsWindowsAdmin = func() bool { return true }
	probeLocalRelaunchAsAdminInstall = func() error { return nil }
}

func TestProbeLocalWintunAdapterMatches(t *testing.T) {
	if !probeLocalWintunAdapterMatches(probeLocalTUNAdapterName, "") {
		t.Fatal("expected exact adapter name to match")
	}
	if !probeLocalWintunAdapterMatches(strings.ToLower(probeLocalTUNAdapterName)+" 3", "") {
		t.Fatal("expected prefixed adapter name to match")
	}
	if !probeLocalWintunAdapterMatches("other", probeLocalTUNAdapterDescription) {
		t.Fatal("expected adapter description to match")
	}
	if !probeLocalWintunAdapterMatches("Maple", "") {
		t.Fatal("expected legacy maple adapter name to match for compatibility")
	}
	if probeLocalWintunAdapterMatches("other", "other") {
		t.Fatal("unexpected adapter match")
	}
}

func TestInstallProbeLocalTUNDriverSkipsCreateWhenAdapterExists(t *testing.T) {
	forceProbeLocalInstallAsAdminForTest()
	probeLocalEnsureWintunLibrary = func() error { return nil }
	probeLocalInspectWintunVisibility = func() (probeLocalWintunVisibilityEvidence, error) {
		return probeLocalWintunVisibilityEvidence{
			NetAdapterMatched: true,
			PresentPnPMatched: false,
			NetAdapter: probeLocalWindowsNetAdapter{
				InterfaceIndex: 19,
			},
		}, nil
	}
	createCalled := 0
	probeLocalCreateWintunAdapter = func(_, _, _ string) (uintptr, error) {
		createCalled++
		return 0, errors.New("should not create")
	}
	probeLocalEnsureWindowsInterfaceIPv4 = func(_ int, _ string, _ int) error { return nil }
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := installProbeLocalTUNDriver(); err != nil {
		t.Fatalf("installProbeLocalTUNDriver returned error: %v", err)
	}
	if createCalled != 0 {
		t.Fatalf("create called=%d, want 0", createCalled)
	}
}

func TestInstallProbeLocalTUNDriverCreateAndVerify(t *testing.T) {
	forceProbeLocalInstallAsAdminForTest()
	clearProbeLocalTUNInstallObservation()
	t.Cleanup(func() { clearProbeLocalTUNInstallObservation() })
	probeLocalEnsureWintunLibrary = func() error { return nil }
	tempDLL := t.TempDir() + `\\wintun.dll`
	if err := os.WriteFile(tempDLL, []byte("test"), 0o644); err != nil {
		t.Fatalf("write temp wintun dll failed: %v", err)
	}
	probeLocalResolveWintunPath = func() (string, error) { return tempDLL, nil }
	inspectSeq := 0
	probeLocalInspectWintunVisibility = func() (probeLocalWintunVisibilityEvidence, error) {
		inspectSeq++
		if inspectSeq >= 3 {
			return probeLocalWintunVisibilityEvidence{
				NetAdapterMatched: true,
				PresentPnPMatched: true,
				NetAdapter: probeLocalWindowsNetAdapter{
					InterfaceIndex: 7,
				},
			}, nil
		}
		return probeLocalWintunVisibilityEvidence{}, nil
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
	probeLocalTUNInstallSleep = func(_Duration time.Duration) {}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := installProbeLocalTUNDriver(); err != nil {
		t.Fatalf("installProbeLocalTUNDriver returned error: %v", err)
	}
	if createCalled != 1 {
		t.Fatalf("create called=%d, want 1", createCalled)
	}
	if closeCalled != 0 {
		t.Fatalf("close called=%d, want 0 before retained-handle release", closeCalled)
	}
	releaseProbeLocalRetainedWintunAdapterHandle()
	if closeCalled != 1 {
		t.Fatalf("close called=%d, want 1 after retained-handle release", closeCalled)
	}
	observation, ok := currentProbeLocalTUNInstallObservation()
	if !ok {
		t.Fatal("expected install observation after success")
	}
	if !observation.Driver.PackageExists {
		t.Fatalf("observation.driver.package_exists=%v, want true", observation.Driver.PackageExists)
	}
	if observation.Driver.PackagePath != tempDLL {
		t.Fatalf("observation.driver.package_path=%q, want %q", observation.Driver.PackagePath, tempDLL)
	}
	if !observation.Create.Called || !observation.Create.HandleNonZero {
		t.Fatalf("observation.create invalid: %+v", observation.Create)
	}
	if strings.TrimSpace(observation.Create.RawError) != "" {
		t.Fatalf("observation.create.raw_error=%q, want empty", observation.Create.RawError)
	}
	if !observation.Visibility.DetectVisible {
		t.Fatalf("observation.visibility.detect_visible=%v, want true", observation.Visibility.DetectVisible)
	}
	if !observation.Final.Success || observation.Final.ReasonCode == "" || observation.Final.Reason == "" {
		t.Fatalf("observation.final invalid: %+v", observation.Final)
	}
}

func TestInstallProbeLocalTUNDriverCreateFailure(t *testing.T) {
	forceProbeLocalInstallAsAdminForTest()
	clearProbeLocalTUNInstallObservation()
	t.Cleanup(func() { clearProbeLocalTUNInstallObservation() })
	probeLocalEnsureWintunLibrary = func() error { return nil }
	tempDLL := t.TempDir() + `\\wintun.dll`
	if err := os.WriteFile(tempDLL, []byte("test"), 0o644); err != nil {
		t.Fatalf("write temp wintun dll failed: %v", err)
	}
	probeLocalResolveWintunPath = func() (string, error) { return tempDLL, nil }
	probeLocalInspectWintunVisibility = func() (probeLocalWintunVisibilityEvidence, error) {
		return probeLocalWintunVisibilityEvidence{}, nil
	}
	probeLocalCreateWintunAdapter = func(_, _, _ string) (uintptr, error) {
		return 0, errors.New("access denied")
	}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	err := installProbeLocalTUNDriver()
	if err == nil {
		t.Fatal("expected installProbeLocalTUNDriver error")
	}
	if !strings.Contains(err.Error(), "create/open wintun adapter") {
		t.Fatalf("unexpected error: %v", err)
	}
	observation, ok := currentProbeLocalTUNInstallObservation()
	if !ok {
		t.Fatal("expected install observation after failure")
	}
	if observation.Final.Success {
		t.Fatalf("observation.final.success=%v, want false", observation.Final.Success)
	}
	if observation.Final.ReasonCode != probeLocalTUNInstallCodeAdapterCreateFailed {
		t.Fatalf("observation.final.reason_code=%q, want %q", observation.Final.ReasonCode, probeLocalTUNInstallCodeAdapterCreateFailed)
	}
	if !strings.Contains(strings.ToLower(observation.Create.RawError), "access denied") {
		t.Fatalf("observation.create.raw_error=%q, want contains access denied", observation.Create.RawError)
	}
	if observation.Diagnostic.Code != probeLocalTUNInstallCodeAdapterCreateFailed {
		t.Fatalf("observation.diagnostic.code=%q, want %q", observation.Diagnostic.Code, probeLocalTUNInstallCodeAdapterCreateFailed)
	}
	if !strings.Contains(strings.ToLower(observation.Diagnostic.RawError), "access denied") {
		t.Fatalf("observation.diagnostic.raw_error=%q, want contains access denied", observation.Diagnostic.RawError)
	}
}

func TestInstallProbeLocalTUNDriverVerifyFailureWithoutAdapterHandle(t *testing.T) {
	forceProbeLocalInstallAsAdminForTest()
	probeLocalEnsureWintunLibrary = func() error { return nil }
	probeLocalResolveWintunPath = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	detectCalls := 0
	probeLocalInspectWintunVisibility = func() (probeLocalWintunVisibilityEvidence, error) {
		detectCalls++
		return probeLocalWintunVisibilityEvidence{}, errors.New("adapter enumerate failed")
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
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "verify wintun adapter") && !strings.Contains(msg, "inspect existing wintun adapter") {
		t.Fatalf("unexpected error: %v", err)
	}
	if detectCalls < 1 {
		t.Fatalf("detect calls=%d, want >=1", detectCalls)
	}
}

func TestInstallProbeLocalTUNDriverElevationWaitDetectDelayedSuccess(t *testing.T) {
	probeLocalIsWindowsAdmin = func() bool { return false }
	probeLocalEnsureWintunLibrary = func() error { return nil }
	relaunchCalls := 0
	probeLocalRelaunchAsAdminInstall = func() error {
		relaunchCalls++
		return nil
	}
	detectCalls := 0
	probeLocalInspectWintunVisibility = func() (probeLocalWintunVisibilityEvidence, error) {
		detectCalls++
		if detectCalls >= 3 {
			return probeLocalWintunVisibilityEvidence{
				NetAdapterMatched: true,
				PresentPnPMatched: true,
				NetAdapter: probeLocalWindowsNetAdapter{
					InterfaceIndex: 8,
				},
			}, nil
		}
		return probeLocalWintunVisibilityEvidence{}, nil
	}
	createCalls := 0
	probeLocalCreateWintunAdapter = func(_, _, _ string) (uintptr, error) {
		createCalls++
		return uintptr(33), nil
	}
	probeLocalEnsureWindowsInterfaceIPv4 = func(_ int, _ string, _ int) error { return nil }
	probeLocalTUNInstallSleep = func(_ time.Duration) {}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := installProbeLocalTUNDriver(); err != nil {
		t.Fatalf("installProbeLocalTUNDriver returned error: %v", err)
	}
	if relaunchCalls != 1 {
		t.Fatalf("relaunch calls=%d, want 1", relaunchCalls)
	}
	if detectCalls < 3 {
		t.Fatalf("detect calls=%d, want >=3", detectCalls)
	}
	if createCalls < 1 {
		t.Fatalf("create calls=%d, want >=1 when retaining handle in elevation wait path", createCalls)
	}
}

func TestInstallProbeLocalTUNDriverElevationWaitDetectTimeout(t *testing.T) {
	probeLocalIsWindowsAdmin = func() bool { return false }
	probeLocalEnsureWintunLibrary = func() error { return nil }
	probeLocalRelaunchAsAdminInstall = func() error { return nil }
	detectCalls := 0
	probeLocalInspectWintunVisibility = func() (probeLocalWintunVisibilityEvidence, error) {
		detectCalls++
		return probeLocalWintunVisibilityEvidence{}, nil
	}
	sleepDelays := make([]time.Duration, 0, 8)
	probeLocalTUNInstallSleep = func(delay time.Duration) {
		sleepDelays = append(sleepDelays, delay)
	}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	err := installProbeLocalTUNDriver()
	if err == nil {
		t.Fatal("expected installProbeLocalTUNDriver error")
	}
	var installErr *probeLocalTUNInstallError
	if !errors.As(err, &installErr) || installErr == nil {
		t.Fatalf("expected probeLocalTUNInstallError, got: %T %v", err, err)
	}
	if installErr.Diagnostic.Code != probeLocalTUNInstallCodeElevationTimeout {
		t.Fatalf("diagnostic code=%q, want %q", installErr.Diagnostic.Code, probeLocalTUNInstallCodeElevationTimeout)
	}
	if installErr.Diagnostic.Stage != "await_adapter_visibility_after_elevation" {
		t.Fatalf("diagnostic stage=%q, want await_adapter_visibility_after_elevation", installErr.Diagnostic.Stage)
	}
	if detectCalls != 7 {
		t.Fatalf("detect calls=%d, want 7", detectCalls)
	}
	wantDelays := []time.Duration{
		150 * time.Millisecond,
		300 * time.Millisecond,
		600 * time.Millisecond,
		1000 * time.Millisecond,
		1600 * time.Millisecond,
		2500 * time.Millisecond,
	}
	if len(sleepDelays) != len(wantDelays) {
		t.Fatalf("sleep delays=%v, want %v", sleepDelays, wantDelays)
	}
	for i := range wantDelays {
		if sleepDelays[i] != wantDelays[i] {
			t.Fatalf("sleep delay[%d]=%s, want %s", i, sleepDelays[i], wantDelays[i])
		}
	}
}

func TestDetectProbeLocalTUNInstalledWindowsRepairsRouteTarget(t *testing.T) {
	inspectCalls := 0
	probeLocalInspectWintunVisibility = func() (probeLocalWintunVisibilityEvidence, error) {
		inspectCalls++
		return probeLocalWintunVisibilityEvidence{
			NetAdapterMatched: true,
			PresentPnPMatched: true,
			NetAdapter:        probeLocalWindowsNetAdapter{InterfaceIndex: 23},
		}, nil
	}
	routeCalls := 0
	probeLocalEnsureWindowsInterfaceIPv4 = func(interfaceIndex int, _ string, _ int) error {
		routeCalls++
		if interfaceIndex != 23 {
			t.Fatalf("interface index=%d, want 23", interfaceIndex)
		}
		return nil
	}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	installed, err := detectProbeLocalTUNInstalledWindows()
	if err != nil {
		t.Fatalf("detectProbeLocalTUNInstalledWindows returned error: %v", err)
	}
	if !installed {
		t.Fatalf("installed=%v, want true", installed)
	}
	if inspectCalls != 1 || routeCalls != 1 {
		t.Fatalf("inspectCalls=%d routeCalls=%d, want 1/1", inspectCalls, routeCalls)
	}
}

func TestInstallProbeLocalTUNDriverLUIDIfIndexDiagnosticOnlyWithoutDetect(t *testing.T) {
	forceProbeLocalInstallAsAdminForTest()
	probeLocalEnsureWintunLibrary = func() error { return nil }
	probeLocalResolveWintunPath = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalInspectWintunVisibility = func() (probeLocalWintunVisibilityEvidence, error) {
		return probeLocalWintunVisibilityEvidence{}, nil
	}
	probeLocalCreateWintunAdapter = func(_, _, _ string) (uintptr, error) { return uintptr(1), nil }
	probeLocalGetWintunAdapterLUIDFromHandle = func(_ string, _ uintptr) (uint64, error) { return 1001, nil }
	probeLocalFindWintunAdapterByLUID = func(uint64) (probeLocalWindowsNetAdapter, bool, error) {
		return probeLocalWindowsNetAdapter{}, false, nil
	}
	probeLocalConvertInterfaceLUIDToIndex = func(uint64) (int, error) { return 7, nil }
	closeCalled := 0
	probeLocalCloseWintunAdapter = func(_ string, _ uintptr) error {
		closeCalled++
		return nil
	}
	probeLocalTUNInstallSleep = func(_ time.Duration) {}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	err := installProbeLocalTUNDriver()
	if err != nil {
		t.Fatalf("installProbeLocalTUNDriver returned error: %v", err)
	}
	observation, ok := currentProbeLocalTUNInstallObservation()
	if !ok {
		t.Fatal("expected install observation after success")
	}
	if !observation.Final.Success {
		t.Fatalf("observation.final.success=%v, want true", observation.Final.Success)
	}
	if observation.Final.ReasonCode != probeLocalTUNInstallCodeAdapterJointVisibilityMiss {
		t.Fatalf("observation.final.reason_code=%q, want %q", observation.Final.ReasonCode, probeLocalTUNInstallCodeAdapterJointVisibilityMiss)
	}
	if !strings.Contains(observation.Final.Reason, "联合可见") {
		t.Fatalf("observation.final.reason=%q, want mention 联合可见", observation.Final.Reason)
	}
	if closeCalled != 0 {
		t.Fatalf("close called=%d, want 0 before retained-handle release", closeCalled)
	}
	releaseProbeLocalRetainedWintunAdapterHandle()
	if closeCalled != 1 {
		t.Fatalf("close called=%d, want 1 after retained-handle release", closeCalled)
	}
}

func TestInstallProbeLocalTUNDriverFallbackVisibilityConflictNoRecreate(t *testing.T) {
	forceProbeLocalInstallAsAdminForTest()
	clearProbeLocalTUNInstallObservation()
	t.Cleanup(func() { clearProbeLocalTUNInstallObservation() })
	probeLocalEnsureWintunLibrary = func() error { return nil }
	probeLocalResolveWintunPath = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapter = func(_, _, _ string) (uintptr, error) { return uintptr(1), nil }
	probeLocalGetWintunAdapterLUIDFromHandle = func(_ string, _ uintptr) (uint64, error) { return 1001, nil }
	probeLocalFindWintunAdapterByLUID = func(uint64) (probeLocalWindowsNetAdapter, bool, error) {
		return probeLocalWindowsNetAdapter{InterfaceIndex: 9}, true, nil
	}
	closeCalls := 0
	probeLocalCloseWintunAdapter = func(_ string, _ uintptr) error {
		closeCalls++
		return nil
	}
	probeLocalTUNInstallSleep = func(_ time.Duration) {}
	inspectCalls := 0
	probeLocalInspectWintunVisibility = func() (probeLocalWintunVisibilityEvidence, error) {
		inspectCalls++
		if inspectCalls <= 11 {
			return probeLocalWintunVisibilityEvidence{}, nil
		}
		return probeLocalWintunVisibilityEvidence{}, errors.New("joint visibility still missing")
	}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	err := installProbeLocalTUNDriver()
	if err != nil {
		t.Fatalf("installProbeLocalTUNDriver returned error: %v", err)
	}
	observation, ok := currentProbeLocalTUNInstallObservation()
	if !ok {
		t.Fatal("expected install observation")
	}
	if !observation.Final.Success {
		t.Fatalf("observation.final.success=%v, want true", observation.Final.Success)
	}
	if !strings.Contains(observation.Final.Reason, "禁止重建") {
		t.Fatalf("observation.final.reason=%q, want mention 禁止重建", observation.Final.Reason)
	}
	releaseProbeLocalRetainedWintunAdapterHandle()
	if closeCalls != 1 {
		t.Fatalf("close calls=%d, want 1", closeCalls)
	}
}

func TestEnsureProbeLocalWindowsRouteTargetByInterfaceIndexRetriesOnBindableTimeout(t *testing.T) {
	probeLocalEnsureWindowsInterfaceIPv4 = func(_ int, _ string, _ int) error {
		return errors.New("ipv4 address not bindable in time: if=18 ip=198.18.0.1")
	}
	probeLocalRepairWindowsRouteTargetIPv4Hook = func(_ int, _ string, _ int) error {
		return errors.New("disabled in unit test")
	}
	probeLocalRecycleWindowsTunAdapterHook = func(_ int) error {
		return errors.New("disabled in unit test")
	}
	sleepCalls := 0
	probeLocalTUNInstallSleep = func(_ time.Duration) { sleepCalls++ }
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	err := ensureProbeLocalWindowsRouteTargetByInterfaceIndex(18)
	if err == nil {
		t.Fatal("expected route target configure error")
	}
	if sleepCalls != 3 {
		t.Fatalf("sleepCalls=%d, want 3", sleepCalls)
	}
}

func TestEnsureProbeLocalWindowsRouteTargetByInterfaceIndexRetryRecovers(t *testing.T) {
	calls := 0
	probeLocalEnsureWindowsInterfaceIPv4 = func(_ int, _ string, _ int) error {
		calls++
		if calls <= 2 {
			return errors.New("ipv4 address not bindable in time: if=18 ip=198.18.0.1")
		}
		return nil
	}
	probeLocalTUNInstallSleep = func(_ time.Duration) {}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := ensureProbeLocalWindowsRouteTargetByInterfaceIndex(18); err != nil {
		t.Fatalf("ensureProbeLocalWindowsRouteTargetByInterfaceIndex returned error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("ensure calls=%d, want 3", calls)
	}
	if got := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_INDEX")); got != "18" {
		t.Fatalf("PROBE_LOCAL_TUN_IF_INDEX=%q, want 18", got)
	}
}

func TestEnsureProbeLocalWindowsRouteTargetByInterfaceIndexRepairPathRecovers(t *testing.T) {
	calls := 0
	probeLocalEnsureWindowsInterfaceIPv4 = func(_ int, _ string, _ int) error {
		calls++
		if calls <= 4 {
			return errors.New("ipv4 address not bindable in time: if=18 ip=198.18.0.1")
		}
		return nil
	}
	repairCalls := 0
	probeLocalRepairWindowsRouteTargetIPv4Hook = func(_ int, _ string, _ int) error {
		repairCalls++
		return nil
	}
	probeLocalRecycleWindowsTunAdapterHook = func(_ int) error {
		return errors.New("unexpected recycle path")
	}
	probeLocalTUNInstallSleep = func(_ time.Duration) {}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := ensureProbeLocalWindowsRouteTargetByInterfaceIndex(18); err != nil {
		t.Fatalf("ensureProbeLocalWindowsRouteTargetByInterfaceIndex returned error: %v", err)
	}
	if repairCalls != 1 {
		t.Fatalf("repair calls=%d, want 1", repairCalls)
	}
	if calls != 5 {
		t.Fatalf("ensure calls=%d, want 5", calls)
	}
	if got := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_INDEX")); got != "18" {
		t.Fatalf("PROBE_LOCAL_TUN_IF_INDEX=%q, want 18", got)
	}
}

func TestEnsureProbeLocalWindowsRouteTargetByInterfaceIndexRepairPathFails(t *testing.T) {
	probeLocalEnsureWindowsInterfaceIPv4 = func(_ int, _ string, _ int) error {
		return errors.New("ipv4 address not bindable in time: if=18 ip=198.18.0.1")
	}
	probeLocalRepairWindowsRouteTargetIPv4Hook = func(_ int, _ string, _ int) error {
		return errors.New("remove/new failed")
	}
	probeLocalRecycleWindowsTunAdapterHook = func(_ int) error {
		return errors.New("powershell failed")
	}
	probeLocalTUNInstallSleep = func(_ time.Duration) {}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	err := ensureProbeLocalWindowsRouteTargetByInterfaceIndex(18)
	if err == nil {
		t.Fatal("expected repair path error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "repair windows tun ipv4 target failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureProbeLocalWindowsRouteTargetByInterfaceIndexRecyclePathRecovers(t *testing.T) {
	calls := 0
	probeLocalEnsureWindowsInterfaceIPv4 = func(_ int, _ string, _ int) error {
		calls++
		if calls <= 4 {
			return errors.New("ipv4 address not bindable in time: if=18 ip=198.18.0.1")
		}
		return nil
	}
	repairCalls := 0
	recycleCalls := 0
	probeLocalRepairWindowsRouteTargetIPv4Hook = func(_ int, _ string, _ int) error {
		repairCalls++
		return errors.New("remove/new failed")
	}
	probeLocalRecycleWindowsTunAdapterHook = func(_ int) error {
		recycleCalls++
		return nil
	}
	probeLocalTUNInstallSleep = func(_ time.Duration) {}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := ensureProbeLocalWindowsRouteTargetByInterfaceIndex(18); err != nil {
		t.Fatalf("ensureProbeLocalWindowsRouteTargetByInterfaceIndex returned error: %v", err)
	}
	if repairCalls != 1 {
		t.Fatalf("repair calls=%d, want 1", repairCalls)
	}
	if recycleCalls != 1 {
		t.Fatalf("recycle calls=%d, want 1", recycleCalls)
	}
	if calls != 5 {
		t.Fatalf("ensure calls=%d, want 5", calls)
	}
	if got := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_INDEX")); got != "18" {
		t.Fatalf("PROBE_LOCAL_TUN_IF_INDEX=%q, want 18", got)
	}
}

func TestInstallProbeLocalTUNDriverPhantomOnlyPrecheckRecheckThenCreate(t *testing.T) {
	forceProbeLocalInstallAsAdminForTest()
	clearProbeLocalTUNInstallObservation()
	t.Cleanup(func() { clearProbeLocalTUNInstallObservation() })
	probeLocalEnsureWintunLibrary = func() error { return nil }
	probeLocalResolveWintunPath = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalTUNInstallSleep = func(_ time.Duration) {}

	removeCalls := 0
	probeLocalRemovePhantomWintunDevices = func() (int, error) {
		removeCalls++
		return 1, nil
	}

	inspectCalls := 0
	probeLocalInspectWintunVisibility = func() (probeLocalWintunVisibilityEvidence, error) {
		inspectCalls++
		switch inspectCalls {
		case 1:
			return probeLocalWintunVisibilityEvidence{
				PhantomPnPMatched:      true,
				MatchedPnPFriendlyName: "Maple Tunnel",
				MatchedPnPStatus:       "Unknown",
				MatchedPnPProblem:      "CM_PROB_PHANTOM",
			}, nil
		case 2:
			return probeLocalWintunVisibilityEvidence{}, nil
		default:
			return probeLocalWintunVisibilityEvidence{
				NetAdapterMatched: true,
				PresentPnPMatched: true,
				NetAdapter:        probeLocalWindowsNetAdapter{InterfaceIndex: 12},
			}, nil
		}
	}

	createCalls := 0
	probeLocalCreateWintunAdapter = func(_, adapterName, _ string) (uintptr, error) {
		createCalls++
		if adapterName != probeLocalTUNAdapterName {
			return 0, errors.New("unexpected non-default adapter name")
		}
		return uintptr(1), nil
	}
	probeLocalGetWintunAdapterLUIDFromHandle = func(_ string, _ uintptr) (uint64, error) { return 1002, nil }
	closeCalls := 0
	probeLocalCloseWintunAdapter = func(_ string, _ uintptr) error {
		closeCalls++
		return nil
	}
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := installProbeLocalTUNDriver(); err != nil {
		t.Fatalf("installProbeLocalTUNDriver returned error: %v", err)
	}
	if removeCalls != 1 {
		t.Fatalf("remove phantom calls=%d, want 1", removeCalls)
	}
	if createCalls != 1 {
		t.Fatalf("create calls=%d, want 1", createCalls)
	}

	observation, ok := currentProbeLocalTUNInstallObservation()
	if !ok {
		t.Fatal("expected install observation")
	}
	if !observation.Final.Success {
		t.Fatalf("observation.final.success=%v, want true", observation.Final.Success)
	}
	if observation.Visibility.IfIndexValue != 12 {
		t.Fatalf("observation.visibility.ifindex=%d, want 12", observation.Visibility.IfIndexValue)
	}
	releaseProbeLocalRetainedWintunAdapterHandle()
	if closeCalls != 1 {
		t.Fatalf("close calls=%d, want 1", closeCalls)
	}
}

func TestEnsureProbeLocalWindowsRouteTargetConfiguredFallbackAfterBindableTimeout(t *testing.T) {
	probeLocalFindWintunAdapter = func() (probeLocalWindowsNetAdapter, bool, error) {
		return probeLocalWindowsNetAdapter{InterfaceIndex: 18}, true, nil
	}
	probeLocalFindWindowsAdapterByIfIndex = func(interfaceIndex int) (windowsAdapterInfo, error) {
		if interfaceIndex == 19 {
			return windowsAdapterInfo{InterfaceIndex: 19}, nil
		}
		return windowsAdapterInfo{}, errors.New("adapter not found")
	}
	probeLocalEnsureWindowsInterfaceIPv4 = func(interfaceIndex int, _ string, _ int) error {
		if interfaceIndex == 18 {
			return errors.New("ipv4 address not bindable in time: if=18 ip=198.18.0.1")
		}
		if interfaceIndex == 19 {
			return nil
		}
		return errors.New("unexpected interface index")
	}
	probeLocalTUNInstallSleep = func(_ time.Duration) {}
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "19")
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := ensureProbeLocalWindowsRouteTargetConfigured(); err != nil {
		t.Fatalf("ensureProbeLocalWindowsRouteTargetConfigured returned error: %v", err)
	}
	if got := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_INDEX")); got != "19" {
		t.Fatalf("PROBE_LOCAL_TUN_IF_INDEX=%q, want 19", got)
	}
}

func TestEnsureProbeLocalWindowsRouteTargetConfiguredFallbackAfterCreateUnicastNotFound(t *testing.T) {
	probeLocalFindWintunAdapter = func() (probeLocalWindowsNetAdapter, bool, error) {
		return probeLocalWindowsNetAdapter{InterfaceIndex: 18}, true, nil
	}
	probeLocalFindWindowsAdapterByIfIndex = func(interfaceIndex int) (windowsAdapterInfo, error) {
		if interfaceIndex == 19 {
			return windowsAdapterInfo{InterfaceIndex: 19}, nil
		}
		return windowsAdapterInfo{}, errors.New("adapter not found")
	}
	probeLocalEnsureWindowsInterfaceIPv4 = func(interfaceIndex int, _ string, _ int) error {
		if interfaceIndex == 18 {
			return errors.New("CreateUnicastIpAddressEntry failed: code=1168")
		}
		if interfaceIndex == 19 {
			return nil
		}
		return errors.New("unexpected interface index")
	}
	probeLocalTUNInstallSleep = func(_ time.Duration) {}
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "19")
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	if err := ensureProbeLocalWindowsRouteTargetConfigured(); err != nil {
		t.Fatalf("ensureProbeLocalWindowsRouteTargetConfigured returned error: %v", err)
	}
	if got := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_INDEX")); got != "19" {
		t.Fatalf("PROBE_LOCAL_TUN_IF_INDEX=%q, want 19", got)
	}
}

func TestResolveProbeLocalWintunInterfaceIndexFallbackSkipsStaleEnvIfIndex(t *testing.T) {
	probeLocalFindWindowsAdapterByIfIndex = func(interfaceIndex int) (windowsAdapterInfo, error) {
		if interfaceIndex == 19 {
			return windowsAdapterInfo{}, errors.New("adapter not found")
		}
		return windowsAdapterInfo{InterfaceIndex: interfaceIndex}, nil
	}
	probeLocalResolveWintunPath = func() (string, error) { return `C:\\temp\\wintun.dll`, nil }
	probeLocalCreateWintunAdapter = func(_, _, _ string) (uintptr, error) { return uintptr(1), nil }
	probeLocalCloseWintunAdapter = func(_ string, _ uintptr) error { return nil }
	probeLocalGetWintunAdapterLUIDFromHandle = func(_ string, _ uintptr) (uint64, error) { return 12345, nil }
	probeLocalConvertInterfaceLUIDToIndex = func(luid uint64) (int, error) {
		if luid != 12345 {
			t.Fatalf("luid=%d", luid)
		}
		return 21, nil
	}
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "19")
	t.Cleanup(func() { resetProbeLocalTUNInstallWindowsHooksForTest() })

	ifIndex, err := resolveProbeLocalWintunInterfaceIndexFallback(0)
	if err != nil {
		t.Fatalf("resolveProbeLocalWintunInterfaceIndexFallback returned error: %v", err)
	}
	if ifIndex != 21 {
		t.Fatalf("ifIndex=%d, want 21", ifIndex)
	}
}
