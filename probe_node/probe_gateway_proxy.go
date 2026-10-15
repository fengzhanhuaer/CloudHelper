package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
)

// Remote LAN access is independent of the side router's LAN ingress proxy.
type probeGatewayProxyConfig struct {
	Enabled        bool     `json:"enabled"`
	ProxyCIDRs     []string `json:"proxy_cidrs"`
	AllowedNodeIDs []string `json:"allowed_node_ids"`
}

var probeGatewayState = struct {
	sync.Mutex
	config      probeGatewayProxyConfig
	loaded      bool
	runner      *probeVirtualRouterExitNetstack
	connections map[net.Conn]struct{}
	epoch       uint64
}{}

var probeGatewayLegacyConfig = func() (probeGatewayProxyConfig, error) { return probeGatewayProxyConfig{}, nil }

func normalizeProbeGatewayConfig(c probeGatewayProxyConfig) (probeGatewayProxyConfig, error) {
	if len(c.ProxyCIDRs) > 32 || len(c.AllowedNodeIDs) > 256 {
		return c, errors.New("最多32个网段、256个允许节点")
	}
	prefixes := []string{}
	for _, raw := range c.ProxyCIDRs {
		p, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || !p.Addr().Is4() {
			return c, fmt.Errorf("无效IPv4网段：%s", raw)
		}
		p = p.Masked()
		private := false
		for _, root := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
			r := netip.MustParsePrefix(root)
			private = private || (p.Bits() >= r.Bits() && r.Contains(p.Addr()))
		}
		if !private {
			return c, fmt.Errorf("必须是完整包含在私有地址范围的网段：%s", raw)
		}
		prefixes = append(prefixes, p.String())
	}
	ids := []string{}
	for _, raw := range c.AllowedNodeIDs {
		id := normalizeProbeRouteNodeID(raw)
		if id == "" || len(id) > 128 || strings.ContainsAny(id, " \t\r\n") {
			return c, errors.New("无效节点ID")
		}
		ids = append(ids, id)
	}
	slices.Sort(prefixes)
	slices.Sort(ids)
	c.ProxyCIDRs = slices.Compact(prefixes)
	c.AllowedNodeIDs = slices.Compact(ids)
	if c.Enabled && (len(c.ProxyCIDRs) == 0 || len(c.AllowedNodeIDs) == 0) {
		return c, errors.New("启用时必须填写代理网段和允许节点")
	}
	return c, nil
}

func writeProbeGatewayAtomic(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil && string(old) == string(raw) {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".gateway-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	return err
}

func probeGatewayConfigPath() (string, error) {
	dir, err := resolveDataDir()
	return filepath.Join(dir, "gateway_proxy.json"), err
}

func loadProbeGatewayConfig() error {
	probeGatewayState.Lock()
	probeGatewayState.loaded = true
	probeGatewayState.config = probeGatewayProxyConfig{}
	probeGatewayState.Unlock()
	path, err := probeGatewayConfigPath()
	if err != nil {
		return err
	}
	var c probeGatewayProxyConfig
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		c, err = probeGatewayLegacyConfig()
	} else if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	if err != nil {
		return err
	}
	return saveProbeGatewayConfig(c)
}

func saveProbeGatewayConfig(c probeGatewayProxyConfig) error {
	c, err := normalizeProbeGatewayConfig(c)
	if err != nil {
		return err
	}
	path, err := probeGatewayConfigPath()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	probeGatewayState.Lock()
	defer probeGatewayState.Unlock()
	if err = writeProbeGatewayAtomic(path, append(raw, '\n')); err != nil {
		return err
	}
	if !reflect.DeepEqual(c, probeGatewayState.config) {
		probeGatewayState.epoch++
		for conn := range probeGatewayState.connections {
			_ = conn.Close()
		}
		probeGatewayState.connections = nil
		if probeGatewayState.runner != nil {
			_ = probeGatewayState.runner.Close()
			probeGatewayState.runner = nil
		}
	}
	probeGatewayState.config = c
	probeGatewayState.loaded = true
	return nil
}

func currentProbeGatewayConfig() *probeGatewayProxyConfig {
	probeGatewayState.Lock()
	defer probeGatewayState.Unlock()
	if !probeGatewayState.loaded {
		return nil
	}
	c := probeGatewayState.config
	c.ProxyCIDRs = slices.Clone(c.ProxyCIDRs)
	c.AllowedNodeIDs = slices.Clone(c.AllowedNodeIDs)
	return &c
}

func probeGatewayContains(c probeGatewayProxyConfig, dst string) bool {
	a, err := netip.ParseAddr(dst)
	if err != nil {
		return false
	}
	for _, raw := range c.ProxyCIDRs {
		p, err := netip.ParsePrefix(raw)
		if err == nil && p.Contains(a) {
			return true
		}
	}
	return false
}

func probeGatewayPacketAllowed(c probeGatewayProxyConfig, packet []byte, path []string, local string, config probeVirtualRouterConfig) bool {
	path = cleanProbeVirtualRouterPath(path)
	if !c.Enabled || len(path) < 2 || path[len(path)-1] != local || !probeGatewayContains(c, probeVirtualRouterIPv4Destination(packet)) || !slices.Contains(c.AllowedNodeIDs, path[0]) {
		return false
	}
	src := probeVirtualRouterIPv4Source(packet)
	for _, node := range config.ProbeIPs {
		if normalizeProbeRouteNodeID(node.NodeID) == path[0] && node.IP == src {
			return true
		}
	}
	return false
}

// Claim private destinations at the final hop even when disabled, so legacy
// product LAN delivery cannot bypass the new endpoint authorization check.
func handleProbeGatewayPacket(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, packet []byte, path []string) bool {
	dst := probeVirtualRouterIPv4Destination(packet)
	a, err := netip.ParseAddr(dst)
	local := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if err != nil || !a.Is4() || !a.IsPrivate() || !probeVirtualRouterFrameTargetsLocalPathEnd(path, local) || probeVirtualRouterPacketTargetsLocalIP(runtime, dst) {
		return false
	}
	config := currentProbeVirtualRouterConfig()
	probeGatewayState.Lock()
	defer probeGatewayState.Unlock()
	if !probeGatewayState.loaded {
		return false
	}
	if !probeGatewayPacketAllowed(probeGatewayState.config, packet, path, local, config) {
		return true
	}
	if len(packet) > 9 && packet[9] == 1 {
		entry := probeVirtualRouterFakeIPEntry{Domain: dst, FakeIP: dst, Action: "gateway_proxy", ExitNodeID: local}
		handleProbeVirtualRouterFakeIPExitICMPEchoRequest(runtime, link, packet, path, entry)
		return true
	}
	if probeGatewayState.runner == nil {
		runner, err := newProbeVirtualRouterExitNetstack()
		if err != nil {
			return true
		}
		runner.gateway = true
		probeGatewayState.runner = runner
	}
	_ = probeGatewayState.runner.Inject(packet)
	return true
}

func probeGatewayDial(addr tcpip.Address, port uint16, network string) (net.Conn, error) {
	probeGatewayState.Lock()
	c := probeGatewayState.config
	epoch := probeGatewayState.epoch
	allowed := probeGatewayState.loaded && c.Enabled && probeGatewayContains(c, addr.String()) && port != 0 && len(probeGatewayState.connections) < 256
	probeGatewayState.Unlock()
	if !allowed {
		return nil, errors.New("gateway target is disabled")
	}
	// Let the physical OS route select the LAN interface; never use the Internet
	// exit/Mihomo dialer, which may send the request back into the virtual router.
	conn, err := (&net.Dialer{Timeout: 12 * time.Second}).Dial(network, net.JoinHostPort(addr.String(), fmt.Sprint(port)))
	if err != nil {
		return nil, err
	}
	probeGatewayState.Lock()
	defer probeGatewayState.Unlock()
	if epoch != probeGatewayState.epoch || len(probeGatewayState.connections) >= 256 {
		_ = conn.Close()
		return nil, errors.New("gateway configuration changed")
	}
	if probeGatewayState.connections == nil {
		probeGatewayState.connections = make(map[net.Conn]struct{})
	}
	probeGatewayState.connections[conn] = struct{}{}
	return conn, nil
}

func closeProbeGatewayConn(conn net.Conn) {
	_ = conn.Close()
	probeGatewayState.Lock()
	delete(probeGatewayState.connections, conn)
	probeGatewayState.Unlock()
}

func relayProbeGatewayTCP(inbound net.Conn, addr tcpip.Address, port uint16) {
	defer inbound.Close()
	out, err := probeGatewayDial(addr, port, "tcp4")
	if err != nil {
		return
	}
	defer closeProbeGatewayConn(out)
	if !trackProbeGatewayInbound(inbound, out) {
		return
	}
	defer closeProbeGatewayConn(inbound)
	relayProbeVirtualRouterExitTCPConns(inbound, out)
}
func relayProbeGatewayUDP(inbound *gonet.UDPConn, addr tcpip.Address, port uint16) {
	defer inbound.Close()
	out, err := probeGatewayDial(addr, port, "udp4")
	if err != nil {
		return
	}
	defer closeProbeGatewayConn(out)
	if !trackProbeGatewayInbound(inbound, out) {
		return
	}
	defer closeProbeGatewayConn(inbound)
	relayProbeVirtualRouterExitUDPConns(inbound, out)
}

func isProbeGatewayPublishedRuleID(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	return strings.HasPrefix(id, "linux-router-") || strings.HasPrefix(id, "gateway-proxy-")
}

func trackProbeGatewayInbound(inbound, outbound net.Conn) bool {
	probeGatewayState.Lock()
	defer probeGatewayState.Unlock()
	if _, ok := probeGatewayState.connections[outbound]; !ok {
		return false
	}
	probeGatewayState.connections[inbound] = struct{}{}
	return true
}
