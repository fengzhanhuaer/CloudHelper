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
		"PROBE_NODE_DOCKER_HOST_DNS: 'true'",
		"PROBE_NODE_HOST_DBUS_SOCKET: '/host/run/dbus/system_bus_socket'",
		"PROBE_NODE_HOST_RESOLV_CONF: '/host/etc/resolv.conf'",
		"/run/dbus:/host/run/dbus:ro",
		"/etc/resolv.conf:/host/etc/resolv.conf",
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

func TestProbePageCreatesLinuxRouterAndKeepsLegacyMihomoReadOnly(t *testing.T) {
	required := []string{
		`id="create-node-kind"`,
		`value="linux_router"`,
		`value="mihomo_exit" disabled`,
		`JSON.stringify({ node_name: nodeName, node_kind: nodeKind })`,
		`/mng/api/probe/node/install?node_id=`,
		`data-action="node-install-command"`,
		`Linux 旁路由探针`,
		`${kindLabel}安装信息`,
		`targetSystemSelect.disabled = false`,
	}
	for _, item := range required {
		if !strings.Contains(mngProbePageHTML, item) {
			t.Fatalf("probe page unified router workflow missing %q", item)
		}
	}
	createSelectStart := strings.Index(mngProbePageHTML, `id="create-node-kind"`)
	createSelectEnd := strings.Index(mngProbePageHTML[createSelectStart:], `</select>`)
	if createSelectStart < 0 || createSelectEnd < 0 || strings.Contains(mngProbePageHTML[createSelectStart:createSelectStart+createSelectEnd], `value="mihomo_exit"`) {
		t.Fatal("new probe form must not expose the legacy mihomo_exit kind")
	}
	for _, forbidden := range []string{"cloudhelper-probe-exit-node-shell", `id="install-mode"`, "formatSpecialExitInstallInfo"} {
		if strings.Contains(mngProbePageHTML, forbidden) {
			t.Fatalf("probe page still contains standalone Mihomo install marker %q", forbidden)
		}
	}
	createStart := strings.Index(mngProbePageHTML, "async function createNode()")
	if createStart < 0 {
		t.Fatal("probe page createNode function not found")
	}
	createEnd := strings.Index(mngProbePageHTML[createStart:], "function closeEditNodeModal()")
	if createEnd < 0 {
		t.Fatal("probe page createNode function end not found")
	}
	createBody := mngProbePageHTML[createStart : createStart+createEnd]
	if strings.Contains(createBody, "copyNodeInstallCommand") {
		t.Fatal("creating a probe must not open the install dialog")
	}
}

func TestProbePageSupportsKindReinstallAndStreamingShell(t *testing.T) {
	required := []string{
		`id="edit-node-kind"`,
		`value="linux_router"`,
		`node_kind: nodeKind`,
		`reinstall_required`,
		`修改探针类型会轮换密钥`,
		`copyNodeInstallCommand(payload.node_no)`,
		`/mng/api/probe/shell/session/input`,
		`/mng/api/probe/shell/session/read`,
		`async function pollShellOutput`,
		`isWindows ? 'PowerShell' : 'sh'`,
		`event.key === 'Enter' && !event.shiftKey`,
	}
	for _, item := range required {
		if !strings.Contains(mngProbePageHTML, item) {
			t.Fatalf("probe page type conversion or streaming shell missing %q", item)
		}
	}
}
