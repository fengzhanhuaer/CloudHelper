package main

import (
	"context"
	"errors"
	"net/http"
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

func TestPersistAndLoadProbeLinkConfigCache(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())

	config := probeLinkConfig{
		NodeID:        "node-1",
		Enabled:       true,
		ServiceType:   "https",
		ServiceScheme: "https",
		ServiceHost:   "example.com",
		ServicePort:   443,
		ListenAddr:    "0.0.0.0:18443",
		UpdatedAt:     "2026-05-15T00:00:00Z",
	}
	if err := persistProbeLinkConfigCache(config); err != nil {
		t.Fatalf("persistProbeLinkConfigCache returned error: %v", err)
	}

	got, ok, err := loadProbeLinkConfigCache()
	if err != nil {
		t.Fatalf("loadProbeLinkConfigCache returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected cached config to exist")
	}
	if got.ListenAddr != "0.0.0.0:18443" || got.ServiceType != "https" || got.ServiceHost != "example.com" || got.ServicePort != 443 {
		t.Fatalf("unexpected cached config: %+v", got)
	}
	if got.SavedAt == "" {
		t.Fatalf("expected SavedAt to be populated")
	}
}

func TestSyncProbeServiceFromLinkConfigFallsBackToCacheOnFetchError(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	cached := probeLinkConfig{
		NodeID:        "node-1",
		Enabled:       true,
		ServiceType:   "https",
		ServiceScheme: "https",
		ServiceHost:   "cached.example.com",
		ServicePort:   443,
		ListenAddr:    "0.0.0.0:19443",
	}
	if err := persistProbeLinkConfigCache(cached); err != nil {
		t.Fatalf("persist cache failed: %v", err)
	}

	oldFetch := probeFetchLinkConfig
	oldApply := probeApplyLinkConfig
	t.Cleanup(func() {
		probeFetchLinkConfig = oldFetch
		probeApplyLinkConfig = oldApply
	})

	probeFetchLinkConfig = func(context.Context, string, nodeIdentity) (probeLinkConfig, error) {
		return probeLinkConfig{}, errors.New("context deadline exceeded")
	}

	applied := probeLinkConfig{}
	appliedSource := ""
	probeApplyLinkConfig = func(_ http.Handler, _ nodeIdentity, _ string, config probeLinkConfig, source string) {
		applied = config
		appliedSource = source
	}

	syncProbeServiceFromLinkConfig(http.NewServeMux(), nodeIdentity{NodeID: "node-1"}, "https://controller.example.com")

	if appliedSource != "cache" {
		t.Fatalf("applied source=%q, want cache", appliedSource)
	}
	if applied.ListenAddr != "0.0.0.0:19443" {
		t.Fatalf("applied listen addr=%q, want 0.0.0.0:19443", applied.ListenAddr)
	}
}

func TestSyncProbeServiceFromLinkConfigPersistsControllerConfig(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())

	oldFetch := probeFetchLinkConfig
	oldApply := probeApplyLinkConfig
	t.Cleanup(func() {
		probeFetchLinkConfig = oldFetch
		probeApplyLinkConfig = oldApply
	})

	want := probeLinkConfig{
		NodeID:        "node-1",
		Enabled:       true,
		ServiceType:   "https",
		ServiceScheme: "https",
		ServiceHost:   "controller.example.com",
		ServicePort:   443,
		ListenAddr:    "0.0.0.0:20443",
	}
	probeFetchLinkConfig = func(context.Context, string, nodeIdentity) (probeLinkConfig, error) {
		return want, nil
	}
	appliedSource := ""
	probeApplyLinkConfig = func(_ http.Handler, _ nodeIdentity, _ string, _ probeLinkConfig, source string) {
		appliedSource = source
	}

	syncProbeServiceFromLinkConfig(http.NewServeMux(), nodeIdentity{NodeID: "node-1"}, "https://controller.example.com")

	if appliedSource != "controller" {
		t.Fatalf("applied source=%q, want controller", appliedSource)
	}
	got, ok, err := loadProbeLinkConfigCache()
	if err != nil {
		t.Fatalf("load cache failed: %v", err)
	}
	if !ok {
		t.Fatal("expected controller config to be cached")
	}
	if got.ListenAddr != want.ListenAddr || got.ServiceHost != want.ServiceHost {
		t.Fatalf("cached config=%+v, want listen=%q host=%q", got, want.ListenAddr, want.ServiceHost)
	}
}
