package main

import (
	"testing"
	"time"
)

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

func TestDefaultProbePublicIPRefreshIntervalForLinuxRouter(t *testing.T) {
	oldProfile := activeProbeProductProfile
	t.Cleanup(func() { activeProbeProductProfile = oldProfile })

	activeProbeProductProfile.BuildKind = probeBuildKindNormal
	if got := defaultProbePublicIPRefreshInterval(); got != 10*time.Minute {
		t.Fatalf("normal refresh interval=%s want=10m", got)
	}

	activeProbeProductProfile.BuildKind = probeBuildKindLinuxRouter
	if got := defaultProbePublicIPRefreshInterval(); got != time.Minute {
		t.Fatalf("linux router refresh interval=%s want=1m", got)
	}
}

func TestPublicIPCollectorUpdateDetectsAddressChange(t *testing.T) {
	collector := newPublicIPCollector()
	if changed := collector.update([]string{"203.0.113.10"}, nil, true); !changed {
		t.Fatal("initial public address should be reported as changed")
	}
	if changed := collector.update([]string{"203.0.113.10"}, nil, true); changed {
		t.Fatal("unchanged public address must not trigger another report")
	}
	if changed := collector.update([]string{"203.0.113.11"}, nil, true); !changed {
		t.Fatal("new public address should trigger an immediate report")
	}
	if changed := collector.update(nil, nil, false); changed {
		t.Fatal("failed refresh must preserve cache without reporting a change")
	}
}
