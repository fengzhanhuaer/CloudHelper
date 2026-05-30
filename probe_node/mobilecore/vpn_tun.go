package mobilecore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	vpnNICID            = tcpip.NICID(1)
	vpnQueueSize        = 4096
	vpnMTU              = 1500
	vpnTCPWindow        = 1024
	vpnTCPInFlight      = 512
	vpnRelayIdle        = 5 * time.Minute
	vpnUDPRelayTimeout  = 30 * time.Second
	vpnDNSCacheTTL      = 10 * time.Minute
	vpnDNSReadTimeout   = 20 * time.Second
	vpnDNSLookupTimeout = 5 * time.Second
)

var (
	vpnIPv4Address = tcpip.AddrFrom4([4]byte{10, 111, 0, 2})
	vpnIPv6Address = tcpip.AddrFrom16([16]byte{0xfd, 0x00, 0x01, 0x11, 0x01, 0x11, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2})
)

var vpnRuntime = &androidVPNRuntime{}
var vpnDNSState = &androidVPNDNSState{
	nextFakeOffset: 2,
	fakeDomainToIP: map[string]string{},
	fakeIPToEntry:  map[string]androidVPNDNSFakeEntry{},
	routeIPHints:   map[string]androidVPNDNSRouteHintEntry{},
}

type androidVPNRuntime struct {
	mu        sync.Mutex
	configDir string
	tun       *os.File
	stack     *androidVPNNetstack
	status    string
	lastError string
	updatedAt string
	selfCheck map[string]any
}

type androidVPNNetstack struct {
	stack     *stack.Stack
	linkEP    *channel.Endpoint
	tun       *os.File
	cancel    context.CancelFunc
	doneCh    chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
}

type vpnRouteDecision struct {
	Direct          bool
	Reject          bool
	TargetAddr      string
	Group           string
	SelectedChainID string
}

type androidVPNDNSState struct {
	mu             sync.Mutex
	nextFakeOffset uint32
	fakeDomainToIP map[string]string
	fakeIPToEntry  map[string]androidVPNDNSFakeEntry
	routeIPHints   map[string]androidVPNDNSRouteHintEntry
}

type androidVPNDNSFakeEntry struct {
	Domain    string
	Group     string
	ExpiresAt time.Time
}

type androidVPNDNSRouteHintEntry struct {
	Domain    string
	IP        string
	Group     string
	ExpiresAt time.Time
}

type vpnTunnelUDPConn struct {
	stream net.Conn
	reader *bufio.Reader

	readMu    sync.Mutex
	writeMu   sync.Mutex
	closeOnce sync.Once
}

// VpnStart attaches a VpnService TUN fd to the mobilecore data plane.
func VpnStart(fd int64, configDir string) string {
	if fd < 0 {
		return "vpn start failed: invalid tun fd"
	}
	if strings.TrimSpace(configDir) == "" {
		_ = os.NewFile(uintptr(fd), "cloudhelper-vpn-tun").Close()
		return "vpn start failed: config dir is required"
	}
	tun := os.NewFile(uintptr(fd), "cloudhelper-vpn-tun")
	if tun == nil {
		return "vpn start failed: open tun fd failed"
	}
	netstack, err := newAndroidVPNNetstack(tun)
	if err != nil {
		_ = tun.Close()
		return "vpn start failed: " + err.Error()
	}
	vpnRuntime.mu.Lock()
	oldStack := vpnRuntime.stack
	oldTun := vpnRuntime.tun
	vpnRuntime.configDir = strings.TrimSpace(configDir)
	vpnRuntime.tun = tun
	vpnRuntime.stack = netstack
	vpnRuntime.status = "running"
	vpnRuntime.lastError = ""
	vpnRuntime.updatedAt = time.Now().UTC().Format(time.RFC3339)
	vpnRuntime.selfCheck = map[string]any{"ok": false, "status": "pending", "updated_at": vpnRuntime.updatedAt}
	vpnRuntime.mu.Unlock()
	proxyRuntime.mu.Lock()
	proxyRuntime.configDir = strings.TrimSpace(configDir)
	proxyRuntime.mu.Unlock()
	if oldStack != nil {
		_ = oldStack.Close()
	}
	if oldTun != nil {
		_ = oldTun.Close()
	}
	go runVPNStartupSelfCheck(strings.TrimSpace(configDir))
	return "vpn running"
}

func VpnStop() string {
	vpnRuntime.mu.Lock()
	netstack := vpnRuntime.stack
	tun := vpnRuntime.tun
	vpnRuntime.stack = nil
	vpnRuntime.tun = nil
	vpnRuntime.status = "stopped"
	vpnRuntime.updatedAt = time.Now().UTC().Format(time.RFC3339)
	vpnRuntime.selfCheck = map[string]any{"ok": false, "status": "stopped", "updated_at": vpnRuntime.updatedAt}
	vpnRuntime.mu.Unlock()
	if netstack != nil {
		_ = netstack.Close()
	}
	if tun != nil {
		_ = tun.Close()
	}
	return "vpn stopped"
}

func VpnStatus() string {
	vpnRuntime.mu.Lock()
	selfCheck := cloneVPNMap(vpnRuntime.selfCheck)
	running := vpnRuntime.stack != nil
	status := firstNonEmptyString(vpnRuntime.status, "stopped")
	lastError := vpnRuntime.lastError
	updatedAt := vpnRuntime.updatedAt
	defer vpnRuntime.mu.Unlock()
	dnsStatus := snapshotAndroidVPNDNSStatus()
	return marshalLinkJSON(map[string]any{
		"ok":         true,
		"running":    running,
		"status":     status,
		"last_error": lastError,
		"updated_at": updatedAt,
		"dns":        dnsStatus,
		"self_check": selfCheck,
	})
}

func VpnSelfCheck(configDir string) string {
	result := runAndroidVPNSelfCheck(strings.TrimSpace(configDir))
	setVPNSelfCheckResult(result)
	return marshalLinkJSON(result)
}

func runVPNStartupSelfCheck(configDir string) {
	result := runAndroidVPNSelfCheck(configDir)
	setVPNSelfCheckResult(result)
	level := "info"
	if ok, _ := result["ok"].(bool); !ok {
		level = "warn"
	}
	androidLogStore.add("vpn", level, "startup self-check: "+firstNonEmptyString(stringFromAny(result["status"]), stringFromAny(result["error"]), "unknown"))
}

func runAndroidVPNSelfCheck(configDir string) map[string]any {
	startedAt := time.Now().UTC()
	result := map[string]any{
		"ok":         false,
		"status":     "running",
		"target":     "www.google.com:443",
		"started_at": startedAt.Format(time.RFC3339),
	}
	if strings.TrimSpace(configDir) == "" {
		vpnRuntime.mu.Lock()
		configDir = vpnRuntime.configDir
		vpnRuntime.mu.Unlock()
	}
	if strings.TrimSpace(configDir) == "" {
		result["status"] = "config_missing"
		result["error"] = "config dir is empty"
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	vpnRuntime.mu.Lock()
	vpnRuntime.configDir = strings.TrimSpace(configDir)
	vpnRuntime.mu.Unlock()
	proxyRuntime.mu.Lock()
	proxyRuntime.configDir = strings.TrimSpace(configDir)
	proxyRuntime.mu.Unlock()
	query, err := buildAndroidVPNDNSQuery("www.google.com", dnsmessage.TypeA)
	if err != nil {
		result["status"] = "dns_query_build_failed"
		result["error"] = err.Error()
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	response, err := resolveAndroidVPNDNSPacket(query)
	if err != nil {
		result["status"] = "dns_failed"
		result["error"] = err.Error()
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	ips := extractAndroidVPNDNSResponseIPs(response)
	result["dns_ips"] = ips
	if len(ips) == 0 {
		result["status"] = "dns_empty"
		result["error"] = "dns response has no A record"
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	route, err := decideAndroidProxyRouteForTarget(configDir, net.JoinHostPort(ips[0], "443"))
	if err != nil {
		result["status"] = "route_failed"
		result["error"] = err.Error()
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	result["route"] = map[string]any{
		"group":             route.Group,
		"direct":            route.Direct,
		"reject":            route.Reject,
		"target":            route.TargetAddr,
		"selected_chain_id": route.SelectedChainID,
	}
	if route.Reject {
		result["status"] = "route_rejected"
		result["error"] = "self-check target is rejected"
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	if route.Direct {
		result["ok"] = true
		result["status"] = "direct_ready"
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	if strings.TrimSpace(route.SelectedChainID) == "" {
		result["status"] = "chain_missing"
		result["error"] = "tunnel route missing selected_chain_id"
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	conn, err := openAndroidProxyChainStream(route.SelectedChainID, "tcp", route.TargetAddr)
	if err != nil {
		result["status"] = "chain_open_failed"
		result["error"] = err.Error()
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	_ = conn.Close()
	result["ok"] = true
	result["status"] = "tunnel_ready"
	result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
	result["duration_ms"] = time.Since(startedAt).Milliseconds()
	return result
}

func setVPNSelfCheckResult(result map[string]any) {
	if result == nil {
		return
	}
	if _, ok := result["duration_ms"]; !ok {
		if startedAt, ok := parseRFC3339Time(stringFromAny(result["started_at"])); ok {
			result["duration_ms"] = time.Since(startedAt).Milliseconds()
		}
	}
	vpnRuntime.mu.Lock()
	vpnRuntime.selfCheck = cloneVPNMap(result)
	if ok, _ := result["ok"].(bool); !ok {
		if errText := strings.TrimSpace(stringFromAny(result["error"])); errText != "" {
			vpnRuntime.lastError = "self_check: " + errText
		}
	}
	vpnRuntime.updatedAt = time.Now().UTC().Format(time.RFC3339)
	vpnRuntime.mu.Unlock()
}

func newAndroidVPNNetstack(tun *os.File) (*androidVPNNetstack, error) {
	gStack := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	linkEP := channel.New(vpnQueueSize, vpnMTU, "")
	if err := tcpipErrToError(gStack.CreateNIC(vpnNICID, linkEP)); err != nil {
		gStack.Destroy()
		return nil, err
	}
	if err := tcpipErrToError(gStack.SetPromiscuousMode(vpnNICID, true)); err != nil {
		gStack.Destroy()
		return nil, err
	}
	if err := tcpipErrToError(gStack.SetSpoofing(vpnNICID, true)); err != nil {
		gStack.Destroy()
		return nil, err
	}
	if err := tcpipErrToError(gStack.AddProtocolAddress(vpnNICID, tcpip.ProtocolAddress{
		Protocol: ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{
			Address:   vpnIPv4Address,
			PrefixLen: 32,
		},
	}, stack.AddressProperties{})); err != nil {
		gStack.Destroy()
		return nil, err
	}
	if err := tcpipErrToError(gStack.AddProtocolAddress(vpnNICID, tcpip.ProtocolAddress{
		Protocol: ipv6.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{
			Address:   vpnIPv6Address,
			PrefixLen: 128,
		},
	}, stack.AddressProperties{})); err != nil {
		gStack.Destroy()
		return nil, err
	}
	gStack.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: vpnNICID},
		{Destination: header.IPv6EmptySubnet, NIC: vpnNICID},
	})
	ctx, cancel := context.WithCancel(context.Background())
	runner := &androidVPNNetstack{
		stack:  gStack,
		linkEP: linkEP,
		tun:    tun,
		cancel: cancel,
		doneCh: make(chan struct{}),
	}
	tcpForwarder := tcp.NewForwarder(gStack, vpnTCPWindow, vpnTCPInFlight, runner.handleTCPForwarder)
	udpForwarder := udp.NewForwarder(gStack, runner.handleUDPForwarder)
	gStack.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)
	gStack.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder.HandlePacket)
	go runner.inputLoop(ctx)
	go runner.outputLoop(ctx)
	return runner, nil
}

func (n *androidVPNNetstack) inputLoop(ctx context.Context) {
	defer close(n.doneCh)
	buf := make([]byte, vpnMTU+128)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		readN, err := n.tun.Read(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, os.ErrClosed) {
				return
			}
			recordVPNRuntimeError("tun_read", err)
			continue
		}
		if readN <= 0 {
			continue
		}
		_, _ = n.Write(buf[:readN])
	}
}

func (n *androidVPNNetstack) outputLoop(ctx context.Context) {
	for {
		packet := n.linkEP.ReadContext(ctx)
		if packet == nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		view := packet.ToView()
		payload := append([]byte(nil), view.AsSlice()...)
		view.Release()
		packet.DecRef()
		if len(payload) == 0 {
			continue
		}
		if _, err := n.tun.Write(payload); err != nil {
			if ctx.Err() != nil || errors.Is(err, os.ErrClosed) {
				return
			}
			recordVPNRuntimeError("tun_write", err)
		}
	}
}

func (n *androidVPNNetstack) Write(packet []byte) (int, error) {
	if len(packet) == 0 {
		return 0, nil
	}
	if n == nil || n.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	protocol, err := vpnProtocolFromPacket(packet)
	if err != nil {
		return 0, err
	}
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(append([]byte(nil), packet...)),
	})
	defer pkt.DecRef()
	n.linkEP.InjectInbound(protocol, pkt)
	return len(packet), nil
}

func (n *androidVPNNetstack) Close() error {
	if n == nil {
		return nil
	}
	n.closeOnce.Do(func() {
		n.closed.Store(true)
		if n.cancel != nil {
			n.cancel()
		}
		if n.linkEP != nil {
			n.linkEP.Close()
		}
		select {
		case <-n.doneCh:
		case <-time.After(2 * time.Second):
		}
		if n.stack != nil {
			n.stack.Destroy()
		}
	})
	return nil
}

func (n *androidVPNNetstack) handleTCPForwarder(req *tcp.ForwarderRequest) {
	if req == nil {
		return
	}
	id := req.ID()
	targetAddr, err := vpnTransportIDToTarget(id.LocalAddress, id.LocalPort)
	if err != nil {
		req.Complete(true)
		return
	}
	var wq waiter.Queue
	ep, createErr := req.CreateEndpoint(&wq)
	if createErr != nil {
		req.Complete(true)
		return
	}
	req.Complete(false)
	inbound := gonet.NewTCPConn(&wq, ep)
	outbound, err := openVPNOutboundTCP(targetAddr)
	if err != nil {
		recordVPNRuntimeError("tcp_open "+targetAddr, err)
		_ = inbound.Close()
		return
	}
	go pipeVPNConn(outbound, inbound)
	go pipeVPNConn(inbound, outbound)
}

func (n *androidVPNNetstack) handleUDPForwarder(req *udp.ForwarderRequest) {
	if req == nil {
		return
	}
	id := req.ID()
	targetAddr, err := vpnTransportIDToTarget(id.LocalAddress, id.LocalPort)
	if err != nil {
		return
	}
	if isAndroidVPNDNSTarget(targetAddr) {
		n.handleDNSForwarder(req, targetAddr)
		return
	}
	var wq waiter.Queue
	ep, createErr := req.CreateEndpoint(&wq)
	if createErr != nil {
		return
	}
	inbound := gonet.NewUDPConn(&wq, ep)
	outbound, err := openVPNOutboundUDPStream(id, targetAddr)
	if err != nil {
		recordVPNRuntimeError("udp_open "+targetAddr, err)
		_ = inbound.Close()
		return
	}
	go relayVPNUDP(inbound, outbound)
}

func (n *androidVPNNetstack) handleDNSForwarder(req *udp.ForwarderRequest, targetAddr string) {
	var wq waiter.Queue
	ep, createErr := req.CreateEndpoint(&wq)
	if createErr != nil {
		recordVPNRuntimeError("dns_create "+targetAddr, errors.New(createErr.String()))
		return
	}
	inbound := gonet.NewUDPConn(&wq, ep)
	go serveAndroidVPNDNS(inbound, targetAddr)
}

func openVPNOutboundTCP(targetAddr string) (net.Conn, error) {
	route, err := decideVPNRouteForTarget(targetAddr)
	if err != nil {
		return nil, err
	}
	if route.Reject {
		return nil, errors.New("route rejected")
	}
	if route.Direct {
		dialer := net.Dialer{Timeout: proxyConnectTimeout}
		return dialer.Dial("tcp", route.TargetAddr)
	}
	return openAndroidProxyChainStream(route.SelectedChainID, "tcp", route.TargetAddr)
}

func openVPNOutboundUDP(targetAddr string) (*net.UDPConn, error) {
	route, err := decideVPNRouteForTarget(targetAddr)
	if err != nil {
		return nil, err
	}
	if route.Reject {
		return nil, errors.New("route rejected")
	}
	if !route.Direct {
		return nil, errors.New("tunnel udp requires stream bridge")
	}
	udpAddr, err := net.ResolveUDPAddr("udp", targetAddr)
	if err != nil {
		return nil, err
	}
	return net.DialUDP("udp", nil, udpAddr)
}

func openVPNOutboundUDPStream(id stack.TransportEndpointID, targetAddr string) (io.ReadWriteCloser, error) {
	route, err := decideVPNRouteForTarget(targetAddr)
	if err != nil {
		return nil, err
	}
	if route.Reject {
		return nil, errors.New("route rejected")
	}
	if route.Direct {
		return openVPNOutboundUDP(route.TargetAddr)
	}
	srcIP := strings.TrimSpace(id.RemoteAddress.String())
	dstIP := strings.TrimSpace(id.LocalAddress.String())
	assocKey := strings.ToLower(strings.TrimSpace(route.TargetAddr)) + "|" + srcIP + ":" + strconv.Itoa(int(id.RemotePort)) + "->" + dstIP + ":" + strconv.Itoa(int(id.LocalPort))
	association := &linkAssociationV2Meta{
		Version:          2,
		Transport:        "udp",
		RouteGroup:       strings.TrimSpace(route.Group),
		RouteNodeID:      formatProxyLegacyTunnelNodeID(route.SelectedChainID),
		RouteTarget:      strings.TrimSpace(route.TargetAddr),
		RouteFingerprint: strings.ToLower(strings.TrimSpace(route.TargetAddr)),
		NATMode:          "default",
		TTLProfile:       "default",
		IdleTimeoutMS:    vpnUDPRelayTimeout.Milliseconds(),
		GCIntervalMS:     (vpnUDPRelayTimeout / 2).Milliseconds(),
		CreatedAtUnixMS:  time.Now().UnixMilli(),
		AssocKeyV2:       assocKey,
		FlowID:           assocKey,
		SrcIP:            srcIP,
		SrcPort:          uint16(id.RemotePort),
		DstIP:            dstIP,
		DstPort:          uint16(id.LocalPort),
		SourceKey:        srcIP + ":" + strconv.Itoa(int(id.RemotePort)),
		SourceRefs:       1,
	}
	if ip := net.ParseIP(srcIP); ip != nil {
		if ip.To4() != nil {
			association.IPFamily = 4
		} else {
			association.IPFamily = 6
		}
	}
	stream, err := openAndroidProxyChainPacketStream(route.SelectedChainID, "udp", route.TargetAddr, association)
	if err != nil {
		return nil, err
	}
	return newVPNTunnelUDPConn(stream), nil
}

func decideVPNRouteForTarget(targetAddr string) (vpnRouteDecision, error) {
	vpnRuntime.mu.Lock()
	configDir := vpnRuntime.configDir
	vpnRuntime.mu.Unlock()
	if rewrittenTarget, domain, ok := rewriteAndroidVPNFakeIPTarget(targetAddr); ok {
		route, err := decideAndroidProxyRouteForTarget(configDir, rewrittenTarget)
		if err != nil {
			return vpnRouteDecision{}, err
		}
		if !route.Direct && !route.Reject && route.SelectedChainID == "" {
			return vpnRouteDecision{}, errors.New("fake ip tunnel route missing selected_chain_id")
		}
		route.TargetAddr = rewrittenTarget
		if strings.TrimSpace(route.Group) == "" {
			route.Group = "fallback"
		}
		androidLogStore.add("vpn", "debug", "fake ip route "+targetAddr+" -> "+domain+" via "+route.Group)
		return vpnRouteDecision{
			Direct:          route.Direct,
			Reject:          route.Reject,
			TargetAddr:      route.TargetAddr,
			Group:           route.Group,
			SelectedChainID: route.SelectedChainID,
		}, nil
	}
	route, err := decideAndroidProxyRouteForTarget(configDir, targetAddr)
	if err != nil {
		return vpnRouteDecision{}, err
	}
	return vpnRouteDecision{
		Direct:          route.Direct,
		Reject:          route.Reject,
		TargetAddr:      route.TargetAddr,
		Group:           route.Group,
		SelectedChainID: route.SelectedChainID,
	}, nil
}

func pipeVPNConn(dst net.Conn, src net.Conn) {
	defer closeVPNWrite(dst)
	defer closeVPNRead(src)
	if vpnRelayIdle > 0 {
		deadline := time.Now().Add(vpnRelayIdle)
		_ = src.SetReadDeadline(deadline)
		_ = dst.SetReadDeadline(deadline)
	}
	_, _ = io.Copy(dst, src)
}

func relayVPNUDP(inbound *gonet.UDPConn, outbound io.ReadWriteCloser) {
	defer inbound.Close()
	defer outbound.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(outbound, inbound)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(inbound, outbound)
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(vpnUDPRelayTimeout):
	}
}

func serveAndroidVPNDNS(conn *gonet.UDPConn, targetAddr string) {
	defer conn.Close()
	buf := make([]byte, 4096)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(vpnDNSReadTimeout))
		n, err := conn.Read(buf)
		if err != nil {
			if !errors.Is(err, os.ErrDeadlineExceeded) && !isTimeoutError(err) {
				recordVPNRuntimeError("dns_read "+targetAddr, err)
			}
			return
		}
		if n <= 0 {
			continue
		}
		response, err := resolveAndroidVPNDNSPacket(buf[:n])
		if err != nil {
			recordVPNRuntimeError("dns_resolve "+targetAddr, err)
			response = buildAndroidVPNDNSRCode(buf[:n], dnsmessage.RCodeServerFailure)
		}
		if len(response) == 0 {
			response = buildAndroidVPNDNSRCode(buf[:n], dnsmessage.RCodeServerFailure)
		}
		if len(response) == 0 {
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(vpnDNSLookupTimeout))
		if _, err := conn.Write(response); err != nil {
			recordVPNRuntimeError("dns_write "+targetAddr, err)
			return
		}
	}
}

func resolveAndroidVPNDNSPacket(packet []byte) ([]byte, error) {
	domain, qType, err := parseAndroidVPNDNSQuestion(packet)
	if err != nil {
		return nil, err
	}
	if domain == "" {
		return buildAndroidVPNDNSRCode(packet, dnsmessage.RCodeNameError), nil
	}
	vpnRuntime.mu.Lock()
	configDir := vpnRuntime.configDir
	vpnRuntime.mu.Unlock()
	route, routeErr := decideAndroidProxyRouteForTarget(configDir, net.JoinHostPort(domain, "443"))
	if routeErr != nil {
		return nil, routeErr
	}
	if route.Reject {
		return buildAndroidVPNDNSRCode(packet, dnsmessage.RCodeRefused), nil
	}
	if shouldUseAndroidVPNDNSFakeIP(route, qType, domain) {
		fakeIP, ok := allocateAndroidVPNDNSFakeIP(domain, route)
		if !ok {
			return nil, errors.New("allocate fake ip failed")
		}
		androidLogStore.add("vpn", "debug", "dns fake "+domain+" -> "+fakeIP+" group="+route.Group)
		return buildAndroidVPNDNSSuccess(packet, []net.IP{net.ParseIP(fakeIP).To4()}, dnsmessage.TypeA), nil
	}
	if qType != dnsmessage.TypeA && qType != dnsmessage.TypeAAAA {
		return buildAndroidVPNDNSSuccess(packet, nil, qType), nil
	}
	if qType == dnsmessage.TypeAAAA && !route.Direct {
		return buildAndroidVPNDNSSuccess(packet, nil, qType), nil
	}
	response, err := queryAndroidVPNDNSUpstream(packet)
	if err != nil {
		return nil, err
	}
	storeAndroidVPNDNSRouteHints(domain, response, route)
	return response, nil
}

func parseAndroidVPNDNSQuestion(packet []byte) (string, dnsmessage.Type, error) {
	parser := dnsmessage.Parser{}
	if _, err := parser.Start(packet); err != nil {
		return "", dnsmessage.TypeA, err
	}
	question, err := parser.Question()
	if err != nil {
		return "", dnsmessage.TypeA, err
	}
	domain := strings.TrimSpace(strings.TrimSuffix(strings.ToLower(question.Name.String()), "."))
	return domain, question.Type, nil
}

func buildAndroidVPNDNSRCode(request []byte, rcode dnsmessage.RCode) []byte {
	parser := dnsmessage.Parser{}
	requestHeader, err := parser.Start(request)
	if err != nil {
		return nil
	}
	questions, err := collectAndroidVPNDNSQuestions(&parser)
	if err != nil {
		return nil
	}
	header := dnsmessage.Header{
		ID:                 requestHeader.ID,
		Response:           true,
		OpCode:             requestHeader.OpCode,
		RecursionDesired:   requestHeader.RecursionDesired,
		RecursionAvailable: true,
		RCode:              rcode,
	}
	builder := dnsmessage.NewBuilder(nil, header)
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil
	}
	for _, question := range questions {
		if err := builder.Question(question); err != nil {
			return nil
		}
	}
	message, err := builder.Finish()
	if err != nil {
		return nil
	}
	return message
}

func buildAndroidVPNDNSSuccess(request []byte, ips []net.IP, qType dnsmessage.Type) []byte {
	parser := dnsmessage.Parser{}
	requestHeader, err := parser.Start(request)
	if err != nil {
		return nil
	}
	questions, err := collectAndroidVPNDNSQuestions(&parser)
	if err != nil {
		return nil
	}
	header := dnsmessage.Header{
		ID:                 requestHeader.ID,
		Response:           true,
		OpCode:             requestHeader.OpCode,
		RecursionDesired:   requestHeader.RecursionDesired,
		RecursionAvailable: true,
		RCode:              dnsmessage.RCodeSuccess,
	}
	builder := dnsmessage.NewBuilder(nil, header)
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil
	}
	var answerName dnsmessage.Name
	answerNameSet := false
	for _, question := range questions {
		if err := builder.Question(question); err != nil {
			return nil
		}
		if !answerNameSet && question.Type == qType {
			answerName = question.Name
			answerNameSet = true
		}
	}
	if err := builder.StartAnswers(); err != nil {
		return nil
	}
	if answerNameSet {
		for _, ip := range ips {
			if ip == nil {
				continue
			}
			if qType == dnsmessage.TypeA {
				ip4 := ip.To4()
				if ip4 == nil {
					continue
				}
				var answer dnsmessage.AResource
				copy(answer.A[:], ip4)
				if err := builder.AResource(dnsmessage.ResourceHeader{Name: answerName, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: uint32(vpnDNSCacheTTL / time.Second)}, answer); err != nil {
					return nil
				}
				continue
			}
			if qType == dnsmessage.TypeAAAA {
				ip16 := ip.To16()
				if ip16 == nil || ip.To4() != nil {
					continue
				}
				var answer dnsmessage.AAAAResource
				copy(answer.AAAA[:], ip16)
				if err := builder.AAAAResource(dnsmessage.ResourceHeader{Name: answerName, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET, TTL: uint32(vpnDNSCacheTTL / time.Second)}, answer); err != nil {
					return nil
				}
			}
		}
	}
	message, err := builder.Finish()
	if err != nil {
		return nil
	}
	return message
}

func collectAndroidVPNDNSQuestions(parser *dnsmessage.Parser) ([]dnsmessage.Question, error) {
	questions := make([]dnsmessage.Question, 0, 1)
	for {
		question, err := parser.Question()
		if err != nil {
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			return nil, err
		}
		questions = append(questions, question)
	}
	return questions, nil
}

func queryAndroidVPNDNSUpstream(packet []byte) ([]byte, error) {
	upstreams := []string{"1.1.1.1:53", "8.8.8.8:53"}
	var lastErr error
	for _, upstream := range upstreams {
		conn, err := net.DialTimeout("udp", upstream, vpnDNSLookupTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(vpnDNSLookupTimeout))
		if _, err := conn.Write(packet); err != nil {
			lastErr = err
			_ = conn.Close()
			continue
		}
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		_ = conn.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if n <= 0 {
			lastErr = errors.New("empty dns upstream response")
			continue
		}
		return append([]byte(nil), buf[:n]...), nil
	}
	if lastErr == nil {
		lastErr = errors.New("dns upstream resolve failed")
	}
	return nil, lastErr
}

func storeAndroidVPNDNSRouteHints(domain string, response []byte, route proxyRouteDecision) {
	cleanDomain := strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	if cleanDomain == "" || strings.EqualFold(strings.TrimSpace(route.Group), "fallback") {
		return
	}
	ips := extractAndroidVPNDNSResponseIPs(response)
	if len(ips) == 0 {
		return
	}
	now := time.Now().UTC()
	vpnDNSState.mu.Lock()
	defer vpnDNSState.mu.Unlock()
	pruneAndroidVPNDNSFakeLocked(now)
	if vpnDNSState.routeIPHints == nil {
		vpnDNSState.routeIPHints = map[string]androidVPNDNSRouteHintEntry{}
	}
	for _, ip := range ips {
		parsed := net.ParseIP(strings.TrimSpace(strings.Trim(ip, "[]")))
		if parsed == nil {
			continue
		}
		ipText := parsed.String()
		vpnDNSState.routeIPHints[ipText] = androidVPNDNSRouteHintEntry{
			Domain:    cleanDomain,
			IP:        ipText,
			Group:     strings.TrimSpace(route.Group),
			ExpiresAt: now.Add(vpnDNSCacheTTL),
		}
	}
}

func lookupAndroidVPNDNSRouteHint(configDir string, ipText string, port string) (proxyRouteDecision, bool) {
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(ipText, "[]")))
	if ip == nil {
		return proxyRouteDecision{}, false
	}
	now := time.Now().UTC()
	vpnDNSState.mu.Lock()
	pruneAndroidVPNDNSFakeLocked(now)
	entry, ok := vpnDNSState.routeIPHints[ip.String()]
	vpnDNSState.mu.Unlock()
	if !ok || strings.TrimSpace(entry.Domain) == "" {
		return proxyRouteDecision{}, false
	}
	route, err := decideAndroidProxyRouteForTarget(configDir, net.JoinHostPort(entry.Domain, firstNonEmptyString(strings.TrimSpace(port), "443")))
	if err != nil {
		return proxyRouteDecision{}, false
	}
	if strings.TrimSpace(entry.Group) != "" && !strings.EqualFold(strings.TrimSpace(entry.Group), strings.TrimSpace(route.Group)) {
		route.Group = strings.TrimSpace(entry.Group)
		route.Direct = true
		route.Reject = false
		route.SelectedChainID = ""
	}
	route.TargetAddr = net.JoinHostPort(ip.String(), firstNonEmptyString(strings.TrimSpace(port), "443"))
	return route, true
}

func extractAndroidVPNDNSResponseIPs(packet []byte) []string {
	parser := dnsmessage.Parser{}
	if _, err := parser.Start(packet); err != nil {
		return nil
	}
	for {
		if _, err := parser.Question(); err != nil {
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			return nil
		}
	}
	seen := map[string]struct{}{}
	var out []string
	for {
		header, err := parser.AnswerHeader()
		if err != nil {
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			return out
		}
		switch header.Type {
		case dnsmessage.TypeA:
			answer, err := parser.AResource()
			if err != nil {
				return out
			}
			ip := net.IP(answer.A[:]).String()
			if ip == "" {
				continue
			}
			if _, ok := seen[ip]; ok {
				continue
			}
			seen[ip] = struct{}{}
			out = append(out, ip)
		case dnsmessage.TypeAAAA:
			answer, err := parser.AAAAResource()
			if err != nil {
				return out
			}
			ip := net.IP(answer.AAAA[:]).String()
			if ip == "" {
				continue
			}
			if _, ok := seen[ip]; ok {
				continue
			}
			seen[ip] = struct{}{}
			out = append(out, ip)
		default:
			if err := parser.SkipAnswer(); err != nil {
				return out
			}
		}
	}
	return out
}

func buildAndroidVPNDNSQuery(domain string, qType dnsmessage.Type) ([]byte, error) {
	cleanDomain := strings.TrimSpace(strings.Trim(domain, "."))
	if cleanDomain == "" {
		return nil, errors.New("dns domain is empty")
	}
	name, err := dnsmessage.NewName(cleanDomain + ".")
	if err != nil {
		return nil, err
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:               uint16(time.Now().UnixNano()),
		RecursionDesired: true,
	})
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(dnsmessage.Question{Name: name, Type: qType, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	return builder.Finish()
}

func snapshotAndroidVPNDNSStatus() map[string]any {
	now := time.Now().UTC()
	vpnDNSState.mu.Lock()
	defer vpnDNSState.mu.Unlock()
	pruneAndroidVPNDNSFakeLocked(now)
	fakeItems := make([]map[string]any, 0, len(vpnDNSState.fakeIPToEntry))
	for ip, entry := range vpnDNSState.fakeIPToEntry {
		fakeItems = append(fakeItems, map[string]any{
			"ip":         ip,
			"domain":     entry.Domain,
			"group":      entry.Group,
			"expires_at": entry.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}
	routeItems := make([]map[string]any, 0, len(vpnDNSState.routeIPHints))
	for ip, entry := range vpnDNSState.routeIPHints {
		routeItems = append(routeItems, map[string]any{
			"ip":         ip,
			"domain":     entry.Domain,
			"group":      entry.Group,
			"expires_at": entry.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}
	if len(fakeItems) > 8 {
		fakeItems = fakeItems[:8]
	}
	if len(routeItems) > 8 {
		routeItems = routeItems[:8]
	}
	return map[string]any{
		"enabled":          true,
		"listen":           "10.111.0.2:53",
		"fake_ip_cidr":     "198.18.0.0/15",
		"fake_ip_count":    len(vpnDNSState.fakeIPToEntry),
		"route_hint_count": len(vpnDNSState.routeIPHints),
		"fake_ip_items":    fakeItems,
		"route_hint_items": routeItems,
	}
}

func shouldUseAndroidVPNDNSFakeIP(route proxyRouteDecision, qType dnsmessage.Type, domain string) bool {
	if qType != dnsmessage.TypeA {
		return false
	}
	if route.Direct || route.Reject {
		return false
	}
	if strings.TrimSpace(route.SelectedChainID) == "" {
		return false
	}
	return strings.TrimSpace(domain) != ""
}

func allocateAndroidVPNDNSFakeIP(domain string, route proxyRouteDecision) (string, bool) {
	cleanDomain := strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	if cleanDomain == "" {
		return "", false
	}
	now := time.Now().UTC()
	vpnDNSState.mu.Lock()
	defer vpnDNSState.mu.Unlock()
	pruneAndroidVPNDNSFakeLocked(now)
	if existingIP, ok := vpnDNSState.fakeDomainToIP[cleanDomain]; ok {
		entry := vpnDNSState.fakeIPToEntry[existingIP]
		entry.Domain = cleanDomain
		entry.Group = strings.TrimSpace(route.Group)
		entry.ExpiresAt = now.Add(vpnDNSCacheTTL)
		vpnDNSState.fakeIPToEntry[existingIP] = entry
		return existingIP, true
	}
	for attempts := 0; attempts < 131000; attempts++ {
		ip := nextAndroidVPNDNSFakeIPLocked()
		if ip == "" {
			return "", false
		}
		if _, exists := vpnDNSState.fakeIPToEntry[ip]; exists {
			continue
		}
		vpnDNSState.fakeDomainToIP[cleanDomain] = ip
		vpnDNSState.fakeIPToEntry[ip] = androidVPNDNSFakeEntry{
			Domain:    cleanDomain,
			Group:     strings.TrimSpace(route.Group),
			ExpiresAt: now.Add(vpnDNSCacheTTL),
		}
		return ip, true
	}
	return "", false
}

func nextAndroidVPNDNSFakeIPLocked() string {
	const fakeSize uint32 = 2 * 256 * 256
	offset := vpnDNSState.nextFakeOffset
	if offset < 2 || offset >= fakeSize-1 {
		offset = 2
	}
	vpnDNSState.nextFakeOffset = offset + 1
	second := byte(18 + offset/65536)
	third := byte((offset / 256) % 256)
	fourth := byte(offset % 256)
	return net.IPv4(198, second, third, fourth).String()
}

func pruneAndroidVPNDNSFakeLocked(now time.Time) {
	for ip, entry := range vpnDNSState.fakeIPToEntry {
		if entry.ExpiresAt.IsZero() || now.Before(entry.ExpiresAt) {
			continue
		}
		delete(vpnDNSState.fakeIPToEntry, ip)
		if strings.TrimSpace(entry.Domain) != "" && vpnDNSState.fakeDomainToIP[entry.Domain] == ip {
			delete(vpnDNSState.fakeDomainToIP, entry.Domain)
		}
	}
	for ip, entry := range vpnDNSState.routeIPHints {
		if entry.ExpiresAt.IsZero() || now.Before(entry.ExpiresAt) {
			continue
		}
		delete(vpnDNSState.routeIPHints, ip)
	}
}

func rewriteAndroidVPNFakeIPTarget(targetAddr string) (string, string, bool) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return "", "", false
	}
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if net.ParseIP(cleanHost) == nil {
		return "", "", false
	}
	now := time.Now().UTC()
	vpnDNSState.mu.Lock()
	defer vpnDNSState.mu.Unlock()
	pruneAndroidVPNDNSFakeLocked(now)
	entry, ok := vpnDNSState.fakeIPToEntry[net.ParseIP(cleanHost).String()]
	if !ok || strings.TrimSpace(entry.Domain) == "" {
		return "", "", false
	}
	return net.JoinHostPort(entry.Domain, strings.TrimSpace(port)), entry.Domain, true
}

func isAndroidVPNDNSTarget(targetAddr string) bool {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return false
	}
	if strings.TrimSpace(port) != "53" {
		return false
	}
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(host, "[]")))
	return ip != nil
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func cloneVPNMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func parseRFC3339Time(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	return parsed, err == nil
}

func newVPNTunnelUDPConn(stream net.Conn) *vpnTunnelUDPConn {
	return &vpnTunnelUDPConn{stream: stream, reader: bufio.NewReader(stream)}
}

func (c *vpnTunnelUDPConn) Read(payload []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, io.ErrClosedPipe
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	return readProxyFramedPacket(c.reader, payload)
}

func (c *vpnTunnelUDPConn) Write(payload []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, io.ErrClosedPipe
	}
	if len(payload) == 0 {
		return 0, nil
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := writeProxyFramedPacket(c.stream, payload); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func (c *vpnTunnelUDPConn) Close() error {
	if c == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		if c.stream != nil {
			err = c.stream.Close()
		}
	})
	return err
}

func vpnTransportIDToTarget(addr tcpip.Address, port uint16) (string, error) {
	if port == 0 {
		return "", errors.New("transport target port is empty")
	}
	host := strings.TrimSpace(addr.String())
	if host == "" {
		return "", errors.New("transport target address is empty")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func vpnProtocolFromPacket(packet []byte) (tcpip.NetworkProtocolNumber, error) {
	if len(packet) == 0 {
		return 0, errors.New("empty packet")
	}
	switch packet[0] >> 4 {
	case 4:
		return ipv4.ProtocolNumber, nil
	case 6:
		return ipv6.ProtocolNumber, nil
	default:
		return 0, errors.New("unsupported ip version")
	}
}

func tcpipErrToError(err tcpip.Error) error {
	if err == nil {
		return nil
	}
	return errors.New(err.String())
}

func recordVPNRuntimeError(stage string, err error) {
	if err == nil {
		return
	}
	message := strings.TrimSpace(stage)
	if message == "" {
		message = "vpn"
	}
	message += ": " + err.Error()
	vpnRuntime.mu.Lock()
	vpnRuntime.lastError = message
	vpnRuntime.updatedAt = time.Now().UTC().Format(time.RFC3339)
	vpnRuntime.mu.Unlock()
	androidLogStore.add("vpn", "error", message)
}

func closeVPNWrite(conn net.Conn) {
	if conn == nil {
		return
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.CloseWrite()
		return
	}
	_ = conn.Close()
}

func closeVPNRead(conn net.Conn) {
	if conn == nil {
		return
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.CloseRead()
		return
	}
	_ = conn.Close()
}
