package core

import "testing"

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
