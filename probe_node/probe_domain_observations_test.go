package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestProbeDomainObservationsAggregateHitsAndSortByRequestTime(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeDomainObservations()
	t.Cleanup(resetProbeDomainObservations)
	recordProbeDomainObservation("first.example", "dns", "192.168.51.20:53001", "direct", []string{"203.0.113.10"}, nil)
	recordProbeDomainObservation("ads.example", "dns", "192.168.51.21:53002", "reject", nil, nil)
	recordProbeDomainObservation("ads.example", "dns", "192.168.51.20:53003", "reject", nil, nil)
	recordProbeDomainObservation("ads.example", "sni", "192.168.51.20", "reject", nil, nil)
	recordProbeDomainObservation("ads.example", "quic", "192.168.51.20", "reject", nil, nil)

	items, sources, err := snapshotProbeDomainObservations()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Domain != "ads.example" {
		t.Fatalf("items=%+v, want latest domain first", items)
	}
	item := items[0]
	if item.Status != "tracking" || item.Events != 4 || item.DNSQueries != 2 || item.SNIObservations != 1 || item.QUICObservations != 1 {
		t.Fatalf("unexpected aggregated hits: %+v", item)
	}
	if !reflect.DeepEqual(item.ObservedVia, []string{"dns", "quic", "sni"}) || !reflect.DeepEqual(item.Sources, []string{"192.168.51.20", "192.168.51.21"}) {
		t.Fatalf("unexpected source aggregation: %+v", item)
	}
	if !reflect.DeepEqual(sources, []string{"192.168.51.20", "192.168.51.21"}) {
		t.Fatalf("sources=%v", sources)
	}
}

func TestProbeDomainObservationEmptyActionPreservesLastKnownAction(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeDomainObservations()
	t.Cleanup(resetProbeDomainObservations)

	recordProbeDomainObservation("mixed.example", "dns", "192.168.51.20:53001", "direct", nil, nil)
	recordProbeDomainObservation("mixed.example", "sni", "192.168.51.20", "", nil, nil)

	items, _, err := snapshotProbeDomainObservations()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].LastAction != "direct" {
		t.Fatalf("items=%+v, want last known action direct", items)
	}
}

func TestProbeVirtualRouterDNSRecordsRequestSource(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeVirtualRouterStateForTest()
	resetProbeDomainObservations()
	t.Cleanup(resetProbeVirtualRouterStateForTest)
	t.Cleanup(resetProbeDomainObservations)
	probeVirtualRouterState.mu.Lock()
	probeVirtualRouterState.config.RouteRules = []probeVirtualRouterRouteRule{{
		Name: "Reject ads", Action: "reject", Entries: []string{"domain_suffix:ads.example"},
	}}
	probeVirtualRouterState.mu.Unlock()
	query, err := buildProbeLocalDNSQueryA("track.ads.example")
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolveProbeVirtualRouterDNSPacketFromSource(query, "192.168.51.25:53053")
	if err != nil || len(result.Response) == 0 {
		t.Fatalf("resolve result=%+v err=%v", result, err)
	}
	items, _, err := snapshotProbeDomainObservations()
	if err != nil || len(items) != 1 || items[0].Domain != "track.ads.example" || items[0].DNSQueries != 1 || items[0].LastSource != "192.168.51.25" {
		t.Fatalf("DNS observation=%+v err=%v", items, err)
	}
}

func TestProbeDomainAllowlistPersistsOnlyOnMembershipChanges(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PROBE_NODE_DATA_DIR", dataDir)
	resetProbeDomainObservations()
	t.Cleanup(resetProbeDomainObservations)
	recordProbeDomainObservation("allowed.example", "dns", "192.168.51.20", "direct", nil, nil)
	path := filepath.Join(dataDir, probeDomainAllowlistFileName)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("ordinary hit unexpectedly wrote allowlist: err=%v", err)
	}
	if _, err := setProbeDomainObservationStatus("allowed.example", "allowed"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var payload probeDomainAllowlistFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload.Domains, []string{"allowed.example"}) || string(raw) == "" {
		t.Fatalf("unexpected allowlist payload: %s", raw)
	}
	if string(raw) != "" && (containsJSONField(raw, "events") || containsJSONField(raw, "sources")) {
		t.Fatalf("allowlist persisted observation details: %s", raw)
	}
	fixedTime := time.Unix(123456789, 0)
	if err := os.Chtimes(path, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	recordProbeDomainObservation("allowed.example", "dns", "192.168.51.21", "direct", nil, nil)
	if _, err := setProbeDomainObservationStatus("allowed.example", "allowed"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(fixedTime) {
		t.Fatalf("ordinary hit or unchanged status rewrote allowlist: mod_time=%s", info.ModTime())
	}

	resetProbeDomainObservations()
	recordProbeDomainObservation("allowed.example", "sni", "192.168.51.22", "", nil, nil)
	items, _, err := snapshotProbeDomainObservations()
	if err != nil || len(items) != 1 || items[0].Status != "allowed" {
		t.Fatalf("reloaded allowlist item=%+v err=%v", items, err)
	}
	if _, err := setProbeDomainObservationStatus("allowed.example", "tracking"); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Domains) != 0 {
		t.Fatalf("domain remained in allowlist: %s", raw)
	}
}

func containsJSONField(raw []byte, field string) bool {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return false
	}
	_, ok := payload[field]
	return ok
}
