package main

import (
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestProbeIPReportInterfaceAddressesRequireSelection(t *testing.T) {
	for _, legacyFilter := range []bool{false, true} {
		settings := probeIPReportSettings{
			OnlySelectedLANInterfaces: legacyFilter,
			SelectedInterfaceIDs:      []string{"name:eth0"},
		}
		for _, raw := range []string{"192.168.52.172", "100.64.0.1", "100.127.255.254", "198.18.0.2", "169.254.1.2", "fe80::1", "fd00::1"} {
			if !shouldIncludeProbeReportInterfaceIP(net.ParseIP(raw), "name:eth0", settings) {
				t.Errorf("selected interface address %s rejected (legacy flag %v)", raw, legacyFilter)
			}
			if shouldIncludeProbeReportInterfaceIP(net.ParseIP(raw), "name:wlan0", settings) {
				t.Errorf("unselected interface address %s leaked (legacy flag %v)", raw, legacyFilter)
			}
		}
		for _, raw := range []string{"172.11.100.200", "8.8.8.8", "100.63.255.254", "100.128.0.1", "2606:4700:4700::1111", "127.0.0.1", "0.0.0.0", "::", "::1", "224.0.0.1", "ff02::1"} {
			if shouldIncludeProbeReportInterfaceIP(net.ParseIP(raw), "name:eth0", settings) {
				t.Errorf("public or unusable address %s came from interface enumeration", raw)
			}
		}
	}
}

func TestCollectIPsUsesAPIPublicAddressesWithoutSelectedInterfaces(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	t.Setenv("PROBE_PUBLIC_IP_SNIFF", "1")
	oldProfile, oldCollector := activeProbeProductProfile, probePublicIPCollector
	activeProbeProductProfile.BuildKind = probeBuildKindNormal
	probePublicIPCollector = newPublicIPCollector()
	probePublicIPCollector.lastAttempt = time.Now()
	probePublicIPCollector.update([]string{"123.232.8.66"}, []string{"2606:4700:4700::1111"}, true)
	t.Cleanup(func() {
		activeProbeProductProfile, probePublicIPCollector = oldProfile, oldCollector
	})
	if _, err := persistProbeIPReportSettings(probeIPReportSettings{}); err != nil {
		t.Fatal(err)
	}
	ipv4, ipv6 := collectIPs()
	if !reflect.DeepEqual(ipv4, []string{"123.232.8.66"}) || !reflect.DeepEqual(ipv6, []string{"2606:4700:4700::1111"}) {
		t.Fatalf("unselected interface IPs leaked or API IPs lost: ipv4=%v ipv6=%v", ipv4, ipv6)
	}
	t.Setenv("PROBE_PUBLIC_IP_SNIFF", "0")
	ipv4, ipv6 = collectIPs()
	if len(ipv4) != 0 || len(ipv6) != 0 {
		t.Fatalf("disabled API discovery must not fall back to interface public IPs: %v %v", ipv4, ipv6)
	}
}

func TestEffectiveProbeIPReportSettingsRequiresSelectionForNormalProbe(t *testing.T) {
	oldProfile := activeProbeProductProfile
	activeProbeProductProfile.BuildKind = probeBuildKindNormal
	t.Cleanup(func() { activeProbeProductProfile = oldProfile })

	settings := effectiveProbeIPReportSettings(probeIPReportSettings{})
	if !settings.OnlySelectedLANInterfaces {
		t.Fatal("normal probe must filter LAN addresses by selected interfaces")
	}
	if shouldIncludeProbeReportInterfaceIP(net.ParseIP("192.168.1.20"), "name:eth0", settings) {
		t.Fatal("normal probe must not report LAN addresses before an interface is selected")
	}
	if shouldIncludeProbeReportInterfaceIP(net.ParseIP("8.8.8.8"), "name:eth0", settings) {
		t.Fatal("public addresses must come from API discovery, not an unselected interface")
	}
}

func TestEffectiveProbeIPReportSettingsUsesLinuxRouterInterface(t *testing.T) {
	oldProfile := activeProbeProductProfile
	oldReport := probeProductLinuxRouterReport
	activeProbeProductProfile.BuildKind = probeBuildKindLinuxRouter
	probeProductLinuxRouterReport = func() probeLinuxRouterRuntimeReport {
		return probeLinuxRouterRuntimeReport{Interface: "eth0"}
	}
	t.Cleanup(func() {
		activeProbeProductProfile = oldProfile
		probeProductLinuxRouterReport = oldReport
	})

	settings := effectiveProbeIPReportSettings(probeIPReportSettings{})
	if !settings.OnlySelectedLANInterfaces {
		t.Fatal("linux router must always filter LAN addresses by its selected interface")
	}
	if want := []string{"name:eth0"}; !reflect.DeepEqual(settings.SelectedInterfaceIDs, want) {
		t.Fatalf("selected interfaces=%v want=%v", settings.SelectedInterfaceIDs, want)
	}
	if !shouldIncludeProbeReportInterfaceIP(net.ParseIP("192.168.1.20"), "name:eth0", settings) {
		t.Fatal("selected router interface LAN address should be included")
	}
	if shouldIncludeProbeReportInterfaceIP(net.ParseIP("198.18.0.17"), "name:cloudhelper0", settings) {
		t.Fatal("virtual router address must not be reported as a router LAN address")
	}
	if shouldIncludeProbeReportInterfaceIP(net.ParseIP("8.8.8.8"), "name:eth0", settings) {
		t.Fatal("selected router uplink must still use API discovery for public addresses")
	}
}

func TestProbeIPReportLANFilterAcceptsLegacyInterfaceAlias(t *testing.T) {
	settings := probeIPReportSettings{
		OnlySelectedLANInterfaces: true,
		SelectedInterfaceIDs:      []string{"mac:00:11:22:33:44:55"},
	}
	interfaceIDs := []string{
		"guid:90078d57-7beb-4176-8bfb-82a52fe5d5b1",
		"mac:00:11:22:33:44:55",
		"name:ethernet",
	}

	if !shouldIncludeProbeReportInterfaceIPByIDs(net.ParseIP("192.168.1.20"), interfaceIDs, settings) {
		t.Fatal("legacy mac selection should match the stable interface identity aliases")
	}
}

func TestProbeIPReportFallbackInterfaceIdentityPrefersMAC(t *testing.T) {
	hardwareAddr, err := net.ParseMAC("00:11:22:33:44:55")
	if err != nil {
		t.Fatalf("parse mac: %v", err)
	}
	ids := probeIPReportFallbackInterfaceIDs(net.Interface{Index: 15, Name: "Ethernet", HardwareAddr: hardwareAddr})
	want := []string{"mac:00:11:22:33:44:55", "name:ethernet", "index:15"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("fallback ids=%v want=%v", ids, want)
	}
}

func TestSortProbeIPReportStringsIPv4First(t *testing.T) {
	values := []string{"2001:db8::2", "192.168.1.20", "10.0.0.5", "fe80::1"}

	sortProbeIPReportStringsIPv4First(values)

	want := []string{"10.0.0.5", "192.168.1.20", "2001:db8::2", "fe80::1"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("sorted ips=%v, want %v", values, want)
	}
}

func TestProbeLocalSystemIPReportSettingsAPI(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	immediateReports := 0
	setProbeImmediateReporter(func() error {
		immediateReports++
		return nil
	})
	t.Cleanup(func() { setProbeImmediateReporter(nil) })

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
	if immediateReports != 1 {
		t.Fatalf("immediate reports=%d, want 1", immediateReports)
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
