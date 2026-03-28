package core

import (
	"reflect"
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

