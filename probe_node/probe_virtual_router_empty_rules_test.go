package main

import (
	"encoding/json"
	"testing"
)

func TestProbeVirtualRouterEmptyEntriesKeepOtherRules(t *testing.T) {
	for _, entries := range []string{"", `,"entries":null`, `,"entries":[]`} {
		var config probeVirtualRouterConfig
		if err := json.Unmarshal([]byte(`{"enabled":true,"route_rules":[{"id":"empty","name":"Empty","action":"reject"`+entries+`},{"id":"valid","name":"Valid","action":"reject","entries":["domain_suffix:blocked.test"]}]}`), &config); err != nil {
			t.Fatal(err)
		}
		rules := sanitizeProbeVirtualRouterRouteRules(config.RouteRules)
		if len(rules) != 2 {
			t.Fatalf("empty rule invalidated config: %+v", rules)
		}
		if _, ok := probeVirtualRouterRouteRuleForFakeIPDomain(rules, "allowed.test"); ok {
			t.Fatal("empty rule matched")
		}
		if rule, ok := probeVirtualRouterRouteRuleForFakeIPDomain(rules, "blocked.test"); !ok || rule.ID != "valid" {
			t.Fatalf("later rule lost: %+v", rule)
		}
	}
}
