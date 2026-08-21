//go:build linux && linux_router

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestBuildProbeLinuxRouterNFTScriptOnlyMarksLANIngress(t *testing.T) {
	snapshot := probeLinuxRouterSnapshot{
		GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: true, DNSEnabled: true, GatewayAddress: "192.168.1.150/24", LANCIDRs: []string{"192.168.1.0/24"}},
		LocalIPProxy: probeLinuxRouterLocalIPConfig{Enabled: true, PublishedCIDRs: []string{"192.168.50.0/24"}},
	}
	script := buildProbeLinuxRouterNFTScript(snapshot, "eth0", "cloudhelper0", "198.18.0.15", []string{"198.18.0.0/15", "149.154.160.0/20"}, false)
	for _, marker := range []string{`iifname "eth0" ip saddr @lan4`, "meta mark set 0x4348", `iifname "cloudhelper0" ip daddr @published4 ct mark set 0x4349`, `iifname "eth0" ct mark 0x4349`, `iifname "cloudhelper0" oifname "eth0"`, "dnat to 198.18.0.2:53", "149.154.160.0/20", `oifname "cloudhelper0" ip saddr @lan4 ip daddr @routed4 snat to 198.18.0.15`} {
		if !strings.Contains(script, marker) {
			t.Fatalf("nft script missing %q:\n%s", marker, script)
		}
	}
	if strings.Contains(script, "0.0.0.0/1") || strings.Contains(script, "128.0.0.0/1") {
		t.Fatalf("router nft script contains host takeover routes:\n%s", script)
	}
}

func TestBuildProbeLinuxRouterNFTScriptPassesNFTCheck(t *testing.T) {
	if os.Getenv("CLOUDHELPER_ROUTER_NFT_CHECK") != "1" || os.Geteuid() != 0 {
		t.Skip("set CLOUDHELPER_ROUTER_NFT_CHECK=1 and run as root to check generated nft syntax")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skipf("nft is unavailable: %v", err)
	}
	snapshot := probeLinuxRouterSnapshot{
		GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: true, DNSEnabled: true, GatewayAddress: "192.168.1.150/24", LANCIDRs: []string{"192.168.1.0/24"}},
		LocalIPProxy: probeLinuxRouterLocalIPConfig{Enabled: true, PublishedCIDRs: []string{"192.168.50.0/24"}},
	}
	cmd := exec.Command("nft", "--check", "-f", "-")
	cmd.Stdin = strings.NewReader(buildProbeLinuxRouterNFTScript(snapshot, "chlan0", "chprobe0", "198.18.0.21", []string{"198.18.0.0/15"}, false))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nft check failed: %v: %s", err, strings.TrimSpace(string(output)))
	}
}

func TestProbeLinuxRouterPhysicalCIDRsFollowIndependentSwitches(t *testing.T) {
	snapshot := probeLinuxRouterSnapshot{
		GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: false, LANCIDRs: []string{"192.168.1.0/24"}},
		LocalIPProxy: probeLinuxRouterLocalIPConfig{Enabled: true, PublishedCIDRs: []string{"192.168.50.0/24", "192.168.50.0/24"}},
	}
	got := probeLinuxRouterPhysicalCIDRs(snapshot)
	if len(got) != 1 || got[0] != "192.168.50.0/24" {
		t.Fatalf("physical CIDRs = %v", got)
	}
}

func TestProbeLinuxRouterSysctlsRestoreOriginalValues(t *testing.T) {
	oldRun := probeLinuxRouterRunCommand
	probeLinuxRouterLinuxState.mu.Lock()
	oldOriginal := probeLinuxRouterLinuxState.sysctlOriginal
	probeLinuxRouterLinuxState.sysctlOriginal = nil
	probeLinuxRouterLinuxState.mu.Unlock()
	values := map[string]string{"net.ipv4.ip_forward": "0", "net.ipv4.conf.all.rp_filter": "1"}
	var writes []string
	probeLinuxRouterRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		if name != "sysctl" || len(args) != 2 && len(args) != 3 {
			return "", fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
		if args[0] == "-n" {
			return values[args[1]], nil
		}
		if args[0] == "-w" {
			writes = append(writes, args[1])
			return args[1], nil
		}
		return "", fmt.Errorf("unexpected sysctl args: %v", args)
	}
	t.Cleanup(func() {
		probeLinuxRouterRunCommand = oldRun
		probeLinuxRouterLinuxState.mu.Lock()
		probeLinuxRouterLinuxState.sysctlOriginal = oldOriginal
		probeLinuxRouterLinuxState.mu.Unlock()
	})
	settings := [][2]string{{"net.ipv4.ip_forward", "1"}, {"net.ipv4.conf.all.rp_filter", "2"}}
	if err := applyProbeLinuxRouterSysctls(settings); err != nil {
		t.Fatal(err)
	}
	if err := applyProbeLinuxRouterSysctls(settings); err != nil {
		t.Fatal(err)
	}
	if err := restoreProbeLinuxRouterSysctls(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"net.ipv4.ip_forward=0", "net.ipv4.conf.all.rp_filter=1"} {
		found := false
		for _, got := range writes {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing sysctl restore %q in %v", want, writes)
		}
	}
}

func TestBuildProbeLinuxRouterFailOpenNFTScriptRemovesTUNMarkAndDNSRedirect(t *testing.T) {
	snapshot := probeLinuxRouterSnapshot{GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: true, DNSEnabled: true, GatewayAddress: "192.168.1.150/24", LANCIDRs: []string{"192.168.1.0/24"}}}
	script := buildProbeLinuxRouterNFTScript(snapshot, "eth0", "cloudhelper0", "", nil, true)
	if strings.Contains(script, "meta mark set") || strings.Contains(script, "dnat to") || strings.Contains(script, "snat to") {
		t.Fatalf("fail-open script still redirects traffic:\n%s", script)
	}
	if !strings.Contains(script, `oifname "eth0" ip saddr @lan4 masquerade`) {
		t.Fatalf("fail-open script lacks direct NAT:\n%s", script)
	}
}

func TestReconcileProbeLinuxRouterDNSRuntimeAppliesSystemDNS(t *testing.T) {
	resetProbeLinuxRouterDNSHooksForTest(t)
	var calls []string
	probeLinuxRouterVirtualDNSConfigured = func() bool { return false }
	probeLinuxRouterStartDNSService = func() error {
		calls = append(calls, "start")
		return nil
	}
	probeLinuxRouterApplySystemDNS = func() error {
		calls = append(calls, "apply")
		return nil
	}
	probeLinuxRouterStopDNSService = func() { calls = append(calls, "stop") }
	probeLinuxRouterRestoreSystemDNS = func() error {
		calls = append(calls, "restore")
		return nil
	}

	if err := reconcileProbeLinuxRouterDNSRuntime(true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ","); got != "start,apply" {
		t.Fatalf("DNS enable calls=%q", got)
	}
}

func TestReconcileProbeLinuxRouterDNSRuntimeRestoresSystemDNS(t *testing.T) {
	resetProbeLinuxRouterDNSHooksForTest(t)
	var calls []string
	probeLinuxRouterVirtualDNSConfigured = func() bool { return false }
	probeLinuxRouterStopDNSService = func() { calls = append(calls, "stop") }
	probeLinuxRouterRestoreSystemDNS = func() error {
		calls = append(calls, "restore")
		return nil
	}

	if err := reconcileProbeLinuxRouterDNSRuntime(false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ","); got != "stop,restore" {
		t.Fatalf("DNS disable calls=%q", got)
	}
}

func TestReconcileProbeLinuxRouterDNSRuntimeKeepsVirtualDNSOwner(t *testing.T) {
	resetProbeLinuxRouterDNSHooksForTest(t)
	var calls []string
	probeLinuxRouterVirtualDNSConfigured = func() bool { return true }
	probeLinuxRouterStartDNSService = func() error {
		calls = append(calls, "start")
		return nil
	}
	probeLinuxRouterApplySystemDNS = func() error {
		calls = append(calls, "apply")
		return nil
	}
	probeLinuxRouterStopDNSService = func() { calls = append(calls, "stop") }
	probeLinuxRouterRestoreSystemDNS = func() error {
		calls = append(calls, "restore")
		return nil
	}

	if err := reconcileProbeLinuxRouterDNSRuntime(false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ","); got != "start,apply" {
		t.Fatalf("shared DNS disable calls=%q", got)
	}
}

func TestEnsureProbeLinuxRouterDNSHealthReappliesSystemDNS(t *testing.T) {
	resetProbeLinuxRouterDNSHooksForTest(t)
	applyCalls := 0
	probeLinuxRouterDNSStatus = func() probeVirtualRouterDNSStatus {
		return probeVirtualRouterDNSStatus{Enabled: true, ListenAddr: "198.18.0.2:53"}
	}
	probeLinuxRouterApplySystemDNS = func() error {
		applyCalls++
		return nil
	}

	if err := ensureProbeLinuxRouterDNSHealth(true); err != nil {
		t.Fatal(err)
	}
	if applyCalls != 1 {
		t.Fatalf("system DNS apply calls=%d want 1", applyCalls)
	}
}

func TestEnsureProbeLinuxRouterDNSHealthRejectsStoppedService(t *testing.T) {
	resetProbeLinuxRouterDNSHooksForTest(t)
	probeLinuxRouterDNSStatus = func() probeVirtualRouterDNSStatus { return probeVirtualRouterDNSStatus{} }
	probeLinuxRouterApplySystemDNS = func() error {
		t.Fatal("system DNS must not be applied while the DNS service is stopped")
		return nil
	}

	if err := ensureProbeLinuxRouterDNSHealth(true); err == nil || !strings.Contains(err.Error(), "DNS service is not running") {
		t.Fatalf("health error=%v", err)
	}
}

func resetProbeLinuxRouterDNSHooksForTest(t *testing.T) {
	t.Helper()
	oldStart := probeLinuxRouterStartDNSService
	oldStop := probeLinuxRouterStopDNSService
	oldApply := probeLinuxRouterApplySystemDNS
	oldRestore := probeLinuxRouterRestoreSystemDNS
	oldConfigured := probeLinuxRouterVirtualDNSConfigured
	oldStatus := probeLinuxRouterDNSStatus
	t.Cleanup(func() {
		probeLinuxRouterStartDNSService = oldStart
		probeLinuxRouterStopDNSService = oldStop
		probeLinuxRouterApplySystemDNS = oldApply
		probeLinuxRouterRestoreSystemDNS = oldRestore
		probeLinuxRouterVirtualDNSConfigured = oldConfigured
		probeLinuxRouterDNSStatus = oldStatus
	})
}

func TestProbeLinuxRouterNetworkNamespacePolicy(t *testing.T) {
	if os.Getenv("CLOUDHELPER_ROUTER_NETNS_TEST") != "1" || os.Geteuid() != 0 {
		t.Skip("set CLOUDHELPER_ROUTER_NETNS_TEST=1 and run as root inside an isolated network namespace")
	}
	for _, tool := range []string{"ip", "nft"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is unavailable: %v", tool, err)
		}
	}
	mustRun := func(name string, args ...string) string {
		t.Helper()
		output, err := probeLocalLinuxRunCommand(5*time.Second, name, args...)
		if err != nil {
			t.Fatalf("%s %s: %v", name, strings.Join(args, " "), err)
		}
		return output
	}
	mustRun("ip", "link", "add", "chlan0", "type", "dummy")
	mustRun("ip", "link", "add", "chprobe0", "type", "dummy")
	t.Cleanup(func() {
		_ = cleanupProbeLinuxRouterPolicyRouting()
		_, _ = probeLocalLinuxRunCommand(5*time.Second, "nft", "delete", "table", "ip", probeLinuxRouterNFTTable)
		_, _ = probeLocalLinuxRunCommand(5*time.Second, "ip", "link", "del", "chlan0")
		_, _ = probeLocalLinuxRunCommand(5*time.Second, "ip", "link", "del", "chprobe0")
	})
	mustRun("ip", "address", "add", "192.168.1.10/24", "dev", "chlan0")
	mustRun("ip", "address", "add", "198.18.0.2/15", "dev", "chprobe0")
	mustRun("ip", "link", "set", "chlan0", "up")
	mustRun("ip", "link", "set", "chprobe0", "up")
	mainBefore := mustRun("ip", "-4", "route", "show", "table", "main")
	snapshot := probeLinuxRouterSnapshot{
		GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: true, DNSEnabled: true, GatewayAddress: "192.168.1.150/24", UpstreamGateway: "192.168.1.1", LANCIDRs: []string{"192.168.1.0/24"}},
		LocalIPProxy: probeLinuxRouterLocalIPConfig{Enabled: true, PublishedCIDRs: []string{"192.168.50.0/24"}},
	}
	if err := applyProbeLinuxRouterPolicyRouting(snapshot, "chlan0", "chprobe0"); err != nil {
		t.Fatal(err)
	}
	if err := replaceProbeLinuxRouterNFTTable(buildProbeLinuxRouterNFTScript(snapshot, "chlan0", "chprobe0", "198.18.0.21", []string{"198.18.0.0/15"}, false)); err != nil {
		t.Fatal(err)
	}
	mainAfter := mustRun("ip", "-4", "route", "show", "table", "main")
	if mainAfter != mainBefore || strings.Contains(mainAfter, "0.0.0.0/1") || strings.Contains(mainAfter, "128.0.0.0/1") {
		t.Fatalf("main route table changed\nbefore:\n%s\nafter:\n%s", mainBefore, mainAfter)
	}
	for _, check := range []struct {
		args   []string
		marker string
	}{
		{[]string{"-4", "rule", "show", "priority", probeLinuxRouterRulePriority}, "lookup 208"},
		{[]string{"-4", "rule", "show", "priority", probeLinuxRouterDirectRule}, "lookup 209"},
		{[]string{"-4", "route", "show", "table", probeLinuxRouterDirectTable}, "default via 192.168.1.1 dev chlan0"},
		{[]string{"-4", "route", "show", "table", probeLinuxRouterDirectTable}, "192.168.50.0/24 dev chlan0"},
	} {
		if output := mustRun("ip", check.args...); !strings.Contains(output, check.marker) {
			t.Fatalf("ip %s missing %q: %s", strings.Join(check.args, " "), check.marker, output)
		}
	}
	if output := mustRun("nft", "list", "table", "ip", probeLinuxRouterNFTTable); !strings.Contains(output, "ct mark 0x00004349") && !strings.Contains(output, "ct mark 0x4349") {
		t.Fatalf("nft table lacks local subnet return connmark: %s", output)
	}
}
