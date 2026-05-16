package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestNormalizeProbeChainNodeID(t *testing.T) {
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
			if got := normalizeProbeChainNodeID(tc.in); got != tc.want {
				t.Fatalf("normalizeProbeChainNodeID(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildChainRouteAndResolveProbeNodeChainRole(t *testing.T) {
	item := probeLinkChainServerItem{
		EntryNodeID:    "node-10",
		CascadeNodeIDs: []string{"21", "node_35", "21", ""},
		ExitNodeID:     "35",
	}

	if got, want := buildChainRoute(item), []string{"10", "21", "35"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("buildChainRoute()=%v, want %v", got, want)
	}

	if role := resolveProbeNodeChainRole(item, "10"); role != "entry" {
		t.Fatalf("role for node 10=%q, want entry", role)
	}
	if role := resolveProbeNodeChainRole(item, "node-21"); role != "relay" {
		t.Fatalf("role for node 21=%q, want relay", role)
	}
	if role := resolveProbeNodeChainRole(item, "node_35"); role != "exit" {
		t.Fatalf("role for node 35=%q, want exit", role)
	}
}

func TestResolveProbeNodeChainRoleFallbackWhenEntryMissing(t *testing.T) {
	item := probeLinkChainServerItem{
		EntryNodeID:    "",
		CascadeNodeIDs: []string{"9"},
		ExitNodeID:     "10",
	}
	if got, want := buildChainRoute(item), []string{"9", "10"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("buildChainRoute()=%v, want %v", got, want)
	}
	if role := resolveProbeNodeChainRole(item, "9"); role != "entry" {
		t.Fatalf("role for node 9=%q, want entry", role)
	}
	if role := resolveProbeNodeChainRole(item, "10"); role != "exit" {
		t.Fatalf("role for node 10=%q, want exit", role)
	}
}

func TestFindHopConfigForNodeIdentityAndLegacyFallback(t *testing.T) {
	identityItem := probeLinkChainServerItem{
		EntryNodeID:    "10",
		CascadeNodeIDs: []string{"21"},
		ExitNodeID:     "35",
		HopConfigs: []probeLinkChainHopServerItem{
			{NodeNo: 10, ListenPort: 11010},
			{NodeNo: 21, ListenPort: 12021},
			{NodeNo: 35, ListenPort: 13035},
		},
	}
	hop := findHopConfigForNode(identityItem, "node-21")
	if hop.ListenPort != 12021 || hop.NodeNo != 21 {
		t.Fatalf("identity hop match failed: %+v", hop)
	}

	legacyItem := probeLinkChainServerItem{
		EntryNodeID:    "10",
		CascadeNodeIDs: []string{"21"},
		ExitNodeID:     "35",
		HopConfigs: []probeLinkChainHopServerItem{
			{NodeNo: 1, ListenPort: 21001},
			{NodeNo: 2, ListenPort: 22002},
			{NodeNo: 3, ListenPort: 23003},
		},
	}
	hop = findHopConfigForNode(legacyItem, "21")
	if hop.ListenPort != 22002 || hop.NodeNo != 2 {
		t.Fatalf("legacy positional fallback failed: %+v", hop)
	}
}

func TestResolveProbeChainNextPrevHopFromItemWithNonContiguousNodeIDs(t *testing.T) {
	item := probeLinkChainServerItem{
		EntryNodeID:    "10",
		CascadeNodeIDs: []string{"21"},
		ExitNodeID:     "35",
		LinkLayer:      "http",
		HopConfigs: []probeLinkChainHopServerItem{
			{NodeNo: 10, ListenPort: 11010, ExternalPort: 11110, DialMode: "reverse", LinkLayer: "http2", RelayHost: "entry.example"},
			{NodeNo: 21, ListenPort: 12021, ExternalPort: 12121, DialMode: "forward", LinkLayer: "http", RelayHost: "relay.example"},
			{NodeNo: 35, ListenPort: 13035, ExternalPort: 0, DialMode: "forward", LinkLayer: "http3", RelayHost: "exit.example"},
		},
	}

	nextHost, nextPort, nextLayer, nextDialMode, nextAuthMode := resolveProbeChainNextHopFromItem(item, "10", "entry")
	if nextHost != "relay.example" || nextPort != 12121 {
		t.Fatalf("unexpected next hop: host=%q port=%d", nextHost, nextPort)
	}
	if nextLayer != "http" {
		t.Fatalf("unexpected next layer: %q", nextLayer)
	}
	if nextDialMode != probeChainDialModeReverse {
		t.Fatalf("unexpected next dial mode: %q", nextDialMode)
	}
	if nextAuthMode != "secret" {
		t.Fatalf("unexpected next auth mode: %q", nextAuthMode)
	}

	nextHost, nextPort, _, nextDialMode, nextAuthMode = resolveProbeChainNextHopFromItem(item, "35", "exit")
	if nextHost != "" || nextPort != 0 || nextDialMode != probeChainDialModeNone || nextAuthMode != "proxy" {
		t.Fatalf("unexpected exit next hop: host=%q port=%d dial=%q auth=%q", nextHost, nextPort, nextDialMode, nextAuthMode)
	}

	prevHost, prevPort, prevLayer, prevDialMode := resolveProbeChainPrevHopFromItem(item, "35", "exit")
	if prevHost != "relay.example" || prevPort != 12121 {
		t.Fatalf("unexpected prev hop for exit: host=%q port=%d", prevHost, prevPort)
	}
	if prevLayer != "http" {
		t.Fatalf("unexpected prev layer for exit: %q", prevLayer)
	}
	if prevDialMode != probeChainDialModeForward {
		t.Fatalf("unexpected prev dial mode for exit: %q", prevDialMode)
	}

	prevHost, prevPort, _, prevDialMode = resolveProbeChainPrevHopFromItem(item, "10", "entry")
	if prevHost != "" || prevPort != 0 || prevDialMode != probeChainDialModeNone {
		t.Fatalf("unexpected prev hop for entry: host=%q port=%d dial=%q", prevHost, prevPort, prevDialMode)
	}
}

func TestFetchProbeLinkChainConfigUsesProbeGroupedEndpoint(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PROBE_NODE_DATA_DIR", dataDir)

	var requestedPath string
	var requestedNodeID string
	var requestedSecret string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		requestedNodeID = r.URL.Query().Get("node_id")
		requestedSecret = r.URL.Query().Get("secret")
		if r.URL.Path != probeLinkChainGroupedConfigAPIPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(probeLinkChainConfigResponse{
			SelfChains: []probeLinkChainServerItem{
				{ChainID: "self-pf", ChainType: "port_forward", Name: "Self Port Forward"},
				{ChainID: "self-proxy", ChainType: "proxy_chain", Name: "Self Proxy"},
			},
			PortForwardChains: []probeLinkChainServerItem{
				{ChainID: "self-pf", ChainType: "port_forward", Name: "Self Port Forward"},
			},
			ProxyChains: []probeLinkChainServerItem{
				{ChainID: "self-proxy", ChainType: "proxy_chain", Name: "Self Proxy"},
			},
			GlobalProxyForwardChains: []probeLinkChainServerItem{
				{ChainID: "global-proxy", ChainType: "proxy_chain", Name: "Global Proxy"},
			},
		})
	}))
	defer server.Close()

	config, err := fetchProbeLinkChainConfig(context.Background(), server.URL, nodeIdentity{NodeID: "7", Secret: "secret-7"})
	if err != nil {
		t.Fatalf("fetchProbeLinkChainConfig failed: %v", err)
	}
	if requestedPath != probeLinkChainGroupedConfigAPIPath {
		t.Fatalf("requested path=%q, want %q", requestedPath, probeLinkChainGroupedConfigAPIPath)
	}
	if requestedNodeID != "7" || requestedSecret != "secret-7" {
		t.Fatalf("unexpected auth query node_id=%q secret=%q", requestedNodeID, requestedSecret)
	}
	if got := len(config.SelfChains); got != 2 {
		t.Fatalf("self chains len=%d, want 2", got)
	}
	if got := len(config.PortForwardChains); got != 1 {
		t.Fatalf("port forward chains len=%d, want 1", got)
	}
	if got := len(config.ProxyChains); got != 1 {
		t.Fatalf("proxy chains len=%d, want 1", got)
	}
	if got := len(config.GlobalProxyForwardChains); got != 1 || config.GlobalProxyForwardChains[0].ChainID != "global-proxy" {
		t.Fatalf("unexpected global proxy chains: %+v", config.GlobalProxyForwardChains)
	}
}

func TestFetchProbeLinkChainsReturnsSelfChainsFromGroupedEndpoint(t *testing.T) {
	origRequestConfig := probeRequestLinkChainConfig
	defer func() { probeRequestLinkChainConfig = origRequestConfig }()

	var requestedIdentity nodeIdentity
	probeRequestLinkChainConfig = func(ctx context.Context, controllerBaseURL string, identity nodeIdentity) (probeLinkChainConfigFetchResult, error) {
		requestedIdentity = identity
		return probeLinkChainConfigFetchResult{
			SelfChains: []probeLinkChainServerItem{{ChainID: "self-chain", ChainType: "port_forward", Name: "Self"}},
			GlobalProxyForwardChains: []probeLinkChainServerItem{
				{ChainID: "global-proxy", ChainType: "proxy_chain", Name: "Global"},
			},
		}, nil
	}

	items, err := fetchProbeLinkChains(context.Background(), "http://controller.example.invalid", nodeIdentity{NodeID: "9", Secret: "secret-9"})
	if err != nil {
		t.Fatalf("fetchProbeLinkChains failed: %v", err)
	}
	if requestedIdentity.NodeID != "9" || requestedIdentity.Secret != "secret-9" {
		t.Fatalf("unexpected identity: %+v", requestedIdentity)
	}
	if len(items) != 1 || items[0].ChainID != "self-chain" {
		t.Fatalf("unexpected items: %+v", items)
	}
}
