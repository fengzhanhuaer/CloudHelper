package core

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildProbeVirtualRouterConfigForNodeScopesCredentialsToAdjacentEdges(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore })
	setProbeVirtualRouterTestProbeStore(t, probeConfigData{
		ProbeNodes: []probeNodeRecord{
			{NodeNo: 1, NodeName: "node-1"},
			{NodeNo: 2, NodeName: "node-2"},
			{NodeNo: 3, NodeName: "node-3"},
			{NodeNo: 4, NodeName: "node-4"},
		},
		ProbeSecrets: map[string]string{},
	})

	ProbeRouteConfigStore = &probeRouteConfigStore{
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: "198.18.0.0/15",
				ProbeIPs: []probeVirtualRouterProbeIP{
					{NodeID: "1", IP: "198.18.0.11"},
					{NodeID: "2", IP: "198.18.0.12"},
					{NodeID: "3", IP: "198.18.0.13"},
					{NodeID: "4", IP: "198.18.0.14"},
				},
				TopologyRules: []probeVirtualRouterTopologyRule{
					{FromNodeID: "1", ToNodeID: "2", Direction: "bidirectional", Enabled: true},
					{FromNodeID: "2", ToNodeID: "3", Direction: "bidirectional", Enabled: true},
					{FromNodeID: "3", ToNodeID: "4", Direction: "bidirectional", Enabled: true},
				},
			},
		},
	}

	config := buildProbeVirtualRouterConfigForNodeLocked("1")
	if len(config.ProbeIPs) != 4 {
		t.Fatalf("probe ips=%+v, want full virtual ip map", config.ProbeIPs)
	}
	if len(config.TopologyRules) != 3 {
		t.Fatalf("topology rules=%+v, want full virtual topology", config.TopologyRules)
	}
	for _, rule := range config.TopologyRules {
		adjacent := rule.FromNodeID == "1" || rule.ToNodeID == "1"
		if adjacent && strings.TrimSpace(rule.Secret) == "" {
			t.Fatalf("adjacent rule credentials missing: %+v", rule)
		}
		if !adjacent && (strings.TrimSpace(rule.Secret) != "" || strings.TrimSpace(rule.AuthTicket) != "") {
			t.Fatalf("unrelated rule credentials leaked: %+v", rule)
		}
	}
	if config.ProbeIPs[0].ServicePort != probeVirtualRouterDefaultServicePort {
		t.Fatalf("default probe service port=%d, want %d", config.ProbeIPs[0].ServicePort, probeVirtualRouterDefaultServicePort)
	}
	if config.ProbeIPs[1].DisplayName != "node-2" {
		t.Fatalf("probe display name=%q, want node-2", config.ProbeIPs[1].DisplayName)
	}
	if config.TopologyRules[0].FromServicePort != 0 || config.TopologyRules[0].ToServicePort != 0 {
		t.Fatalf("topology service ports=%d/%d, want zero", config.TopologyRules[0].FromServicePort, config.TopologyRules[0].ToServicePort)
	}
}

func TestProbeVirtualRouterProbeIPPoolUsesFirst1024FakeIPs(t *testing.T) {
	pool := probeVirtualRouterProbeIPPool("198.18.0.0/15")
	if len(pool) != 1022 {
		t.Fatalf("pool size=%d, want 1022", len(pool))
	}
	if pool[0] != "198.18.0.3" {
		t.Fatalf("first pool ip=%q", pool[0])
	}
	if pool[len(pool)-1] != "198.18.4.0" {
		t.Fatalf("last pool ip=%q", pool[len(pool)-1])
	}
	for _, ip := range pool {
		if ip == probeVirtualRouterReservedGatewayIP || ip == probeVirtualRouterReservedTUNIP {
			t.Fatalf("pool contains reserved ip %s", ip)
		}
	}
}

func TestEnsureProbeVirtualRouterProbeIPsAllocatesFreedAddress(t *testing.T) {
	setProbeVirtualRouterTestProbeStore(t, probeConfigData{
		ProbeNodes: []probeNodeRecord{
			{NodeNo: 1, NodeName: "node-1"},
			{NodeNo: 3, NodeName: "node-3"},
			{NodeNo: 4, NodeName: "node-4"},
		},
		DeletedProbeNodes:   []probeNodeRecord{{NodeNo: 2, NodeName: "node-2"}},
		DeletedProbeNodeNos: []int{2},
		ProbeSecrets:        map[string]string{},
	})
	clearProbeVirtualRouterTestRuntimes(t)

	config := ensureProbeVirtualRouterProbeIPsForKnownNodes(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
		ProbeIPs: []probeVirtualRouterProbeIP{
			{NodeID: "1", IP: "198.18.0.3"},
			{NodeID: "2", IP: "198.18.0.4"},
			{NodeID: "3", IP: "198.18.0.5"},
		},
	})
	ipByNode := probeVirtualRouterTestIPByNode(config.ProbeIPs)
	if _, ok := ipByNode["2"]; ok {
		t.Fatalf("deleted node should be released: %+v", config.ProbeIPs)
	}
	if ipByNode["1"] != "198.18.0.3" || ipByNode["3"] != "198.18.0.5" {
		t.Fatalf("existing active ips should be preserved: %+v", config.ProbeIPs)
	}
	if ipByNode["4"] != "198.18.0.4" {
		t.Fatalf("node 4 ip=%q, want freed 198.18.0.4; ips=%+v", ipByNode["4"], config.ProbeIPs)
	}
}

func TestEnsureProbeVirtualRouterProbeIPsAllocatesHighNodeIDFromFreePool(t *testing.T) {
	setProbeVirtualRouterTestProbeStore(t, probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 2000, NodeName: "node-2000"}},
		ProbeSecrets: map[string]string{},
	})
	clearProbeVirtualRouterTestRuntimes(t)

	config := ensureProbeVirtualRouterProbeIPsForKnownNodes(probeVirtualRouterConfig{
		Enabled:    true,
		FakeIPCIDR: "198.18.0.0/15",
	})
	ipByNode := probeVirtualRouterTestIPByNode(config.ProbeIPs)
	if ipByNode["2000"] != "198.18.0.3" {
		t.Fatalf("node 2000 ip=%q, ips=%+v", ipByNode["2000"], config.ProbeIPs)
	}
}

func TestProbeVirtualRouterFakeIPLibraryAllocatesReusesAndResetsIndependentStore(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore })
	setProbeVirtualRouterTestProbeStore(t, probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 9, NodeName: "exit-9"}},
		ProbeSecrets: map[string]string{"9": "secret-9"},
	})

	tmpDir := t.TempDir()
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
				ProbeIPs:   []probeVirtualRouterProbeIP{{NodeID: "9", IP: "198.18.0.9"}},
			},
			VirtualRouterFakeIP: defaultProbeVirtualRouterFakeIPLibrary(),
		},
	}
	rule := probeVirtualRouterRouteRule{ID: "rr-1", Action: probeVirtualRouterRouteRuleActionExit, ExitNodeID: "9"}

	first, firstLibrary, firstChanged, err := allocateProbeVirtualRouterFakeIPForDomain("WWW.Reddit.COM.", rule)
	if err != nil {
		t.Fatalf("allocate fake ip failed: %v", err)
	}
	if !firstChanged {
		t.Fatalf("first allocation did not report library change")
	}
	if first.Domain != "www.reddit.com" || first.FakeIP != "198.18.4.1" || first.ExitNodeID != "9" {
		t.Fatalf("first fake ip entry=%+v", first)
	}
	if firstLibrary.Version != 2 || len(firstLibrary.Items) != 1 {
		t.Fatalf("first library=%+v", firstLibrary)
	}
	expiresAt, err := time.Parse(time.RFC3339, first.ExpiresAt)
	if err != nil || time.Until(expiresAt) < 29*24*time.Hour {
		t.Fatalf("expires_at=%q err=%v", first.ExpiresAt, err)
	}
	oldReuseExpiresAt := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	ProbeRouteConfigStore.data.VirtualRouterFakeIP.Items[0].ExpiresAt = oldReuseExpiresAt

	second, secondLibrary, secondChanged, err := allocateProbeVirtualRouterFakeIPForDomain("www.reddit.com", rule)
	if err != nil {
		t.Fatalf("reuse fake ip failed: %v", err)
	}
	if !secondChanged {
		t.Fatalf("reused fake ip lookup should renew library ttl")
	}
	if second.FakeIP != first.FakeIP || secondLibrary.Version != firstLibrary.Version+1 || second.ExpiresAt == oldReuseExpiresAt {
		t.Fatalf("reuse entry=%+v library=%+v first=%+v first_library=%+v", second, secondLibrary, first, firstLibrary)
	}

	resetLibrary, err := resetProbeVirtualRouterFakeIPLibrary()
	if err != nil {
		t.Fatalf("reset fake ip library failed: %v", err)
	}
	if len(resetLibrary.Items) != 0 || resetLibrary.Version != secondLibrary.Version+1 {
		t.Fatalf("reset library=%+v second_version=%d", resetLibrary, secondLibrary.Version)
	}
}

func TestProbeVirtualRouterFakeIPLibraryDoesNotBatchRenewOnMaintenance(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore })

	tmpDir := t.TempDir()
	now := time.Now().UTC()
	oldUpdatedAt := now.Add(-49 * time.Hour).Format(time.RFC3339)
	oldExpiresAt := now.Add(2 * time.Hour).Format(time.RFC3339)
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
				RouteRules: []probeVirtualRouterRouteRule{{
					ID: "rr-1", Name: "reddit", Action: probeVirtualRouterRouteRuleActionExit, ExitNodeID: "9", Entries: []string{"domain_suffix:reddit.com"},
				}},
			},
			VirtualRouterFakeIP: probeVirtualRouterFakeIPLibrary{
				Version:   7,
				UpdatedAt: oldUpdatedAt,
				Items: []probeVirtualRouterFakeIPEntry{{
					Domain:     "www.reddit.com",
					FakeIP:     "198.18.4.1",
					RuleID:     "rr-1",
					Action:     probeVirtualRouterRouteRuleActionExit,
					ExitNodeID: "9",
					ExpiresAt:  oldExpiresAt,
					UpdatedAt:  oldUpdatedAt,
				}},
			},
		},
	}

	if reconcileProbeVirtualRouterFakeIPLibraryBestEffort() {
		t.Fatalf("maintenance should not batch renew fake ip library")
	}
	library := ProbeRouteConfigStore.data.VirtualRouterFakeIP
	if library.Version != 7 || len(library.Items) != 1 || library.Items[0].ExpiresAt != oldExpiresAt {
		t.Fatalf("library should remain unchanged without hit renew: %+v", library)
	}
}

func TestProbeRouteFakeIPResolveHandlerPersistsLibrary(t *testing.T) {
	t.Setenv("PROBE_TRUSTED_PROXY_CIDRS", "192.0.2.0/24")
	oldRouteStore := ProbeRouteConfigStore
	oldProbeStore := ProbeStore
	t.Cleanup(func() {
		ProbeRouteConfigStore = oldRouteStore
		ProbeStore = oldProbeStore
	})
	tmpDir := t.TempDir()
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 1, NodeName: "node-1"}, {NodeNo: 9, NodeName: "exit-9"}},
		ProbeSecrets: map[string]string{"1": "secret-1", "9": "secret-9"},
	}}
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
				RouteRules: []probeVirtualRouterRouteRule{{
					ID:         "rr-1",
					Name:       "reddit",
					Action:     probeVirtualRouterRouteRuleActionExit,
					ExitNodeID: "9",
					Entries:    []string{"domain_suffix:reddit.com"},
				}},
			},
			VirtualRouterFakeIP: defaultProbeVirtualRouterFakeIPLibrary(),
		},
	}
	body := bytes.NewBufferString(`{"domain":"api.reddit.com","rule_id":"rr-1","action":"probe_exit","exit_node_id":"9"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/probe/route/fake_ip/resolve", body)
	req.Header.Set("X-Forwarded-Proto", "https")
	applyProbeChallengeAuthForTest(t, req, "1", "secret-1")
	rr := httptest.NewRecorder()

	ProbeRouteFakeIPResolveHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("resolve status=%d body=%s", rr.Code, rr.Body.String())
	}
	var payload probeRouteFakeIPResolveResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response failed: %v body=%s", err, rr.Body.String())
	}
	if payload.Item.Domain != "api.reddit.com" || payload.Item.FakeIP == "" {
		t.Fatalf("payload=%+v", payload)
	}

	body = bytes.NewBufferString(`{"domain":"api.reddit.com","rule_id":"rr-1","action":"probe_exit","exit_node_id":"9"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/probe/route/fake_ip/resolve", body)
	req.Header.Set("X-Forwarded-Proto", "https")
	applyProbeChallengeAuthForTest(t, req, "1", "secret-1")
	rr = httptest.NewRecorder()
	ProbeRouteFakeIPResolveHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("second resolve status=%d body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode second response failed: %v body=%s", err, rr.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(tmpDir, "probe_route_config.json"))
	if err != nil {
		t.Fatalf("read persisted route config failed: %v", err)
	}
	if !strings.Contains(string(raw), `"virtual_router_fake_ip"`) || !strings.Contains(string(raw), `"api.reddit.com"`) {
		t.Fatalf("persisted config missing fake ip library: %s", string(raw))
	}
}

func TestAuthorizedProbeVirtualRouterFakeIPRuleRejectsClientRuleOverride(t *testing.T) {
	oldRouteStore := ProbeRouteConfigStore
	ProbeRouteConfigStore = &probeRouteConfigStore{data: probeRouteConfigStoreData{
		VirtualRouter: probeVirtualRouterConfig{
			Enabled: true,
			RouteRules: []probeVirtualRouterRouteRule{{
				ID: "rr-1", Name: "reddit", Action: probeVirtualRouterRouteRuleActionExit, ExitNodeID: "9", Entries: []string{"domain_suffix:reddit.com"},
			}},
		},
	}}
	t.Cleanup(func() { ProbeRouteConfigStore = oldRouteStore })

	if _, err := authorizedProbeVirtualRouterFakeIPRule("api.reddit.com", "rr-other", "probe_exit", "9"); err == nil {
		t.Fatal("client-supplied rule override should be rejected")
	}
	if _, err := authorizedProbeVirtualRouterFakeIPRule("api.reddit.com", "rr-1", "probe_exit", "8"); err == nil {
		t.Fatal("client-supplied exit override should be rejected")
	}
	if _, err := authorizedProbeVirtualRouterFakeIPRule("api.reddit.com", "rr-1", "probe_exit", "9"); err != nil {
		t.Fatalf("matching stored rule rejected: %v", err)
	}
}

func TestProbeRouteFakeIPRenewHandlerRenewsOnlyReportedDomains(t *testing.T) {
	t.Setenv("PROBE_TRUSTED_PROXY_CIDRS", "192.0.2.0/24")
	oldRouteStore := ProbeRouteConfigStore
	oldProbeStore := ProbeStore
	t.Cleanup(func() {
		ProbeRouteConfigStore = oldRouteStore
		ProbeStore = oldProbeStore
	})
	tmpDir := t.TempDir()
	now := time.Now().UTC()
	oldExpiresAt := now.Add(2 * time.Hour).Format(time.RFC3339)
	otherExpiresAt := now.Add(3 * time.Hour).Format(time.RFC3339)
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 1, NodeName: "node-1"}, {NodeNo: 9, NodeName: "exit-9"}},
		ProbeSecrets: map[string]string{"1": "secret-1", "9": "secret-9"},
	}}
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
				ProbeIPs:   []probeVirtualRouterProbeIP{{NodeID: "9", IP: "198.18.0.9"}},
				RouteRules: []probeVirtualRouterRouteRule{{
					ID:         "rr-1",
					Name:       "reddit",
					Action:     probeVirtualRouterRouteRuleActionExit,
					ExitNodeID: "9",
					Entries:    []string{"domain_suffix:reddit.com"},
				}},
			},
			VirtualRouterFakeIP: probeVirtualRouterFakeIPLibrary{
				Version:   4,
				UpdatedAt: now.Add(-24 * time.Hour).Format(time.RFC3339),
				Items: []probeVirtualRouterFakeIPEntry{
					{Domain: "api.reddit.com", FakeIP: "198.18.4.1", RuleID: "rr-1", Action: probeVirtualRouterRouteRuleActionExit, ExitNodeID: "9", ExpiresAt: oldExpiresAt, UpdatedAt: oldExpiresAt},
					{Domain: "old.example.com", FakeIP: "198.18.4.2", RuleID: "rr-old", Action: probeVirtualRouterRouteRuleActionExit, ExitNodeID: "9", ExpiresAt: otherExpiresAt, UpdatedAt: otherExpiresAt},
				},
			},
		},
	}

	body := bytes.NewBufferString(`{"domains":["api.reddit.com"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/probe/route/fake_ip/renew", body)
	req.Header.Set("X-Forwarded-Proto", "https")
	applyProbeChallengeAuthForTest(t, req, "1", "secret-1")
	rr := httptest.NewRecorder()
	ProbeRouteFakeIPRenewHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("renew status=%d body=%s", rr.Code, rr.Body.String())
	}
	var payload probeRouteFakeIPRenewResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode renew response failed: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Domain != "api.reddit.com" {
		t.Fatalf("renew payload=%+v", payload)
	}
	library := ProbeRouteConfigStore.data.VirtualRouterFakeIP
	if library.Version != 5 || len(library.Items) != 2 {
		t.Fatalf("library=%+v", library)
	}
	renewed, err := time.Parse(time.RFC3339, library.Items[0].ExpiresAt)
	if err != nil || time.Until(renewed) < 29*24*time.Hour {
		t.Fatalf("renewed expires_at=%q err=%v", library.Items[0].ExpiresAt, err)
	}
	if library.Items[1].ExpiresAt != otherExpiresAt {
		t.Fatalf("unreachable/unmatched domain should not renew: %+v", library.Items[1])
	}
}

func TestProbeRouteFakeIPResolveHandlerRenewsOnFakeIPLookup(t *testing.T) {
	t.Setenv("PROBE_TRUSTED_PROXY_CIDRS", "192.0.2.0/24")
	oldRouteStore := ProbeRouteConfigStore
	oldProbeStore := ProbeStore
	t.Cleanup(func() {
		ProbeRouteConfigStore = oldRouteStore
		ProbeStore = oldProbeStore
	})
	tmpDir := t.TempDir()
	now := time.Now().UTC()
	oldExpiresAt := now.Add(2 * time.Hour).Format(time.RFC3339)
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 1, NodeName: "node-1"}, {NodeNo: 9, NodeName: "exit-9"}},
		ProbeSecrets: map[string]string{"1": "secret-1", "9": "secret-9"},
	}}
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
				ProbeIPs:   []probeVirtualRouterProbeIP{{NodeID: "9", IP: "198.18.0.9"}},
				RouteRules: []probeVirtualRouterRouteRule{{
					ID:         "rr-1",
					Name:       "reddit",
					Action:     probeVirtualRouterRouteRuleActionExit,
					ExitNodeID: "9",
					Entries:    []string{"domain_suffix:reddit.com"},
				}},
			},
			VirtualRouterFakeIP: probeVirtualRouterFakeIPLibrary{
				Version:   4,
				UpdatedAt: now.Add(-24 * time.Hour).Format(time.RFC3339),
				Items: []probeVirtualRouterFakeIPEntry{{
					Domain:     "api.reddit.com",
					FakeIP:     "198.18.4.1",
					RuleID:     "rr-old",
					Action:     probeVirtualRouterRouteRuleActionExit,
					ExitNodeID: "9",
					ExpiresAt:  oldExpiresAt,
					UpdatedAt:  oldExpiresAt,
				}},
			},
		},
	}

	body := bytes.NewBufferString(`{"fake_ip":"198.18.4.1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/probe/route/fake_ip/resolve", body)
	req.Header.Set("X-Forwarded-Proto", "https")
	applyProbeChallengeAuthForTest(t, req, "1", "secret-1")
	rr := httptest.NewRecorder()
	ProbeRouteFakeIPResolveHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("fake ip resolve status=%d body=%s", rr.Code, rr.Body.String())
	}
	var payload probeRouteFakeIPResolveResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode fake ip resolve failed: %v", err)
	}
	if payload.Item.Domain != "api.reddit.com" || payload.Item.RuleID != "rr-1" {
		t.Fatalf("payload item=%+v", payload.Item)
	}
	library := ProbeRouteConfigStore.data.VirtualRouterFakeIP
	if library.Version != 5 || len(library.Items) != 1 {
		t.Fatalf("library=%+v", library)
	}
	renewed, err := time.Parse(time.RFC3339, library.Items[0].ExpiresAt)
	if err != nil || time.Until(renewed) < 29*24*time.Hour {
		t.Fatalf("fake ip lookup should renew expires_at=%q err=%v", library.Items[0].ExpiresAt, err)
	}
}

func TestMngLinkVirtualRouterFakeIPResetHandlerDoesNotDispatchRouteConfigSync(t *testing.T) {
	oldRouteStore := ProbeRouteConfigStore
	oldProbeStore := ProbeStore
	t.Cleanup(func() {
		ProbeRouteConfigStore = oldRouteStore
		ProbeStore = oldProbeStore
	})
	tmpDir := t.TempDir()
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 1, NodeName: "node-1"}},
		ProbeSecrets: map[string]string{"1": "secret-1"},
	}}
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(tmpDir, "probe_route_config.json"),
		data: probeRouteConfigStoreData{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: probeVirtualRouterDefaultCIDR,
			},
			VirtualRouterFakeIP: probeVirtualRouterFakeIPLibrary{
				Version:   3,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
				Items: []probeVirtualRouterFakeIPEntry{{
					Domain:    "api.reddit.com",
					FakeIP:    "198.18.4.1",
					ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
				}},
			},
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/mng/api/route/virtual_router/fake_ip/reset", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	mngRouteVirtualRouterFakeIPResetHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", rr.Code, rr.Body.String())
	}
	var payload struct {
		FakeIPLibrary probeVirtualRouterFakeIPLibrary `json:"fake_ip_library"`
		Sync          any                             `json:"sync"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode reset payload failed: %v body=%s", err, rr.Body.String())
	}
	if len(payload.FakeIPLibrary.Items) != 0 || payload.FakeIPLibrary.Version != 4 {
		t.Fatalf("library after reset=%+v", payload.FakeIPLibrary)
	}
	if payload.Sync != nil {
		t.Fatalf("fake ip reset should not dispatch route config sync: %+v", payload.Sync)
	}
}

func attachProbeVirtualRouterTestSession(t *testing.T, nodeID string) (<-chan map[string]any, func()) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	session := &probeSession{nodeID: nodeID, stream: serverConn, enc: json.NewEncoder(serverConn)}
	probeSessions.mu.Lock()
	oldSession := probeSessions.data[nodeID]
	probeSessions.data[nodeID] = session
	probeSessions.mu.Unlock()
	commandCh := make(chan map[string]any, 1)
	go func() {
		var msg map[string]any
		_ = json.NewDecoder(clientConn).Decode(&msg)
		commandCh <- msg
	}()
	return commandCh, func() {
		probeSessions.mu.Lock()
		if oldSession != nil {
			probeSessions.data[nodeID] = oldSession
		} else {
			delete(probeSessions.data, nodeID)
		}
		probeSessions.mu.Unlock()
		_ = clientConn.Close()
		_ = serverConn.Close()
	}
}

func assertProbeVirtualRouterRouteConfigSyncCommand(t *testing.T, commandCh <-chan map[string]any) {
	t.Helper()
	assertProbeVirtualRouterCommandType(t, commandCh, "route_config_sync")
}

func assertProbeVirtualRouterCommandType(t *testing.T, commandCh <-chan map[string]any, commandType string) {
	t.Helper()
	select {
	case msg := <-commandCh:
		if msg["type"] != commandType {
			t.Fatalf("command=%v want type=%s", msg, commandType)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting %s command", commandType)
	}
}

func setProbeVirtualRouterTestProbeStore(t *testing.T, data probeConfigData) {
	t.Helper()
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{data: data}
	t.Cleanup(func() { ProbeStore = oldStore })
}

func clearProbeVirtualRouterTestRuntimes(t *testing.T) {
	t.Helper()
	probeRuntimeStore.mu.Lock()
	oldRuntimeData := probeRuntimeStore.data
	probeRuntimeStore.data = make(map[string]probeRuntimeStatus)
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		probeRuntimeStore.mu.Lock()
		probeRuntimeStore.data = oldRuntimeData
		probeRuntimeStore.mu.Unlock()
	})
}

func probeVirtualRouterTestIPByNode(items []probeVirtualRouterProbeIP) map[string]string {
	out := make(map[string]string, len(items))
	for _, item := range items {
		out[item.NodeID] = item.IP
	}
	return out
}

func TestProbeVirtualRouterRuntimeRouteIDIsStableAcrossServiceEndpointChanges(t *testing.T) {
	base := probeVirtualRouterTopologyRule{
		ID:                "edge-a-b",
		FromNodeID:        "1",
		ToNodeID:          "2",
		FromServiceDomain: "old-a.internal",
		FromServicePort:   12040,
		ToServiceDomain:   "old-b.internal",
		ToServicePort:     12040,
		Enabled:           true,
	}
	changedEndpoint := base
	changedEndpoint.FromServiceDomain = "new-a.internal"
	changedEndpoint.FromServicePort = 13040
	changedEndpoint.ToServiceDomain = "new-b.internal"
	changedEndpoint.ToServicePort = 13041
	changedEndpoint.FromNodeID = "3"
	changedEndpoint.ToNodeID = "4"

	if left, right := probeVirtualRouterRuntimeRouteID(base), probeVirtualRouterRuntimeRouteID(changedEndpoint); left != right {
		t.Fatalf("same rule should keep route id across topology endpoint changes: %s != %s", left, right)
	}

	changedRule := base
	changedRule.ID = "edge-a-b-other"
	if left, right := probeVirtualRouterRuntimeRouteID(base), probeVirtualRouterRuntimeRouteID(changedRule); left == right {
		t.Fatalf("different rule ids should produce different route ids: %s", left)
	}
}

func TestNormalizeProbeVirtualRouterTopologyRulesInitializesRuleIDsBySequence(t *testing.T) {
	config := normalizeProbeVirtualRouterConfig(probeVirtualRouterConfig{
		Enabled: true,
		TopologyRules: []probeVirtualRouterTopologyRule{
			{FromNodeID: "1", ToNodeID: "2", Enabled: true},
			{ID: "vr-1", FromNodeID: "2", ToNodeID: "3", Enabled: true},
			{FromNodeID: "3", ToNodeID: "4", Enabled: true},
		},
	})
	if len(config.TopologyRules) != 3 {
		t.Fatalf("topology rules=%+v", config.TopologyRules)
	}
	idsByFrom := map[string]string{}
	for _, rule := range config.TopologyRules {
		idsByFrom[rule.FromNodeID] = rule.ID
	}
	if idsByFrom["1"] != "vr-2" || idsByFrom["2"] != "vr-1" || idsByFrom["3"] != "vr-3" {
		t.Fatalf("rule ids should be initialized once by sequence: %+v", config.TopologyRules)
	}
}

func TestNormalizeProbeVirtualRouterRouteRuleEntryInfersBareValues(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "domain", input: "Example.COM", want: "domain_suffix:example.com", ok: true},
		{name: "wildcard domain", input: "*.Example.COM.", want: "domain_suffix:example.com", ok: true},
		{name: "top level wildcard", input: "Google.*", want: "domain_prefix:google.", ok: true},
		{name: "subdomain and top level wildcard", input: "*.Google.*", want: "domain_keyword:.google.", ok: true},
		{name: "ipv4 cidr", input: "91.108.4.9/22", want: "cidr:91.108.4.0/22", ok: true},
		{name: "ipv4 address", input: "203.0.113.9", want: "cidr:203.0.113.9/32", ok: true},
		{name: "ipv6 cidr", input: "2001:db8::9/64", want: "cidr:2001:db8::/64", ok: true},
		{name: "ipv6 address", input: "2001:db8::9", want: "cidr:2001:db8::9/128", ok: true},
		{name: "explicit domain", input: "domain_keyword:API", want: "domain_keyword:api", ok: true},
		{name: "asn", input: "asn:AS13335", want: "asn:13335", ok: true},
		{name: "asn lowercase", input: "ASN:13335", want: "asn:13335", ok: true},
		{name: "clash domain", input: "DOMAIN-SUFFIX,Example.COM,Proxy", want: "domain_suffix:example.com", ok: true},
		{name: "invalid asn", input: "asn:AS0", ok: false},
		{name: "invalid ipv4", input: "999.1.1.1", ok: false},
		{name: "url is not domain", input: "https://example.com", ok: false},
		{name: "domain with port", input: "example.com:443", ok: false},
		{name: "invalid label", input: "bad/domain", ok: false},
		{name: "wildcard only", input: "*.*", ok: false},
		{name: "middle wildcard", input: "google.*.com", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeProbeVirtualRouterRouteRuleEntry(tt.input)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("normalize %q=(%q,%t), want (%q,%t)", tt.input, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestProbeVirtualRouterRouteRuleWildcardConversionsPreserveDomainBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		domain string
		entry  string
		want   bool
	}{
		{name: "prefix apex", domain: "google.com", entry: "domain_prefix:google.", want: true},
		{name: "prefix multi label tld", domain: "google.co.uk", entry: "domain_prefix:google.", want: true},
		{name: "prefix excludes subdomain", domain: "mail.google.com", entry: "domain_prefix:google.", want: false},
		{name: "prefix excludes adjacent label", domain: "googleapis.com", entry: "domain_prefix:google.", want: false},
		{name: "keyword subdomain", domain: "mail.google.com", entry: "domain_keyword:.google.", want: true},
		{name: "keyword deep subdomain", domain: "api.mail.google.co.uk", entry: "domain_keyword:.google.", want: true},
		{name: "keyword excludes apex", domain: "google.com", entry: "domain_keyword:.google.", want: false},
		{name: "keyword excludes adjacent label", domain: "mail.notgoogle.com", entry: "domain_keyword:.google.", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := probeVirtualRouterRouteRuleEntryMatchesFakeIPDomain(tt.domain, tt.entry); got != tt.want {
				t.Fatalf("match domain %q against %q=%t, want %t", tt.domain, tt.entry, got, tt.want)
			}
		})
	}
}
