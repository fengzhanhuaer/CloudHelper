package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeProbeRouteNodeID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "plain numeric", in: " 001 ", want: "1"},
		{name: "node dash numeric", in: "node-21", want: "21"},
		{name: "node underscore numeric", in: "Node_003", want: "3"},
		{name: "node dash text", in: "NODE-ABC", want: "abc"},
		{name: "custom id", in: " custom-id ", want: "custom-id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeProbeRouteNodeID(tc.in); got != tc.want {
				t.Fatalf("normalizeProbeRouteNodeID(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFetchProbeRouteConfigUsesRouteEndpoint(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PROBE_NODE_DATA_DIR", dataDir)

	var requestedPath string
	var requestedNodeID string
	var requestedSecret string
	var requestedAuthNodeID string
	var requestedSignature string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/probe/auth/challenge" {
			_ = json.NewEncoder(w).Encode(map[string]string{"challenge": "controller-issued-test-challenge"})
			return
		}
		requestedPath = r.URL.Path
		requestedNodeID = r.URL.Query().Get("node_id")
		requestedSecret = r.URL.Query().Get("secret")
		requestedAuthNodeID = r.Header.Get("X-Probe-Node-Id")
		requestedSignature = r.Header.Get("X-Probe-Signature")
		if r.URL.Path != probeRouteConfigAPIPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(probeRouteConfigResponse{
			VirtualRouter: probeVirtualRouterConfig{
				Enabled:    true,
				FakeIPCIDR: "198.18.0.0/15",
				TopologyRules: []probeVirtualRouterTopologyRule{{
					ID:         "vr-a",
					FromNodeID: "1",
					ToNodeID:   "2",
					Enabled:    true,
				}},
				RouteRules: []probeVirtualRouterRouteRule{{
					Name:       "media",
					Action:     "probe_exit",
					ExitNodeID: "2",
					Entries:    []string{"domain_suffix:reddit.com"},
				}},
			},
		})
	}))
	defer server.Close()

	config, err := fetchProbeRouteConfig(context.Background(), server.URL, nodeIdentity{NodeID: "7", Secret: "secret-7"})
	if err != nil {
		t.Fatalf("fetchProbeRouteConfig failed: %v", err)
	}
	if requestedPath != probeRouteConfigAPIPath {
		t.Fatalf("requested path=%q, want %q", requestedPath, probeRouteConfigAPIPath)
	}
	if requestedNodeID != "" || requestedSecret != "" {
		t.Fatalf("node credentials must not appear in query: node_id=%q secret=%q", requestedNodeID, requestedSecret)
	}
	if requestedAuthNodeID != "7" || requestedSignature == "" {
		t.Fatalf("missing challenge auth headers: node_id=%q signature=%q", requestedAuthNodeID, requestedSignature)
	}
	if len(config.TopologyRules) != 1 || len(config.RouteRules) != 1 {
		t.Fatalf("unexpected route config: %+v", config)
	}
	if config.RouteRules[0].Action != "probe_exit" || config.RouteRules[0].ExitNodeID != "2" {
		t.Fatalf("unexpected route rule action: %+v", config.RouteRules[0])
	}
}
