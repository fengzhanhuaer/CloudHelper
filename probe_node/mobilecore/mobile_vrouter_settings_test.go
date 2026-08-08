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

func TestMobileVRouteSettingsSaveToControllerAndRefreshConfig(t *testing.T) {
	configDir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, configDir)
	var mu sync.Mutex
	exitNodeID := "17"
	settingsPosts := 0
	configGets := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMobileProbeChallengeForTest(w, r) {
			return
		}
		if r.Header.Get("X-Probe-Node-Id") != "9" || r.Header.Get("X-Probe-Signature") == "" {
			t.Fatalf("missing auth headers: %+v", r.Header)
		}
		switch r.URL.Path {
		case mobileVRouteSettingsAPIPath:
			if r.Method != http.MethodPost {
				t.Fatalf("settings method=%s", r.Method)
			}
			var request mobileVRouteSettingsRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode settings request: %v", err)
			}
			mu.Lock()
			exitNodeID = request.ExitNodes["rr-ai"]
			settingsPosts++
			current := exitNodeID
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(mobileVRouteSettingsResponse{
				OK:      true,
				Message: "路由设置已保存到主控",
				Groups:  []mobileVRouteSettingsGroup{{ID: "rr-ai", Name: "AI", ExitNodeID: current}},
				Nodes:   []mobileVRouteSettingsNode{{NodeID: "17", DisplayName: "Los Angeles"}, {NodeID: "18", DisplayName: "Tokyo"}},
			})
		case mobileVRouteConfigAPIPath:
			mu.Lock()
			current := exitNodeID
			configGets++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(mobileVRouteConfigResponse{
				NodeID: "9",
				VirtualRouter: mobileVRouteConfig{
					Enabled: true,
					ProbeIPs: []mobileVRouteProbeIP{
						{NodeID: "9", IP: "198.18.0.9"},
						{NodeID: "17", IP: "198.18.0.17"},
						{NodeID: "18", IP: "198.18.0.18"},
					},
					RouteRules: []mobileVRouteRouteRule{{
						ID: "rr-ai", Name: "AI", Action: "probe_exit", ExitNodeID: current, Entries: []string{"domain_suffix:chatgpt.com"},
					}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	response := decodeMobileVRouteSettingsResponse(t, SaveVRouteSettings(server.URL, "9", "secret-9", configDir, `{"exit_nodes":{"rr-ai":"18"}}`))
	if ok, _ := response["ok"].(bool); !ok {
		t.Fatalf("save response=%+v", response)
	}
	mu.Lock()
	posts, gets := settingsPosts, configGets
	mu.Unlock()
	if posts != 1 || gets != 1 {
		t.Fatalf("settings posts=%d config gets=%d", posts, gets)
	}
	config, err := loadMobileVRouteConfig(configDir)
	if err != nil {
		t.Fatalf("load refreshed config: %v", err)
	}
	decision, ok := decideMobileVRouteForDomain(config, "www.chatgpt.com", "443")
	if !ok || decision.ExitNodeID != "18" || mobileVRouteRouteIDForDecision(decision) != "vroute:18" {
		t.Fatalf("refreshed decision=%+v ok=%v", decision, ok)
	}
	if _, err := os.Stat(filepath.Join(configDir, "mobile_vroute_settings.json")); !os.IsNotExist(err) {
		t.Fatalf("device-local override file should not exist: err=%v", err)
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
