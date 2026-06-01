package main

import (
	"testing"
)

func TestResolveProbeLocalChainEntryEndpointUsesClientEntryIDWithRelayChainID(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())

	if err := persistProbeProxyChainCache([]probeLinkChainServerItem{{
		ChainID:         "chain-proxy-1_cf",
		RelayChainID:    "chain-proxy-1",
		ClientEntryID:   "chain-proxy-1_cf",
		ClientEntryType: "cf",
		ChainType:       "proxy_chain",
		Name:            "Proxy 1_cf",
		Secret:          "secret-1",
		EntryNodeID:     "10",
		ExitNodeID:      "10",
		LinkLayer:       "",
		HopConfigs: []probeLinkChainHopServerItem{{
			NodeNo:       10,
			ListenPort:   16030,
			ExternalPort: 443,
			LinkLayer:    "",
			RelayHost:    "api_copilot_example.com",
		}},
	}}); err != nil {
		t.Fatalf("persist proxy chain cache failed: %v", err)
	}

	endpoint, err := resolveProbeLocalChainEntryEndpointByID("chain-proxy-1_cf")
	if err != nil {
		t.Fatalf("resolve endpoint failed: %v", err)
	}
	if endpoint.ChainID != "chain-proxy-1" {
		t.Fatalf("endpoint relay chain id=%q, want original chain id", endpoint.ChainID)
	}
	if endpoint.EntryHost != "api_copilot_example.com" || endpoint.EntryPort != 443 || endpoint.LinkLayer != "" {
		t.Fatalf("unexpected endpoint: %+v", endpoint)
	}
}
