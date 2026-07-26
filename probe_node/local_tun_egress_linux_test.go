//go:build linux

package main

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestProbeLocalLinuxPrimaryEgressRouteOptionsSortsDefaultRoutes(t *testing.T) {
	stubProbeLocalLinuxEgressHooks(t, []string{
		"default via 192.168.50.1 dev eth0 proto dhcp metric 200",
		"default via 10.20.30.1 dev eth1 proto dhcp metric 50",
		"default dev cloudhelper0 metric 3",
	})

	options, err := probeLocalLinuxPrimaryEgressRouteOptions("cloudhelper0")
	if err != nil {
		t.Fatalf("list egress options: %v", err)
	}
	if len(options) != 2 {
		t.Fatalf("options=%+v, want two physical defaults", options)
	}
	if options[0].Name != "eth1" || options[0].NextHop != "10.20.30.1" || options[0].RouteMetric != 50 {
		t.Fatalf("first option=%+v, want eth1 metric 50", options[0])
	}
	if options[1].Name != "eth0" || options[1].RouteMetric != 200 {
		t.Fatalf("second option=%+v, want eth0 metric 200", options[1])
	}
}

func TestEnsureProbeRouteDirectBypassLinuxWritesPhysicalHostRouteOnce(t *testing.T) {
	calls := stubProbeLocalLinuxEgressHooks(t, []string{
		"default via 192.168.50.1 dev eth0 proto dhcp metric 100",
	})
	resetProbeRouteLinuxDirectBypassStateForTest()
	resetProbeRouteLinuxEgressStateForTest()
	t.Cleanup(resetProbeRouteLinuxDirectBypassStateForTest)
	t.Cleanup(resetProbeRouteLinuxEgressStateForTest)

	if err := ensureProbeRouteDirectBypass("203.0.113.7:16030"); err != nil {
		t.Fatalf("ensure linux direct bypass: %v", err)
	}
	if err := ensureProbeRouteDirectBypass("203.0.113.7:16030"); err != nil {
		t.Fatalf("ensure cached linux direct bypass: %v", err)
	}
	want := "ip -4 route replace 203.0.113.7/32 via 192.168.50.1 dev eth0"
	count := 0
	for _, call := range *calls {
		if call == want {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("host route writes=%d want 1 calls=%v", count, *calls)
	}
}

func TestProbeLocalTUNEgressUpdateLinuxRebindsExistingBypass(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	calls := stubProbeLocalLinuxEgressHooks(t, []string{
		"default via 192.168.50.1 dev eth0 proto dhcp metric 100",
		"default via 10.20.30.1 dev eth1 proto dhcp metric 200",
	})
	resetProbeRouteLinuxDirectBypassStateForTest()
	resetProbeRouteLinuxEgressStateForTest()
	t.Cleanup(resetProbeRouteLinuxDirectBypassStateForTest)
	t.Cleanup(resetProbeRouteLinuxEgressStateForTest)

	if err := ensureProbeRouteDirectBypass("203.0.113.7:443"); err != nil {
		t.Fatalf("seed direct bypass: %v", err)
	}
	status, err := probeLocalTUNEgressUpdate(probeLocalTUNEgressUpdateRequest{
		Mode:        "manual",
		CandidateID: probeLocalLinuxEgressCandidateID("eth1", "10.20.30.1"),
	})
	if err != nil {
		t.Fatalf("select linux egress: %v", err)
	}
	if !status.ManualValid || status.Selected == nil || status.Selected.Name != "eth1" {
		t.Fatalf("status=%+v, want valid eth1 manual selection", status)
	}
	want := "ip -4 route replace 203.0.113.7/32 via 10.20.30.1 dev eth1"
	if !containsProbeLocalLinuxCall(*calls, want) {
		t.Fatalf("missing rebound route %q calls=%v", want, *calls)
	}
	persisted, err := loadProbeLocalTUNEgressStateFile()
	if err != nil {
		t.Fatalf("load persisted linux egress: %v", err)
	}
	if persisted.TUNEgress.Name != "eth1" || persisted.TUNEgress.NextHop != "10.20.30.1" {
		t.Fatalf("persisted=%+v, want eth1 via 10.20.30.1", persisted.TUNEgress)
	}
}

func TestApplyProbeLocalTUNEgressPersistentStateLinuxKeepsUnavailableManualTarget(t *testing.T) {
	stubProbeLocalLinuxEgressHooks(t, []string{
		"default via 192.168.50.1 dev eth0 proto dhcp metric 100",
	})
	resetProbeRouteLinuxEgressStateForTest()
	t.Cleanup(resetProbeRouteLinuxEgressStateForTest)

	applyProbeLocalTUNEgressPersistentState(probeLocalTUNEgressPersistentState{
		Mode:        "manual",
		CandidateID: probeLocalLinuxEgressCandidateID("eth9", "10.99.0.1"),
		NextHop:     "10.99.0.1",
		Name:        "eth9",
	})
	target, _, _, manual := currentProbeRouteLinuxManualEgressTarget()
	if !manual || target.Dev != "eth9" || target.Gateway != "10.99.0.1" {
		t.Fatalf("manual=%v target=%+v, want unavailable selection retained", manual, target)
	}
	if _, err := resolveProbeRouteLinuxSelectedEgressRoute("cloudhelper0"); err == nil {
		t.Fatal("unavailable manual egress must not fall back automatically")
	}
}

func stubProbeLocalLinuxEgressHooks(t *testing.T, defaults []string) *[]string {
	t.Helper()
	oldRun := probeLocalLinuxRunCommand
	oldInterfaceByName := probeLocalLinuxInterfaceByName
	calls := make([]string, 0)
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		if call == "ip -4 route show default" {
			return strings.Join(defaults, "\n") + "\n", nil
		}
		return "", nil
	}
	probeLocalLinuxInterfaceByName = func(name string) (*net.Interface, error) {
		index := 10
		if name == "eth1" {
			index = 11
		}
		return &net.Interface{Index: index, Name: name}, nil
	}
	t.Cleanup(func() {
		probeLocalLinuxRunCommand = oldRun
		probeLocalLinuxInterfaceByName = oldInterfaceByName
	})
	return &calls
}

func containsProbeLocalLinuxCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}
