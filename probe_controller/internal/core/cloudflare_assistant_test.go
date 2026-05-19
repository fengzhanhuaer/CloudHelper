package core

import (
	"reflect"
	"strings"
	"testing"
)

func TestIsCloudflareManagedDDNSRecordName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "public prefix", input: "ddns_go_abc.example.com", expected: true},
		{name: "local prefix", input: "local_go_node-1.example.com", expected: true},
		{name: "business prefix", input: "api.codex.abcd.example.com", expected: true},
		{name: "business prefix uppercase with dot", input: "API.CODEX.XYZ.example.com.", expected: true},
		{name: "copilot candidate is not ddns managed", input: "api_copilot_abcd.example.com", expected: false},
		{name: "not managed", input: "www.example.com", expected: false},
		{name: "empty", input: "", expected: false},
	}
	for _, tt := range tests {
		got := isCloudflareManagedDDNSRecordName(tt.input)
		if got != tt.expected {
			t.Fatalf("%s: expected %v, got %v", tt.name, tt.expected, got)
		}
	}
}

func TestCloudflareCopilotCandidateDomainIsEditOnly(t *testing.T) {
	oldProbeStore := ProbeStore
	oldCloudflareStore := CloudflareStore
	t.Cleanup(func() {
		ProbeStore = oldProbeStore
		CloudflareStore = oldCloudflareStore
	})

	ProbeStore = &probeConfigStore{
		data: probeConfigData{
			ProbeNodes: []probeNodeRecord{
				{
					NodeNo:      7,
					NodeName:    "node-7",
					DDNS:        "node7.example.net",
					ServiceHost: "service7.example.net",
					CloudflareDDNSRecords: []cloudflareDDNSRecord{
						{RecordName: "api.codex.nw.example.com", RecordClass: "business", RecordType: "A", Sequence: 1},
					},
				},
			},
			ProbeSecrets: map[string]string{},
		},
	}
	CloudflareStore = &cloudflareStore{
		data: cloudflareStoreData{
			ZoneName: "example.com",
		},
	}

	copilotDomain := buildCloudflareCopilotCandidateDomain(7, "example.com")
	if copilotDomain != "api_copilot_nw.example.com" {
		t.Fatalf("unexpected copilot candidate: %q", copilotDomain)
	}

	runtimeDomains := listProbeLinkNodeDomains("7")
	if containsStringFold(runtimeDomains, copilotDomain) {
		t.Fatalf("copilot candidate must not be used by runtime relay host fallback: %v", runtimeDomains)
	}

	editDomains := listProbeLinkNodeEditCandidateDomains("7")
	if !containsStringFold(editDomains, copilotDomain) {
		t.Fatalf("copilot candidate missing from edit domains: %v", editDomains)
	}
	if len(editDomains) == 0 || !strings.EqualFold(editDomains[len(editDomains)-1], copilotDomain) {
		t.Fatalf("copilot candidate should be appended after existing domains: %v", editDomains)
	}

	desired := buildCloudflareDesiredRecords(probeNodeStatusRecord{
		NodeNo:   7,
		NodeName: "node-7",
		Runtime:  probeRuntimeStatus{IPv4: []string{"8.8.8.8"}},
	}, "example.com")
	for _, item := range desired {
		if strings.HasPrefix(item.RecordName, cloudflareCopilotCandidateNamePrefix) {
			t.Fatalf("copilot candidate must not be part of ddns desired records: %+v", item)
		}
	}
}

func containsStringFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func TestCloudflareRecordNameTypeKey(t *testing.T) {
	key := cloudflareRecordNameTypeKey("DDNS_GO_ABC.example.com.", "aaaa")
	if key != "ddns_go_abc.example.com|AAAA" {
		t.Fatalf("unexpected key: %q", key)
	}
	if cloudflareRecordNameTypeKey("", "A") != "" {
		t.Fatalf("expected empty key for empty record name")
	}
	if cloudflareRecordNameTypeKey("a.example.com", "") != "" {
		t.Fatalf("expected empty key for empty record type")
	}
}

func TestNormalizeCloudflareZeroTrustIPOrCIDR(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		ok       bool
	}{
		{name: "ipv4 plain", input: "1.1.1.1", expected: "1.1.1.1/32", ok: true},
		{name: "ipv4 cidr normalize to /32", input: "1.1.1.10/24", expected: "1.1.1.10/32", ok: true},
		{name: "ipv6 plain", input: "2001:db8::1", expected: "2001:db8::/56", ok: true},
		{name: "ipv6 cidr normalize to /56", input: "2001:db8::abcd/64", expected: "2001:db8::/56", ok: true},
		{name: "invalid", input: "not-an-ip", expected: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := normalizeCloudflareZeroTrustIPOrCIDR(tt.input)
		if ok != tt.ok || got != tt.expected {
			t.Fatalf("%s: expected (%q, %v), got (%q, %v)", tt.name, tt.expected, tt.ok, got, ok)
		}
	}
}

func TestParseCloudflareZeroTrustWhitelistIPs_NoDNS(t *testing.T) {
	raw := "\n1.1.1.1\n1.1.1.1/24\n# full comment\n2001:db8::1\ninvalid_token\n"
	ips, sourceLines, warnings, err := parseCloudflareZeroTrustWhitelistIPs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedIPs := []string{"1.1.1.1/32", "2001:db8::/56"}
	if !reflect.DeepEqual(ips, expectedIPs) {
		t.Fatalf("unexpected ips: got=%v expected=%v", ips, expectedIPs)
	}
	if sourceLines != 5 {
		t.Fatalf("unexpected sourceLines: got=%d expected=5", sourceLines)
	}
	if len(warnings) != 1 {
		t.Fatalf("unexpected warnings: got=%v", warnings)
	}
}
