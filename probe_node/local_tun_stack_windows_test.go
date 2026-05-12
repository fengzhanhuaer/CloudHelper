//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseProbeLocalTUNIPv4TargetTCP(t *testing.T) {
	packet := make([]byte, 40)
	packet[0] = 0x45
	packet[9] = 6
	copy(packet[16:20], []byte{1, 2, 3, 4})
	binary.BigEndian.PutUint16(packet[22:24], uint16(443))

	network, target, err := parseProbeLocalTUNPacketTarget(packet)
	if err != nil {
		t.Fatalf("parse target failed: %v", err)
	}
	if network != "tcp" {
		t.Fatalf("network=%q", network)
	}
	if target != "1.2.3.4:443" {
		t.Fatalf("target=%q", target)
	}
}

func TestParseProbeLocalTUNIPv4TargetUDP(t *testing.T) {
	packet := make([]byte, 40)
	packet[0] = 0x45
	packet[9] = 17
	copy(packet[16:20], []byte{8, 8, 8, 8})
	binary.BigEndian.PutUint16(packet[22:24], uint16(53))

	network, target, err := parseProbeLocalTUNPacketTarget(packet)
	if err != nil {
		t.Fatalf("parse target failed: %v", err)
	}
	if network != "udp" {
		t.Fatalf("network=%q", network)
	}
	if target != "8.8.8.8:53" {
		t.Fatalf("target=%q", target)
	}
}

func TestParseProbeLocalTUNIPv6TargetTCP(t *testing.T) {
	packet := make([]byte, 60)
	packet[0] = 0x60
	packet[6] = 6
	dst := net.ParseIP("2001:db8::1").To16()
	copy(packet[24:40], dst)
	binary.BigEndian.PutUint16(packet[42:44], uint16(443))

	network, target, err := parseProbeLocalTUNPacketTarget(packet)
	if err != nil {
		t.Fatalf("parse target failed: %v", err)
	}
	if network != "tcp" {
		t.Fatalf("network=%q", network)
	}
	if !strings.Contains(target, "2001:db8::1") || !strings.HasSuffix(target, ":443") {
		t.Fatalf("target=%q", target)
	}
}

func TestProbeLocalTUNSimplePacketStackWriteTunnelValidatesSelectedChain(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	if err := ensureProbeLocalProxyDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults failed: %v", err)
	}

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{{Group: "media", Rules: []string{"domain_suffix:example.com"}}}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{{Group: "media", Action: "tunnel", SelectedChainID: "chain-proxy-1", TunnelNodeID: "chain:chain-proxy-1"}}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	dnsDecision := resolveProbeLocalProxyRouteDecisionByDomain("api.example.com")
	fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", dnsDecision)
	if !ok {
		t.Fatal("allocate fake ip failed")
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	packet := make([]byte, 40)
	packet[0] = 0x45
	packet[9] = 6
	ip := net.ParseIP(fakeIP).To4()
	copy(packet[16:20], ip)
	binary.BigEndian.PutUint16(packet[22:24], uint16(443))

	stack := &probeLocalTUNSimplePacketStack{}
	n, err := stack.Write(packet)
	if err != nil {
		t.Fatalf("write packet failed: %v", err)
	}
	if n != len(packet) {
		t.Fatalf("n=%d len=%d", n, len(packet))
	}
}

func TestProbeLocalTUNSimplePacketStackWriteTunnelValidatesSelectedChainUDPFakeIP(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	if err := ensureProbeLocalProxyDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults failed: %v", err)
	}

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{{Group: "media", Rules: []string{"domain_suffix:example.com"}}}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{{Group: "media", Action: "tunnel", SelectedChainID: "chain-proxy-1", TunnelNodeID: "chain:chain-proxy-1"}}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	dnsDecision := resolveProbeLocalProxyRouteDecisionByDomain("api.example.com")
	fakeIP, ok := allocateProbeLocalDNSFakeIP("api.example.com", dnsDecision)
	if !ok {
		t.Fatal("allocate fake ip failed")
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	packet := make([]byte, 40)
	packet[0] = 0x45
	packet[9] = 17
	ip := net.ParseIP(fakeIP).To4()
	copy(packet[16:20], ip)
	binary.BigEndian.PutUint16(packet[22:24], uint16(443))

	stack := &probeLocalTUNSimplePacketStack{}
	n, err := stack.Write(packet)
	if err != nil {
		t.Fatalf("write packet failed: %v", err)
	}
	if n != len(packet) {
		t.Fatalf("n=%d len=%d", n, len(packet))
	}
}

func TestProbeLocalTUNSimplePacketStackWriteRejectRoute(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{{Group: "blocked", Rules: []string{"domain_suffix:blocked.example"}}}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{{Group: "blocked", Action: "reject"}}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	dnsDecision := resolveProbeLocalProxyRouteDecisionByDomain("x.blocked.example")
	fakeIP, ok := allocateProbeLocalDNSFakeIP("x.blocked.example", dnsDecision)
	if !ok {
		t.Fatal("allocate fake ip failed")
	}

	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()

	packet := make([]byte, 40)
	packet[0] = 0x45
	packet[9] = 6
	ip := net.ParseIP(fakeIP).To4()
	copy(packet[16:20], ip)
	binary.BigEndian.PutUint16(packet[22:24], uint16(443))

	stack := &probeLocalTUNSimplePacketStack{}
	n, err := stack.Write(packet)
	if err != nil {
		t.Fatalf("write packet failed: %v", err)
	}
	if n != len(packet) {
		t.Fatalf("n=%d len=%d", n, len(packet))
	}
}

func TestProbeLocalTUNSimplePacketStackWriteClosed(t *testing.T) {
	stack := &probeLocalTUNSimplePacketStack{closed: true}
	_, err := stack.Write([]byte{0x45})
	if err == nil {
		t.Fatal("expected closed error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "closed") {
		t.Fatalf("err=%v", err)
	}
}

func TestParseProbeLocalTUNPacketTargetRejectUnsupportedVersion(t *testing.T) {
	_, _, err := parseProbeLocalTUNPacketTarget([]byte{0x10})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Fatalf("unexpected err=%v", err)
	}
}

func TestEnsureProbeLocalDirectBypassForTargetRefCountAndRelease(t *testing.T) {
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(resetProbeLocalDirectBypassStateForTest)

	acquireCalls := 0
	releaseCalls := 0
	probeLocalAcquireDirectBypassRoute = func(host string) (func(), error) {
		acquireCalls++
		if host != "1.2.3.4" {
			t.Fatalf("host=%q", host)
		}
		return func() {}, nil
	}
	probeLocalReleaseDirectBypassRoute = func(host string) {
		releaseCalls++
		if host != "1.2.3.4" {
			t.Fatalf("host=%q", host)
		}
	}

	if err := ensureProbeLocalDirectBypassForTarget("1.2.3.4:443"); err != nil {
		t.Fatalf("ensure bypass #1 failed: %v", err)
	}
	if err := ensureProbeLocalDirectBypassForTarget("1.2.3.4:8443"); err != nil {
		t.Fatalf("ensure bypass #2 failed: %v", err)
	}
	if acquireCalls != 1 {
		t.Fatalf("acquireCalls=%d", acquireCalls)
	}

	releaseProbeLocalDirectBypassForHost("1.2.3.4")
	if releaseCalls != 0 {
		t.Fatalf("releaseCalls=%d", releaseCalls)
	}
	releaseProbeLocalDirectBypassForHost("1.2.3.4")
	if releaseCalls != 1 {
		t.Fatalf("releaseCalls=%d", releaseCalls)
	}
}

func TestProbeLocalTUNSimplePacketStackCloseReleasesBypass(t *testing.T) {
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(resetProbeLocalDirectBypassStateForTest)

	releaseCalls := 0
	probeLocalAcquireDirectBypassRoute = func(host string) (func(), error) { return func() {}, nil }
	probeLocalReleaseDirectBypassRoute = func(host string) {
		releaseCalls++
		if host != "1.2.3.4" {
			t.Fatalf("host=%q", host)
		}
	}

	if err := ensureProbeLocalDirectBypassForTarget("1.2.3.4:443"); err != nil {
		t.Fatalf("ensure bypass failed: %v", err)
	}
	stack := &probeLocalTUNSimplePacketStack{}
	if err := stack.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if releaseCalls != 1 {
		t.Fatalf("releaseCalls=%d", releaseCalls)
	}
}

func TestAcquireProbeLocalTUNUDPSourceRefCount(t *testing.T) {
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(resetProbeLocalDirectBypassStateForTest)

	sourceKey, refs, release1 := acquireProbeLocalTUNUDPSource("10.0.0.8", 53000)
	if sourceKey != "10.0.0.8:53000" {
		t.Fatalf("sourceKey=%q", sourceKey)
	}
	if refs != 1 {
		t.Fatalf("refs=%d", refs)
	}
	if got := probeLocalTUNUDPSourceRefs(sourceKey); got != 1 {
		t.Fatalf("got refs=%d", got)
	}

	_, refs2, release2 := acquireProbeLocalTUNUDPSource("10.0.0.8", 53000)
	if refs2 != 2 {
		t.Fatalf("refs2=%d", refs2)
	}
	if got := probeLocalTUNUDPSourceRefs(sourceKey); got != 2 {
		t.Fatalf("got refs=%d", got)
	}

	release1()
	if got := probeLocalTUNUDPSourceRefs(sourceKey); got != 1 {
		t.Fatalf("after release1 refs=%d", got)
	}
	release2()
	if got := probeLocalTUNUDPSourceRefs(sourceKey); got != 0 {
		t.Fatalf("after release2 refs=%d", got)
	}
}

func TestShouldFallbackProbeLocalUDPBind(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{err: nil, want: false},
		{err: syscall.Errno(10048), want: true},
		{err: syscall.Errno(10049), want: true},
		{err: errors.New("address already in use"), want: true},
		{err: errors.New("requested address is not valid in its context"), want: true},
		{err: errors.New("random network error"), want: false},
	}
	for i, tc := range cases {
		if got := shouldFallbackProbeLocalUDPBind(tc.err); got != tc.want {
			t.Fatalf("case=%d got=%v want=%v err=%v", i, got, tc.want, tc.err)
		}
	}
}

func TestEnsureProbeLocalDirectBypassForTargetUsesPrimaryEgressRoute(t *testing.T) {
	resetProbeLocalDirectBypassStateForTest()
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(resetProbeLocalDirectBypassStateForTest)
	oldRun := probeLocalWindowsRunCommand
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	detected := false
	added := false
	probeLocalWindowsRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		joined := name + " " + strings.Join(args, " ")
		switch name {
		case "powershell":
			detected = true
			if !strings.Contains(joined, "$exclude=9") {
				t.Fatalf("powershell command did not exclude tun ifindex: %s", joined)
			}
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		case "route":
			added = true
			if strings.Contains(joined, "198.18.0.1") || strings.Contains(joined, " IF 9") {
				t.Fatalf("route add unexpectedly used tun route target: %s", joined)
			}
			if !strings.Contains(joined, "192.168.1.1") || !strings.Contains(joined, " IF 12") {
				t.Fatalf("route add used unexpected egress target: %s", joined)
			}
			return "", nil
		default:
			return "", nil
		}
	}

	if err := ensureProbeLocalDirectBypassForTarget("1.2.3.4:443"); err != nil {
		t.Fatalf("ensure bypass failed: %v", err)
	}
	if !detected {
		t.Fatal("expected primary egress route detection")
	}
	if !added {
		t.Fatal("expected route add command")
	}
}

func TestReleaseProbeLocalTUNDirectBypassRouteUsesStoredPrimaryEgressRoute(t *testing.T) {
	resetProbeLocalDirectBypassStateForTest()
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(resetProbeLocalDirectBypassStateForTest)
	oldRun := probeLocalWindowsRunCommand
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	probeLocalWindowsRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		joined := name + " " + strings.Join(args, " ")
		switch name {
		case "powershell":
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		case "route":
			if !strings.Contains(joined, "192.168.1.1") || !strings.Contains(joined, " IF 12") {
				t.Fatalf("unexpected route create target: %s", joined)
			}
			return "", nil
		default:
			return "", nil
		}
	}
	if err := ensureProbeLocalDirectBypassForTarget("1.2.3.4:443"); err != nil {
		t.Fatalf("ensure bypass failed: %v", err)
	}

	detectedOnRelease := false
	deleted := false
	probeLocalWindowsRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		joined := name + " " + strings.Join(args, " ")
		switch name {
		case "powershell":
			detectedOnRelease = true
			return `{"interface_index":13,"next_hop":"192.168.1.254"}`, nil
		case "route":
			deleted = true
			if !strings.Contains(joined, "DELETE 1.2.3.4") {
				t.Fatalf("unexpected route delete command: %s", joined)
			}
			if strings.Contains(joined, "192.168.1.254") || strings.Contains(joined, " IF 13") {
				t.Fatalf("route delete should use stored egress target: %s", joined)
			}
			if !strings.Contains(joined, "192.168.1.1") || !strings.Contains(joined, " IF 12") {
				t.Fatalf("route delete used unexpected stored target: %s", joined)
			}
			return "", nil
		default:
			return "", nil
		}
	}

	releaseProbeLocalDirectBypassForHost("1.2.3.4")
	if detectedOnRelease {
		t.Fatal("release should not redetect primary egress route")
	}
	if !deleted {
		t.Fatal("expected route delete command")
	}
}

func TestReleaseProbeLocalAllDirectBypassRoutesUsesStoredPrimaryEgressRoute(t *testing.T) {
	resetProbeLocalDirectBypassStateForTest()
	useProbeLocalWindowsCommandBackedRouteHooksForTest()
	t.Cleanup(resetProbeLocalDirectBypassStateForTest)
	oldRun := probeLocalWindowsRunCommand
	t.Cleanup(func() {
		probeLocalWindowsRunCommand = oldRun
		resetProbeLocalWindowsNativeRouteHooksForTest()
	})
	t.Setenv("PROBE_LOCAL_TUN_GATEWAY", "198.18.0.1")
	t.Setenv("PROBE_LOCAL_TUN_IF_INDEX", "9")

	createCalls := 0
	deleteCalls := 0
	probeLocalWindowsRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		joined := name + " " + strings.Join(args, " ")
		switch name {
		case "powershell":
			return `{"interface_index":12,"next_hop":"192.168.1.1"}`, nil
		case "route":
			if strings.Contains(joined, "DELETE") {
				deleteCalls++
				if !strings.Contains(joined, "192.168.1.1") || !strings.Contains(joined, " IF 12") {
					t.Fatalf("route delete used unexpected stored target: %s", joined)
				}
				return "", nil
			}
			createCalls++
			return "", nil
		default:
			return "", nil
		}
	}

	if err := ensureProbeLocalDirectBypassForTarget("1.2.3.4:443"); err != nil {
		t.Fatalf("ensure bypass #1 failed: %v", err)
	}
	if err := ensureProbeLocalDirectBypassForTarget("5.6.7.8:443"); err != nil {
		t.Fatalf("ensure bypass #2 failed: %v", err)
	}
	if createCalls != 2 {
		t.Fatalf("createCalls=%d", createCalls)
	}

	releaseProbeLocalAllDirectBypassRoutes()
	if deleteCalls != 2 {
		t.Fatalf("deleteCalls=%d", deleteCalls)
	}
}

func TestClassifyProbeLocalTUNError(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{err: errors.New("i/o timeout"), want: "timeout"},
		{err: errors.New("connection refused"), want: "connection_refused"},
		{err: errors.New("connection reset by peer"), want: "connection_reset"},
		{err: errors.New("broken pipe"), want: "broken_pipe"},
		{err: errors.New("address already in use"), want: "address_in_use"},
		{err: errors.New("cannot assign requested address"), want: "address_not_available"},
		{err: errors.New("EOF"), want: "eof"},
		{err: errors.New("use of closed network connection"), want: "closed"},
	}
	for i, tc := range cases {
		if got := classifyProbeLocalTUNError("open_failed", tc.err); got != tc.want {
			t.Fatalf("case=%d got=%q want=%q", i, got, tc.want)
		}
	}
	if got := classifyProbeLocalTUNError("open_failed", nil); got != "open_failed" {
		t.Fatalf("nil err got=%q", got)
	}
}
