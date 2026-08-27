//go:build linux

package main

import (
	"errors"
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

func TestCleanupProbeRouteDirectBypassForVirtualRouterRulesLinuxKeepsTransportHostRoute(t *testing.T) {
	resetProbeRouteLinuxDirectBypassStateForTest()
	t.Cleanup(resetProbeRouteLinuxDirectBypassStateForTest)
	oldRun := probeLocalLinuxRunCommand
	t.Cleanup(func() { probeLocalLinuxRunCommand = oldRun })

	routeDef := probeVirtualRouterLinuxRouteDef{Prefix: "172.18.52.205/32", Dev: "eth0", Gateway: "192.168.51.1"}
	probeRouteLinuxDirectBypassState.mu.Lock()
	probeRouteLinuxDirectBypassState.routes[routeDef.Prefix] = routeDef
	probeRouteLinuxDirectBypassState.transportProtected["172.18.52.205"] = struct{}{}
	probeRouteLinuxDirectBypassState.mu.Unlock()
	probeLocalLinuxRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		t.Fatalf("transport host route must not be deleted: %s %s", name, strings.Join(args, " "))
		return "", nil
	}

	cleanupProbeRouteDirectBypassForVirtualRouterRules(probeVirtualRouterConfig{
		RouteRules: []probeVirtualRouterRouteRule{{
			ID: "linux-router-22", Name: "router", Action: "probe_exit", ExitNodeID: "22", Entries: []string{"cidr:172.18.52.0/22"},
		}},
	})
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

func TestResolveProbeRouteLinuxSelectedEgressUsesReachableSavedManualGateway(t *testing.T) {
	calls := stubProbeLocalLinuxEgressHooks(t, []string{
		"default via 192.168.50.94 dev eth0 metric 202",
	})
	baseRun := probeLocalLinuxRunCommand
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		call := name + " " + strings.Join(args, " ")
		if call == "ip -4 route get 192.168.50.1 oif eth0" {
			return "192.168.50.1 dev eth0 src 192.168.50.94 uid 0\n", nil
		}
		return baseRun(timeout, name, args...)
	}
	resetProbeRouteLinuxEgressStateForTest()
	t.Cleanup(resetProbeRouteLinuxEgressStateForTest)

	applyProbeLocalTUNEgressPersistentState(probeLocalTUNEgressPersistentState{
		Mode:        "manual",
		CandidateID: probeLocalLinuxEgressCandidateID("eth0", "192.168.50.1"),
		NextHop:     "192.168.50.1",
		Name:        "eth0",
	})
	target, err := resolveProbeRouteLinuxSelectedEgressRoute("cloudhelper0")
	if err != nil {
		t.Fatalf("resolve reachable saved manual egress: %v", err)
	}
	if target.Dev != "eth0" || target.Gateway != "192.168.50.1" {
		t.Fatalf("target=%+v, want saved eth0 gateway", target)
	}
	if !containsProbeLocalLinuxCall(*calls, "ip -4 route replace default via 192.168.50.1 dev eth0 metric 202") {
		t.Fatalf("saved manual egress was not forced into the default route: calls=%v", *calls)
	}
}

func TestResolveProbeRouteLinuxSelectedEgressRejectsSelfGateway(t *testing.T) {
	stubProbeLocalLinuxEgressHooks(t, []string{
		"default via 192.168.50.1 dev eth0 metric 202",
	})
	baseRun := probeLocalLinuxRunCommand
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		call := name + " " + strings.Join(args, " ")
		if call == "ip -4 route get 192.168.50.94 oif eth0" {
			return "local 192.168.50.94 dev lo src 192.168.50.94 uid 0\n", nil
		}
		return baseRun(timeout, name, args...)
	}
	resetProbeRouteLinuxEgressStateForTest()
	t.Cleanup(resetProbeRouteLinuxEgressStateForTest)

	applyProbeLocalTUNEgressPersistentState(probeLocalTUNEgressPersistentState{
		Mode:        "manual",
		CandidateID: probeLocalLinuxEgressCandidateID("eth0", "192.168.50.94"),
		NextHop:     "192.168.50.94",
		Name:        "eth0",
	})
	if _, err := resolveProbeRouteLinuxSelectedEgressRoute("cloudhelper0"); err == nil {
		t.Fatal("self-referential manual gateway must be rejected")
	}
}

func TestProbeLocalLinuxManualEgressIsEffectiveRejectsConflictingBestDefault(t *testing.T) {
	selected := probeLocalTUNEgressRouteTargetOption{
		CandidateID: probeLocalLinuxEgressCandidateID("eth0", "192.168.50.1"),
		Name:        "eth0",
		NextHop:     "192.168.50.1",
		RouteMetric: 202,
	}
	defaults := []probeLocalTUNEgressRouteTargetOption{
		{
			CandidateID: probeLocalLinuxEgressCandidateID("eth0", "192.168.50.94"),
			Name:        "eth0",
			NextHop:     "192.168.50.94",
			RouteMetric: 100,
		},
		selected,
	}
	if probeLocalLinuxManualEgressIsEffective(defaults, selected) {
		t.Fatal("lower-metric conflicting default must make the selected egress ineffective")
	}
	if metric := probeLocalLinuxManualEgressDefaultRouteMetric(defaults); metric != 100 {
		t.Fatalf("force metric=%d want current best metric 100", metric)
	}
}

func TestProbeLocalLinuxRouteGetUsesInterfaceRejectsIndirectGateway(t *testing.T) {
	output := "192.168.50.1 via 192.168.50.94 dev eth0 src 192.168.50.94 uid 0\n"
	if probeLocalLinuxRouteGetUsesInterface(output, "192.168.50.1", "eth0") {
		t.Fatal("manual upstream reached through another gateway must not be treated as a direct neighbor")
	}
}

func TestReconcileProbeLocalLinuxManualEgressRecoversAfterNetworkReturns(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeRouteLinuxEgressStateForTest()
	resetProbeRouteLinuxDirectBypassStateForTest()
	t.Cleanup(resetProbeRouteLinuxEgressStateForTest)
	t.Cleanup(resetProbeRouteLinuxDirectBypassStateForTest)
	if err := persistProbeLocalTUNEgressStateFile(probeLocalTUNEgressStateFile{
		Version: 1,
		TUNEgress: probeLocalTUNEgressPersistentState{
			Mode:        "manual",
			CandidateID: probeLocalLinuxEgressCandidateID("eth0", "192.168.50.1"),
			NextHop:     "192.168.50.1",
			Name:        "eth0",
		},
	}); err != nil {
		t.Fatalf("persist manual egress: %v", err)
	}

	oldRun := probeLocalLinuxRunCommand
	oldInterfaceByName := probeLocalLinuxInterfaceByName
	networkReady := false
	currentGateway := "192.168.50.94"
	var calls []string
	probeLocalLinuxInterfaceByName = func(name string) (*net.Interface, error) {
		return &net.Interface{Index: 2, Name: name}, nil
	}
	probeLocalLinuxRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		switch call {
		case "ip -4 route show default":
			return "default via " + currentGateway + " dev eth0 metric 202\n", nil
		case "ip -4 route get 192.168.50.1 oif eth0":
			if !networkReady {
				return "", errors.New("network unreachable")
			}
			return "192.168.50.1 dev eth0 src 192.168.50.94 uid 0\n", nil
		case "ip -4 route replace default via 192.168.50.1 dev eth0 metric 202":
			currentGateway = "192.168.50.1"
			return "", nil
		default:
			return "", nil
		}
	}
	t.Cleanup(func() {
		probeLocalLinuxRunCommand = oldRun
		probeLocalLinuxInterfaceByName = oldInterfaceByName
	})

	if err := reconcileProbeLocalLinuxManualEgress(); err == nil {
		t.Fatal("network outage should leave manual egress recovery pending")
	}
	networkReady = true
	if err := reconcileProbeLocalLinuxManualEgress(); err != nil {
		t.Fatalf("recover manual egress after network returns: %v", err)
	}
	if currentGateway != "192.168.50.1" {
		t.Fatalf("gateway=%s, want restored 192.168.50.1 calls=%v", currentGateway, calls)
	}
	forceCalls := 0
	for _, call := range calls {
		if call == "ip -4 route replace default via 192.168.50.1 dev eth0 metric 202" {
			forceCalls++
		}
	}
	if forceCalls != 1 {
		t.Fatalf("force route calls=%d want 1 calls=%v", forceCalls, calls)
	}
	if err := reconcileProbeLocalLinuxManualEgress(); err != nil {
		t.Fatalf("healthy manual egress reconcile: %v", err)
	}
	secondForceCalls := 0
	for _, call := range calls {
		if call == "ip -4 route replace default via 192.168.50.1 dev eth0 metric 202" {
			secondForceCalls++
		}
	}
	if secondForceCalls != 1 {
		t.Fatalf("healthy route was rewritten again: calls=%v", calls)
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
