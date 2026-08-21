//go:build !linux_router

package main

import (
	"net"
	"testing"
)

func TestOrdinaryProbeIgnoresASNRouteEntries(t *testing.T) {
	config := sanitizeProbeVirtualRouterConfigForCache(probeVirtualRouterConfig{
		RouteRules: []probeVirtualRouterRouteRule{{
			Name: "Cloudflare ASN", Action: "probe_exit", ExitNodeID: "9", Entries: []string{"asn:13335"},
		}},
	})
	if len(config.RouteRules) != 1 || len(config.RouteRules[0].Entries) != 1 || config.RouteRules[0].Entries[0] != "asn:13335" {
		t.Fatalf("ordinary probe did not preserve downlinked ASN entry: %+v", config.RouteRules)
	}
	if probeVirtualRouterRouteRuleEntryMatchesIP(net.ParseIP("1.1.1.1"), "asn:13335") {
		t.Fatal("ordinary probe must not evaluate ASN route entries")
	}
}
