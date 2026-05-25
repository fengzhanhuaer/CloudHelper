package core

import "testing"

func TestResolveProbeLinkChainDispatchNextHopDirectForEntryExit(t *testing.T) {
	chain := probeLinkChainRecord{
		ChainID:     "home",
		Name:        "Home",
		ChainType:   "proxy_chain",
		EntryNodeID: "1",
		ExitNodeID:  "1",
		EgressHost:  "127.0.0.1",
		EgressPort:  1080,
	}
	route := buildProbeChainRouteNodes(chain)
	host, port, layer, dialMode, authMode, err := resolveProbeLinkChainDispatchNextHop(chain, route, 0, probeLinkChainNodeSettings{DialMode: "forward"})
	if err != nil {
		t.Fatalf("resolve next hop failed: %v", err)
	}
	if host != "" || port != 0 || layer != "" {
		t.Fatalf("entry_exit should not use egress as next hop: host=%q port=%d layer=%q", host, port, layer)
	}
	if dialMode != "none" || authMode != "proxy" {
		t.Fatalf("entry_exit mode mismatch: dial=%q auth=%q", dialMode, authMode)
	}
}
