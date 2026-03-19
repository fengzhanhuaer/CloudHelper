package core

import "testing"

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

