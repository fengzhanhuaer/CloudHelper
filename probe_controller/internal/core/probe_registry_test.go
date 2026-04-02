package core

import (
	"reflect"
	"testing"
)

func TestNormalizeProbeNodesNormalizesLinkFields(t *testing.T) {
	nodes, _ := normalizeProbeNodes([]probeNodeRecord{
		{
			NodeNo:        1,
			NodeName:      "node-a",
			NodeSecret:    "secret",
			TargetSystem:  "linux",
			ServiceScheme: "HTTPS",
			ServiceHost:   " 10.0.0.8 ",
		},
	})

	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	node := nodes[0]
	if node.ServiceScheme != "https" {
		t.Fatalf("expected service_scheme=https, got %q", node.ServiceScheme)
	}
	if node.ServiceHost != "10.0.0.8" {
		t.Fatalf("expected trimmed service_host, got %q", node.ServiceHost)
	}
}

func TestNormalizeProbeEndpointSchemeSupportsTransportTypes(t *testing.T) {
	tests := map[string]string{
		"https":     "https",
		"TCP":       "tcp",
		"http3":     "http3",
		"h3":        "http3",
		"websocket": "websocket",
		"ws":        "websocket",
		"wss":       "websocket",
		"http":      "http",
		"":          "http",
		"unknown":   "http",
	}

	for input, expected := range tests {
		got := normalizeProbeEndpointScheme(input)
		if got != expected {
			t.Fatalf("normalizeProbeEndpointScheme(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestNextProbeNodeNoSkipsDeletedRange(t *testing.T) {
	nodes := []probeNodeRecord{{NodeNo: 1}, {NodeNo: 2}}
	deleted := map[int]struct{}{3: {}, 8: {}}
	if got := nextProbeNodeNo(nodes, deleted); got != 9 {
		t.Fatalf("nextProbeNodeNo() = %d, want 9", got)
	}
}

func TestSyncProbeNodesLockedRejectsUnknownNodeNo(t *testing.T) {
	ProbeStore.mu.Lock()
	ProbeStore.data.ProbeNodes = []probeNodeRecord{{NodeNo: 1, NodeName: "n1", NodeSecret: "s1", TargetSystem: "linux"}}
	ProbeStore.data.ProbeSecrets = map[string]string{"1": "s1"}
	ProbeStore.data.DeletedProbeNodeNos = nil
	_, err := syncProbeNodesLocked([]probeNodeRecord{{NodeNo: 2, NodeName: "n2", TargetSystem: "linux"}})
	ProbeStore.mu.Unlock()

	if err == nil {
		t.Fatalf("expected error for unassigned node_no")
	}
}

func TestSyncProbeNodesLockedTracksDeletedAndKeepsExistingSecret(t *testing.T) {
	ProbeStore.mu.Lock()
	ProbeStore.data.ProbeNodes = []probeNodeRecord{
		{NodeNo: 1, NodeName: "n1", NodeSecret: "secret-1", TargetSystem: "linux", CreatedAt: "c1", UpdatedAt: "u1"},
		{NodeNo: 2, NodeName: "n2", NodeSecret: "secret-2", TargetSystem: "linux", CreatedAt: "c2", UpdatedAt: "u2"},
	}
	ProbeStore.data.ProbeSecrets = map[string]string{"1": "secret-1", "2": "secret-2"}
	ProbeStore.data.DeletedProbeNodeNos = []int{7}

	nodes, err := syncProbeNodesLocked([]probeNodeRecord{{NodeNo: 1, NodeName: "n1-new", NodeSecret: "", TargetSystem: "linux"}})
	ProbeStore.mu.Unlock()
	if err != nil {
		t.Fatalf("syncProbeNodesLocked returned error: %v", err)
	}

	if len(nodes) != 1 || nodes[0].NodeNo != 1 {
		t.Fatalf("unexpected synced nodes: %+v", nodes)
	}
	if nodes[0].NodeSecret != "secret-1" {
		t.Fatalf("expected existing secret preserved, got %q", nodes[0].NodeSecret)
	}
	if !reflect.DeepEqual(ProbeStore.data.DeletedProbeNodeNos, []int{2, 7}) {
		t.Fatalf("deleted list = %v, want [2 7]", ProbeStore.data.DeletedProbeNodeNos)
	}
}
