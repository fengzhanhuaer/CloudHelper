package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeProbeNodesDefaultsKindAndPreservesSpecialKind(t *testing.T) {
	nodes, _ := normalizeProbeNodes([]probeNodeRecord{{NodeNo: 1, NodeName: "ordinary"}, {NodeNo: 2, NodeName: "exit", NodeKind: probeNodeKindMihomoExit}})
	if len(nodes) != 2 || nodes[0].NodeKind != probeNodeKindNormal || nodes[1].NodeKind != probeNodeKindMihomoExit {
		t.Fatalf("normalized nodes=%+v", nodes)
	}
	req := probeNodeUpdateRequest{NodeNo: 2, NodeName: "exit-renamed", TargetSystem: "linux"}
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{data: probeConfigData{ProbeNodes: nodes}}
	t.Cleanup(func() { ProbeStore = oldStore })
	updated, err := updateProbeNodeLocked(req)
	if err != nil || updated.NodeKind != probeNodeKindMihomoExit {
		t.Fatalf("legacy update lost node kind: item=%+v err=%v", updated, err)
	}
}

func TestMihomoExitNodeKindIsImmutableAndSupportsLinuxOrDocker(t *testing.T) {
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{data: probeConfigData{ProbeNodes: []probeNodeRecord{{NodeNo: 2, NodeName: "exit", NodeKind: probeNodeKindMihomoExit, TargetSystem: "linux"}}}}
	t.Cleanup(func() { ProbeStore = oldStore })

	if _, err := updateProbeNodeLocked(probeNodeUpdateRequest{NodeNo: 2, NodeName: "exit", NodeKind: probeNodeKindNormal, TargetSystem: "linux"}); err == nil || !strings.Contains(err.Error(), "cannot be changed") {
		t.Fatalf("expected immutable node kind error, got %v", err)
	}
	updated, err := updateProbeNodeLocked(probeNodeUpdateRequest{NodeNo: 2, NodeName: "exit", NodeKind: probeNodeKindMihomoExit, TargetSystem: "docker"})
	if err != nil || updated.TargetSystem != "docker" {
		t.Fatalf("expected docker target to be accepted: item=%+v err=%v", updated, err)
	}
	if _, err := updateProbeNodeLocked(probeNodeUpdateRequest{NodeNo: 2, NodeName: "exit", NodeKind: probeNodeKindMihomoExit, TargetSystem: "windows"}); err == nil || !strings.Contains(err.Error(), "linux or docker") {
		t.Fatalf("expected linux-or-docker error, got %v", err)
	}
}

func TestSpecialExitManagedRuleIsStableAndNotPersistedAsManual(t *testing.T) {
	item := probeSpecialExitConfig{NodeID: "19", Name: "Exit 19", Enabled: true, DefaultAction: "direct", Rules: []probeSpecialExitRule{
		{ID: "b", Name: "b", Enabled: true, Action: "reject", Entries: []string{"DOMAIN-SUFFIX,example.com", "domain_suffix:example.com"}},
		{ID: "a", Name: "a", Enabled: true, Action: "direct", Entries: []string{"10.0.0.0/8"}},
	}}
	normalized, err := normalizeProbeSpecialExitConfig(item, nil)
	if err != nil {
		t.Fatal(err)
	}
	managed := buildProbeSpecialExitManagedRules([]probeSpecialExitConfig{normalized})
	if len(managed) != 1 || managed[0].ID != "special-exit:19" || managed[0].ExitNodeID != "19" {
		t.Fatalf("managed=%+v", managed)
	}
	want := []string{"cidr:10.0.0.0/8", "domain_suffix:example.com"}
	if !reflect.DeepEqual(managed[0].Entries, want) {
		t.Fatalf("entries=%v want=%v", managed[0].Entries, want)
	}
	manual := []probeVirtualRouterRouteRule{{ID: "rr-1", Name: "manual", Action: "direct", Entries: []string{"domain_suffix:manual.test"}}}
	effective := buildEffectiveProbeVirtualRouterRouteRules(manual, []probeSpecialExitConfig{normalized})
	if len(effective) != 2 || len(manual) != 1 || manual[0].ID != "rr-1" {
		t.Fatalf("effective=%+v manual=%+v", effective, manual)
	}
}

func TestSpecialExitConflictValidatorDetectsSemanticOverlap(t *testing.T) {
	tests := []struct{ name, left, right string }{
		{name: "nested suffix", left: "domain_suffix:example.com", right: "domain_suffix:api.example.com"},
		{name: "overlapping cidr", left: "cidr:10.0.0.0/8", right: "cidr:10.1.0.0/16"},
		{name: "broad keyword", left: "domain_keyword:example", right: "domain_suffix:example.com"},
		{name: "prefix can combine with suffix", left: "domain_prefix:api.", right: "domain_suffix:example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manual := []probeVirtualRouterRouteRule{{ID: "rr-1", Name: "manual", Action: "direct", Entries: []string{tc.left}}}
			special := []probeSpecialExitConfig{{NodeID: "19", Name: "exit", Enabled: true, DefaultAction: "direct", Rules: []probeSpecialExitRule{{ID: "r1", Name: "r1", Enabled: true, Action: "direct", Entries: []string{tc.right}}}}}
			if err := validateProbeSpecialExitConflicts(manual, special); err == nil {
				t.Fatalf("expected overlap for %q and %q", tc.left, tc.right)
			}
		})
	}
	if err := validateProbeSpecialExitConflicts([]probeVirtualRouterRouteRule{{ID: "special-exit:19", Name: "manual", Entries: []string{"example.com"}}}, nil); err == nil {
		t.Fatal("reserved manual rule ID accepted")
	}
	manual := []probeVirtualRouterRouteRule{{ID: "rr-ip", Name: "ip", Action: "direct", Entries: []string{"cidr:10.0.0.0/8"}}}
	special := []probeSpecialExitConfig{{NodeID: "19", Enabled: true, DefaultAction: "direct", Rules: []probeSpecialExitRule{{ID: "domain", Enabled: true, Action: "direct", Entries: []string{"domain_keyword:example"}}}}}
	if err := validateProbeSpecialExitConflicts(manual, special); err != nil {
		t.Fatalf("domain and CIDR were incorrectly treated as overlapping: %v", err)
	}
}

func TestProbeRouteConfigScopesSpecialExitSnapshotAndSecrets(t *testing.T) {
	oldProbeStore := ProbeStore
	oldRouteStore := ProbeRouteConfigStore
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 1, NodeName: "ordinary", NodeKind: probeNodeKindNormal, NodeSecret: "secret-1"}, {NodeNo: 19, NodeName: "exit", NodeKind: probeNodeKindMihomoExit, NodeSecret: "secret-19"}},
		ProbeSecrets: map[string]string{"1": "secret-1", "19": "secret-19"},
	}}
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", Name: "exit", Enabled: true, SubscriptionURL: "https://subscription.example/secret", SubscriptionHeaders: map[string]string{"Authorization": "Bearer secret"}, DefaultAction: "direct",
		Rules:   []probeSpecialExitRule{{ID: "r1", Name: "r1", Enabled: true, Action: "proxy", Target: "proxy-a", Entries: []string{"api.example.com"}}},
		Proxies: []map[string]interface{}{{"name": "proxy-a", "type": "socks5", "server": "proxy.example", "port": 1080, "password": "node-secret"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ProbeRouteConfigStore = &probeRouteConfigStore{data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig(), SpecialExits: []probeSpecialExitConfig{item}}}
	resetProbeAuthChallengeStateForTest()
	t.Cleanup(func() {
		ProbeStore = oldProbeStore
		ProbeRouteConfigStore = oldRouteStore
		resetProbeAuthChallengeStateForTest()
	})

	request := func(nodeID, secret string) probeRouteConfigResponse {
		req := httptest.NewRequest(http.MethodGet, "/api/probe/route/config", nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		applyProbeChallengeAuthForTest(t, req, nodeID, secret)
		rec := httptest.NewRecorder()
		ProbeRouteConfigHandler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("node=%s status=%d body=%s", nodeID, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "subscription.example") || strings.Contains(rec.Body.String(), "Bearer secret") {
			t.Fatalf("controller subscription secret leaked: %s", rec.Body.String())
		}
		var payload probeRouteConfigResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}

	ordinary := request("1", "secret-1")
	if ordinary.ExpectedNodeKind != probeNodeKindNormal || ordinary.SpecialExit != nil {
		t.Fatalf("ordinary response=%+v", ordinary)
	}
	exit := request("19", "secret-19")
	if exit.ExpectedNodeKind != probeNodeKindMihomoExit || exit.SpecialExit == nil || exit.SpecialExit.Revision != item.Revision || exit.SpecialExit.SHA256 != item.SHA256 {
		t.Fatalf("exit response=%+v", exit)
	}
	if len(exit.SpecialExit.Proxies) != 1 || exit.SpecialExit.Proxies[0]["password"] != "node-secret" {
		t.Fatalf("private proxy snapshot missing: %+v", exit.SpecialExit)
	}
}

func TestParseProbeSpecialExitSubscriptionRejectsProvidersAndNormalizesProxies(t *testing.T) {
	content := []byte("proxies:\n  - name: node-b\n    type: socks5\n    server: b.example\n    port: 1080\n  - name: node-a\n    type: ss\n    server: a.example\n    port: 443\n    password: secret\n")
	proxies, err := parseProbeSpecialExitSubscription(content)
	if err != nil || len(proxies) != 2 || proxies[0]["name"] != "node-a" || proxies[1]["name"] != "node-b" {
		t.Fatalf("proxies=%+v err=%v", proxies, err)
	}
	if _, err := parseProbeSpecialExitSubscription([]byte("proxy-providers:\n  remote:\n    type: http\n    url: https://provider.example/config.yaml\n")); err == nil || !strings.Contains(err.Error(), "proxy-providers") {
		t.Fatalf("remote provider accepted: %v", err)
	}
}

func TestNormalizeProbeSpecialExitSubscriptionsPreservesAndClearsSecrets(t *testing.T) {
	previous, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", DefaultAction: "direct",
		Subscriptions: []probeSpecialExitSubscription{{
			ID: "primary", Name: "Primary", Enabled: true, URL: "https://primary.example/config",
			Headers: map[string]string{"Authorization": "Bearer secret"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", DefaultAction: "direct",
		Subscriptions: []probeSpecialExitSubscription{{ID: "primary", Name: "Renamed", Enabled: true}},
	}, &previous)
	if err != nil {
		t.Fatal(err)
	}
	if source := preserved.Subscriptions[0]; source.URL != "https://primary.example/config" || source.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("redacted credentials were not preserved: %+v", source)
	}
	cleared, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", DefaultAction: "direct",
		Subscriptions: []probeSpecialExitSubscription{{ID: "primary", Name: "Renamed", Enabled: true, ClearHeaders: true}},
	}, &previous)
	if err != nil {
		t.Fatal(err)
	}
	if source := cleared.Subscriptions[0]; source.URL != "https://primary.example/config" || len(source.Headers) != 0 {
		t.Fatalf("headers were not explicitly cleared: %+v", source)
	}
}

func TestNormalizeProbeSpecialExitSubscriptionsMigratesUnnormalizedPreviousConfig(t *testing.T) {
	previous := probeSpecialExitConfig{
		NodeID: "19", DefaultAction: "direct", SubscriptionURL: "https://legacy.example/config",
		SubscriptionHeaders: map[string]string{"Authorization": "Bearer legacy"},
	}
	updated, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{NodeID: "19", DefaultAction: "direct"}, &previous)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Subscriptions) != 1 || updated.Subscriptions[0].URL != "https://legacy.example/config" || updated.Subscriptions[0].Headers["Authorization"] != "Bearer legacy" {
		t.Fatalf("unnormalized legacy subscription was not migrated: %+v", updated.Subscriptions)
	}
}

func TestRefreshSpecialExitMergesMultipleSubscriptionsAtomically(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	oldFetch := probeSpecialExitFetchSubscription
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", Name: "exit", Enabled: true, DefaultAction: "direct",
		Subscriptions: []probeSpecialExitSubscription{
			{ID: "primary", Name: "Primary", Enabled: true, URL: "https://primary.example/config", Headers: map[string]string{"Authorization": "Bearer primary"}},
			{ID: "backup", Name: "Backup", Enabled: true, URL: "https://backup.example/config"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ProbeRouteConfigStore = &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "route.json"), data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig(), SpecialExits: []probeSpecialExitConfig{item}}}
	probeSpecialExitFetchSubscription = func(_ context.Context, rawURL string, headers map[string]string) ([]byte, error) {
		switch rawURL {
		case "https://primary.example/config":
			if headers["Authorization"] != "Bearer primary" {
				return nil, fmt.Errorf("primary headers=%v", headers)
			}
			return []byte("proxies:\n  - name: node-a\n    type: socks5\n    server: a.example\n    port: 1080\n"), nil
		case "https://backup.example/config":
			return []byte("proxies:\n  - name: node-b\n    type: socks5\n    server: b.example\n    port: 1080\n"), nil
		default:
			return nil, fmt.Errorf("unexpected source")
		}
	}
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore; probeSpecialExitFetchSubscription = oldFetch })
	result, err := refreshMngProbeSpecialExitSubscription(context.Background(), "19", "https://controller.example")
	if err != nil {
		t.Fatal(err)
	}
	if result["proxy_count"] != 2 || result["subscription_count"] != 2 {
		t.Fatalf("result=%+v", result)
	}
	ProbeRouteConfigStore.mu.RLock()
	refreshed := ProbeRouteConfigStore.data.SpecialExits[0]
	ProbeRouteConfigStore.mu.RUnlock()
	if len(refreshed.Proxies) != 2 || refreshed.Proxies[0]["name"] != "node-a" || refreshed.Proxies[1]["name"] != "node-b" {
		t.Fatalf("merged proxies=%+v", refreshed.Proxies)
	}
	for _, source := range refreshed.Subscriptions {
		if source.LastSubscriptionRefreshAt == "" || source.LastSubscriptionRefreshErr != "" {
			t.Fatalf("source status=%+v", source)
		}
	}
}

func TestRefreshSpecialExitRejectsDuplicateProxyAcrossSubscriptions(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	oldFetch := probeSpecialExitFetchSubscription
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", Enabled: true, DefaultAction: "direct", Proxies: []map[string]interface{}{{"name": "last-good", "type": "socks5"}},
		Subscriptions: []probeSpecialExitSubscription{
			{ID: "one", Name: "One", Enabled: true, URL: "https://one.example/config"},
			{ID: "two", Name: "Two", Enabled: true, URL: "https://two.example/config"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ProbeRouteConfigStore = &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "route.json"), data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig(), SpecialExits: []probeSpecialExitConfig{item}}}
	probeSpecialExitFetchSubscription = func(context.Context, string, map[string]string) ([]byte, error) {
		return []byte("proxies:\n  - name: duplicate\n    type: socks5\n    server: same.example\n    port: 1080\n"), nil
	}
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore; probeSpecialExitFetchSubscription = oldFetch })
	if _, err := refreshMngProbeSpecialExitSubscription(context.Background(), "19", "https://controller.example"); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate proxy accepted: %v", err)
	}
	ProbeRouteConfigStore.mu.RLock()
	after := ProbeRouteConfigStore.data.SpecialExits[0]
	ProbeRouteConfigStore.mu.RUnlock()
	if len(after.Proxies) != 1 || after.Proxies[0]["name"] != "last-good" {
		t.Fatalf("last-good proxies were overwritten: %+v", after.Proxies)
	}
}

func TestRefreshSpecialExitSourceFailurePreservesLastGood(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	oldFetch := probeSpecialExitFetchSubscription
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", Enabled: true, DefaultAction: "direct", Proxies: []map[string]interface{}{{"name": "last-good", "type": "socks5"}},
		Subscriptions: []probeSpecialExitSubscription{
			{ID: "good", Name: "Good", Enabled: true, URL: "https://good.example/config"},
			{ID: "failed", Name: "Failed", Enabled: true, URL: "https://failed.example/config"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ProbeRouteConfigStore = &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "route.json"), data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig(), SpecialExits: []probeSpecialExitConfig{item}}}
	probeSpecialExitFetchSubscription = func(_ context.Context, rawURL string, _ map[string]string) ([]byte, error) {
		if rawURL == "https://failed.example/config" {
			return nil, context.DeadlineExceeded
		}
		return []byte("proxies:\n  - name: replacement\n    type: socks5\n    server: replacement.example\n    port: 1080\n"), nil
	}
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore; probeSpecialExitFetchSubscription = oldFetch })
	if _, err := refreshMngProbeSpecialExitSubscription(context.Background(), "19", "https://controller.example"); err == nil || !strings.Contains(err.Error(), "Failed") {
		t.Fatalf("source failure was not returned: %v", err)
	}
	ProbeRouteConfigStore.mu.RLock()
	after := ProbeRouteConfigStore.data.SpecialExits[0]
	ProbeRouteConfigStore.mu.RUnlock()
	if len(after.Proxies) != 1 || after.Proxies[0]["name"] != "last-good" || after.Revision != item.Revision {
		t.Fatalf("source failure overwrote last-good snapshot: %+v", after)
	}
	if after.Subscriptions[0].LastSubscriptionRefreshErr != "" || !strings.Contains(after.Subscriptions[1].LastSubscriptionRefreshErr, "deadline") {
		t.Fatalf("source failure status is incorrect: %+v", after.Subscriptions)
	}
}

func TestSpecialExitProxyAndPolicyNamesRejectMihomoRuleDelimiters(t *testing.T) {
	for _, name := range []string{"bad,name", "DIRECT", "line\nbreak"} {
		if _, err := normalizeProbeSpecialExitProxies([]map[string]interface{}{{"name": name, "type": "socks5"}}); err == nil {
			t.Fatalf("invalid proxy name %q accepted", name)
		}
	}
	if _, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{NodeID: "19", DefaultAction: "group", DefaultTarget: "bad,group"}, nil); err == nil {
		t.Fatal("comma-delimited group name accepted")
	}
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", Enabled: true, DefaultAction: "group", DefaultTarget: "node-a",
		Proxies: []map[string]interface{}{{"name": "node-a", "type": "socks5"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProbeSpecialExitResolvedPolicies(item); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("group/proxy name conflict accepted: %v", err)
	}
}

func TestApplySpecialExitSubscriptionRefreshMergesLatestGUIConfig(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", Name: "before", Enabled: true, DefaultAction: "direct",
		Rules: []probeSpecialExitRule{{ID: "old", Name: "old", Enabled: true, Action: "direct", Entries: []string{"old.example"}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	latest := item
	latest.Name = "saved while downloading"
	latest.Rules = []probeSpecialExitRule{{ID: "new", Name: "new", Enabled: true, Action: "node", Target: "node-a", Entries: []string{"new.example"}}}
	latest.Revision++
	latest.SHA256 = probeSpecialExitSnapshotHash(latest)
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(t.TempDir(), "route.json"),
		data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig(), SpecialExits: []probeSpecialExitConfig{latest}},
	}
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore })
	refreshed, err := applyProbeSpecialExitSubscriptionRefresh("19", probeSpecialExitSubscriptionSourceHash(item), []map[string]interface{}{{"name": "node-a", "type": "socks5"}}, time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Name != latest.Name || len(refreshed.Rules) != 1 || refreshed.Rules[0].ID != "new" || refreshed.Revision != latest.Revision+1 {
		t.Fatalf("refresh overwrote latest GUI config: %+v", refreshed)
	}
}

func TestApplySpecialExitSubscriptionRefreshRollsBackMemoryWhenSaveFails(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{NodeID: "19", Name: "before", Enabled: true, DefaultAction: "direct"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: t.TempDir(), // Writing JSON to a directory forces the persistence step to fail.
		data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig(), SpecialExits: []probeSpecialExitConfig{item}},
	}
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore })
	if _, err := applyProbeSpecialExitSubscriptionRefresh("19", probeSpecialExitSubscriptionSourceHash(item), []map[string]interface{}{{"name": "node-a", "type": "socks5"}}, time.Unix(200, 0).UTC()); err == nil {
		t.Fatal("expected persistence failure")
	}
	ProbeRouteConfigStore.mu.RLock()
	after := ProbeRouteConfigStore.data.SpecialExits[0]
	ProbeRouteConfigStore.mu.RUnlock()
	if after.Revision != item.Revision || len(after.Proxies) != 0 || after.SHA256 != item.SHA256 {
		t.Fatalf("memory state was not rolled back: before=%+v after=%+v", item, after)
	}
}

func TestApplySpecialExitSubscriptionRefreshRejectsChangedSource(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{NodeID: "19", Enabled: true, SubscriptionURL: "https://old.example/config", DefaultAction: "direct"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Subscriptions) != 1 || item.Subscriptions[0].URL != "https://old.example/config" {
		t.Fatalf("legacy subscription was not migrated: %+v", item.Subscriptions)
	}
	oldSource := probeSpecialExitSubscriptionSourceHash(item)
	item.Subscriptions[0].URL = "https://new.example/config"
	item.Revision++
	item.SHA256 = probeSpecialExitSnapshotHash(item)
	ProbeRouteConfigStore = &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "route.json"), data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig(), SpecialExits: []probeSpecialExitConfig{item}}}
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore })
	if _, err := applyProbeSpecialExitSubscriptionRefresh("19", oldSource, []map[string]interface{}{{"name": "stale-node", "type": "socks5"}}, time.Unix(300, 0).UTC()); err == nil || !strings.Contains(err.Error(), "subscription changed") {
		t.Fatalf("stale subscription result accepted: %v", err)
	}
	ProbeRouteConfigStore.mu.RLock()
	after := ProbeRouteConfigStore.data.SpecialExits[0]
	ProbeRouteConfigStore.mu.RUnlock()
	if len(after.Subscriptions) != 1 || after.Subscriptions[0].URL != item.Subscriptions[0].URL || after.Revision != item.Revision || len(after.Proxies) != 0 {
		t.Fatalf("stale result changed config: %+v", after)
	}
}

func TestProbeRouteConfigStoreUpdateSerializesConcurrentChanges(t *testing.T) {
	store := &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "route.json"), data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig()}}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, nodeID := range []string{"19", "20"} {
		nodeID := nodeID
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := store.update(func(data *probeRouteConfigStoreData) error {
				data.SpecialExits = append(data.SpecialExits, probeSpecialExitConfig{NodeID: nodeID, DefaultAction: "direct", Revision: 1})
				return nil
			}); err != nil {
				t.Errorf("update %s: %v", nodeID, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	store.mu.RLock()
	count := len(store.data.SpecialExits)
	store.mu.RUnlock()
	if count != 2 {
		t.Fatalf("concurrent updates lost data: count=%d", count)
	}
	var persisted probeRouteConfigStoreData
	raw, err := os.ReadFile(store.path)
	if err != nil || json.Unmarshal(raw, &persisted) != nil || len(persisted.SpecialExits) != 2 {
		t.Fatalf("persisted concurrent updates invalid: items=%d err=%v raw=%s", len(persisted.SpecialExits), err, raw)
	}
}

func TestValidateProbeSpecialExitSubscriptionURLRejectsPrivateAndReservedAddresses(t *testing.T) {
	oldLookup := probeSpecialExitLookupIP
	t.Cleanup(func() { probeSpecialExitLookupIP = oldLookup })
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "198.18.0.10", "192.0.2.1", "2001:db8::1"} {
		probeSpecialExitLookupIP = func(context.Context, string) ([]net.IPAddr, error) { return []net.IPAddr{{IP: net.ParseIP(raw)}}, nil }
		if _, _, err := validateProbeSpecialExitSubscriptionURL(context.Background(), "https://subscription.example/config.yaml"); err == nil {
			t.Fatalf("address %s accepted", raw)
		}
	}
	probeSpecialExitLookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	if target, _, err := validateProbeSpecialExitSubscriptionURL(context.Background(), "https://subscription.example/config.yaml"); err != nil || target.Hostname() != "subscription.example" {
		t.Fatalf("public subscription rejected: target=%v err=%v", target, err)
	}
	if _, _, err := validateProbeSpecialExitSubscriptionURL(context.Background(), "http://subscription.example/config.yaml"); err == nil {
		t.Fatal("HTTP subscription accepted")
	}
}

func TestFetchProbeSpecialExitSubscriptionDoesNotLeakURLOnTransportError(t *testing.T) {
	oldLookup := probeSpecialExitLookupIP
	oldDial := probeSpecialExitDialContext
	probeSpecialExitLookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	probeSpecialExitDialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, context.DeadlineExceeded
	}
	t.Cleanup(func() {
		probeSpecialExitLookupIP = oldLookup
		probeSpecialExitDialContext = oldDial
	})
	secretURL := "https://subscription.example/config.yaml?token=do-not-leak"
	_, err := fetchProbeSpecialExitSubscription(context.Background(), secretURL, nil)
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), "subscription.example") {
		t.Fatalf("subscription URL leaked in error: %v", err)
	}
}

func TestMngSpecialExitListRedactsControllerAndProxySecrets(t *testing.T) {
	oldProbeStore := ProbeStore
	oldRouteStore := ProbeRouteConfigStore
	ProbeStore = &probeConfigStore{data: probeConfigData{ProbeNodes: []probeNodeRecord{{NodeNo: 19, NodeName: "exit", NodeKind: probeNodeKindMihomoExit, NodeSecret: "node-secret"}}}}
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", Name: "exit", Enabled: true, SubscriptionURL: "https://subscription.example/secret", SubscriptionHeaders: map[string]string{"Authorization": "Bearer secret"}, DefaultAction: "direct",
		Rules:   []probeSpecialExitRule{{ID: "r1", Name: "r1", Enabled: true, Action: "direct", Entries: []string{"example.com"}}},
		Proxies: []map[string]interface{}{{"name": "proxy-a", "type": "socks5", "server": "proxy.example", "password": "proxy-secret"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ProbeRouteConfigStore = &probeRouteConfigStore{data: probeRouteConfigStoreData{SpecialExits: []probeSpecialExitConfig{item}}}
	t.Cleanup(func() { ProbeStore = oldProbeStore; ProbeRouteConfigStore = oldRouteStore })
	result, err := listMngProbeSpecialExits()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{"subscription.example", "Bearer secret", "proxy-secret", "node-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q leaked: %s", secret, text)
		}
	}
	if !strings.Contains(text, `"subscription_configured":true`) || !strings.Contains(text, `"proxy_names":["proxy-a"]`) {
		t.Fatalf("redacted metadata missing: %s", text)
	}
}

func TestMngRoutePageIncludesSpecialExitWorkflow(t *testing.T) {
	for _, marker := range []string{
		`data-tab="special-exits"`, `id="section-special-exits"`, `id="special-exit-subscriptions"`,
		`id="btn-special-exit-subscription-add"`, `function addSpecialExitSubscription()`, `可选请求头 JSON`,
		`id="special-exit-clear-subscription"`, `id="special-exit-status-list"`,
		`id="special-exit-managed-rule"`,
		`/mng/api/route/special_exits/subscription/refresh`,
	} {
		if !strings.Contains(mngRoutePageHTML, marker) {
			t.Fatalf("route page missing %q", marker)
		}
	}
	for _, marker := range []string{`id="special-exit-new-node-name"`, `id="btn-special-exit-create-node"`, `id="special-exit-install-mode"`, `/mng/api/route/special_exits/install?`} {
		if strings.Contains(mngRoutePageHTML, marker) {
			t.Fatalf("route page must not include probe creation or install marker %q", marker)
		}
	}
	if !strings.Contains(mngRoutePageHTML, `.special-exit-layout { display:grid; grid-template-columns:minmax(0, 1fr);`) {
		t.Fatal("special exit workflow must use a single-column layout")
	}
	ordered := []string{`<span>运行状态</span>`, `<span>聚合路由规则</span>`, `<span>基础配置</span>`, `<span>Clash 订阅源</span>`, `<span>二次分流规则</span>`}
	position := -1
	for _, marker := range ordered {
		next := strings.Index(mngRoutePageHTML, marker)
		if next <= position {
			t.Fatalf("special exit sections are not in single-column order at %q", marker)
		}
		position = next
	}
}
