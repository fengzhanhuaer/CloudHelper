package main

import (
	"net"
	"net/http"
	"testing"
)

func TestProbeIPReportLANFilterOnlyAppliesToLANIPs(t *testing.T) {
	settings := probeIPReportSettings{
		OnlySelectedLANInterfaces: true,
		SelectedInterfaceIDs:      []string{"name:eth0"},
	}

	if !shouldIncludeProbeReportInterfaceIP(net.ParseIP("192.168.1.20"), "name:eth0", settings) {
		t.Fatal("selected lan interface ip should be included")
	}
	if shouldIncludeProbeReportInterfaceIP(net.ParseIP("192.168.1.21"), "name:wlan0", settings) {
		t.Fatal("unselected lan interface ip should be filtered")
	}
	if !shouldIncludeProbeReportInterfaceIP(net.ParseIP("8.8.8.8"), "name:wlan0", settings) {
		t.Fatal("public ip should not be filtered by selected lan interface setting")
	}
}

func TestProbeLocalSystemIPReportSettingsAPI(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	saveResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/ip_report_settings", map[string]any{
		"only_selected_lan_interfaces": true,
		"selected_interface_ids":       []string{"name:eth0", " name:eth0 ", "mac:00:11:22:33:44:55"},
	}, sessionCookie)
	if saveResp.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", saveResp.Code, saveResp.Body.String())
	}
	savePayload := decodeProbeLocalJSON(t, saveResp)
	settings, ok := savePayload["settings"].(map[string]any)
	if !ok {
		t.Fatalf("missing settings payload: %+v", savePayload)
	}
	if got, _ := settings["only_selected_lan_interfaces"].(bool); !got {
		t.Fatalf("only_selected_lan_interfaces=%v, want true", settings["only_selected_lan_interfaces"])
	}
	ids, ok := settings["selected_interface_ids"].([]any)
	if !ok || len(ids) != 2 {
		t.Fatalf("selected ids=%+v, want two normalized unique ids", settings["selected_interface_ids"])
	}

	getResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/system/ip_report_settings", nil, sessionCookie)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getResp.Code, getResp.Body.String())
	}
	getPayload := decodeProbeLocalJSON(t, getResp)
	getSettings, ok := getPayload["settings"].(map[string]any)
	if !ok {
		t.Fatalf("missing get settings payload: %+v", getPayload)
	}
	if got, _ := getSettings["only_selected_lan_interfaces"].(bool); !got {
		t.Fatalf("persisted only_selected_lan_interfaces=%v, want true", getSettings["only_selected_lan_interfaces"])
	}
}
