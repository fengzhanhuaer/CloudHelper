package main

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"testing"
)

func localExitTestConfig() probeVirtualRouterConfig {
	return probeVirtualRouterConfig{Enabled: true, FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs:      []probeVirtualRouterProbeIP{{NodeID: "1", IP: "198.18.0.2", DisplayName: "Source"}, {NodeID: "2", IP: "198.18.0.3", DisplayName: "Default"}, {NodeID: "3", IP: "198.18.0.4", DisplayName: "Local"}},
		TopologyRules: []probeVirtualRouterTopologyRule{{ID: "link12", FromNodeID: "1", ToNodeID: "2", Enabled: true, UserID: "owner"}, {ID: "link13", FromNodeID: "1", ToNodeID: "3", Enabled: true, UserID: "owner"}},
		RouteRules:    []probeVirtualRouterRouteRule{{ID: "domain", Name: "Domain", Action: "probe_exit", ExitNodeID: "2", BackupExitNodeID: "1", Entries: []string{"domain_suffix:example.com"}}, {ID: "cidr", Name: "CIDR", Action: "probe_exit", ExitNodeID: "2", Entries: []string{"cidr:203.0.113.0/24"}}, {ID: "direct", Name: "Direct", Action: "direct", Entries: []string{"domain:direct.test"}}},
		FakeIPLibrary: probeVirtualRouterFakeIPLibrary{Items: []probeVirtualRouterFakeIPEntry{{Domain: "www.example.com", FakeIP: "198.18.4.9", RuleID: "domain", Action: "probe_exit", ExitNodeID: "2"}}},
	}
}

func localExitTestRuntime(t *testing.T) {
	t.Helper()
	probeVirtualRouterExitSettingsState.Lock()
	probeVirtualRouterExitSettingsState.overrides = nil
	probeVirtualRouterExitSettingsState.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterExitSettingsState.Lock()
		probeVirtualRouterExitSettingsState.overrides = nil
		probeVirtualRouterExitSettingsState.Unlock()
	})
	previous := activeProbeProductProfile.EnableVRoutePlatformInterface
	activeProbeProductProfile.EnableVRoutePlatformInterface = false
	t.Cleanup(func() {
		waitProbeVirtualRouterLocalInterfaceIPEnsure()
		activeProbeProductProfile.EnableVRoutePlatformInterface = previous
	})
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
}

func TestProbeLocalExitChoicesPersistWithoutChangingControllerPolicy(t *testing.T) {
	localExitTestRuntime(t)
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	config := localExitTestConfig()
	applyProbeVirtualRouterConfigForNode(config, "1")
	if err := saveProbeVirtualRouterExitOverrides(map[string]string{"domain": "3"}); err != nil {
		t.Fatal(err)
	}
	rule, _ := currentProbeVirtualRouterRouteRuleForDomain("www.example.com")
	if rule.ExitNodeID != "3" || rule.BackupExitNodeID != "" {
		t.Fatalf("override=%+v", rule)
	}
	cidr, _ := currentProbeVirtualRouterRouteRuleForIP("203.0.113.8")
	if cidr.ExitNodeID != "2" {
		t.Fatal("other rule changed")
	}
	path := currentProbeVirtualRouterPathToIP("198.18.4.9")
	if !reflect.DeepEqual(path, []string{"1", "3"}) {
		t.Fatalf("fake path=%v", path)
	}
	for _, target := range []string{"www.example.com:443", "198.18.4.9:443"} {
		d, err := decideProbeVRouteProxyTarget(target)
		if err != nil || d.ExitNodeID != "3" {
			t.Fatalf("target=%s decision=%+v err=%v", target, d, err)
		}
	}
	raw := currentProbeVirtualRouterConfig()
	if raw.RouteRules[0].ExitNodeID != "2" || raw.FakeIPLibrary.Items[0].ExitNodeID != "2" {
		t.Fatal("controller defaults polluted")
	}
	// Simulated restart reloads only the separate local file.
	probeVirtualRouterExitSettingsState.Lock()
	pathFile := probeVirtualRouterExitSettingsState.path
	probeVirtualRouterExitSettingsState.path = ""
	probeVirtualRouterExitSettingsState.Unlock()
	if loadProbeVirtualRouterExitOverrides()["domain"] != "3" {
		t.Fatal("override did not survive reload")
	}
	if info, err := os.Stat(pathFile); err != nil || info.Size() > 1024 {
		t.Fatalf("local file=%v err=%v", info, err)
	}
	config.RouteRules[0].ExitNodeID = "1"
	config.RouteRules[0].Entries = append(config.RouteRules[0].Entries, "domain_suffix:changed.test")
	applyProbeVirtualRouterConfigForNode(config, "1")
	rule, _ = currentProbeVirtualRouterRouteRuleForDomain("changed.test")
	if rule.ExitNodeID != "3" {
		t.Fatal("sync overwrote choice")
	}
	if err := saveProbeVirtualRouterExitOverrides(map[string]string{"domain": "3", "cidr": "3"}); err != nil {
		t.Fatal(err)
	}
	rule, _ = currentProbeVirtualRouterRouteRuleForIP("203.0.113.8")
	if rule.ExitNodeID != "3" {
		t.Fatal("CIDR override missing")
	}
	if err := saveProbeVirtualRouterExitOverrides(nil); err != nil {
		t.Fatal(err)
	}
	rule, _ = currentProbeVirtualRouterRouteRuleForDomain("changed.test")
	if rule.ExitNodeID != "1" {
		t.Fatal("reset did not restore latest controller default")
	}
	for _, bad := range []map[string]string{{"unknown": "3"}, {"direct": "3"}, {"domain": "404"}} {
		if err := saveProbeVirtualRouterExitOverrides(bad); err == nil {
			t.Fatalf("accepted invalid choice %v", bad)
		}
	}
}

func TestProbeLocalExitChoicesAPIOnlyAcceptsExits(t *testing.T) {
	localExitTestRuntime(t)
	mux := setupProbeLocalConsoleTest(t)
	config := localExitTestConfig()
	applyProbeVirtualRouterConfigForNode(config, "1")
	session := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	path := "/local/api/virtual_router/exits"
	if r := doProbeLocalRequest(t, mux, http.MethodGet, path, nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous=%d", r.Code)
	}
	for _, body := range []any{map[string]any{"entries": []string{"domain:changed.test"}}, map[string]any{"overrides": map[string]string{"direct": "3"}}, map[string]any{"overrides": map[string]string{"domain": "404"}}} {
		r := doProbeLocalRequest(t, mux, http.MethodPost, path, body, session)
		if r.Code != http.StatusBadRequest {
			t.Fatalf("invalid=%d %s", r.Code, r.Body.String())
		}
	}
	r := doProbeLocalRequest(t, mux, http.MethodPost, path, map[string]any{"overrides": map[string]string{"domain": "3"}}, session)
	if r.Code != http.StatusOK {
		t.Fatalf("save=%d %s", r.Code, r.Body.String())
	}
	var result struct {
		Rules []struct {
			ID        string `json:"id"`
			Default   string `json:"default_exit_node_id"`
			Effective string `json:"effective_exit_node_id"`
		}
	}
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatalf("result=%s", r.Body.String())
	}
	found := false
	for _, rule := range result.Rules {
		if rule.ID == "domain" {
			found = rule.Default == "2" && rule.Effective == "3"
		}
	}
	if !found {
		t.Fatalf("result=%s", r.Body.String())
	}
	r = doProbeLocalRequest(t, mux, http.MethodPost, path, map[string]any{"reset": true}, session)
	if r.Code != http.StatusOK || len(loadProbeVirtualRouterExitOverrides()) != 0 {
		t.Fatal("reset failed")
	}
}

func TestProbeGenericExitAcceptsAuthenticatedNonDefaultPath(t *testing.T) {
	localExitTestRuntime(t)
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	config := localExitTestConfig()
	applyProbeVirtualRouterConfigForNode(config, "3")
	rt := &probeVirtualRouterRuntime{cfg: probeVirtualRouterRuntimeConfig{localNodeID: "3", peerNodeID: "1", userID: "owner", identity: nodeIdentity{NodeID: "3"}}}
	if !probeVirtualRouterGenericExitPath(rt, []string{"1", "3"}) {
		t.Fatal("valid generic path rejected")
	}
	for _, path := range [][]string{{"1", "2"}, {"2", "3"}, {"1", "3", "1", "3"}} {
		if probeVirtualRouterGenericExitPath(rt, path) {
			t.Fatalf("invalid path accepted %v", path)
		}
	}
	rt.cfg.userID = "other"
	if probeVirtualRouterGenericExitPath(rt, []string{"1", "3"}) {
		t.Fatal("cross-tenant path accepted")
	}
	rt.cfg.userID = "owner"
	if !probeVirtualRouterPacketTargetsLocalDelivery(rt, "198.18.4.9", []string{"1", "3"}) {
		t.Fatal("non-default Fake IP exit not delivered")
	}
	if err := authorizeProbeVRouteProxyExitTarget("203.0.113.8:443", []string{"1", "3"}); err != nil {
		t.Fatal(err)
	}
	if err := authorizeProbeVRouteProxyExitTarget("8.8.8.8:53", []string{"1", "3"}); err != nil {
		t.Fatal("general public target rejected", err)
	}
	for _, target := range []string{"127.0.0.1:80", "192.168.52.1:80", "8.8.8.8:0"} {
		if err := authorizeProbeVRouteProxyExitTarget(target, []string{"1", "3"}); err == nil {
			t.Fatalf("invalid target accepted %s", target)
		}
	}
}
