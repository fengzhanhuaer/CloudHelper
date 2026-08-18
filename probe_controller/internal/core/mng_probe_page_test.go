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

func TestProbePageCreatesMihomoExitAndUsesDedicatedInstallButton(t *testing.T) {
	required := []string{
		`id="create-node-kind"`,
		`value="mihomo_exit"`,
		`JSON.stringify({ node_name: nodeName, node_kind: nodeKind })`,
		`/mng/api/probe/node/install?node_id=`,
		`id="install-mode"`,
		`data-action="node-install-command"`,
		`Mihomo 出口探针`,
		`${kindLabel}安装信息`,
		`nodeKind === 'mihomo_exit' && option.value !== 'linux' && option.value !== 'docker'`,
		`targetSystemSelect.disabled = false`,
		`String(node.target_system || '').toLowerCase() === 'docker' ? 'docker' : 'native'`,
		`copyNodeInstallCommand(state.installingNodeNo, event.target.value)`,
	}
	for _, item := range required {
		if !strings.Contains(mngProbePageHTML, item) {
			t.Fatalf("probe page mihomo exit workflow missing %q", item)
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
