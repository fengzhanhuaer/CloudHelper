package core

import (
	"strings"
	"testing"
)

func TestProbePageDockerComposeIncludesLinuxTUNRequirements(t *testing.T) {
	required := []string{
		"network_mode: host",
		"cap_add:",
		"- NET_ADMIN",
		"devices:",
		"/dev/net/tun:/dev/net/tun",
		"./:/opt/cloudhelper/probe_node",
	}
	for _, item := range required {
		if !strings.Contains(mngProbePageHTML, item) {
			t.Fatalf("probe page docker compose template missing %q", item)
		}
	}
}
