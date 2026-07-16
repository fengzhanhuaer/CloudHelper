//go:build windows

package main

import (
	"errors"
	"strings"
	"testing"
)

const (
	testProbeLocalEgressGUID = "{90078D57-7BEB-4176-8BFB-82A52FE5D5B1}"
	testProbeLocalEgressHop  = "172.18.55.254"
)

func TestProbeLocalTUNEgressCandidateIDUsesStableAdapterIdentity(t *testing.T) {
	left := probeLocalTUNEgressCandidateID(testProbeLocalEgressGUID, 1001, 14, testProbeLocalEgressHop)
	right := probeLocalTUNEgressCandidateID(testProbeLocalEgressGUID, 2002, 15, testProbeLocalEgressHop)
	if left != right {
		t.Fatalf("candidate id changed with interface index: left=%q right=%q", left, right)
	}
	if !strings.Contains(left, "guid:90078d57-7beb-4176-8bfb-82a52fe5d5b1") {
		t.Fatalf("candidate id does not contain normalized guid: %q", left)
	}
}

func TestApplyProbeLocalTUNEgressPersistentStateMigratesLegacyIfIndex(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeRouteDirectBypassStateForTest()
	stubProbeLocalWindowsEgressRouteOptions(t, currentProbeLocalEgressTestOptions())
	t.Cleanup(resetProbeRouteDirectBypassStateForTest)

	legacy := probeLocalTUNEgressPersistentState{
		Mode:           "manual",
		CandidateID:    "14|" + testProbeLocalEgressHop,
		InterfaceIndex: 14,
		NextHop:        testProbeLocalEgressHop,
		Label:          "Ethernet / 172.18.55.254 / ifIndex=14",
	}
	applyProbeLocalTUNEgressPersistentState(legacy)

	target, candidateID, _, ok := currentProbeRouteWindowsDirectManualRouteTarget()
	if !ok {
		t.Fatal("manual target was not restored")
	}
	if target.InterfaceIndex != 15 || target.InterfaceLUID != 2002 {
		t.Fatalf("target was not rebound to current adapter: %+v", target)
	}
	if normalizeProbeLocalTUNEgressInterfaceGUID(target.InterfaceGUID) != normalizeProbeLocalTUNEgressInterfaceGUID(testProbeLocalEgressGUID) {
		t.Fatalf("target guid=%q", target.InterfaceGUID)
	}
	if strings.EqualFold(candidateID, legacy.CandidateID) {
		t.Fatalf("legacy candidate id was not migrated: %q", candidateID)
	}

	persisted, err := loadProbeLocalTUNEgressStateFile()
	if err != nil {
		t.Fatalf("load migrated state: %v", err)
	}
	if persisted.TUNEgress.InterfaceIndex != 15 || persisted.TUNEgress.InterfaceLUID != 2002 {
		t.Fatalf("persisted target was not migrated: %+v", persisted.TUNEgress)
	}
	if normalizeProbeLocalTUNEgressInterfaceGUID(persisted.TUNEgress.InterfaceGUID) != normalizeProbeLocalTUNEgressInterfaceGUID(testProbeLocalEgressGUID) {
		t.Fatalf("persisted guid=%q", persisted.TUNEgress.InterfaceGUID)
	}
}

func TestEnsureProbeRouteDirectBypassRebindsStaleManualAdapter(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeRouteDirectBypassStateForTest()
	stubProbeLocalWindowsEgressRouteOptions(t, currentProbeLocalEgressTestOptions())
	t.Cleanup(resetProbeRouteDirectBypassStateForTest)

	setProbeRouteWindowsDirectManualRouteTarget(
		probeRouteWindowsDirectRouteTarget{
			InterfaceIndex: 14,
			InterfaceLUID:  1001,
			InterfaceGUID:  testProbeLocalEgressGUID,
			NextHop:        testProbeLocalEgressHop,
		},
		probeLocalTUNEgressCandidateID(testProbeLocalEgressGUID, 1001, 14, testProbeLocalEgressHop),
		"Ethernet / 172.18.55.254 / ifIndex=14",
	)

	oldCreate := probeLocalCreateWindowsRouteEntry
	var calls []probeRouteWindowsRouteDef
	probeLocalCreateWindowsRouteEntry = func(routeDef probeRouteWindowsRouteDef) (bool, error) {
		calls = append(calls, routeDef)
		if routeDef.IfIndex == 14 {
			return false, errors.New("CreateIpForwardEntry2 failed: code=1168")
		}
		if routeDef.IfIndex != 15 || routeDef.InterfaceLUID != 2002 {
			return false, errors.New("unexpected rebound route target")
		}
		return true, nil
	}
	t.Cleanup(func() { probeLocalCreateWindowsRouteEntry = oldCreate })

	if err := ensureProbeRouteDirectBypass("203.0.113.10:443"); err != nil {
		t.Fatalf("ensure direct bypass after stale adapter: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("route create calls=%d want 2: %+v", len(calls), calls)
	}
	if calls[0].IfIndex != 14 || calls[1].IfIndex != 15 {
		t.Fatalf("route was not retried with rebound index: %+v", calls)
	}
}

func stubProbeLocalWindowsEgressRouteOptions(t *testing.T, options []probeLocalTUNEgressRouteTargetOption) {
	t.Helper()
	old := probeLocalWindowsEgressRouteOptions
	probeLocalWindowsEgressRouteOptions = func(int) ([]probeLocalTUNEgressRouteTargetOption, error) {
		return append([]probeLocalTUNEgressRouteTargetOption(nil), options...), nil
	}
	t.Cleanup(func() { probeLocalWindowsEgressRouteOptions = old })
}

func currentProbeLocalEgressTestOptions() []probeLocalTUNEgressRouteTargetOption {
	option := probeLocalTUNEgressRouteTargetOption{
		InterfaceIndex: 15,
		InterfaceLUID:  2002,
		InterfaceGUID:  testProbeLocalEgressGUID,
		NextHop:        testProbeLocalEgressHop,
		Name:           "Ethernet",
		Description:    "Realtek PCIe GBE Family Controller",
	}
	option.CandidateID = probeLocalTUNEgressCandidateID(option.InterfaceGUID, option.InterfaceLUID, option.InterfaceIndex, option.NextHop)
	return []probeLocalTUNEgressRouteTargetOption{option}
}
