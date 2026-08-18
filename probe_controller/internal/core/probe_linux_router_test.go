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
	ProbeRouteConfigStore = &probeRouteConfigStore{data: probeRouteConfigStoreData{VirtualRouter: defaultProbeVirtualRouterConfig(), LinuxRouters: []probeLinuxRouterConfig{}}}
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

func TestNormalizeProbeLinuxRouterConfigDefaultsAndRevision(t *testing.T) {
	setupProbeLinuxRouterTestStores(t)
	item, err := normalizeProbeLinuxRouterConfig(probeLinuxRouterConfig{NodeID: "21"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if item.GatewayProxy.Enabled || item.LocalIPProxy.Enabled {
		t.Fatalf("switches must default off: %+v", item)
	}
	if item.GatewayProxy.GatewayAddress != "192.168.1.150/24" || item.GatewayProxy.UpstreamGateway != "192.168.1.1" {
		t.Fatalf("unexpected gateway defaults: %+v", item.GatewayProxy)
	}
	if item.Revision != 1 || len(item.SHA256) != 64 {
		t.Fatalf("invalid revision/hash: %+v", item)
	}
	repeated, err := normalizeProbeLinuxRouterConfig(item, &item)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Revision != item.Revision {
		t.Fatalf("unchanged config revision changed: %d -> %d", item.Revision, repeated.Revision)
	}
}

func TestNormalizeProbeLinuxRouterConfigRequiresACLAndRejectsFakePool(t *testing.T) {
	setupProbeLinuxRouterTestStores(t)
	raw := defaultProbeLinuxRouterConfig("21")
	raw.LocalIPProxy.Enabled = true
	if _, err := normalizeProbeLinuxRouterConfig(raw, nil); err == nil || !strings.Contains(err.Error(), "allowed_node_ids") {
		t.Fatalf("missing ACL error = %v", err)
	}
	raw.LocalIPProxy.AllowedNodeIDs = []string{"1"}
	raw.LocalIPProxy.PublishedCIDRs = []string{"198.18.0.0/15"}
	if _, err := normalizeProbeLinuxRouterConfig(raw, nil); err == nil {
		t.Fatal("fake IP pool overlap unexpectedly accepted")
	}
}

func TestAppendProbeLinuxRouterPublishedRouteRulesScopesByACL(t *testing.T) {
	setupProbeLinuxRouterTestStores(t)
	router := defaultProbeLinuxRouterConfig("21")
	router.LocalIPProxy.Enabled = true
	router.LocalIPProxy.PublishedCIDRs = []string{"192.168.50.0/24"}
	router.LocalIPProxy.AllowedNodeIDs = []string{"1"}
	rules := appendProbeLinuxRouterPublishedRouteRules(nil, []probeLinuxRouterConfig{router}, "1")
	if len(rules) != 1 || rules[0].ExitNodeID != "21" || len(rules[0].Entries) != 1 || rules[0].Entries[0] != "cidr:192.168.50.0/24" {
		t.Fatalf("unexpected aggregated rules: %+v", rules)
	}
	if denied := appendProbeLinuxRouterPublishedRouteRules(nil, []probeLinuxRouterConfig{router}, "2"); len(denied) != 0 {
		t.Fatalf("unauthorized node received rules: %+v", denied)
	}
	if self := appendProbeLinuxRouterPublishedRouteRules(nil, []probeLinuxRouterConfig{router}, "21"); len(self) != 0 {
		t.Fatalf("router received its own published rule: %+v", self)
	}
	ProbeStore.mu.Lock()
	ProbeStore.data.ProbeNodes[1].NodeKind = probeNodeKindNormal
	ProbeStore.mu.Unlock()
	if changedKind := appendProbeLinuxRouterPublishedRouteRules(nil, []probeLinuxRouterConfig{router}, "1"); len(changedKind) != 0 {
		t.Fatalf("stale router config remained active after kind change: %+v", changedKind)
	}
	ProbeStore.mu.Lock()
	ProbeStore.data.ProbeNodes[1].NodeKind = probeNodeKindLinuxRouter
	ProbeStore.mu.Unlock()
	probeRuntimeStore.mu.Lock()
	previousRuntime := probeRuntimeStore.data
	probeRuntimeStore.data = map[string]probeRuntimeStatus{"21": {NodeID: "21", Online: false}}
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		probeRuntimeStore.mu.Lock()
		probeRuntimeStore.data = previousRuntime
		probeRuntimeStore.mu.Unlock()
	})
	if offline := appendProbeLinuxRouterPublishedRouteRules(nil, []probeLinuxRouterConfig{router}, "1"); len(offline) != 0 {
		t.Fatalf("offline router still published routes: %+v", offline)
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
	for _, marker := range []string{`data-tab="linux-router"`, `id="linux-router-node"`, `id="linux-router-gateway-enabled"`, `id="linux-router-local-enabled"`, `/mng/api/route/linux_router`, `旁路由运行状态`, `最优邻接延迟`, `runtime.latency_ms`} {
		if !strings.Contains(mngRoutePageHTML, marker) {
			t.Fatalf("route page missing %q", marker)
		}
	}
}

func TestLinuxRouterInstallerIsAlpineOpenRCAndDualArchitecture(t *testing.T) {
	if strings.Contains(probeRouterInstallScriptLinux, "\r") {
		t.Fatal("router installer must use LF line endings for Alpine /bin/sh")
	}
	for _, marker := range []string{"command -v apk", "apk add --no-cache", "kmod", "rc-service rc-update ip nft sysctl", "command -v \"${required_command}\"", "modprobe tun", "/dev/net/tun", "ip tuntap add", "x86_64", "aarch64|arm64", "cloudhelper-probe-router-linux-${GOARCH}", "PROBE_ROUTER_PROGRAM_URL", "/api/probe/proxy/download", "--data-urlencode \"node_id=${PROBE_NODE_ID}\"", "--data-urlencode \"secret=${PROBE_NODE_SECRET}\"", "X-CloudHelper-Download-URL: ${program_url}", "/opt/cloudhelper/probe_router", "--upgrade-verify-build-kind=linux_router", "rc-update add", "PROBE_ROUTER_WEB_LISTEN", "0.0.0.0:18080", "curl -fsS --noproxy", "/local/router", "probe_local_setup_token"} {
		if !strings.Contains(probeRouterInstallScriptLinux, marker) {
			t.Fatalf("router installer missing %q", marker)
		}
	}
	for _, forbidden := range []string{"systemctl", "mihomo", "docker"} {
		if strings.Contains(strings.ToLower(probeRouterInstallScriptLinux), forbidden) {
			t.Fatalf("router installer must not contain %q", forbidden)
		}
	}
}
