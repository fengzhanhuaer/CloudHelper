package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeLocalExitMappingPreservesDefaultsAndSharedAddress(t *testing.T) {
	t.Setenv("PROBE_TRUSTED_PROXY_CIDRS", "192.0.2.0/24")
	oldRoutes, oldNodes := ProbeRouteConfigStore, ProbeStore
	t.Cleanup(func() { ProbeRouteConfigStore, ProbeStore = oldRoutes, oldNodes })
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 1}, {NodeNo: 8}, {NodeNo: 9}},
		ProbeSecrets: map[string]string{"1": "secret", "8": "secret", "9": "secret"},
	}}
	ProbeRouteConfigStore = &probeRouteConfigStore{path: filepath.Join(t.TempDir(), "routes.json"), data: probeRouteConfigStoreData{
		VirtualRouter: probeVirtualRouterConfig{Enabled: true, FakeIPCIDR: probeVirtualRouterDefaultCIDR, RouteRules: []probeVirtualRouterRouteRule{
			{ID: "direct", Name: "a", Action: "direct", Entries: []string{"domain_suffix:direct.test"}},
			{ID: "reject", Name: "b", Action: "reject", Entries: []string{"domain_suffix:reject.test"}},
			{ID: "proxy", Name: "c", Action: "probe_exit", ExitNodeID: "9", Entries: []string{"domain_suffix:proxy.test"}},
		}},
	}}
	call := func(source, body string, want int) probeVirtualRouterFakeIPEntry {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/probe/route/fake_ip/resolve", bytes.NewBufferString(body))
		req.Header.Set("X-Forwarded-Proto", "https")
		applyProbeChallengeAuthForTest(t, req, source, "secret")
		w := httptest.NewRecorder()
		ProbeRouteFakeIPResolveHandler(w, req)
		if w.Code != want {
			t.Fatalf("%s: status %d, want %d: %s", body, w.Code, want, w.Body.String())
		}
		var response probeRouteFakeIPResolveResponse
		if want == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
		}
		return response.Item
	}
	for _, action := range []string{"direct", "reject"} {
		body := `{"domain":"www.` + action + `.test","rule_id":"` + action + `","action":"probe_exit","exit_node_id":"8"`
		call("1", body+`}`, http.StatusForbidden) // Legacy API stays strict.
		item := call("1", body+`,"local_exit":true}`, http.StatusOK)
		if !item.LocalExit || item.ExitNodeID != "1" || item.FakeIP == "" {
			t.Fatalf("local choice leaked or mapping missing: %+v", item)
		}
		again := call("9", body+`,"local_exit":true}`, http.StatusOK)
		if again.FakeIP != item.FakeIP || again.ExitNodeID != "1" {
			t.Fatalf("another source changed address anchor: %+v", again)
		}
		cold := call("8", `{"fake_ip":"`+item.FakeIP+`"}`, http.StatusOK)
		if cold.FakeIP != again.FakeIP || cold.Domain != again.Domain || !cold.LocalExit || cold.ExitNodeID != again.ExitNodeID {
			t.Fatalf("cold exit lookup mismatch: %+v", cold)
		}
		if reconcileProbeVirtualRouterFakeIPLibraryBestEffort() {
			t.Fatal("maintenance rewrote local mapping")
		}
	}
	for _, body := range []string{
		`{"domain":"www.direct.test","rule_id":"unknown","action":"probe_exit","exit_node_id":"8","local_exit":true}`,
		`{"domain":"www.direct.test","rule_id":"direct","action":"probe_exit","exit_node_id":"999","local_exit":true}`,
		`{"domain":"www.direct.test","rule_id":"direct","action":"direct","exit_node_id":"8","local_exit":true}`,
		`{"domain":"www.proxy.test","rule_id":"proxy","action":"probe_exit","exit_node_id":"8","local_exit":true}`,
	} {
		call("1", body, http.StatusForbidden)
	}
	proxy := call("1", `{"domain":"www.proxy.test","rule_id":"proxy","action":"probe_exit","exit_node_id":"9"}`, http.StatusOK)
	if proxy.LocalExit || proxy.ExitNodeID != "9" {
		t.Fatalf("controller proxy default changed: %+v", proxy)
	}
	config := ProbeRouteConfigStore.data.VirtualRouter
	if config.RouteRules[0].Action != "direct" || config.RouteRules[1].Action != "reject" || config.RouteRules[2].ExitNodeID != "9" {
		t.Fatalf("default rules mutated: %+v", config.RouteRules)
	}
	library := ProbeRouteConfigStore.data.VirtualRouterFakeIP
	config.RouteRules[0].Action, config.RouteRules[0].ExitNodeID = "probe_exit", "8"
	next, changed := reconcileProbeVirtualRouterFakeIPLibraryWithRouteRules(library, config.RouteRules, time.Now())
	if !changed {
		t.Fatal("expected default transition")
	}
	for _, item := range next.Items {
		if item.RuleID == "direct" && (item.LocalExit || item.ExitNodeID != "8") {
			t.Fatalf("default transition: %+v", item)
		}
	}
	deleted, changed := reconcileProbeVirtualRouterFakeIPLibraryWithRouteRules(next, config.RouteRules[2:], time.Now())
	if !changed || len(deleted.Items) != 1 || deleted.Items[0].RuleID != "proxy" {
		t.Fatalf("removed rules retained: %+v", deleted)
	}
}
