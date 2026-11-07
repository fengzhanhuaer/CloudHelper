package mobilecore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestMobileVRouteSettingsReadFromController(t *testing.T) {
	var settingsRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMobileProbeChallengeForTest(w, r) {
			return
		}
		if r.URL.Path != mobileVRouteSettingsAPIPath || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		settingsRequests++
		if r.Header.Get("X-Probe-Node-Id") != "9" || r.Header.Get("X-Probe-Signature") == "" {
			t.Fatalf("missing auth headers: %+v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(mobileVRouteSettingsResponse{
			OK:     true,
			Groups: []mobileVRouteSettingsGroup{{ID: "rr-ai", Name: "AI", ExitNodeID: "17"}},
			Nodes: []mobileVRouteSettingsNode{
				{NodeID: "17", DisplayName: "Los Angeles"},
				{NodeID: "18", DisplayName: "Tokyo"},
			},
		})
	}))
	defer server.Close()

	response := decodeMobileVRouteSettingsResponse(t, VRouteSettings(server.URL, "9", "secret-9"))
	if ok, _ := response["ok"].(bool); !ok || settingsRequests != 1 {
		t.Fatalf("settings response=%+v requests=%d", response, settingsRequests)
	}
	groups, _ := response["groups"].([]any)
	group, _ := groups[0].(map[string]any)
	if group["name"] != "AI" || group["exit_node_id"] != "17" {
		t.Fatalf("settings group=%+v", group)
	}
}

func TestMobileVRouteSettingsStayLocalAcrossRefreshAndReset(t *testing.T) {
	dir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, dir)
	config := mobileVRouteConfig{Enabled: true, LocalNodeID: "9", ProbeIPs: []mobileVRouteProbeIP{{NodeID: "9", IP: "198.18.0.9"}, {NodeID: "17", IP: "198.18.0.17"}, {NodeID: "18", IP: "198.18.0.18"}}, RouteRules: []mobileVRouteRouteRule{{ID: "rr-ai", Name: "AI", Action: "probe_exit", ExitNodeID: "17", Entries: []string{"domain_suffix:chatgpt.com"}}, {ID: "cidr", Action: "probe_exit", ExitNodeID: "17", Entries: []string{"cidr:203.0.113.0/24"}}, {ID: "direct", Action: "direct", Entries: []string{"domain_suffix:local.test"}}}}
	if err := persistMobileVRouteConfig(dir, config); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	posts := 0
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMobileProbeChallengeForTest(w, r) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			posts++
			http.Error(w, "must not write controller", 500)
			return
		}
		gets++
		_ = json.NewEncoder(w).Encode(mobileVRouteConfigResponse{NodeID: "9", VirtualRouter: config})
	}))
	defer server.Close()
	save := func(payload string) map[string]any {
		return decodeMobileVRouteSettingsResponse(t, SaveVRouteSettings(server.URL, "9", "secret", dir, payload))
	}
	if response := save(`{"exit_nodes":{"rr-ai":"18"}}`); response["ok"] != true {
		t.Fatal(response)
	}
	loaded, err := loadMobileVRouteConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	decision, ok := decideMobileVRouteForDomain(loaded, "www.chatgpt.com", "443")
	if !ok || decision.ExitNodeID != "18" {
		t.Fatal(decision)
	}
	raw, err := os.ReadFile(filepath.Join(dir, mobileVRouteConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	var original mobileVRouteConfigCacheFile
	if err = json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	if original.Item.RouteRules[0].ExitNodeID != "17" {
		t.Fatal("master defaults mutated")
	}
	if _, err = os.Stat(filepath.Join(dir, "mobile_vroute_settings.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = refreshMobileVRouteConfig(server.URL, "9", "secret", dir); err != nil {
		t.Fatal(err)
	}
	loaded, _ = loadMobileVRouteConfig(dir)
	decision, _ = decideMobileVRouteForDomain(loaded, "www.chatgpt.com", "443")
	if decision.ExitNodeID != "18" {
		t.Fatal("refresh lost choice")
	}
	mu.Lock()
	countPosts, countGets := posts, gets
	mu.Unlock()
	if countPosts != 0 || countGets != 1 {
		t.Fatalf("posts=%d gets=%d", countPosts, countGets)
	}
	for _, payload := range []string{`{"exit_nodes":{"rr-ai":"999"}}`, `{"exit_nodes":{"missing":"18"}}`, `{"exit_nodes":{},"rules":[]}`, `{"reset":true,"exit_nodes":{"rr-ai":"18"}}`} {
		if save(payload)["ok"] == true {
			t.Fatalf("accepted %s", payload)
		}
	}
	if save(`{"reset":true}`)["ok"] != true {
		t.Fatal("reset failed")
	}
	loaded, _ = loadMobileVRouteConfig(dir)
	decision, _ = decideMobileVRouteForDomain(loaded, "www.chatgpt.com", "443")
	if decision.ExitNodeID != "17" {
		t.Fatal("reset failed to restore master default")
	}
}

func decodeMobileVRouteSettingsResponse(t *testing.T, raw string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode settings response %q: %v", raw, err)
	}
	return payload
}

func TestMobileLocalExitDirectAndRejectDNSAndReset(t *testing.T) {
	for _, action := range []string{"direct", "reject"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			resetMobileVRouteVPNStateForTest(t, dir)
			config := mobileVRouteConfig{Enabled: true, LocalNodeID: "9", FakeIPCIDR: "198.18.0.0/15", ProbeIPs: []mobileVRouteProbeIP{{NodeID: "9", IP: "198.18.0.9"}, {NodeID: "18", IP: "198.18.0.18"}}, RouteRules: []mobileVRouteRouteRule{{ID: "pt", Name: "PT", Action: action, Entries: []string{"domain_suffix:hxpt.org", "cidr:203.0.113.0/24"}}}}
			if err := persistMobileVRouteConfig(dir, config); err != nil {
				t.Fatal(err)
			}
			save := func(body string) {
				t.Helper()
				result := decodeMobileVRouteSettingsResponse(t, SaveVRouteSettings("https://controller.invalid", "9", "secret", dir, body))
				if result["ok"] != true {
					t.Fatal(result)
				}
			}
			save(`{"exit_nodes":{"pt":"18"}}`)
			view := decodeMobileVRouteSettingsResponse(t, VRouteLocalSettings("", "", "", dir))
			group := view["groups"].([]any)[0].(map[string]any)
			if group["default_action"] != action || group["local_exit_node_id"] != "18" {
				t.Fatal(group)
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveMobileProbeChallengeForTest(w, r) {
					return
				}
				if r.URL.Path != mobileVRouteFakeIPResolveAPIPath || r.Header.Get("X-Probe-Node-Id") != "9" || r.Header.Get("X-Probe-Signature") == "" {
					t.Errorf("unexpected unauthenticated request %s", r.URL.Path)
					http.Error(w, "bad auth", 403)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["local_exit"] != true || body["exit_node_id"] != "9" || body["rule_id"] != "pt" || body["action"] != "probe_exit" {
					t.Errorf("request=%+v", body)
				}
				requests++
				// Mapping already exists with another source as anchor.
				_ = json.NewEncoder(w).Encode(map[string]any{"item": map[string]any{"domain": "www.hxpt.org", "fake_ip": "198.18.4.95", "action": "probe_exit", "exit_node_id": "17", "local_exit": true}})
			}))
			defer server.Close()
			setMobileVRouteControllerIdentity(server.URL, "9", "secret")
			query, err := buildAndroidVPNDNSQuery("www.hxpt.org", dnsmessage.TypeA)
			if err != nil {
				t.Fatal(err)
			}
			response, err := resolveAndroidVPNDNSPacket(query)
			if err != nil || !reflect.DeepEqual(extractAndroidVPNDNSResponseIPs(response), []string{"198.18.4.95"}) {
				t.Fatalf("DNS err=%v ips=%v", err, extractAndroidVPNDNSResponseIPs(response))
			}
			route, err := decideVPNRouteForTarget("198.18.4.95:443")
			if err != nil || route.Direct || route.Reject || route.SelectedRouteID != "vroute:18" || route.TargetAddr != "www.hxpt.org:443" {
				t.Fatalf("route=%+v err=%v", route, err)
			}
			if requests != 1 {
				t.Fatalf("requests=%d", requests)
			}
			if err := persistMobileVRouteConfig(dir, config); err != nil {
				t.Fatal(err)
			}
			loaded, _ := loadMobileVRouteConfig(dir)
			decision, ok := decideMobileVRouteForDomain(loaded, "www.hxpt.org", "443")
			if !ok || decision.Direct || decision.Reject || decision.ExitNodeID != "18" {
				t.Fatalf("sync/reload=%+v", decision)
			}
			if d, _ := decideMobileVRouteForIP(loaded, []byte{203, 0, 113, 8}, "443"); d.ExitNodeID != "18" || d.Direct || d.Reject {
				t.Fatalf("CIDR=%+v", d)
			}
			save(`{"reset":true}`)
			loaded, _ = loadMobileVRouteConfig(dir)
			decision, _ = decideMobileVRouteForDomain(loaded, "www.hxpt.org", "443")
			if (action == "direct" && !decision.Direct) || (action == "reject" && !decision.Reject) {
				t.Fatalf("reset=%+v", decision)
			}
			route, err = decideVPNRouteForTarget("198.18.4.95:443")
			if err != nil || (action == "direct" && !route.Direct) || (action == "reject" && !route.Reject) {
				t.Fatalf("cached reset route=%+v err=%v", route, err)
			}
		})
	}
}
