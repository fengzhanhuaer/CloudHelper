package main

import (
	"path/filepath"
	"testing"
)

func TestNormalizeProbeListenAddr(t *testing.T) {
	if got := normalizeProbeListenAddr(""); got != "" {
		t.Fatalf("expected empty listen addr, got %q", got)
	}

	got := normalizeProbeListenAddr("0.0.0.0:16030")
	if got != "0.0.0.0:16030" {
		t.Fatalf("unexpected listen addr: %q", got)
	}

	got = normalizeProbeListenAddr("[::1]:443")
	if got != "[::1]:443" {
		t.Fatalf("unexpected ipv6 listen addr: %q", got)
	}

	if got := normalizeProbeListenAddr("0.0.0.0"); got != "" {
		t.Fatalf("expected invalid listen addr to return empty, got %q", got)
	}
}

func TestShouldEnableProbeHTTPServiceForScheme(t *testing.T) {
	if !shouldEnableProbeHTTPServiceForScheme("https") {
		t.Fatalf("expected https to enable http service")
	}
	if !shouldEnableProbeHTTPServiceForScheme("http3") {
		t.Fatalf("expected http3 to enable http service")
	}
	if !shouldEnableProbeHTTPServiceForScheme("websocket") {
		t.Fatalf("expected websocket to enable http service")
	}
	if !shouldEnableProbeHTTPServiceForScheme("wss") {
		t.Fatalf("expected wss to enable http service")
	}
	if shouldEnableProbeHTTPServiceForScheme("tcp") {
		t.Fatalf("expected tcp to disable http service")
	}
	if shouldEnableProbeHTTPServiceForScheme("http") {
		t.Fatalf("expected http to disable http service by link policy")
	}
}

func TestProbeLinkConfigFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe_link_config.json")
	input := probeLinkConfig{
		NodeID:      "1",
		ServiceType: "ws",
		ServiceHost: " 10.0.0.8 ",
		ServicePort: 16030,
		ListenAddr:  "",
	}

	if err := writeProbeLinkConfigFile(path, input); err != nil {
		t.Fatalf("writeProbeLinkConfigFile failed: %v", err)
	}

	output, err := readProbeLinkConfigFile(path)
	if err != nil {
		t.Fatalf("readProbeLinkConfigFile failed: %v", err)
	}
	if output.ServiceType != "websocket" {
		t.Fatalf("unexpected service_type: %q", output.ServiceType)
	}
	if output.ServiceScheme != "websocket" {
		t.Fatalf("unexpected service_scheme: %q", output.ServiceScheme)
	}
	if output.ListenAddr != "10.0.0.8:16030" {
		t.Fatalf("unexpected listen_addr: %q", output.ListenAddr)
	}
	if output.SavedAt == "" {
		t.Fatalf("expected saved_at to be set")
	}
}
