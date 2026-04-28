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
			PresentPnPMatched: true,
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
	if !strings.Contains(strings.ToLower(err.Error()), "verify wintun adapter") {
		t.Fatalf("unexpected error: %v", err)
	}
	if detectCalls < 2 {
		t.Fatalf("detect calls=%d, want >=2", detectCalls)
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
	probeLocalTUNInstallSleep = func(_ time.Duration) {}
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
	if detectCalls < 1 {
		t.Fatalf("detect calls=%d, want >=1", detectCalls)
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
