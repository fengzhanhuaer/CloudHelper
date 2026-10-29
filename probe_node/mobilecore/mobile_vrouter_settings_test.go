package mobilecore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
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
	for _, payload := range []string{`{"exit_nodes":{"rr-ai":"999"}}`, `{"exit_nodes":{"direct":"18"}}`, `{"exit_nodes":{},"rules":[]}`, `{"reset":true,"exit_nodes":{"rr-ai":"18"}}`} {
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
