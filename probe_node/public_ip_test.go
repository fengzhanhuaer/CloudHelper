package main

import "testing"

func TestNormalizePublicIPValueIPv4(t *testing.T) {
	got := normalizePublicIPValue(" 203.0.113.7\n", "tcp4")
	if got != "203.0.113.7" {
		t.Fatalf("normalizePublicIPValue returned %q, want %q", got, "203.0.113.7")
	}
}

func TestNormalizePublicIPValueFamilyMismatch(t *testing.T) {
	got := normalizePublicIPValue("2001:db8::1", "tcp4")
	if got != "" {
		t.Fatalf("normalizePublicIPValue returned %q, want empty for family mismatch", got)
	}
}

func TestParsePublicIPEndpoints(t *testing.T) {
	got := parsePublicIPEndpoints(" https://a.example , https://a.example , https://b.example ", []string{"https://fallback.example"})
	if len(got) != 2 {
		t.Fatalf("parsePublicIPEndpoints length=%d, want 2", len(got))
	}
	if got[0] != "https://a.example" || got[1] != "https://b.example" {
		t.Fatalf("parsePublicIPEndpoints returned %v", got)
	}
}

func TestIsPublicIPSniffEnabledDefault(t *testing.T) {
	t.Setenv("PROBE_PUBLIC_IP_SNIFF", "")
	if !isPublicIPSniffEnabled() {
		t.Fatalf("isPublicIPSniffEnabled returned false, want true by default")
	}
}

func TestIsPublicIPSniffEnabledDisabled(t *testing.T) {
	t.Setenv("PROBE_PUBLIC_IP_SNIFF", "0")
	if isPublicIPSniffEnabled() {
		t.Fatalf("isPublicIPSniffEnabled returned true, want false")
	}
}
