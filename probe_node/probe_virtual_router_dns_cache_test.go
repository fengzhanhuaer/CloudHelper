package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestProbeLocalDNSFakeIPPersistsOnFlushAndReloads(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	decision := probeLocalDNSRouteDecision{Group: "media", Action: "tunnel", SelectedRouteID: "route-1", TunnelNodeID: "route:route-1"}
	fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", decision)
	if !ok || strings.TrimSpace(fakeIP) == "" {
		t.Fatalf("allocate fake ip failed: ip=%q ok=%v", fakeIP, ok)
	}
	cachePath, err := resolveProbeLocalDNSCachePath()
	if err != nil {
		t.Fatalf("resolve dns cache path failed: %v", err)
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fake ip allocation should wait for batched persist, stat err=%v", err)
	}
	flushProbeLocalDNSCacheToDisk()
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("fake ip allocation should persist cache after flush: %v", err)
	}

	resetProbeLocalDNSServiceForTest()

	entry, ok := lookupProbeLocalDNSFakeIPEntry(fakeIP)
	if !ok {
		t.Fatalf("reloaded fake ip %s not found", fakeIP)
	}
	if entry.Domain != "api.example.com" || entry.FakeIP != fakeIP {
		t.Fatalf("reloaded fake ip entry=%+v", entry)
	}
	reusedIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", decision)
	if !ok || reusedIP != fakeIP {
		t.Fatalf("reused fake ip=%q ok=%v want=%q", reusedIP, ok, fakeIP)
	}
}

func TestProbeLocalDNSFakeIPSkipsVirtualRouterProbeReserve(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	decision := probeLocalDNSRouteDecision{Group: "media", Action: "tunnel", SelectedRouteID: "route-1", TunnelNodeID: "route:route-1"}
	fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", decision)
	if !ok {
		t.Fatalf("allocate fake ip failed")
	}
	if fakeIP != "198.18.4.1" {
		t.Fatalf("fake ip=%q, want first ordinary fake ip after reserved 1024 probe IPs", fakeIP)
	}
}

func TestProbeVirtualRouterLocalSettingsMissingFieldKeepsDefaultDisabled(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(resetProbeVirtualRouterLocalSettingsForTest)
	path, err := resolveProbeVirtualRouterLocalSettingsPath()
	if err != nil {
		t.Fatalf("resolve settings path failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"virtual_router_enabled":false}`), 0o644); err != nil {
		t.Fatalf("write settings failed: %v", err)
	}
	settings := loadProbeVirtualRouterLocalSettings()
	if settings.VirtualRouterEnabled || settings.VirtualDNSEnabled {
		t.Fatalf("settings=%+v, want router=false dns default false", settings)
	}
}

func TestResolveProbeVirtualRouterDNSResponseUsesControllerFakeIPForExitRule(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	enableProbeVirtualRouterLocalSettingsForTest(true, true)
	restore := setProbeVirtualRouterDNSConfigForTest(t, probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:         "rr-reddit",
			Name:       "reddit",
			Action:     "probe_exit",
			ExitNodeID: "9",
			Entries:    []string{"domain_suffix:reddit.com"},
		}},
	})
	defer restore()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	oldRequest := probeRequestRouteFakeIP
	probeRequestRouteFakeIP = func(ctx context.Context, controllerBaseURL string, identity nodeIdentity, domain string, rule probeVirtualRouterRouteRule) (probeVirtualRouterFakeIPEntry, probeVirtualRouterFakeIPLibrary, error) {
		entry := probeVirtualRouterFakeIPEntry{
			Domain:     domain,
			FakeIP:     "198.18.4.9",
			RuleID:     rule.ID,
			Action:     rule.Action,
			ExitNodeID: rule.ExitNodeID,
			ExpiresAt:  time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339),
			UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		}
		return entry, probeVirtualRouterFakeIPLibrary{
			Version:   2,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Items:     []probeVirtualRouterFakeIPEntry{entry},
		}, nil
	}
	rememberProbeVirtualRouterController(nodeIdentity{NodeID: "1", Secret: "secret-1"}, "https://controller.example")
	t.Cleanup(func() {
		probeRequestRouteFakeIP = oldRequest
		rememberProbeVirtualRouterController(nodeIdentity{}, "")
	})

	packet, err := buildProbeLocalDNSQueryA("www.reddit.com")
	if err != nil {
		t.Fatalf("build dns query failed: %v", err)
	}
	result, err := resolveProbeVirtualRouterDNSPacket(packet)
	if err == nil {
		t.Fatalf("first resolve should schedule fake ip request in background")
	}
	if result.Domain != "www.reddit.com" || len(result.RealIPs) != 0 {
		t.Fatalf("domain=%q ips=%v", result.Domain, result.RealIPs)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if item, ok := currentProbeVirtualRouterFakeIPEntryByDomain("www.reddit.com"); ok && item.FakeIP == "198.18.4.9" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake ip entry was not applied after background request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	result, err = resolveProbeVirtualRouterDNSPacket(packet)
	if err != nil {
		t.Fatalf("resolve virtual router dns after background request failed: %v", err)
	}
	if got := strings.Join(extractProbeLocalDNSResponseIPsBestEffort(result.Response), ","); got != "198.18.4.9" {
		t.Fatalf("response fake ip=%q", got)
	}
	if item, ok := currentProbeVirtualRouterFakeIPEntryByDomain("www.reddit.com"); !ok || item.FakeIP != "198.18.4.9" {
		t.Fatalf("cached fake ip entry=%+v ok=%v", item, ok)
	}
}

func TestResolveProbeVirtualRouterFakeIPForDNSPrefersLocalCache(t *testing.T) {
	restore := setProbeVirtualRouterDNSConfigForTest(t, probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{
			Version:   9,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Items: []probeVirtualRouterFakeIPEntry{{
				Domain:     "www.reddit.com",
				FakeIP:     "198.18.4.9",
				RuleID:     "rr-reddit",
				Action:     "probe_exit",
				ExitNodeID: "9",
				ExpiresAt:  time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339),
				UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
			}},
		},
	})
	defer restore()

	oldRequest := probeRequestRouteFakeIP
	requests := 0
	probeRequestRouteFakeIP = func(ctx context.Context, controllerBaseURL string, identity nodeIdentity, domain string, rule probeVirtualRouterRouteRule) (probeVirtualRouterFakeIPEntry, probeVirtualRouterFakeIPLibrary, error) {
		requests++
		return probeVirtualRouterFakeIPEntry{}, probeVirtualRouterFakeIPLibrary{}, errors.New("controller should not be called for cached fake ip")
	}
	rememberProbeVirtualRouterController(nodeIdentity{NodeID: "1", Secret: "secret-1"}, "https://controller.example")
	t.Cleanup(func() {
		probeRequestRouteFakeIP = oldRequest
		rememberProbeVirtualRouterController(nodeIdentity{}, "")
	})

	item, err := resolveProbeVirtualRouterFakeIPForDNS("www.reddit.com", probeVirtualRouterRouteRule{
		ID:         "rr-reddit",
		Action:     "probe_exit",
		ExitNodeID: "9",
	})
	if err != nil {
		t.Fatalf("resolve cached fake ip failed: %v", err)
	}
	if item.FakeIP != "198.18.4.9" || requests != 0 {
		t.Fatalf("item=%+v controller_requests=%d", item, requests)
	}
}

func TestResolveProbeVirtualRouterDNSResponseReject(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	resetProbeVirtualRouterLocalSettingsForTest()
	enableProbeVirtualRouterLocalSettingsForTest(true, true)
	restore := setProbeVirtualRouterDNSConfigForTest(t, probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		RouteRules: []probeVirtualRouterRouteRule{{
			ID:      "rr-reject",
			Name:    "reject",
			Action:  "reject",
			Entries: []string{"domain_keyword:block-me"},
		}},
	})
	defer restore()
	t.Cleanup(func() {
		resetProbeLocalDNSServiceForTest()
		resetProbeVirtualRouterLocalSettingsForTest()
	})

	rejectPacket, err := buildProbeLocalDNSQueryA("block-me.example")
	if err != nil {
		t.Fatalf("build reject dns query failed: %v", err)
	}
	rejectResult, err := resolveProbeVirtualRouterDNSPacket(rejectPacket)
	if err != nil {
		t.Fatalf("resolve reject dns failed: %v", err)
	}
	if dnsResponseRCodeForTest(t, rejectResult.Response) != dnsmessage.RCodeRefused {
		t.Fatalf("reject rcode=%v", dnsResponseRCodeForTest(t, rejectResult.Response))
	}
}

func setProbeVirtualRouterDNSConfigForTest(t *testing.T, config probeVirtualRouterConfig) func() {
	t.Helper()
	probeVirtualRouterState.mu.Lock()
	oldConfig := probeVirtualRouterState.config
	oldLocalNodeID := probeVirtualRouterState.localNodeID
	oldLocalIP := probeVirtualRouterState.localIP
	oldNodeToIP := probeVirtualRouterState.nodeToIP
	oldIPToNode := probeVirtualRouterState.ipToNode
	oldNeighbors := probeVirtualRouterState.neighbors
	oldRulesByID := probeVirtualRouterState.rulesByID
	oldSignature := probeVirtualRouterState.topologySignature
	sanitized := sanitizeProbeVirtualRouterConfigForCache(config)
	index := buildProbeVirtualRouterTopologyIndex(sanitized)
	probeVirtualRouterState.config = sanitized
	probeVirtualRouterState.localNodeID = "1"
	probeVirtualRouterState.localIP = index.nodeToIP["1"]
	probeVirtualRouterState.nodeToIP = index.nodeToIP
	probeVirtualRouterState.ipToNode = index.ipToNode
	probeVirtualRouterState.neighbors = index.neighbors
	probeVirtualRouterState.rulesByID = index.rulesByID
	probeVirtualRouterState.topologySignature = "test"
	probeVirtualRouterState.mu.Unlock()
	return func() {
		probeVirtualRouterState.mu.Lock()
		probeVirtualRouterState.config = oldConfig
		probeVirtualRouterState.localNodeID = oldLocalNodeID
		probeVirtualRouterState.localIP = oldLocalIP
		probeVirtualRouterState.nodeToIP = oldNodeToIP
		probeVirtualRouterState.ipToNode = oldIPToNode
		probeVirtualRouterState.neighbors = oldNeighbors
		probeVirtualRouterState.rulesByID = oldRulesByID
		probeVirtualRouterState.topologySignature = oldSignature
		probeVirtualRouterState.mu.Unlock()
	}
}

func enableProbeVirtualRouterLocalSettingsForTest(routerEnabled bool, dnsEnabled bool) {
	probeVirtualRouterLocalSettingsState.mu.Lock()
	probeVirtualRouterLocalSettingsState.loaded = true
	probeVirtualRouterLocalSettingsState.settings = probeVirtualRouterLocalSettings{
		VirtualRouterEnabled: routerEnabled,
		VirtualDNSEnabled:    dnsEnabled,
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339),
	}
	probeVirtualRouterLocalSettingsState.mu.Unlock()
}

func dnsResponseRCodeForTest(t *testing.T, packet []byte) dnsmessage.RCode {
	t.Helper()
	parser := dnsmessage.Parser{}
	header, err := parser.Start(packet)
	if err != nil {
		t.Fatalf("parse dns response failed: %v", err)
	}
	return header.RCode
}
