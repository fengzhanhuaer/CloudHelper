package mobilecore

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	vpnNICID           = tcpip.NICID(1)
	vpnQueueSize       = 4096
	vpnMTU             = 1500
	vpnTCPWindow       = 1024
	vpnTCPInFlight     = 512
	vpnRelayIdle       = 5 * time.Minute
	vpnUDPRelayTimeout = 30 * time.Second
)

var vpnRuntime = &androidVPNRuntime{}

type androidVPNRuntime struct {
	mu        sync.Mutex
	configDir string
	tun       *os.File
	stack     *androidVPNNetstack
	status    string
	lastError string
	updatedAt string
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
	defer vpnRuntime.mu.Unlock()
	return marshalLinkJSON(map[string]any{
		"ok":         true,
		"running":    vpnRuntime.stack != nil,
		"status":     firstNonEmptyString(vpnRuntime.status, "stopped"),
		"last_error": vpnRuntime.lastError,
		"updated_at": vpnRuntime.updatedAt,
	})
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
		_, _ = n.tun.Write(payload)
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
	var wq waiter.Queue
	ep, createErr := req.CreateEndpoint(&wq)
	if createErr != nil {
		return
	}
	inbound := gonet.NewUDPConn(&wq, ep)
	outbound, err := openVPNOutboundUDPStream(id, targetAddr)
	if err != nil {
		_ = inbound.Close()
		return
	}
	go relayVPNUDP(inbound, outbound)
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
