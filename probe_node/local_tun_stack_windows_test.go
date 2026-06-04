//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

type nopProbeLocalTUNUDPReadWriteCloser struct{}

func (nopProbeLocalTUNUDPReadWriteCloser) Read([]byte) (int, error)    { return 0, io.EOF }
func (nopProbeLocalTUNUDPReadWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopProbeLocalTUNUDPReadWriteCloser) Close() error                { return nil }

type deadlineProbeLocalTUNUDPReadWriteCloser struct {
	nopProbeLocalTUNUDPReadWriteCloser
	deadline time.Time
}

func (c *deadlineProbeLocalTUNUDPReadWriteCloser) SetReadDeadline(t time.Time) error {
	c.deadline = t
	return nil
}

type timeoutProbeLocalTUNPacketConnError struct{}

func (timeoutProbeLocalTUNPacketConnError) Error() string   { return "timeout" }
func (timeoutProbeLocalTUNPacketConnError) Timeout() bool   { return true }
func (timeoutProbeLocalTUNPacketConnError) Temporary() bool { return true }

type fakeProbeLocalTUNPacketConn struct {
	mu       sync.Mutex
	packet   []byte
	read     bool
	closed   bool
	writes   [][]byte
	remote   net.Addr
	deadline time.Time
}

func (c *fakeProbeLocalTUNPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, nil, net.ErrClosed
	}
	if c.read {
		return 0, nil, timeoutProbeLocalTUNPacketConnError{}
	}
	c.read = true
	return copy(p, c.packet), c.remote, nil
}

func (c *fakeProbeLocalTUNPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (c *fakeProbeLocalTUNPacketConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *fakeProbeLocalTUNPacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("198.18.0.1"), Port: 53}
}

func (c *fakeProbeLocalTUNPacketConn) SetDeadline(t time.Time) error {
	c.deadline = t
	return nil
}

func (c *fakeProbeLocalTUNPacketConn) SetReadDeadline(t time.Time) error {
	c.deadline = t
	return nil
}

func (c *fakeProbeLocalTUNPacketConn) SetWriteDeadline(t time.Time) error {
	c.deadline = t
	return nil
}

func TestShouldHijackProbeLocalTUNUDPDNSFlow(t *testing.T) {
	tests := []struct {
		target string
		want   bool
	}{
		{target: "192.168.1.1:53", want: true},
		{target: "8.8.8.8:53", want: true},
		{target: "[2001:4860:4860::8888]:53", want: true},
		{target: "192.168.1.1:443", want: false},
		{target: "224.0.0.251:53", want: false},
		{target: "127.0.0.1:53", want: false},
		{target: "resolver.example:53", want: false},
	}
	for _, tt := range tests {
		if got := shouldHijackProbeLocalTUNUDPDNSFlow(tt.target); got != tt.want {
			t.Fatalf("shouldHijackProbeLocalTUNUDPDNSFlow(%q)=%v want=%v", tt.target, got, tt.want)
		}
	}
}

func TestShouldHijackProbeLocalTUNTCPDNSFlow(t *testing.T) {
	if !shouldHijackProbeLocalTUNTCPDNSFlow("192.168.1.1:53") {
		t.Fatal("tcp dns target should be hijacked")
	}
	if shouldHijackProbeLocalTUNTCPDNSFlow("192.168.1.1:853") {
		t.Fatal("dot target should not be hijacked as plain dns")
	}
}

func TestServeProbeLocalTUNUDPDNSHijackResolvesViaLocalDNS(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	storeProbeLocalDNSCacheRecords("cached.example", []string{"203.0.113.44"})
	query, err := buildProbeLocalDNSQueryA("cached.example")
	if err != nil {
		t.Fatalf("build dns query failed: %v", err)
	}
	conn := &fakeProbeLocalTUNPacketConn{
		packet: query,
		remote: &net.UDPAddr{
			IP:   net.ParseIP("198.18.0.2"),
			Port: 53124,
		},
	}

	serveProbeLocalTUNUDPDNSHijack(conn, "192.168.1.1:53")
	if len(conn.writes) != 1 {
		t.Fatalf("writes=%d want 1", len(conn.writes))
	}
	ips := extractProbeLocalTestDNSARecords(t, conn.writes[0])
	if len(ips) != 1 || ips[0] != "203.0.113.44" {
		t.Fatalf("dns response ips=%v", ips)
	}
}

func TestServeProbeLocalTUNTCPDNSHijackResolvesViaLocalDNS(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	storeProbeLocalDNSCacheRecords("tcp.cached.example", []string{"203.0.113.45"})
	query, err := buildProbeLocalDNSQueryA("tcp.cached.example")
	if err != nil {
		t.Fatalf("build dns query failed: %v", err)
	}
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveProbeLocalTUNTCPDNSHijack(server, "192.168.1.1:53")
	}()

	if err := client.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set client deadline failed: %v", err)
	}
	lengthBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(lengthBuf, uint16(len(query)))
	if _, err := client.Write(append(lengthBuf, query...)); err != nil {
		t.Fatalf("write tcp dns query failed: %v", err)
	}
	if _, err := io.ReadFull(client, lengthBuf); err != nil {
		t.Fatalf("read tcp dns response length failed: %v", err)
	}
	responseLen := int(binary.BigEndian.Uint16(lengthBuf))
	if responseLen <= 0 {
		t.Fatalf("invalid tcp dns response length=%d", responseLen)
	}
	response := make([]byte, responseLen)
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatalf("read tcp dns response failed: %v", err)
	}
	ips := extractProbeLocalTestDNSARecords(t, response)
	if len(ips) != 1 || ips[0] != "203.0.113.45" {
		t.Fatalf("dns response ips=%v", ips)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tcp dns hijack server did not exit")
	}
}

func extractProbeLocalTestDNSARecords(t *testing.T, packet []byte) []string {
	t.Helper()
	parser := dnsmessage.Parser{}
	if _, err := parser.Start(packet); err != nil {
		t.Fatalf("parse dns response failed: %v", err)
	}
	if err := parser.SkipAllQuestions(); err != nil {
		t.Fatalf("skip dns questions failed: %v", err)
	}
	var ips []string
	for {
		header, err := parser.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			return ips
		}
		if err != nil {
			t.Fatalf("parse dns answer header failed: %v", err)
		}
		if header.Type != dnsmessage.TypeA {
			if err := parser.SkipAnswer(); err != nil {
				t.Fatalf("skip dns answer failed: %v", err)
			}
			continue
		}
		answer, err := parser.AResource()
		if err != nil {
			t.Fatalf("parse dns a answer failed: %v", err)
		}
		ips = append(ips, net.IP(answer.A[:]).String())
	}
}

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

func TestOpenProbeLocalTUNOutboundTCPDirectConnects(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(resetProbeLocalDirectBypassStateForTest)
	if err := ensureProbeLocalProxyDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults failed: %v", err)
	}
	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()
	t.Cleanup(func() {
		probeLocalControl.mu.Lock()
		probeLocalControl.proxy.Enabled = false
		probeLocalControl.proxy.Mode = probeLocalProxyModeDirect
		probeLocalControl.mu.Unlock()
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
		close(accepted)
	}()

	// 出口接口未准备（测试环境无 TUN），绑定 Control 退化为 no-op，direct 拨号应仍能直连成功。
	conn, route, err := openProbeLocalTUNOutboundTCP(ln.Addr().String())
	if err != nil {
		t.Fatalf("open direct tcp failed: %v", err)
	}
	if !route.Direct {
		t.Fatalf("route=%+v", route)
	}
	if acceptedConn := <-accepted; acceptedConn != nil {
		_ = acceptedConn.Close()
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close direct tcp failed: %v", err)
	}
}

func TestProbeLocalTUNTCPDirectFailureCache(t *testing.T) {
	resetProbeLocalTUNTCPDirectFailureCacheForTest()
	t.Cleanup(resetProbeLocalTUNTCPDirectFailureCacheForTest)

	target := "162.159.61.4:443"
	if got := lookupProbeLocalTUNTCPDirectFailure(target); got != nil {
		t.Fatalf("empty cache lookup=%v", got)
	}
	if shouldCacheProbeLocalTUNTCPDirectFailure(errors.New("connection refused")) {
		t.Fatal("connection refused should not be cached")
	}
	rememberProbeLocalTUNTCPDirectFailure(target, errors.New("i/o timeout"))
	if got := lookupProbeLocalTUNTCPDirectFailure(target); got == nil {
		t.Fatal("expected cached timeout")
	} else if !strings.Contains(got.Error(), "162.159.61.4:443") || !strings.Contains(got.Error(), "i/o timeout") {
		t.Fatalf("unexpected cached error=%v", got)
	}
	stats := snapshotProbeLocalTUNTCPDirectFailureCacheStats()
	if stats.Active != 1 || stats.Hits != 1 || stats.Stored != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	clearProbeLocalTUNTCPDirectFailure(target)
	if got := lookupProbeLocalTUNTCPDirectFailure(target); got != nil {
		t.Fatalf("lookup after clear=%v", got)
	}
}

func TestBuildProbeLocalTUNIPv4FallbackRouteFromDNSHint(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDNSServiceForTest()
	t.Cleanup(resetProbeLocalDNSServiceForTest)

	groups := defaultProbeLocalProxyGroupFile()
	groups.Groups = []probeLocalProxyGroupEntry{
		{Group: "telegram", Rules: []string{"domain_suffix:telegram.org"}},
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		t.Fatalf("persist groups failed: %v", err)
	}
	state := defaultProbeLocalProxyStateFile()
	state.Groups = []probeLocalProxyStateGroupEntry{
		{Group: "telegram", Action: "direct"},
	}
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		t.Fatalf("persist state failed: %v", err)
	}

	decision := resolveProbeLocalProxyRouteDecisionByDomain("api.telegram.org")
	storeProbeLocalDNSRouteHints("api.telegram.org", []string{"2001:67c:4e8:f004::9", "149.154.167.220"}, decision)
	storeProbeLocalDNSCacheRecords("api.telegram.org", []string{"149.154.167.220"})

	route := probeLocalTunnelRouteDecision{
		Direct:     true,
		TargetAddr: "[2001:67c:4e8:f004::9]:443",
		Group:      "telegram",
	}
	fallback, ok := buildProbeLocalTUNIPv4FallbackRoute(route, errors.New("dial tcp [2001:67c:4e8:f004::9]:443: i/o timeout"))
	if !ok {
		t.Fatal("expected ipv4 fallback route")
	}
	if !fallback.Direct || fallback.Reject {
		t.Fatalf("fallback flags=%+v", fallback)
	}
	if fallback.TargetAddr != "149.154.167.220:443" {
		t.Fatalf("fallback target=%q", fallback.TargetAddr)
	}
	if fallback.Group != "telegram" {
		t.Fatalf("fallback group=%q", fallback.Group)
	}
}

func TestOpenProbeLocalTUNOutboundUDPDirectConnects(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(resetProbeLocalDirectBypassStateForTest)
	if err := ensureProbeLocalProxyDefaultsInitialized(); err != nil {
		t.Fatalf("ensure defaults failed: %v", err)
	}
	probeLocalControl.mu.Lock()
	probeLocalControl.proxy.Enabled = true
	probeLocalControl.proxy.Mode = probeLocalProxyModeTUN
	probeLocalControl.mu.Unlock()
	t.Cleanup(func() {
		probeLocalControl.mu.Lock()
		probeLocalControl.proxy.Enabled = false
		probeLocalControl.proxy.Mode = probeLocalProxyModeDirect
		probeLocalControl.mu.Unlock()
	})

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen udp failed: %v", err)
	}
	defer udpConn.Close()
	id := stack.TransportEndpointID{
		LocalAddress:  tcpip.AddrFrom4([4]byte{127, 0, 0, 1}),
		LocalPort:     uint16(udpConn.LocalAddr().(*net.UDPAddr).Port),
		RemoteAddress: tcpip.AddrFrom4([4]byte{10, 0, 0, 8}),
		RemotePort:    53000,
	}

	// 出口接口未准备（测试环境无 TUN），绑定 Control 退化为 no-op，direct UDP 拨号应仍能成功。
	outbound, route, err := openProbeLocalTUNOutboundUDP(id, udpConn.LocalAddr().String())
	if err != nil {
		t.Fatalf("open direct udp failed: %v", err)
	}
	if !route.Direct {
		t.Fatalf("route=%+v", route)
	}
	if err := outbound.Close(); err != nil {
		t.Fatalf("close direct udp failed: %v", err)
	}
}

func TestResolveProbeLocalTUNUDPAssociationTimeoutForQUIC(t *testing.T) {
	cases := []struct {
		target      string
		wantTTL     time.Duration
		wantProfile string
	}{
		{
			target:      "example.com:443",
			wantTTL:     probeLocalTUNUDPQUICAssociationTimeout,
			wantProfile: probeChainUDPAssociationTTLProfileQUICStable,
		},
		{
			target:      "example.com:8443",
			wantTTL:     probeLocalTUNUDPQUICAssociationTimeout,
			wantProfile: probeChainUDPAssociationTTLProfileQUICStable,
		},
		{
			target:      "example.com:53",
			wantTTL:     probeLocalTUNUDPAssociationTimeout,
			wantProfile: probeChainUDPAssociationTTLProfileDefault,
		},
	}
	for _, tc := range cases {
		if got := resolveProbeLocalTUNUDPAssociationTimeout(tc.target); got != tc.wantTTL {
			t.Fatalf("target=%s ttl=%s want=%s", tc.target, got, tc.wantTTL)
		}
		if got := resolveProbeLocalTUNUDPTTLProfile(tc.target); got != tc.wantProfile {
			t.Fatalf("target=%s profile=%s want=%s", tc.target, got, tc.wantProfile)
		}
	}
}

func TestResolveProbeLocalTUNUDPBridgeTimeoutForShortLivedTraffic(t *testing.T) {
	direct := probeLocalTunnelRouteDecision{Direct: true, TargetAddr: "192.168.1.20:7680", Group: "fallback"}
	if got := resolveProbeLocalTUNUDPBridgeTimeout("192.168.1.20:7680", direct); got != probeLocalTUNUDPShortAssociationTTL {
		t.Fatalf("delivery optimization ttl=%s want=%s", got, probeLocalTUNUDPShortAssociationTTL)
	}
	if got := resolveProbeLocalTUNUDPBridgeTimeout("192.168.1.20:443", direct); got != probeLocalTUNUDPShortAssociationTTL {
		t.Fatalf("private direct quic ttl=%s want=%s", got, probeLocalTUNUDPShortAssociationTTL)
	}
	tunnel := probeLocalTunnelRouteDecision{Direct: false, TargetAddr: "example.com:443", Group: "media"}
	if got := resolveProbeLocalTUNUDPBridgeTimeout("example.com:443", tunnel); got != probeLocalTUNUDPQUICAssociationTimeout {
		t.Fatalf("tunnel quic ttl=%s want=%s", got, probeLocalTUNUDPQUICAssociationTimeout)
	}
}

func TestProbeLocalTUNUDPManagedOutboundReleaseSourceOnce(t *testing.T) {
	released := 0
	outbound := &probeLocalTUNUDPManagedOutbound{
		ReadWriteCloser: nopProbeLocalTUNUDPReadWriteCloser{},
		releaseSource: func() {
			released++
		},
	}
	if err := outbound.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if err := outbound.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
	if released != 1 {
		t.Fatalf("released=%d want=1", released)
	}
}

func TestProbeLocalTUNUDPManagedOutboundForwardsReadDeadline(t *testing.T) {
	inner := &deadlineProbeLocalTUNUDPReadWriteCloser{}
	outbound := &probeLocalTUNUDPManagedOutbound{ReadWriteCloser: inner}
	deadline := time.Now().Add(time.Second)
	if err := outbound.SetReadDeadline(deadline); err != nil {
		t.Fatalf("set deadline failed: %v", err)
	}
	if !inner.deadline.Equal(deadline) {
		t.Fatalf("deadline=%s want=%s", inner.deadline, deadline)
	}
}

func TestProbeLocalTUNUDPBridgeNoResponseTimeout(t *testing.T) {
	bridge := &probeLocalTUNUDPBridge{
		route:   probeLocalTunnelRouteDecision{Direct: false, Group: "media"},
		monitor: &probeLocalTUNUDPBridgeMonitorItemState{},
	}
	if bridge.shouldUseNoResponseTimeout() {
		t.Fatal("empty bridge should not use no-response timeout")
	}
	if got := bridge.currentReadTimeout(); got != 0 {
		t.Fatalf("empty bridge timeout=%s want 0", got)
	}
	bridge.monitor.bytesUp.Store(128)
	if !bridge.shouldUseNoResponseTimeout() {
		t.Fatal("tunnel with only upstream bytes should use no-response timeout")
	}
	if got := bridge.currentReadTimeout(); got != probeLocalTUNUDPNoResponseTunnelTTL {
		t.Fatalf("tunnel no-response timeout=%s want=%s", got, probeLocalTUNUDPNoResponseTunnelTTL)
	}
	bridge.monitor.bytesDown.Store(64)
	if bridge.shouldUseNoResponseTimeout() {
		t.Fatal("tunnel with downstream bytes should use normal timeout")
	}
	bridge.route.Direct = true
	bridge.monitor.bytesDown.Store(0)
	if !bridge.shouldUseNoResponseTimeout() {
		t.Fatal("direct bridge with only upstream bytes should use no-response timeout")
	}
	if got := bridge.currentReadTimeout(); got != probeLocalTUNUDPNoResponseDirectTTL {
		t.Fatalf("direct no-response timeout=%s want=%s", got, probeLocalTUNUDPNoResponseDirectTTL)
	}
}

func TestShouldDropProbeLocalTUNUDPFlowForDiscoveryTraffic(t *testing.T) {
	cases := []string{
		"224.0.0.251:5353",
		"239.255.255.250:1900",
		"255.255.255.255:1900",
		"198.19.255.255:137",
	}
	for _, target := range cases {
		if !shouldDropProbeLocalTUNUDPFlow(target) {
			t.Fatalf("target=%s should be dropped", target)
		}
	}
	if shouldDropProbeLocalTUNUDPFlow("8.8.8.8:53") {
		t.Fatal("public dns udp should not be dropped")
	}
}

func TestShouldDropProbeLocalTUNTCPFlowForDeliveryOptimization(t *testing.T) {
	cases := []string{
		"172.18.52.141:7680",
		"192.168.10.28:7680",
	}
	for _, target := range cases {
		if !shouldDropProbeLocalTUNTCPFlow(target) {
			t.Fatalf("target=%s should be dropped", target)
		}
	}
	if shouldDropProbeLocalTUNTCPFlow("8.8.8.8:7680") {
		t.Fatal("public tcp 7680 should not be dropped")
	}
	if shouldDropProbeLocalTUNTCPFlow("192.168.10.28:443") {
		t.Fatal("private tcp non-7680 should not be dropped")
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
		{err: syscall.Errno(10013), want: true},
		{err: syscall.Errno(10048), want: true},
		{err: syscall.Errno(10049), want: true},
		{err: errors.New("bind: An attempt was made to access a socket in a way forbidden by its access permissions."), want: true},
		{err: errors.New("wsaeacces"), want: true},
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

func TestShouldReportProbeLocalTUNUDPFailureSuppressesRepeats(t *testing.T) {
	resetProbeLocalDirectBypassStateForTest()
	t.Cleanup(resetProbeLocalDirectBypassStateForTest)

	route := probeLocalTunnelRouteDecision{
		Direct:     true,
		TargetAddr: "198.41.192.167:7844",
		Group:      "fallback",
	}
	err := errors.New("bind: An attempt was made to access a socket in a way forbidden by its access permissions.")
	if !shouldReportProbeLocalTUNUDPFailure("open_failed", "198.41.192.167:7844", route, err) {
		t.Fatal("first udp failure should be reported")
	}
	if shouldReportProbeLocalTUNUDPFailure("open_failed", "198.41.192.167:7844", route, err) {
		t.Fatal("repeat udp failure should be suppressed")
	}
	if !shouldReportProbeLocalTUNUDPFailure("open_failed", "198.41.192.7:7844", route, err) {
		t.Fatal("different target should be reported")
	}
}

func TestBuildProbeLocalTUNUDPAssociationFallbackNATMode(t *testing.T) {
	id := stack.TransportEndpointID{
		LocalAddress:  tcpip.AddrFrom4([4]byte{198, 41, 192, 167}),
		LocalPort:     7844,
		RemoteAddress: tcpip.AddrFrom4([4]byte{198, 18, 0, 2}),
		RemotePort:    52849,
	}
	route := probeLocalTunnelRouteDecision{
		TargetAddr:      "198.41.192.167:7844",
		Group:           "fallback",
		SelectedChainID: "5",
	}
	assoc := buildProbeLocalTUNUDPAssociation(id, "198.41.192.167:7844", route, "198.18.0.2:52849", 1, probeLocalTUNUDPNATModeFallbackEphemeral)
	if assoc.NATMode != probeLocalTUNUDPNATModeFallbackEphemeral {
		t.Fatalf("NATMode=%q", assoc.NATMode)
	}
	if assoc.SourceKey != "198.18.0.2:52849" || assoc.SourceRefs != 1 {
		t.Fatalf("source key/refs=%q/%d", assoc.SourceKey, assoc.SourceRefs)
	}
	if assoc.AssocKeyV2 != "198.41.192.167:7844|198.18.0.2:52849->198.41.192.167:7844" {
		t.Fatalf("assoc key=%q", assoc.AssocKeyV2)
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
