package core

import (
	"strings"
	"testing"
)

func setupProbeLinuxRouterTestStores(t *testing.T) {
	t.Helper()
	oldProbeStore := ProbeStore
	oldRouteStore := ProbeRouteConfigStore
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes: []probeNodeRecord{
			{NodeNo: 1, NodeName: "client", NodeKind: probeNodeKindNormal, TargetSystem: "linux", NodeSecret: "client-secret"},
			{NodeNo: 21, NodeName: "router", NodeKind: probeNodeKindLinuxRouter, TargetSystem: "linux", NodeSecret: "router-secret"},
		},
		ProbeSecrets: map[string]string{"1": "client-secret", "21": "router-secret"},
	}}
	ProbeRouteConfigStore = &probeRouteConfigStore{data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig()}}
	t.Cleanup(func() {
		ProbeStore = oldProbeStore
		ProbeRouteConfigStore = oldRouteStore
	})
}

func TestNormalizeProbeNodeKindPreservesLinuxRouter(t *testing.T) {
	if got := normalizeProbeNodeKind(" LINUX_ROUTER "); got != probeNodeKindLinuxRouter {
		t.Fatalf("node kind = %q", got)
	}
}

func TestAppendProbeLinuxRouterPublishedRouteRulesScopesByACL(t *testing.T) {
	setupProbeLinuxRouterTestStores(t)
	router := probeRuntimeStatus{NodeID: "21", Online: true, LinuxRouter: probeLinuxRouterRuntimeReport{
		AppliedRevision: 2, LocalIPProxyEnabled: true, Healthy: true,
		PublishedCIDRs: []string{"192.168.50.0/24"}, AllowedNodeIDs: []string{"1"}, UpdatedAt: "2026-08-18T00:00:00Z",
	}}
	rules := appendProbeLinuxRouterPublishedRouteRules(nil, []probeRuntimeStatus{router}, "1")
	if len(rules) != 1 || rules[0].ExitNodeID != "21" || len(rules[0].Entries) != 1 || rules[0].Entries[0] != "cidr:192.168.50.0/24" {
		t.Fatalf("unexpected aggregated rules: %+v", rules)
	}
	if denied := appendProbeLinuxRouterPublishedRouteRules(nil, []probeRuntimeStatus{router}, "2"); len(denied) != 0 {
		t.Fatalf("unauthorized node received rules: %+v", denied)
	}
	if self := appendProbeLinuxRouterPublishedRouteRules(nil, []probeRuntimeStatus{router}, "21"); len(self) != 0 {
		t.Fatalf("router received its own published rule: %+v", self)
	}
	ProbeStore.mu.Lock()
	ProbeStore.data.ProbeNodes[1].NodeKind = probeNodeKindNormal
	ProbeStore.mu.Unlock()
	if changedKind := appendProbeLinuxRouterPublishedRouteRules(nil, []probeRuntimeStatus{router}, "1"); len(changedKind) != 0 {
		t.Fatalf("stale router config remained active after kind change: %+v", changedKind)
	}
	ProbeStore.mu.Lock()
	ProbeStore.data.ProbeNodes[1].NodeKind = probeNodeKindLinuxRouter
	ProbeStore.mu.Unlock()
	router.Online = false
	if offline := appendProbeLinuxRouterPublishedRouteRules(nil, []probeRuntimeStatus{router}, "1"); len(offline) != 0 {
		t.Fatalf("offline router still published routes: %+v", offline)
	}
}

func TestUpdateProbeRuntimeProductStatusAcceptsOnlyValidatedLocalRouterRoutes(t *testing.T) {
	setupProbeLinuxRouterTestStores(t)
	probeRuntimeStore.mu.Lock()
	previous := probeRuntimeStore.data
	probeRuntimeStore.data = make(map[string]probeRuntimeStatus)
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		probeRuntimeStore.mu.Lock()
		probeRuntimeStore.data = previous
		probeRuntimeStore.mu.Unlock()
	})

	report := probeLinuxRouterRuntimeReport{
		AppliedRevision: 4, AppliedSHA256: "ABC", LocalIPProxyEnabled: true, Healthy: true,
		PublishedCIDRs: []string{"192.168.50.20/24", "192.168.50.0/24"},
		AllowedNodeIDs: []string{"1", "21", "999"},
	}
	if changed := updateProbeRuntimeProductStatus("21", probeNodeKindLinuxRouter, probeSpecialExitRuntimeReport{}, report); !changed {
		t.Fatal("initial local router report did not trigger route refresh")
	}
	runtime, ok := getProbeRuntime("21")
	if !ok || len(runtime.LinuxRouter.PublishedCIDRs) != 1 || runtime.LinuxRouter.PublishedCIDRs[0] != "192.168.50.0/24" {
		t.Fatalf("published CIDRs were not normalized: %+v", runtime.LinuxRouter)
	}
	if len(runtime.LinuxRouter.AllowedNodeIDs) != 1 || runtime.LinuxRouter.AllowedNodeIDs[0] != "1" {
		t.Fatalf("allowed nodes were not scoped to known peers: %+v", runtime.LinuxRouter.AllowedNodeIDs)
	}

	report.AppliedRevision++
	report.AppliedSHA256 = "DEF"
	if changed := updateProbeRuntimeProductStatus("21", probeNodeKindLinuxRouter, probeSpecialExitRuntimeReport{}, report); changed {
		t.Fatal("router applied version-only change triggered route config sync")
	}

	report.Healthy = false
	report.FailOpen = true
	if changed := updateProbeRuntimeProductStatus("21", probeNodeKindLinuxRouter, probeSpecialExitRuntimeReport{}, report); changed {
		t.Fatal("router health-only change triggered route config sync")
	}

	report.PublishedCIDRs = []string{"198.18.0.0/15"}
	if changed := updateProbeRuntimeProductStatus("21", probeNodeKindLinuxRouter, probeSpecialExitRuntimeReport{}, report); !changed {
		t.Fatal("invalid local route withdrawal did not trigger route refresh")
	}
	runtime, _ = getProbeRuntime("21")
	if runtime.LinuxRouter.LocalIPProxyEnabled || runtime.LinuxRouter.Healthy || len(runtime.LinuxRouter.PublishedCIDRs) != 0 {
		t.Fatalf("invalid local route remained active: %+v", runtime.LinuxRouter)
	}
}

func TestLinuxRouterRuntimeReconnectPreservesLocalConfigReport(t *testing.T) {
	setupProbeLinuxRouterTestStores(t)
	report := probeLinuxRouterRuntimeReport{
		AppliedRevision: 4, AppliedSHA256: "abc", LocalIPProxyEnabled: true, Healthy: true,
		PublishedCIDRs: []string{"192.168.50.0/24"}, AllowedNodeIDs: []string{"1"},
	}
	probeRuntimeStore.mu.Lock()
	previous := probeRuntimeStore.data
	probeRuntimeStore.data = map[string]probeRuntimeStatus{
		"21": {NodeID: "21", Online: true, BuildKind: probeNodeKindLinuxRouter, LinuxRouter: report},
	}
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		probeRuntimeStore.mu.Lock()
		probeRuntimeStore.data = previous
		probeRuntimeStore.mu.Unlock()
	})

	setProbeRuntimeOnline("21", false)
	runtime, _ := getProbeRuntime("21")
	if runtime.Online {
		t.Fatal("router remained online after disconnect")
	}
	if !runtime.LinuxRouter.LocalIPProxyEnabled || len(runtime.LinuxRouter.PublishedCIDRs) != 1 {
		t.Fatalf("router disconnect unexpectedly cleared local config: %+v", runtime.LinuxRouter)
	}

	setProbeRuntimeOnline("21", true)
	runtime, _ = getProbeRuntime("21")
	if !runtime.LinuxRouter.LocalIPProxyEnabled || len(runtime.LinuxRouter.PublishedCIDRs) != 1 {
		t.Fatalf("router reconnect unexpectedly cleared local config: %+v", runtime.LinuxRouter)
	}
	if changed := updateProbeRuntimeProductStatus("21", probeNodeKindLinuxRouter, probeSpecialExitRuntimeReport{}, report); changed {
		t.Fatal("unchanged local router report triggered route refresh after reconnect")
	}
}

func TestBuildMngProbeLinuxRouterInstallInfoSupportsTwoArchitectures(t *testing.T) {
	setupProbeLinuxRouterTestStores(t)
	info, err := buildMngProbeLinuxRouterInstallInfo("21", "https://controller.example")
	if err != nil {
		t.Fatal(err)
	}
	architectures, ok := info["architectures"].([]string)
	if !ok || len(architectures) != 2 || architectures[0] != "amd64" || architectures[1] != "arm64" {
		t.Fatalf("architectures = %#v", info["architectures"])
	}
	command := info["command"].(string)
	if !strings.Contains(command, "/api/probe/proxy/probe-router/install-script") || !strings.Contains(command, "command -v curl") || !strings.Contains(command, "wget -qO-") || !strings.HasSuffix(command, " sh") {
		t.Fatalf("unexpected command: %s", command)
	}
	if strings.Contains(command, "sudo") {
		t.Fatalf("clean Alpine install command must not require sudo: %s", command)
	}
}

func TestMngPagesExposeLinuxRouterWorkflow(t *testing.T) {
	for _, marker := range []string{`value="linux_router"`, `Linux 旁路由探针`, `/mng/api/probe/node/install?node_id=`} {
		if !strings.Contains(mngProbePageHTML, marker) {
			t.Fatalf("probe page missing %q", marker)
		}
	}
	for _, marker := range []string{`data-tab="linux-router"`, `id="linux-router-node"`, `/mng/api/route/linux_router`, `旁路由运行状态`, `配置来源：本地 Web`, `最优邻接延迟`, `runtime.latency_ms`, `runtime.allowed_node_ids`} {
		if !strings.Contains(mngRoutePageHTML, marker) {
			t.Fatalf("route page missing %q", marker)
		}
	}
	for _, forbidden := range []string{`id="btn-linux-router-save"`, `id="linux-router-gateway-enabled"`, `id="linux-router-local-enabled"`} {
		if strings.Contains(mngRoutePageHTML, forbidden) {
			t.Fatalf("route page still exposes controller router config %q", forbidden)
		}
	}
}

func TestLinuxRouterInstallerIsAlpineOpenRCAndDualArchitecture(t *testing.T) {
	if strings.Contains(probeRouterInstallScriptLinux, "\r") {
		t.Fatal("router installer must use LF line endings for Alpine /bin/sh")
	}
	for _, marker := range []string{"command -v apk", "apk add --no-cache", "kmod", "rc-service rc-update ip nft sysctl", "command -v \"${required_command}\"", "modprobe tun", "/dev/net/tun", "ip tuntap add", "x86_64", "aarch64|arm64", "cloudhelper-probe-router-linux-${GOARCH}", "PROBE_ROUTER_PROGRAM_URL", "/api/probe/proxy/download", "--data-urlencode \"node_id=${PROBE_NODE_ID}\"", "--data-urlencode \"secret=${PROBE_NODE_SECRET}\"", "X-CloudHelper-Download-URL: ${program_url}", "/opt/cloudhelper/probe_router", "--upgrade-verify-build-kind=linux_router", "rc-update add", "PROBE_LOCAL_LISTEN", "PROBE_LOCAL_CONSOLE_ENABLED", "0.0.0.0:16032", "curl -fsS --noproxy", "/local/router"} {
		if !strings.Contains(probeRouterInstallScriptLinux, marker) {
			t.Fatalf("router installer missing %q", marker)
		}
	}
	for _, forbidden := range []string{"systemctl", "mihomo", "docker", "PROBE_ROUTER_WEB_LISTEN", "0.0.0.0:18080", `echo "PROBE_LOCAL_LISTEN='`, "export PROBE_NODE_ID PROBE_NODE_SECRET PROBE_CONTROLLER_URL PROBE_LOCAL_LISTEN"} {
		if strings.Contains(strings.ToLower(probeRouterInstallScriptLinux), strings.ToLower(forbidden)) {
			t.Fatalf("router installer must not contain %q", forbidden)
		}
	}
	if strings.Contains(probeRouterInstallScriptLinux, "probe_local_setup_token") {
		t.Fatal("router installer must not instruct users to retrieve a setup token")
	}
}
