package main

import (
	"io"
	"net"
	"net/netip"
	"os"
	"reflect"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
)

func prepareGatewayTest(t *testing.T) {
	t.Helper()
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	cleanup := func() {
		probeGatewayState.Lock()
		for c := range probeGatewayState.connections {
			_ = c.Close()
		}
		r := probeGatewayState.runner
		probeGatewayState.runner = nil
		probeGatewayState.loaded = false
		probeGatewayState.config = probeGatewayProxyConfig{}
		probeGatewayState.connections = nil
		probeGatewayState.epoch++
		probeGatewayState.Unlock()
		if r != nil {
			_ = r.Close()
		}
	}
	cleanup()
	t.Cleanup(cleanup)
}

func TestProbeGatewayPersistenceValidationAndACL(t *testing.T) {
	prepareGatewayTest(t)
	c := probeGatewayProxyConfig{true, []string{"192.168.50.7/24", "10.20.0.0/16", "192.168.50.0/24"}, []string{"9", "9", "14"}}
	if err := saveProbeGatewayConfig(c); err != nil {
		t.Fatal(err)
	}
	expected := currentProbeGatewayConfig()
	if len(expected.ProxyCIDRs) != 2 || len(expected.AllowedNodeIDs) != 2 {
		t.Fatal(expected)
	}
	path, _ := probeGatewayConfigPath()
	before, _ := os.ReadFile(path)
	stat, _ := os.Stat(path)
	if err := saveProbeGatewayConfig(*expected); err != nil {
		t.Fatal(err)
	}
	again, _ := os.Stat(path)
	if !os.SameFile(stat, again) {
		t.Fatal("unchanged config rewrote flash file")
	}
	for _, bad := range []string{"0.0.0.0/0", "172.16.0.0/8", "192.168.0.0/8", "198.18.0.0/15", "127.0.0.1/32", "::1/128"} {
		invalid := c
		invalid.ProxyCIDRs = []string{bad}
		if saveProbeGatewayConfig(invalid) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid config changed disk")
	}
	if err := loadProbeGatewayConfig(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expected, currentProbeGatewayConfig()) {
		t.Fatal("restart lost configuration")
	}
	config := probeVirtualRouterConfig{ProbeIPs: []probeVirtualRouterProbeIP{{NodeID: "9", IP: "198.18.0.9"}, {NodeID: "14", IP: "198.18.0.14"}}}
	packet := buildProbeVirtualRouterTestUDPPacket(t, "198.18.0.9", "192.168.50.1", 45000, 53)
	if !probeGatewayPacketAllowed(*expected, packet, []string{"9", "22", "23"}, "23", config) {
		t.Fatal("authorized relay path denied")
	}
	for _, path := range [][]string{{"14", "23"}, {"8", "23"}, {"9", "22"}, nil} {
		if probeGatewayPacketAllowed(*expected, packet, path, "23", config) {
			t.Fatalf("unauthorized path accepted: %v", path)
		}
	}
	expected.Enabled = false
	if err := saveProbeGatewayConfig(*expected); err != nil {
		t.Fatal(err)
	}
	if err := loadProbeGatewayConfig(); err != nil {
		t.Fatal(err)
	}
	if currentProbeGatewayConfig().Enabled {
		t.Fatal("revocation did not survive restart")
	}
}

func privateGatewayTestIP(t *testing.T) net.IP {
	t.Helper()
	ifs, _ := net.Interfaces()
	for _, iface := range ifs {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		as, _ := iface.Addrs()
		for _, a := range as {
			p, e := netip.ParsePrefix(a.String())
			if e == nil && p.Addr().Is4() && p.Addr().IsPrivate() {
				return net.ParseIP(p.Addr().String())
			}
		}
	}
	t.Skip("no private IPv4 interface for physical gateway round trip")
	return nil
}

func TestProbeGatewayPhysicalTCPUDPAndRevocation(t *testing.T) {
	prepareGatewayTest(t)
	ip := privateGatewayTestIP(t)
	if err := saveProbeGatewayConfig(probeGatewayProxyConfig{true, []string{ip.String() + "/32"}, []string{"9"}}); err != nil {
		t.Fatal(err)
	}
	server, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	observed := make(chan string, 1)
	go func() {
		c, e := server.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		observed <- c.RemoteAddr().(*net.TCPAddr).IP.String()
		_, _ = io.Copy(c, c)
	}()
	inbound, client := net.Pipe()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	go relayProbeGatewayTCP(inbound, tcpip.AddrFrom4Slice(ip.To4()), uint16(server.Addr().(*net.TCPAddr).Port))
	if _, err := client.Write([]byte("gateway-roundtrip")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len("gateway-roundtrip"))
	if _, err := io.ReadFull(client, reply); err != nil || string(reply) != "gateway-roundtrip" {
		t.Fatalf("reply %q: %v", reply, err)
	}
	select {
	case src := <-observed:
		if src != ip.String() {
			t.Fatalf("LAN target saw source %s", src)
		}
	case <-time.After(time.Second):
		t.Fatal("LAN target did not accept physical connection")
	}
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	go func() {
		b := make([]byte, 64)
		n, a, e := udp.ReadFromUDP(b)
		if e == nil {
			_, _ = udp.WriteToUDP(b[:n], a)
		}
	}()
	out, err := probeGatewayDial(tcpip.AddrFrom4Slice(ip.To4()), uint16(udp.LocalAddr().(*net.UDPAddr).Port), "udp4")
	if err != nil {
		t.Fatal(err)
	}
	defer closeProbeGatewayConn(out)
	_ = out.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = out.Write([]byte("udp"))
	b := make([]byte, 8)
	n, err := out.Read(b)
	if err != nil || string(b[:n]) != "udp" {
		t.Fatalf("UDP reply %q: %v", b[:n], err)
	}
	if err := saveProbeGatewayConfig(probeGatewayProxyConfig{}); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("revoked")); err == nil {
		t.Fatal("revocation kept physical UDP socket open")
	}
	if _, err := io.ReadFull(client, reply[:1]); err == nil {
		t.Fatal("revocation kept TCP session open")
	}
}
