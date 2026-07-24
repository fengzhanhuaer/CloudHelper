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

func TestProbePageVersionColumnsShowUpgradeAvailability(t *testing.T) {
	required := []string{
		"/mng/api/system/version",
		"function renderProbeVersion(version, online)",
		"已是最新",
		"可升级",
		"有新版本",
		"refreshStatus(false, true)",
		"refreshNodes(false, true)",
	}
	for _, item := range required {
		if !strings.Contains(mngProbePageHTML, item) {
			t.Fatalf("probe page version availability UI missing %q", item)
		}
	}
}
