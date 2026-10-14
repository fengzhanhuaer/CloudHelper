package main

import (
	"errors"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestProbeVirtualRouterBackupExitSelectsHealthyPathAndReturnsToPrimary(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	rule := probeVirtualRouterRouteRule{
		ID: "rr-backup", Name: "backup", Action: "probe_exit", ExitNodeID: "2", BackupExitNodeID: "3",
		Entries: []string{"domain_suffix:example.com", "cidr:203.0.113.0/24"},
	}
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = probeVirtualRouterConfig{
		Enabled: true, FakeIPCIDR: "198.18.0.0/15", RouteRules: []probeVirtualRouterRouteRule{rule},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{Items: []probeVirtualRouterFakeIPEntry{{
			Domain: "api.example.com", FakeIP: "198.18.4.9", RuleID: rule.ID, Action: "probe_exit", ExitNodeID: "2",
		}}},
	}
	probeVirtualRouterState.localNodeID = "1"
	probeVirtualRouterState.neighbors = map[string]map[string]struct{}{
		"1": {"2": {}, "3": {}}, "2": {"1": {}}, "3": {"1": {}},
	}
	probeVirtualRouterState.mu.Unlock()

	primary := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "primary", localNodeID: "1", peerNodeID: "2", dialer: true}}
	backup := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "backup", localNodeID: "1", peerNodeID: "3", dialer: true}}
	probeVirtualRouterRuntimeState.mu.Lock()
	previous := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{"primary": primary, "backup": backup}
	probeVirtualRouterRuntimeState.mu.Unlock()
	t.Cleanup(func() {
		closeProbeVirtualRouterFrameLinks("backup exit test cleanup")
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = previous
		probeVirtualRouterRuntimeState.mu.Unlock()
	})
	attach := func(rt *probeVirtualRouterRuntime) {
		left, right := net.Pipe()
		t.Cleanup(func() { _ = right.Close() })
		key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
		link := newProbeVirtualRouterFrameLink(key, rt, nil, nil)
		link.AttachCarrier(left, rt.cfg.routeID, "pipe")
		probeVirtualRouterFrameLinkState.mu.Lock()
		if probeVirtualRouterFrameLinkState.links == nil {
			probeVirtualRouterFrameLinkState.links = make(map[string]*probeVirtualRouterFrameLink)
		}
		probeVirtualRouterFrameLinkState.links[key] = link
		probeVirtualRouterFrameLinkState.mu.Unlock()
	}
	attach(backup)
	packet := buildProbeVirtualRouterTestTCPPacket(t, "198.18.0.1", "198.18.4.9", 49152, 443)
	for _, path := range [][]string{
		currentProbeVirtualRouterPathForPacket(packet, "198.18.4.9"),
		currentProbeVirtualRouterPathToIP("198.18.4.9"),
		currentProbeVirtualRouterPathToIP("203.0.113.9"),
	} {
		if !reflect.DeepEqual(path, []string{"1", "3"}) {
			t.Fatalf("unavailable primary should use backup, got path=%v", path)
		}
	}
	decision, err := decideProbeVRouteProxyTarget("api.example.com:443")
	if err != nil || decision.ExitNodeID != "3" || !reflect.DeepEqual(decision.Path, []string{"1", "3"}) {
		t.Fatalf("proxy backup decision=%+v err=%v", decision, err)
	}
	attach(primary)
	if path := currentProbeVirtualRouterPathForPacket(packet, "198.18.4.9"); !reflect.DeepEqual(path, []string{"1", "2"}) {
		t.Fatalf("recovered primary should regain priority, got path=%v", path)
	}
	decision, err = decideProbeVRouteProxyTarget("api.example.com:443")
	if err != nil || decision.ExitNodeID != "2" {
		t.Fatalf("proxy primary decision=%+v err=%v", decision, err)
	}
}

func TestProbeVirtualRouterBackupExitUsesRecentEndToEndFailure(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.localNodeID = "1"
	probeVirtualRouterState.neighbors = map[string]map[string]struct{}{
		"1": {"4": {}, "3": {}}, "4": {"1": {}, "2": {}}, "2": {"4": {}}, "3": {"1": {}},
	}
	probeVirtualRouterState.mu.Unlock()
	primaryPath := []string{"1", "4", "2"}
	probeVirtualRouterBackupExitProbeState.mu.Lock()
	probeVirtualRouterBackupExitProbeState.next[probeVirtualRouterPathKey(primaryPath)] = time.Now().Add(time.Hour)
	probeVirtualRouterBackupExitProbeState.mu.Unlock()
	primaryHop := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "primary-hop", localNodeID: "1", peerNodeID: "4", dialer: true}}
	backup := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{routeID: "backup-hop", localNodeID: "1", peerNodeID: "3", dialer: true}}
	probeVirtualRouterRuntimeState.mu.Lock()
	previous := probeVirtualRouterRuntimeState.runtimes
	probeVirtualRouterRuntimeState.runtimes = map[string]*probeVirtualRouterRuntime{"primary-hop": primaryHop, "backup-hop": backup}
	probeVirtualRouterRuntimeState.mu.Unlock()
	t.Cleanup(func() {
		closeProbeVirtualRouterFrameLinks("backup exit test cleanup")
		probeVirtualRouterRuntimeState.mu.Lock()
		probeVirtualRouterRuntimeState.runtimes = previous
		probeVirtualRouterRuntimeState.mu.Unlock()
	})
	for _, rt := range []*probeVirtualRouterRuntime{primaryHop, backup} {
		left, right := net.Pipe()
		t.Cleanup(func() { _ = right.Close() })
		key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
		link := newProbeVirtualRouterFrameLink(key, rt, nil, nil)
		link.AttachCarrier(left, rt.cfg.routeID, "pipe")
		probeVirtualRouterFrameLinkState.mu.Lock()
		if probeVirtualRouterFrameLinkState.links == nil {
			probeVirtualRouterFrameLinkState.links = make(map[string]*probeVirtualRouterFrameLink)
		}
		probeVirtualRouterFrameLinkState.links[key] = link
		probeVirtualRouterFrameLinkState.mu.Unlock()
	}
	rule := probeVirtualRouterRouteRule{Action: "probe_exit", ExitNodeID: "2", BackupExitNodeID: "3"}
	if path := currentProbeVirtualRouterExitPathForRule("1", rule); !reflect.DeepEqual(path, primaryPath) {
		t.Fatalf("healthy primary path=%v", path)
	}
	recordProbeVirtualRouterPathRTTError(primaryPath, errors.New("exit did not respond"))
	recordProbeVirtualRouterPathRTTError(primaryPath, errors.New("exit did not respond"))
	if path := currentProbeVirtualRouterExitPathForRule("1", rule); !reflect.DeepEqual(path, []string{"1", "3"}) {
		t.Fatalf("failed primary should use backup, got path=%v", path)
	}
	recordProbeVirtualRouterPathRTTSuccess(primaryPath, 20*time.Millisecond, "2")
	if path := currentProbeVirtualRouterExitPathForRule("1", rule); !reflect.DeepEqual(path, primaryPath) {
		t.Fatalf("recovered primary path=%v", path)
	}
}

func TestProbeVirtualRouterBackupExitAuthorization(t *testing.T) {
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	rule := probeVirtualRouterRouteRule{ID: "rr-backup", Name: "backup", Action: "probe_exit", ExitNodeID: "2", BackupExitNodeID: "3", Entries: []string{"domain_suffix:example.com", "cidr:203.0.113.0/24"}}
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config = probeVirtualRouterConfig{
		Enabled: true, FakeIPCIDR: "198.18.0.0/15", RouteRules: []probeVirtualRouterRouteRule{rule},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{Items: []probeVirtualRouterFakeIPEntry{{Domain: "api.example.com", FakeIP: "198.18.4.9", RuleID: rule.ID, Action: "probe_exit", ExitNodeID: "2"}}},
	}
	probeVirtualRouterState.localNodeID = "3"
	probeVirtualRouterState.neighbors = map[string]map[string]struct{}{"3": {"1": {}}, "1": {"3": {}}}
	probeVirtualRouterState.mu.Unlock()
	entry, fake, allowed := currentProbeVirtualRouterExitEntryForDestinationIP("198.18.4.9", nil)
	if !fake || !allowed || entry.ExitNodeID != "2" {
		t.Fatalf("backup fake-ip exit entry=%+v fake=%t allowed=%t", entry, fake, allowed)
	}
	_, fake, allowed = currentProbeVirtualRouterExitEntryForDestinationIP("203.0.113.9", nil)
	if fake || !allowed {
		t.Fatalf("backup CIDR exit fake=%t allowed=%t", fake, allowed)
	}
	if err := authorizeProbeVRouteProxyExitTarget("203.0.113.9:443", []string{"1", "3"}); err != nil {
		t.Fatalf("backup proxy exit rejected: %v", err)
	}
	if probeVirtualRouterRuleAllowsExitNode(rule, "4") || probeVirtualRouterFakeIPEntryAllowsExitNode(entry, "4") {
		t.Fatal("unconfigured exit node was accepted")
	}
	stale := entry
	stale.RuleID = "other-rule"
	if probeVirtualRouterFakeIPEntryAllowsExitNode(stale, "3") {
		t.Fatal("stale fake-ip rule was accepted at backup exit")
	}
}
