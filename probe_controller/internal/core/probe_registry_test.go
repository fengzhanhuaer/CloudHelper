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
			ServicePort:   70000,
			PublicScheme:  "",
			PublicHost:    " nat.example.com ",
			PublicPort:    -1,
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
	if node.ServicePort != 16030 {
		t.Fatalf("expected default service_port=16030, got %d", node.ServicePort)
	}
	if node.PublicScheme != "http" {
		t.Fatalf("expected default public_scheme=http, got %q", node.PublicScheme)
	}
	if node.PublicHost != "nat.example.com" {
		t.Fatalf("expected trimmed public_host, got %q", node.PublicHost)
	}
	if node.PublicPort != 0 {
		t.Fatalf("expected public_port=0, got %d", node.PublicPort)
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
