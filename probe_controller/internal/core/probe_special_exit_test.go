package core

import (
	"context"
	"encoding/base64"
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

func TestSpecialExitUsesAssignedRouteRulesWithoutManagedRules(t *testing.T) {
	manual := []probeVirtualRouterRouteRule{
		{ID: "rr-1", Name: "assigned", Action: "probe_exit", ExitNodeID: "19", Entries: []string{"domain_suffix:example.com", "cidr:10.0.0.0/8"}},
		{ID: "rr-2", Name: "direct", Action: "direct", Entries: []string{"domain_suffix:manual.test"}},
	}
	compiled, err := compileProbeSpecialExitRules("19", []probeSpecialExitRule{{RouteRuleID: "rr-1", Target: "node-a"}}, manual, true)
	if err != nil {
		t.Fatal(err)
	}
	wantEntries := []string{"cidr:10.0.0.0/8", "domain_suffix:example.com"}
	if len(compiled) != 1 || compiled[0].RouteRuleID != "rr-1" || compiled[0].Target != "node-a" || !reflect.DeepEqual(compiled[0].Entries, wantEntries) {
		t.Fatalf("compiled=%+v", compiled)
	}
	effective := normalizeProbeVirtualRouterRouteRules(manual)
	if !reflect.DeepEqual(effective, normalizeProbeVirtualRouterRouteRules(manual)) {
		t.Fatalf("effective=%+v manual=%+v", effective, manual)
	}
}

func TestSpecialExitReconcilesAssignedRulesAndBumpsRevision(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	initialRules := []probeVirtualRouterRouteRule{{ID: "rr-1", Name: "assigned", Action: "probe_exit", ExitNodeID: "19", Entries: []string{"domain_suffix:old.example"}}}
	compiled, err := compileProbeSpecialExitRules("19", []probeSpecialExitRule{{RouteRuleID: "rr-1", Target: "node-a"}}, initialRules, true)
	if err != nil {
		t.Fatal(err)
	}
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{NodeID: "19", Rules: compiled, Revision: 7}, nil)
	if err != nil {
		t.Fatal(err)
	}
	changedRules := []probeVirtualRouterRouteRule{{ID: "rr-1", Name: "assigned", Action: "probe_exit", ExitNodeID: "19", Entries: []string{"domain_keyword:new"}}}
	reconciled, changed := reconcileProbeSpecialExitConfigsWithRouteRules([]probeSpecialExitConfig{item}, changedRules, now)
	if !changed || len(reconciled) != 1 || reconciled[0].Revision != 8 || !reflect.DeepEqual(reconciled[0].Rules[0].Entries, []string{"domain_keyword:new"}) {
		t.Fatalf("reconciled=%+v changed=%v", reconciled, changed)
	}
	if reconciled[0].SHA256 != probeSpecialExitSnapshotHash(reconciled[0]) {
		t.Fatalf("reconciled snapshot hash does not match revision %d", reconciled[0].Revision)
	}
	reassigned, changed := reconcileProbeSpecialExitConfigsWithRouteRules(reconciled, []probeVirtualRouterRouteRule{{ID: "rr-1", Name: "moved", Action: "probe_exit", ExitNodeID: "20", Entries: []string{"domain_keyword:new"}}}, now.Add(time.Second))
	if !changed || len(reassigned[0].Rules) != 0 || reassigned[0].Revision != 9 {
		t.Fatalf("reassigned=%+v changed=%v", reassigned, changed)
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
		NodeID:        "19",
		Subscriptions: []probeSpecialExitSubscription{{ID: "primary", Name: "Primary", Enabled: true, URL: "https://subscription.example/secret"}},
		Rules:         []probeSpecialExitRule{{RouteRuleID: "rr-1", Target: "proxy-a", Entries: []string{"domain_suffix:api.example.com"}}},
		Proxies:       []map[string]interface{}{{"name": "proxy-a", "type": "socks5", "server": "proxy.example", "port": 1080, "password": "node-secret"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	virtualRouter := defaultProbeVirtualRouterConfig()
	virtualRouter.RouteRules = []probeVirtualRouterRouteRule{{ID: "rr-1", Name: "API", Action: "probe_exit", ExitNodeID: "19", Entries: []string{"domain_suffix:api.example.com"}}}
	ProbeRouteConfigStore = &probeRouteConfigStore{data: probeRouteConfigStoreData{VirtualRouter: virtualRouter, SpecialExits: []probeSpecialExitConfig{item}}}
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

func TestParseProbeSpecialExitSubscriptionSupportsPlainAndBase64AnyTLS(t *testing.T) {
	plain := "anytls://p%40ss@example.com:8443/?sni=edge.example.com&insecure=1&fp=chrome&alpn=h2,http%2F1.1#冲上云霄\n" +
		"anytls://second@[2001:db8::1]/?insecure=0#IPv6\n" +
		"anytls://reality-secret@reality.example:443/?security=reality&pbk=public-key-secret&sid=0123456789abcdef#Reality"
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))
	parsed, err := parseProbeSpecialExitSubscriptionWithResult([]byte(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SkippedProxyCount != 1 {
		t.Fatalf("skipped proxy count=%d", parsed.SkippedProxyCount)
	}
	proxies := parsed.Proxies
	if len(proxies) != 2 {
		t.Fatalf("proxies=%+v", proxies)
	}
	byName := make(map[string]map[string]interface{}, len(proxies))
	for _, proxy := range proxies {
		byName[fmt.Sprint(proxy["name"])] = proxy
	}
	primary := byName["冲上云霄"]
	if primary == nil || primary["type"] != "anytls" || primary["server"] != "example.com" || primary["port"] != float64(8443) || primary["password"] != "p@ss" || primary["sni"] != "edge.example.com" || primary["client-fingerprint"] != "chrome" || primary["skip-cert-verify"] != true || primary["udp"] != true {
		t.Fatalf("primary anytls proxy=%+v", primary)
	}
	if got := primary["alpn"]; !reflect.DeepEqual(got, []interface{}{"h2", "http/1.1"}) {
		t.Fatalf("primary alpn=%+v", got)
	}
	ipv6 := byName["IPv6"]
	if ipv6 == nil || ipv6["server"] != "2001:db8::1" || ipv6["port"] != float64(443) || ipv6["password"] != "second" || ipv6["skip-cert-verify"] != false {
		t.Fatalf("IPv6 anytls proxy=%+v", ipv6)
	}
	plainProxies, err := parseProbeSpecialExitSubscription([]byte("anytls://secret@plain.example:443/#Plain"))
	if err != nil || len(plainProxies) != 1 || plainProxies[0]["name"] != "Plain" {
		t.Fatalf("plain anytls proxies=%+v err=%v", plainProxies, err)
	}
}

func TestParseProbeSpecialExitSubscriptionRejectsUnsupportedOrInvalidURIWithoutLeakingSecrets(t *testing.T) {
	unsupported := base64.RawStdEncoding.EncodeToString([]byte("vmess://do-not-leak#node"))
	if _, err := parseProbeSpecialExitSubscription([]byte(unsupported)); err == nil || !strings.Contains(err.Error(), `scheme "vmess"`) || strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("unsupported URI error=%v", err)
	}
	secret := "anytls-secret-do-not-leak"
	invalidAnyTLS := "anytls://" + secret + "@example.com:443/?insecure=invalid#node"
	if _, err := parseProbeSpecialExitSubscription([]byte(invalidAnyTLS)); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("invalid AnyTLS error=%v", err)
	}
	realitySecret := "reality-password-do-not-leak"
	realityPublicKey := "reality-public-key-do-not-leak"
	realityOnly := "anytls://" + realitySecret + "@example.com:443/?security=reality&pbk=" + realityPublicKey + "&sid=0123456789abcdef#node"
	if _, err := parseProbeSpecialExitSubscription([]byte(realityOnly)); err == nil || !strings.Contains(err.Error(), "no Mihomo-compatible proxy nodes") || !strings.Contains(err.Error(), "skipped 1 AnyTLS+Reality") || strings.Contains(err.Error(), realitySecret) || strings.Contains(err.Error(), realityPublicKey) {
		t.Fatalf("Reality-only error=%v", err)
	}
	for _, content := range []string{"not yaml or base64", base64.StdEncoding.EncodeToString([]byte("anytls://"))} {
		if _, err := parseProbeSpecialExitSubscription([]byte(content)); err == nil || strings.Contains(err.Error(), content) {
			t.Fatalf("invalid subscription error=%v", err)
		}
	}
}

func TestNormalizeProbeSpecialExitSubscriptionsPreservesRedactedURL(t *testing.T) {
	previous, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID:        "19",
		Subscriptions: []probeSpecialExitSubscription{{ID: "primary", Name: "Primary", Enabled: true, URL: "https://primary.example/config"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID:        "19",
		Subscriptions: []probeSpecialExitSubscription{{ID: "primary", Name: "Renamed", Enabled: true}},
	}, &previous)
	if err != nil {
		t.Fatal(err)
	}
	if source := preserved.Subscriptions[0]; source.URL != "https://primary.example/config" {
		t.Fatalf("redacted URL was not preserved: %+v", source)
	}
}

func TestNormalizeProbeSpecialExitSubscriptionsDropsLegacyHeaders(t *testing.T) {
	var raw probeSpecialExitConfig
	if err := json.Unmarshal([]byte(`{"node_id":"19","subscriptions":[{"id":"primary","name":"Primary","enabled":true,"url":"https://primary.example/config","headers":{"Authorization":"Bearer secret"},"clear_headers":true}]}`), &raw); err != nil {
		t.Fatal(err)
	}
	normalized, err := normalizeProbeSpecialExitConfig(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "headers") || strings.Contains(string(encoded), "Bearer secret") {
		t.Fatalf("legacy request headers survived normalization: %s", encoded)
	}
}

func TestProbeSpecialExitSnapshotUsesVersionThreeRouteEntryModel(t *testing.T) {
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{NodeID: "19", Rules: []probeSpecialExitRule{{RouteRuleID: "rr-1", Target: "node-a", Entries: []string{"domain_suffix:example.com", "cidr:10.0.0.0/8"}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := probeSpecialExitSnapshotForConfig(item)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if snapshot.Version != 3 || !strings.Contains(text, `"route_rule_id":"rr-1"`) || !strings.Contains(text, `"entries":["cidr:10.0.0.0/8","domain_suffix:example.com"]`) {
		t.Fatalf("snapshot=%s", text)
	}
	for _, legacy := range []string{`"enabled"`, `"default_action"`, `"action"`, `"domains"`, `"ports"`, `"network"`} {
		if strings.Contains(text, legacy) {
			t.Fatalf("version 3 snapshot contains legacy field %s: %s", legacy, text)
		}
	}
}

func TestRefreshSpecialExitMergesMultipleSubscriptionsAtomically(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	oldFetch := probeSpecialExitFetchSubscription
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19",
		Subscriptions: []probeSpecialExitSubscription{
			{ID: "primary", Name: "Primary", Enabled: true, URL: "https://primary.example/config"},
			{ID: "backup", Name: "Backup", Enabled: true, URL: "https://backup.example/config"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ProbeRouteConfigStore = &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "route.json"), data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig(), SpecialExits: []probeSpecialExitConfig{item}}}
	probeSpecialExitFetchSubscription = func(_ context.Context, rawURL string) ([]byte, error) {
		switch rawURL {
		case "https://primary.example/config":
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

func TestRefreshSpecialExitSkipsAnyTLSRealityAndReportsCount(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	oldFetch := probeSpecialExitFetchSubscription
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", Proxies: []map[string]interface{}{{"name": "last-good", "type": "socks5"}},
		Subscriptions: []probeSpecialExitSubscription{{ID: "mixed", Name: "Mixed", Enabled: true, URL: "https://mixed.example/config"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ProbeRouteConfigStore = &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "route.json"), data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig(), SpecialExits: []probeSpecialExitConfig{item}}}
	probeSpecialExitFetchSubscription = func(context.Context, string) ([]byte, error) {
		plain := "anytls://compatible@compatible.example:443/?sni=compatible.example#UsableNode\n" +
			"anytls://reality-secret@reality.example:443/?security=reality&pbk=public-key-secret&sid=0123456789abcdef#Reality"
		return []byte(base64.StdEncoding.EncodeToString([]byte(plain))), nil
	}
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore; probeSpecialExitFetchSubscription = oldFetch })
	result, err := refreshMngProbeSpecialExitSubscription(context.Background(), "19", "https://controller.example")
	if err != nil {
		t.Fatal(err)
	}
	if result["proxy_count"] != 1 || result["skipped_proxy_count"] != 1 || result["subscription_count"] != 1 {
		t.Fatalf("result=%+v", result)
	}
	ProbeRouteConfigStore.mu.RLock()
	after := ProbeRouteConfigStore.data.SpecialExits[0]
	ProbeRouteConfigStore.mu.RUnlock()
	if len(after.Proxies) != 1 || after.Proxies[0]["name"] != "UsableNode" || after.Revision != item.Revision+1 || after.LastSubscriptionRefreshErr != "" {
		t.Fatalf("refreshed special exit=%+v", after)
	}
}

func TestRefreshSpecialExitRealityOnlyPreservesLastGood(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	oldFetch := probeSpecialExitFetchSubscription
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", Proxies: []map[string]interface{}{{"name": "last-good", "type": "socks5"}},
		Subscriptions: []probeSpecialExitSubscription{{ID: "reality", Name: "Reality", Enabled: true, URL: "https://reality.example/config"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ProbeRouteConfigStore = &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "route.json"), data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig(), SpecialExits: []probeSpecialExitConfig{item}}}
	probeSpecialExitFetchSubscription = func(context.Context, string) ([]byte, error) {
		plain := "anytls://reality-secret@reality.example:443/?security=reality&pbk=public-key-secret&sid=0123456789abcdef#Reality"
		return []byte(base64.StdEncoding.EncodeToString([]byte(plain))), nil
	}
	t.Cleanup(func() { ProbeRouteConfigStore = oldStore; probeSpecialExitFetchSubscription = oldFetch })
	_, err = refreshMngProbeSpecialExitSubscription(context.Background(), "19", "https://controller.example")
	if err == nil || !strings.Contains(err.Error(), "no Mihomo-compatible proxy nodes") || strings.Contains(err.Error(), "reality-secret") || strings.Contains(err.Error(), "public-key-secret") {
		t.Fatalf("Reality-only refresh error=%v", err)
	}
	ProbeRouteConfigStore.mu.RLock()
	after := ProbeRouteConfigStore.data.SpecialExits[0]
	ProbeRouteConfigStore.mu.RUnlock()
	if len(after.Proxies) != 1 || after.Proxies[0]["name"] != "last-good" || after.Revision != item.Revision {
		t.Fatalf("Reality-only refresh overwrote last-good snapshot: %+v", after)
	}
}

func TestRefreshSpecialExitRejectsDuplicateProxyAcrossSubscriptions(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	oldFetch := probeSpecialExitFetchSubscription
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", Proxies: []map[string]interface{}{{"name": "last-good", "type": "socks5"}},
		Subscriptions: []probeSpecialExitSubscription{
			{ID: "one", Name: "One", Enabled: true, URL: "https://one.example/config"},
			{ID: "two", Name: "Two", Enabled: true, URL: "https://two.example/config"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ProbeRouteConfigStore = &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "route.json"), data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig(), SpecialExits: []probeSpecialExitConfig{item}}}
	probeSpecialExitFetchSubscription = func(context.Context, string) ([]byte, error) {
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
		NodeID: "19", Proxies: []map[string]interface{}{{"name": "last-good", "type": "socks5"}},
		Subscriptions: []probeSpecialExitSubscription{
			{ID: "good", Name: "Good", Enabled: true, URL: "https://good.example/config"},
			{ID: "failed", Name: "Failed", Enabled: true, URL: "https://failed.example/config"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ProbeRouteConfigStore = &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "route.json"), data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig(), SpecialExits: []probeSpecialExitConfig{item}}}
	probeSpecialExitFetchSubscription = func(_ context.Context, rawURL string) ([]byte, error) {
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

func TestSpecialExitRejectsInvalidNodeNamesAndUnassignedRules(t *testing.T) {
	for _, name := range []string{"bad,name", "DIRECT", "line\nbreak"} {
		if _, err := normalizeProbeSpecialExitProxies([]map[string]interface{}{{"name": name, "type": "socks5"}}); err == nil {
			t.Fatalf("invalid proxy name %q accepted", name)
		}
	}
	routeRules := []probeVirtualRouterRouteRule{{ID: "rr-1", Name: "assigned", Action: "probe_exit", ExitNodeID: "19", Entries: []string{"domain_suffix:example.com"}}}
	if _, err := compileProbeSpecialExitRules("19", []probeSpecialExitRule{{RouteRuleID: "rr-missing", Target: "DIRECT"}}, routeRules, true); err == nil {
		t.Fatal("unassigned route rule accepted")
	}
	if _, err := compileProbeSpecialExitRules("19", []probeSpecialExitRule{{RouteRuleID: "rr-1", Target: "DIRECT"}, {RouteRuleID: "rr-1", Target: "DIRECT"}}, routeRules, true); err == nil {
		t.Fatal("duplicate route rule selection accepted")
	}
}

func TestApplySpecialExitSubscriptionRefreshMergesLatestGUIConfig(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID: "19", Rules: []probeSpecialExitRule{{RouteRuleID: "old", Target: "old-node", Entries: []string{"domain_suffix:old.example"}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	latest := item
	latest.Rules = []probeSpecialExitRule{{RouteRuleID: "new", Target: "node-a", Entries: []string{"domain_suffix:new.example"}}}
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
	if len(refreshed.Rules) != 1 || refreshed.Rules[0].RouteRuleID != "new" || refreshed.Revision != latest.Revision+1 {
		t.Fatalf("refresh overwrote latest GUI config: %+v", refreshed)
	}
}

func TestApplySpecialExitSubscriptionRefreshRollsBackMemoryWhenSaveFails(t *testing.T) {
	oldStore := ProbeRouteConfigStore
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{NodeID: "19"}, nil)
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
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{NodeID: "19", Subscriptions: []probeSpecialExitSubscription{{ID: "primary", Name: "Primary", Enabled: true, URL: "https://old.example/config"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Subscriptions) != 1 || item.Subscriptions[0].URL != "https://old.example/config" {
		t.Fatalf("subscription was not normalized: %+v", item.Subscriptions)
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
				data.SpecialExits = append(data.SpecialExits, probeSpecialExitConfig{NodeID: nodeID, Revision: 1})
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
	_, err := fetchProbeSpecialExitSubscription(context.Background(), secretURL)
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), "subscription.example") {
		t.Fatalf("subscription URL leaked in error: %v", err)
	}
}

func TestApplyProbeSpecialExitSubscriptionRequestHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://subscription.example/config.yaml", nil)
	req.Header.Set("User-Agent", "manual-client")
	req.Header.Set("Accept", "application/json")
	applyProbeSpecialExitSubscriptionRequestHeaders(req)
	if got := req.Header.Get("User-Agent"); got != probeSpecialExitSubscriptionUserAgent {
		t.Fatalf("User-Agent=%q", got)
	}
	if got := req.Header.Get("Accept"); got != probeSpecialExitSubscriptionAccept {
		t.Fatalf("Accept=%q", got)
	}
}

func TestMngSpecialExitListRedactsControllerAndProxySecrets(t *testing.T) {
	oldProbeStore := ProbeStore
	oldRouteStore := ProbeRouteConfigStore
	ProbeStore = &probeConfigStore{data: probeConfigData{ProbeNodes: []probeNodeRecord{{NodeNo: 19, NodeName: "exit", NodeKind: probeNodeKindMihomoExit, NodeSecret: "node-secret"}}}}
	item, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID:        "19",
		Subscriptions: []probeSpecialExitSubscription{{ID: "primary", Name: "Primary", Enabled: true, URL: "https://subscription.example/secret"}},
		Rules:         []probeSpecialExitRule{{RouteRuleID: "rr-1", Target: "proxy-a", Entries: []string{"domain_suffix:example.com"}}},
		Proxies:       []map[string]interface{}{{"name": "proxy-a", "type": "socks5", "server": "proxy.example", "password": "proxy-secret"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	virtualRouter := defaultProbeVirtualRouterConfig()
	virtualRouter.RouteRules = []probeVirtualRouterRouteRule{{ID: "rr-1", Name: "Example", Action: "probe_exit", ExitNodeID: "19", Entries: []string{"domain_suffix:example.com"}}}
	ProbeRouteConfigStore = &probeRouteConfigStore{data: probeRouteConfigStoreData{VirtualRouter: virtualRouter, SpecialExits: []probeSpecialExitConfig{item}}}
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
	if _, exists := result["managed_rules"]; exists {
		t.Fatalf("special exit list still exposes managed rules: %+v", result)
	}
	if _, exists := result["effective_rules"]; exists {
		t.Fatalf("special exit list still exposes effective aggregate rules: %+v", result)
	}
	if rules, ok := result["route_rules"].([]probeVirtualRouterRouteRule); !ok || len(rules) != 1 || rules[0].ID != "rr-1" {
		t.Fatalf("original route rule projection missing: %+v", result["route_rules"])
	}
}

func TestUpsertMngSpecialExitUsesAssignedRouteRuleModel(t *testing.T) {
	oldProbeStore := ProbeStore
	oldRouteStore := ProbeRouteConfigStore
	ProbeStore = &probeConfigStore{data: probeConfigData{ProbeNodes: []probeNodeRecord{{NodeNo: 19, NodeName: "exit", NodeKind: probeNodeKindMihomoExit}}}}
	previous, err := normalizeProbeSpecialExitConfig(probeSpecialExitConfig{
		NodeID:  "19",
		Proxies: []map[string]interface{}{{"name": "node-a", "type": "socks5", "password": "secret"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	virtualRouter := defaultProbeVirtualRouterConfig()
	virtualRouter.RouteRules = []probeVirtualRouterRouteRule{
		{ID: "rr-1", Name: "Example domains", Action: "probe_exit", ExitNodeID: "19", Entries: []string{"domain_suffix:example.com", "domain_keyword:api"}},
		{ID: "rr-direct", Name: "Unrelated", Action: "direct", Entries: []string{"domain_suffix:direct.example"}},
	}
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: filepath.Join(t.TempDir(), "route.json"),
		data: probeRouteConfigStoreData{VirtualRouter: virtualRouter, SpecialExits: []probeSpecialExitConfig{previous}},
	}
	t.Cleanup(func() { ProbeStore = oldProbeStore; ProbeRouteConfigStore = oldRouteStore })

	result, err := upsertMngProbeSpecialExit(json.RawMessage(`{"item":{"node_id":"19","subscriptions":[],"rules":[{"route_rule_id":"rr-1","target":"node-a"}]}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	view, ok := result["item"].(probeSpecialExitManagedView)
	wantEntries := []string{"domain_keyword:api", "domain_suffix:example.com"}
	if !ok || len(view.Rules) != 1 || view.Rules[0].RouteRuleID != "rr-1" || view.Rules[0].Name != "Example domains" || view.Rules[0].Target != "node-a" || !reflect.DeepEqual(view.Rules[0].Entries, wantEntries) {
		t.Fatalf("assigned route view=%+v", result["item"])
	}
	stored := ProbeRouteConfigStore.data.SpecialExits[0]
	if len(stored.Rules) != 1 || stored.Rules[0].RouteRuleID != "rr-1" || stored.Rules[0].Target != "node-a" || !reflect.DeepEqual(stored.Rules[0].Entries, wantEntries) {
		t.Fatalf("stored rule was not compiled from the assigned route rule: %+v", stored)
	}
	if _, err := upsertMngProbeSpecialExit(json.RawMessage(`{"item":{"node_id":"19","subscriptions":[],"rules":[{"route_rule_id":"rr-1","target":"DIRECT"}]}}`), ""); err != nil {
		t.Fatalf("direct secondary exit was rejected: %v", err)
	}

	for _, payload := range []string{
		`{"item":{"node_id":"19","default_action":"reject","subscriptions":[],"rules":[]}}`,
		`{"item":{"node_id":"19","subscriptions":[{"id":"primary","name":"Primary","enabled":true,"url":"https://example.com/config","headers":{"Authorization":"Bearer secret"}}],"rules":[]}}`,
		`{"item":{"node_id":"19","subscriptions":[{"id":"primary","name":"Primary","enabled":true,"url":"https://example.com/config","clear_headers":true}],"rules":[]}}`,
		`{"item":{"node_id":"19","subscriptions":[],"rules":[{"route_rule_id":"rr-1","target":"node-a","entries":["domain_suffix:other.example"]}]}}`,
		`{"item":{"node_id":"19","subscriptions":[],"rules":[{"route_rule_id":"rr-1","target":"node-a","domains":["example.com"]}]}}`,
		`{"item":{"node_id":"19","subscriptions":[],"rules":[{"route_rule_id":"rr-1","target":"node-a","name":"forged"}]}}`,
		`{"item":{"node_id":"19","subscriptions":[],"rules":[{"route_rule_id":"rr-1","target":"node-a","action":"direct"}]}}`,
		`{"item":{"node_id":"19","subscriptions":[],"rules":[{"route_rule_id":"rr-1","target":"node-a","exit_node_id":"20"}]}}`,
		`{"item":{"node_id":"19","subscriptions":[],"rules":[{"route_rule_id":"rr-direct","target":"DIRECT"}]}}`,
		`{"item":{"node_id":"19","subscriptions":[],"rules":[{"route_rule_id":"rr-1","target":"missing-node"}]}}`,
	} {
		if _, err := upsertMngProbeSpecialExit(json.RawMessage(payload), ""); err == nil {
			t.Fatalf("invalid simplified payload accepted: %s", payload)
		}
	}
}

func TestMngRoutePageIncludesSpecialExitWorkflow(t *testing.T) {
	for _, marker := range []string{
		`data-tab="special-exits"`, `id="section-special-exits"`, `id="special-exit-subscriptions"`,
		`id="btn-special-exit-subscription-add"`, `function addSpecialExitSubscription()`,
		`id="special-exit-proxies"`, `id="special-exit-status-list"`, `data-se-rule-target`,
		`<span>路由规则出口</span>`, `id="special-exit-detail"`, `id="special-exit-empty"`,
		`<option value="">请选择特殊探针</option>`, `.special-exit-layout[hidden] { display:none; }`,
		`state.specialExitStatuses.find((status) => normalizeNodeID(status.node_id) === nodeID)`,
		`/mng/api/route/special_exits/subscription/refresh`,
		`data-se-subscription-url="${index}" class="mono" type="url"`,
		`result.skipped_proxy_count`, `Mihomo 不支持的 AnyTLS+Reality 节点`,
	} {
		if !strings.Contains(mngRoutePageHTML, marker) {
			t.Fatalf("route page missing %q", marker)
		}
	}
	for _, marker := range []string{
		`id="special-exit-new-node-name"`, `id="btn-special-exit-create-node"`, `id="special-exit-install-mode"`, `/mng/api/route/special_exits/install?`,
		`id="special-exit-name"`, `id="special-exit-enabled"`, `id="special-exit-default-action"`, `data-se-rule-action`, `data-se-rule-network`, `data-se-rule-ports`,
		`id="btn-special-exit-rule-add"`, `data-se-rule-domains`, `data-se-rule-remove`, `id="special-exit-managed-rule"`, `<span>聚合路由规则</span>`,
		`data-se-subscription-url="${index}" class="mono" type="password"`, `<summary>请求设置</summary>`, `data-se-subscription-headers`, `data-se-subscription-clear-headers`, `headers_configured`, `clear_headers`,
	} {
		if strings.Contains(mngRoutePageHTML, marker) {
			t.Fatalf("route page must not include probe creation or install marker %q", marker)
		}
	}
	if !strings.Contains(mngRoutePageHTML, `.special-exit-layout { display:grid; grid-template-columns:minmax(0, 1fr);`) {
		t.Fatal("special exit workflow must use a single-column layout")
	}
	ordered := []string{`<label for="special-exit-node">特殊探针</label>`, `<span>Clash 配置</span>`, `<span>出口节点</span>`, `<span>路由规则出口</span>`, `<span>运行状态</span>`}
	position := -1
	for _, marker := range ordered {
		next := strings.Index(mngRoutePageHTML, marker)
		if next <= position {
			t.Fatalf("special exit sections are not in single-column order at %q", marker)
		}
		position = next
	}
}
