package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestProbeLocalDNSFakeIPPersistsOnFlushAndReloads(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	decision := probeLocalDNSRouteDecision{Group: "media", Action: "tunnel", SelectedChainID: "chain-1", TunnelNodeID: "chain:chain-1"}
	fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", decision)
	if !ok || strings.TrimSpace(fakeIP) == "" {
		t.Fatalf("allocate fake ip failed: ip=%q ok=%v", fakeIP, ok)
	}
	cachePath, err := resolveProbeLocalDNSCachePath()
	if err != nil {
		t.Fatalf("resolve dns cache path failed: %v", err)
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fake ip allocation should wait for batched persist, stat err=%v", err)
	}
	flushProbeLocalDNSCacheToDisk()
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("fake ip allocation should persist cache after flush: %v", err)
	}

	resetProbeLocalDNSServiceForTest()

	entry, ok := lookupProbeLocalDNSFakeIPEntry(fakeIP)
	if !ok {
		t.Fatalf("reloaded fake ip %s not found", fakeIP)
	}
	if entry.Domain != "api.example.com" || entry.FakeIP != fakeIP {
		t.Fatalf("reloaded fake ip entry=%+v", entry)
	}
	reusedIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", decision)
	if !ok || reusedIP != fakeIP {
		t.Fatalf("reused fake ip=%q ok=%v want=%q", reusedIP, ok, fakeIP)
	}
}

func TestClearProbeLocalDNSUnifiedCacheRemovesPersistedCacheFile(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	decision := probeLocalDNSRouteDecision{Group: "media", Action: "tunnel", SelectedChainID: "chain-1", TunnelNodeID: "chain:chain-1"}
	storeProbeLocalDNSCacheRecords("api.example.com", []string{"203.0.113.20"})
	if fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", decision); !ok || strings.TrimSpace(fakeIP) == "" {
		t.Fatalf("allocate fake ip failed: ip=%q ok=%v", fakeIP, ok)
	}
	flushProbeLocalDNSCacheToDisk()
	cachePath, err := resolveProbeLocalDNSCachePath()
	if err != nil {
		t.Fatalf("resolve dns cache path failed: %v", err)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("expected persisted cache before clear: %v", err)
	}

	flushCalls := 0
	probeLocalFlushSystemDNSCache = func() error {
		flushCalls++
		return nil
	}
	clearProbeLocalDNSUnifiedCache()

	if flushCalls != 1 {
		t.Fatalf("system dns flush calls=%d, want 1", flushCalls)
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache file after clear err=%v, want not exist", err)
	}
	resetProbeLocalDNSServiceForTest()
	if got := queryProbeLocalDNSUnifiedRecords(); len(got) != 0 {
		t.Fatalf("records reloaded after clear=%+v", got)
	}
}
