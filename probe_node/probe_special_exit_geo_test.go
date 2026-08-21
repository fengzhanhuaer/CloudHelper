//go:build linux_router

package main

import (
	"strings"
	"testing"
)

func TestCompileProbeMihomoConfigMaintainsSharedASNDatabase(t *testing.T) {
	probeVirtualRouterState.mu.Lock()
	previous := probeVirtualRouterState.config
	probeVirtualRouterState.config.RouteRules = []probeVirtualRouterRouteRule{{
		Name: "Cloudflare ASN", Action: "probe_exit", ExitNodeID: "9", Entries: []string{"asn:13335"},
	}}
	probeVirtualRouterState.mu.Unlock()
	t.Cleanup(func() {
		probeVirtualRouterState.mu.Lock()
		probeVirtualRouterState.config = previous
		probeVirtualRouterState.mu.Unlock()
	})

	raw, err := compileProbeMihomoConfig(probeSpecialExitSnapshot{}, probeMihomoRuntimeSecrets{
		SOCKSUsername: "user", SOCKSPassword: "password", APISecret: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"geo-auto-update: true",
		"geo-update-interval: 24",
		probeMihomoASNDatabaseURL,
		"IP-ASN,13335,DIRECT,no-resolve",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("compiled Mihomo config missing %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "IP-ASN,13335,DIRECT,no-resolve") > strings.Index(text, "MATCH,DIRECT") {
		t.Fatalf("ASN initialization rule must precede the unchanged DIRECT fallback:\n%s", text)
	}
}
