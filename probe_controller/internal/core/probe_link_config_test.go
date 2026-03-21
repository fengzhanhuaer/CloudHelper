package core

import "testing"

func TestBuildProbeServiceListenAddr(t *testing.T) {
	if got := buildProbeServiceListenAddr("", 16030); got != "" {
		t.Fatalf("expected empty listen addr for empty host, got %q", got)
	}

	got := buildProbeServiceListenAddr("0.0.0.0", 16030)
	if got != "0.0.0.0:16030" {
		t.Fatalf("unexpected listen addr: %q", got)
	}

	got = buildProbeServiceListenAddr("[::1]", 16030)
	if got != "[::1]:16030" {
		t.Fatalf("unexpected ipv6 listen addr: %q", got)
	}
}

func TestBuildProbeLinkConfigResponseEnabled(t *testing.T) {
	resp := buildProbeLinkConfigResponse(probeNodeRecord{
		NodeNo:        1,
		ServiceScheme: "https",
		ServiceHost:   " 10.0.0.8 ",
		ServicePort:   16030,
		UpdatedAt:     "2026-03-21T00:00:00Z",
	}, "1")

	if !resp.Enabled {
		t.Fatalf("expected link config enabled")
	}
	if resp.ServiceType != "https" {
		t.Fatalf("expected service_type=https, got %q", resp.ServiceType)
	}
	if resp.ListenAddr != "10.0.0.8:16030" {
		t.Fatalf("unexpected listen addr: %q", resp.ListenAddr)
	}
}

func TestBuildProbeLinkConfigResponseDisabledForTCP(t *testing.T) {
	resp := buildProbeLinkConfigResponse(probeNodeRecord{
		NodeNo:        2,
		ServiceScheme: "tcp",
		ServiceHost:   "10.0.0.9",
		ServicePort:   443,
	}, "2")

	if resp.Enabled {
		t.Fatalf("expected tcp service scheme to disable probe http service")
	}
}
