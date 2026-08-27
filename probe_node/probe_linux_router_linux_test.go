//go:build linux && linux_router

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveProbeLinuxRouterNetworkGatewayPriority(t *testing.T) {
	oldRun := probeLinuxRouterRunCommand
	oldDHCPDir := probeLinuxRouterDHCPServerDir
	dhcpDir := t.TempDir()
	probeLinuxRouterDHCPServerDir = dhcpDir
	t.Cleanup(func() {
		probeLinuxRouterRunCommand = oldRun
		probeLinuxRouterDHCPServerDir = oldDHCPDir
	})

	defaultGateway := "192.168.1.1"
	probeLinuxRouterRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		command := name + " " + strings.Join(args, " ")
		switch command {
		case "ip -4 route show default":
			return "default via " + defaultGateway + " dev eth0 proto dhcp metric 200\n", nil
		case "ip -4 -o address show dev eth0":
			return "2: eth0    inet 192.168.1.150/24 brd 192.168.1.255 scope global dynamic eth0\n", nil
		default:
			return "", fmt.Errorf("unexpected command: %s", command)
		}
	}

	base := probeLinuxRouterSnapshot{GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: true, Interface: "eth0"}}
	effective, err := resolveProbeLinuxRouterNetwork(base)
	if err != nil {
		t.Fatal(err)
	}
	if effective.GatewayProxy.GatewayAddress != "192.168.1.150/24" || effective.GatewayProxy.UpstreamGateway != "192.168.1.1" {
		t.Fatalf("system defaults resolved incorrectly: %+v", effective.GatewayProxy)
	}
	if len(effective.GatewayProxy.LANCIDRs) != 1 || effective.GatewayProxy.LANCIDRs[0] != "192.168.1.0/24" {
		t.Fatalf("automatic LAN CIDR=%v", effective.GatewayProxy.LANCIDRs)
	}

	withUserGateway := base
	withUserGateway.GatewayProxy.UpstreamGateway = "192.168.1.254"
	effective, err = resolveProbeLinuxRouterNetwork(withUserGateway)
	if err != nil {
		t.Fatal(err)
	}
	if effective.GatewayProxy.UpstreamGateway != "192.168.1.254" {
		t.Fatalf("user gateway did not win: %+v", effective.GatewayProxy)
	}

	defaultGateway = "192.168.1.150"
	if err := os.WriteFile(filepath.Join(dhcpDir, "eth0"), []byte("192.168.1.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	effective, err = resolveProbeLinuxRouterNetwork(base)
	if err != nil {
		t.Fatal(err)
	}
	if effective.GatewayProxy.UpstreamGateway != "192.168.1.2" {
		t.Fatalf("DHCP server fallback=%q, want 192.168.1.2", effective.GatewayProxy.UpstreamGateway)
	}
}

func TestResolveProbeLinuxRouterNetworkRejectsSelfUserGateway(t *testing.T) {
	oldRun := probeLinuxRouterRunCommand
	t.Cleanup(func() { probeLinuxRouterRunCommand = oldRun })
	probeLinuxRouterRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		switch name + " " + strings.Join(args, " ") {
		case "ip -4 route show default":
			return "default via 192.168.1.1 dev eth0 proto dhcp metric 200\n", nil
		case "ip -4 -o address show dev eth0":
			return "2: eth0 inet 192.168.1.150/24 scope global eth0\n", nil
		default:
			return "", errors.New("unexpected command")
		}
	}
	_, err := resolveProbeLinuxRouterNetwork(probeLinuxRouterSnapshot{GatewayProxy: probeLinuxRouterGatewayConfig{
		Enabled: true, Interface: "eth0", UpstreamGateway: "192.168.1.150",
	}})
	if err == nil || !strings.Contains(err.Error(), "cannot equal") {
		t.Fatalf("self user gateway error=%v", err)
	}
}

func TestResolveProbeLinuxRouterNetworkRejectsOneArmPhysicalOverlap(t *testing.T) {
	oldRun := probeLinuxRouterRunCommand
	t.Cleanup(func() { probeLinuxRouterRunCommand = oldRun })
	probeLinuxRouterRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		switch name + " " + strings.Join(args, " ") {
		case "ip -4 route show default":
			return "default via 172.18.55.254 dev eth0 proto dhcp metric 200\n", nil
		case "ip -4 -o address show dev eth0":
			return "2: eth0 inet 172.18.52.205/22 scope global eth0\n", nil
		default:
			return "", errors.New("unexpected command")
		}
	}
	_, err := resolveProbeLinuxRouterNetwork(probeLinuxRouterSnapshot{
		GatewayProxy: probeLinuxRouterGatewayConfig{Interface: "eth0"},
		OneArmRouter: probeLinuxRouterOneArmConfig{Enabled: true, SubnetCIDR: "172.18.54.0/24"},
	})
	if err == nil || !strings.Contains(err.Error(), "overlaps interface address") {
		t.Fatalf("one-arm physical overlap error=%v", err)
	}
}

func TestSelectProbeLinuxRouterInterfacePrefixMatchesGatewaySubnet(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("192.168.10.20/24"), netip.MustParsePrefix("10.20.30.40/24")}
	got, ok := selectProbeLinuxRouterInterfacePrefix(prefixes, netip.MustParseAddr("10.20.30.1"))
	if !ok || got.String() != "10.20.30.40/24" {
		t.Fatalf("selected prefix=%v ok=%t", got, ok)
	}
}

func TestEnsureProbeLinuxRouterUDHCPCHooks(t *testing.T) {
	oldDir := probeLinuxRouterUDHCPCDir
	probeLinuxRouterUDHCPCDir = t.TempDir()
	t.Cleanup(func() { probeLinuxRouterUDHCPCDir = oldDir })
	if err := ensureProbeLinuxRouterUDHCPCHooks(); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"bound", "renew"} {
		path := filepath.Join(probeLinuxRouterUDHCPCDir, "post-"+event, "90-cloudhelper-probe-router")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("hook mode=%o", info.Mode().Perm())
		}
		raw, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(raw), "${serverid:-}") {
			t.Fatalf("hook content missing server identifier capture: err=%v content=%q", err, raw)
		}
		if output, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
			t.Fatalf("hook shell syntax is invalid: %v: %s", err, output)
		}
	}
}

func TestBuildProbeLinuxRouterNFTScriptOnlyMarksLANIngress(t *testing.T) {
	snapshot := probeLinuxRouterSnapshot{
		GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: true, DNSEnabled: true, GatewayAddress: "192.168.1.150/24", LANCIDRs: []string{"192.168.1.0/24"}},
		LocalIPProxy: probeLinuxRouterLocalIPConfig{Enabled: true, PublishedCIDRs: []string{"192.168.50.0/24"}},
	}
	script := buildProbeLinuxRouterNFTScript(snapshot, "eth0", "cloudhelper0", "198.18.0.15", []string{"198.18.0.0/15", "149.154.160.0/20"}, nil, false)
	for _, marker := range []string{`iifname "eth0" ip saddr @lan4`, "meta mark set 0x4348", `chain preconntrack { type filter hook prerouting priority raw; policy accept;`, `iifname "eth0" ip saddr @lan4 ip daddr != @lan4 ip daddr != @routed4 notrack`, `iifname "cloudhelper0" ip daddr @published4 ct mark set 0x4349`, `iifname "eth0" ct mark 0x4349`, `iifname "cloudhelper0" oifname "eth0"`, "dnat to 198.18.0.2:53", "149.154.160.0/20", `oifname "cloudhelper0" ip saddr @lan4 ip daddr @routed4 snat to 198.18.0.15`} {
		if !strings.Contains(script, marker) {
			t.Fatalf("nft script missing %q:\n%s", marker, script)
		}
	}
	if strings.Contains(script, "0.0.0.0/1") || strings.Contains(script, "128.0.0.0/1") {
		t.Fatalf("router nft script contains host takeover routes:\n%s", script)
	}
}

func TestBuildProbeLinuxRouterNFTScriptOneArmRetainsProxyAndDirectSNAT(t *testing.T) {
	snapshot := probeLinuxRouterSnapshot{
		GatewayProxy: probeLinuxRouterGatewayConfig{GatewayAddress: "172.18.52.205/22", DNSEnabled: true},
		OneArmRouter: probeLinuxRouterOneArmConfig{Enabled: true, SubnetCIDR: "192.168.205.0/24"},
	}
	script := buildProbeLinuxRouterNFTScript(snapshot, "eth0", "cloudhelper0", "198.18.0.15", []string{"198.18.0.0/15"}, nil, false)
	for _, marker := range []string{
		"set one_arm4", "192.168.205.0/24",
		`iifname "eth0" ip saddr @one_arm4 ip daddr != @one_arm4 meta mark set 0x4348`,
		`oifname "cloudhelper0" ip saddr @one_arm4 ip daddr @routed4 snat to 198.18.0.15`,
		`oifname "eth0" ip saddr @one_arm4 snat to 172.18.52.205`,
		"ip daddr 192.168.205.1 udp dport 53 dnat to 198.18.0.2:53",
	} {
		if !strings.Contains(script, marker) {
			t.Fatalf("one-arm nft script missing %q:\n%s", marker, script)
		}
	}
}

func TestReconcileProbeLinuxRouterOneArmAddressAddsAndRemovesAlias(t *testing.T) {
	oldRun := probeLinuxRouterRunCommand
	probeLinuxRouterLinuxState.mu.Lock()
	oldInterface := probeLinuxRouterLinuxState.oneArmInterface
	oldGateway := probeLinuxRouterLinuxState.oneArmGatewayCIDR
	probeLinuxRouterLinuxState.oneArmInterface = ""
	probeLinuxRouterLinuxState.oneArmGatewayCIDR = ""
	probeLinuxRouterLinuxState.mu.Unlock()
	var commands []string
	probeLinuxRouterRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return "", nil
	}
	t.Cleanup(func() {
		probeLinuxRouterRunCommand = oldRun
		probeLinuxRouterLinuxState.mu.Lock()
		probeLinuxRouterLinuxState.oneArmInterface = oldInterface
		probeLinuxRouterLinuxState.oneArmGatewayCIDR = oldGateway
		probeLinuxRouterLinuxState.mu.Unlock()
	})
	snapshot := probeLinuxRouterSnapshot{OneArmRouter: probeLinuxRouterOneArmConfig{Enabled: true, SubnetCIDR: "192.168.205.0/24"}}
	if err := reconcileProbeLinuxRouterOneArmAddress(snapshot, "eth0"); err != nil {
		t.Fatal(err)
	}
	snapshot.OneArmRouter.Enabled = false
	if err := reconcileProbeLinuxRouterOneArmAddress(snapshot, "eth0"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ip -4 address replace 192.168.205.1/24 dev eth0",
		"ip -4 address del 192.168.205.1/24 dev eth0",
	}
	if strings.Join(commands, "|") != strings.Join(want, "|") {
		t.Fatalf("one-arm address commands=%v want=%v", commands, want)
	}
}

func TestBuildProbeLinuxRouterNFTScriptPassesNFTCheck(t *testing.T) {
	if os.Getenv("CLOUDHELPER_ROUTER_NFT_CHECK") != "1" || os.Geteuid() != 0 {
		t.Skip("set CLOUDHELPER_ROUTER_NFT_CHECK=1 and run as root to check generated nft syntax")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skipf("nft is unavailable: %v", err)
	}
	snapshots := []probeLinuxRouterSnapshot{
		{
			GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: true, DNSEnabled: true, GatewayAddress: "192.168.1.150/24", LANCIDRs: []string{"192.168.1.0/24"}},
			LocalIPProxy: probeLinuxRouterLocalIPConfig{Enabled: true, PublishedCIDRs: []string{"192.168.50.0/24"}},
		},
		{
			GatewayProxy: probeLinuxRouterGatewayConfig{DNSEnabled: true, GatewayAddress: "172.18.52.205/22", LANCIDRs: []string{"172.18.52.0/22"}},
			OneArmRouter: probeLinuxRouterOneArmConfig{Enabled: true, SubnetCIDR: "192.168.205.0/24"},
		},
	}
	for index, snapshot := range snapshots {
		cmd := exec.Command("nft", "--check", "-f", "-")
		cmd.Stdin = strings.NewReader(buildProbeLinuxRouterNFTScript(snapshot, "chlan0", "chprobe0", "198.18.0.21", []string{"198.18.0.0/15"}, nil, false))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("nft check %d failed: %v: %s", index, err, strings.TrimSpace(string(output)))
		}
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

func TestProbeLinuxRouterOneArmDirectTableUsesUpstreamDefault(t *testing.T) {
	snapshot := probeLinuxRouterSnapshot{
		GatewayProxy: probeLinuxRouterGatewayConfig{Enabled: false, LANCIDRs: []string{"172.18.52.0/22"}},
		OneArmRouter: probeLinuxRouterOneArmConfig{Enabled: true, SubnetCIDR: "192.168.205.0/24"},
	}
	got := probeLinuxRouterPhysicalCIDRs(snapshot)
	if len(got) != 1 || got[0] != "192.168.205.0/24" {
		t.Fatalf("one-arm direct table CIDRs=%v, upstream physical subnet must use the configured default gateway", got)
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
	script := buildProbeLinuxRouterNFTScript(snapshot, "eth0", "cloudhelper0", "", nil, nil, true)
	if strings.Contains(script, "meta mark set") || strings.Contains(script, "dnat to") || strings.Contains(script, "snat to") || strings.Contains(script, "notrack") {
		t.Fatalf("fail-open script still redirects traffic:\n%s", script)
	}
	if !strings.Contains(script, `oifname "eth0" ip saddr @lan4 masquerade`) {
		t.Fatalf("fail-open script lacks direct NAT:\n%s", script)
	}
}

func TestBuildProbeLinuxRouterNFTScriptRestrictsOnlyForwardedLANDNS(t *testing.T) {
	snapshot := probeLinuxRouterSnapshot{
		GatewayProxy: probeLinuxRouterGatewayConfig{
			Enabled: true, DNSEnabled: true, DNSWhitelistEnabled: true,
			GatewayAddress: "192.168.1.150/24", LANCIDRs: []string{"192.168.1.0/24"},
		},
	}
	script := buildProbeLinuxRouterNFTScript(snapshot, "eth0", "cloudhelper0", "198.18.0.15", []string{"198.18.0.0/15"}, []string{"1.1.1.1", "8.8.8.8"}, false)
	for _, marker := range []string{
		"set dns_allow4", "1.1.1.1, 8.8.8.8",
		"chain dns_guard { type filter hook forward priority filter; policy accept;",
		`iifname "eth0" ip saddr @lan4 ip daddr @dns_allow4 udp dport { 53, 853 } accept`,
		`iifname "eth0" ip saddr @lan4 tcp dport { 53, 853 } drop`,
		"dnat to 198.18.0.2:53",
	} {
		if !strings.Contains(script, marker) {
			t.Fatalf("DNS whitelist nft script missing %q:\n%s", marker, script)
		}
	}
	if strings.Contains(script, "hook output") || strings.Contains(script, "hook input") {
		t.Fatalf("DNS whitelist unexpectedly restricts router-local DNS:\n%s", script)
	}
}

func TestBuildProbeLinuxRouterNFTScriptEmptyWhitelistBlocksDirectClientDNS(t *testing.T) {
	snapshot := probeLinuxRouterSnapshot{
		GatewayProxy: probeLinuxRouterGatewayConfig{
			Enabled: true, DNSEnabled: true, DNSWhitelistEnabled: true,
			GatewayAddress: "192.168.1.150/24", LANCIDRs: []string{"192.168.1.0/24"},
		},
	}
	script := buildProbeLinuxRouterNFTScript(snapshot, "eth0", "cloudhelper0", "198.18.0.15", nil, nil, false)
	if !strings.Contains(script, `iifname "eth0" ip saddr @lan4 udp dport { 53, 853 } drop`) {
		t.Fatalf("empty DNS whitelist does not block direct client DNS:\n%s", script)
	}
	if strings.Contains(script, "ip daddr @dns_allow4 udp") {
		t.Fatalf("empty DNS whitelist contains an allow rule:\n%s", script)
	}
}

func TestResolveProbeLinuxRouterDNSWhitelistMergesIPsAndDomains(t *testing.T) {
	oldLookup := probeLinuxRouterLookupIP
	probeLinuxRouterLookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "dns.example.com" {
			return nil, fmt.Errorf("unexpected host %s", host)
		}
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("2001:4860:4860::8888")}}, nil
	}
	t.Cleanup(func() { probeLinuxRouterLookupIP = oldLookup })

	got, err := resolveProbeLinuxRouterDNSWhitelist(probeLinuxRouterGatewayConfig{
		Enabled: true, DNSWhitelistEnabled: true,
		DNSWhitelistIPs: []string{"1.1.1.1"}, DNSWhitelistDomains: []string{"dns.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(got, ","); joined != "1.1.1.1,8.8.8.8" {
		t.Fatalf("resolved DNS whitelist=%q", joined)
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
	if err := replaceProbeLinuxRouterNFTTable(buildProbeLinuxRouterNFTScript(snapshot, "chlan0", "chprobe0", "198.18.0.21", []string{"198.18.0.0/15"}, nil, false)); err != nil {
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
