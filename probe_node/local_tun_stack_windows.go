//go:build windows

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
	probeLocalTUNNetstackNICID            = tcpip.NICID(1)
	probeLocalTUNNetstackQueueSize        = 4096
	probeLocalTUNNetstackMTU              = 1500
	probeLocalTUNTCPDialTimeout           = 10 * time.Second
	probeLocalTUNTCPForwarderWindow       = 0
	probeLocalTUNTCPForwarderInFlight     = 2048
	probeLocalTUNUDPAssociationTimeout    = 90 * time.Second
	probeLocalTUNUDPAssociationGCInterval = 15 * time.Second
	probeLocalTUNUDPReadBufferSize        = 65535

	probeLocalTUNUDPNATModeFallbackEphemeral = "auto_fallback_ephemeral"
)

type probeLocalTUNPacketStack interface {
	Write([]byte) (int, error)
	Close() error
}

type probeLocalTUNSimplePacketStack struct {
	closeOnce sync.Once
	closed    bool
}

type probeLocalTUNNetstack struct {
	stack  *stack.Stack
	linkEP *channel.Endpoint

	cancel context.CancelFunc
	doneCh chan struct{}

	closeOnce sync.Once
	closed    atomic.Bool
}

type probeLocalTUNDuplexConn interface {
	net.Conn
	CloseWrite() error
	CloseRead() error
}

type probeLocalTUNReadDeadliner interface {
	SetReadDeadline(time.Time) error
}

type probeLocalDirectBypassManagedConn struct {
	net.Conn
	release   func()
	closeOnce sync.Once
}

type probeLocalTUNUDPBridge struct {
	inbound  *gonet.UDPConn
	outbound io.ReadWriteCloser
	timeout  time.Duration

	closeOnce sync.Once
}

type probeLocalTUNTunnelUDPConn struct {
	stream net.Conn
	reader *bufio.Reader

	readMu    sync.Mutex
	writeMu   sync.Mutex
	closeOnce sync.Once
}

// probeLocalTUNUDPManagedOutbound 在 UDP bridge close 时保证 source refs 被释放。
type probeLocalTUNUDPManagedOutbound struct {
	io.ReadWriteCloser

	releaseSource func()
	closeOnce     sync.Once
}

var (
	probeLocalAcquireDirectBypassRoute = acquireProbeLocalTUNDirectBypassRoute
	probeLocalReleaseDirectBypassRoute = releaseProbeLocalTUNDirectBypassRoute
)

func init() {
	probeLocalDNSEnsureDirectBypassForTarget = ensureProbeLocalDirectBypassForTarget
}

func ensureProbeLocalExplicitDirectBypassForTarget(targetAddr string) error {
	return ensureProbeLocalDirectBypassForTarget(targetAddr)
}

type probeLocalWindowsDirectBypassRouteTarget struct {
	InterfaceIndex int    `json:"interface_index"`
	NextHop        string `json:"next_hop"`
}

var probeLocalDirectBypassState = struct {
	mu      sync.Mutex
	ref     map[string]int
	hosts   map[string]string
	targets map[string]map[string]struct{}
	routes  map[string]probeLocalWindowsDirectBypassRouteTarget
}{
	ref:     map[string]int{},
	hosts:   map[string]string{},
	targets: map[string]map[string]struct{}{},
	routes:  map[string]probeLocalWindowsDirectBypassRouteTarget{},
}

var probeLocalDirectBypassRouteTargetState = struct {
	mu          sync.Mutex
	routeTarget probeLocalWindowsDirectBypassRouteTarget
	ready       bool
}{}

func prepareProbeLocalWindowsDirectBypassRouteTarget() error {
	routeTarget, err := resolveProbeLocalWindowsDirectBypassRouteTarget()
	if err != nil {
		return err
	}
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	probeLocalDirectBypassRouteTargetState.routeTarget = routeTarget
	probeLocalDirectBypassRouteTargetState.ready = true
	probeLocalDirectBypassRouteTargetState.mu.Unlock()
	return nil
}

func currentProbeLocalWindowsDirectBypassRouteTarget() (probeLocalWindowsDirectBypassRouteTarget, bool) {
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	defer probeLocalDirectBypassRouteTargetState.mu.Unlock()
	if !probeLocalDirectBypassRouteTargetState.ready {
		return probeLocalWindowsDirectBypassRouteTarget{}, false
	}
	return probeLocalDirectBypassRouteTargetState.routeTarget, true
}

func clearProbeLocalWindowsDirectBypassRouteTarget() {
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	probeLocalDirectBypassRouteTargetState.routeTarget = probeLocalWindowsDirectBypassRouteTarget{}
	probeLocalDirectBypassRouteTargetState.ready = false
	probeLocalDirectBypassRouteTargetState.mu.Unlock()
}

var probeLocalTUNUDPSourceState = struct {
	mu   sync.Mutex
	refs map[string]int64
}{
	refs: map[string]int64{},
}

func startProbeLocalTUNPacketStack() error {
	probeLocalTUNDataPlaneState.mu.Lock()
	if probeLocalTUNDataPlaneState.packetStack != nil {
		probeLocalTUNDataPlaneState.mu.Unlock()
		return nil
	}
	probeLocalTUNDataPlaneState.mu.Unlock()

	netstackRunner, err := newProbeLocalTUNNetstack()
	if err != nil {
		return err
	}

	probeLocalTUNDataPlaneState.mu.Lock()
	if probeLocalTUNDataPlaneState.packetStack != nil {
		probeLocalTUNDataPlaneState.mu.Unlock()
		_ = netstackRunner.Close()
		return nil
	}
	probeLocalTUNDataPlaneState.packetStack = netstackRunner
	probeLocalTUNDataPlaneState.mu.Unlock()

	logProbeInfof("probe local tun gvisor netstack started")
	return nil
}

func stopProbeLocalTUNPacketStack() error {
	probeLocalTUNDataPlaneState.mu.Lock()
	packetStack := probeLocalTUNDataPlaneState.packetStack
	probeLocalTUNDataPlaneState.packetStack = nil
	probeLocalTUNDataPlaneState.mu.Unlock()
	if packetStack == nil {
		return nil
	}
	err := packetStack.Close()
	logProbeInfof("probe local tun packet stack stopped")
	return err
}

func newProbeLocalTUNNetstack() (*probeLocalTUNNetstack, error) {
	gStack := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol,
			ipv6.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol,
			udp.NewProtocol,
		},
	})

	linkEP := channel.New(probeLocalTUNNetstackQueueSize, probeLocalTUNNetstackMTU, "")
	if err := probeLocalTCPIPErrToError(gStack.CreateNIC(probeLocalTUNNetstackNICID, linkEP)); err != nil {
		return nil, err
	}
	if err := probeLocalTCPIPErrToError(gStack.SetPromiscuousMode(probeLocalTUNNetstackNICID, true)); err != nil {
		return nil, err
	}
	if err := probeLocalTCPIPErrToError(gStack.SetSpoofing(probeLocalTUNNetstackNICID, true)); err != nil {
		return nil, err
	}
	gStack.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: probeLocalTUNNetstackNICID},
		{Destination: header.IPv6EmptySubnet, NIC: probeLocalTUNNetstackNICID},
	})

	ctx, cancel := context.WithCancel(context.Background())
	runner := &probeLocalTUNNetstack{
		stack:  gStack,
		linkEP: linkEP,
		cancel: cancel,
		doneCh: make(chan struct{}),
	}

	tcpForwarder := tcp.NewForwarder(gStack, probeLocalTUNTCPForwarderWindow, probeLocalTUNTCPForwarderInFlight, runner.handleTCPForwarder)
	udpForwarder := udp.NewForwarder(gStack, runner.handleUDPForwarder)
	gStack.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)
	gStack.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder.HandlePacket)

	go runner.outputLoop(ctx)
	return runner, nil
}

func (n *probeLocalTUNNetstack) outputLoop(ctx context.Context) {
	defer close(n.doneCh)
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
		if err := writeProbeLocalTUNPacket(payload); err != nil {
			logProbeWarnf("probe local tun netstack write packet failed: %v", err)
		}
	}
}

func (n *probeLocalTUNNetstack) Write(packet []byte) (int, error) {
	if len(packet) == 0 {
		return 0, nil
	}
	if n == nil || n.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	protocol, err := probeLocalTUNProtocolFromPacket(packet)
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

func (n *probeLocalTUNNetstack) Close() error {
	if n == nil {
		return nil
	}
	n.closeOnce.Do(func() {
		n.closed.Store(true)
		if n.cancel != nil {
			n.cancel()
		}
		select {
		case <-n.doneCh:
		case <-time.After(2 * time.Second):
		}
		if n.linkEP != nil {
			n.linkEP.Close()
		}
		if n.stack != nil {
			n.stack.Destroy()
		}
		releaseProbeLocalAllDirectBypassRoutes()
	})
	return nil
}

func (n *probeLocalTUNNetstack) handleTCPForwarder(req *tcp.ForwarderRequest) {
	if req == nil {
		return
	}
	id := req.ID()
	targetAddr, err := probeLocalTransportIDToTarget(id.LocalAddress, id.LocalPort)
	if err != nil {
		req.Complete(true)
		return
	}

	var wq waiter.Queue
	ep, createErr := req.CreateEndpoint(&wq)
	if createErr != nil {
		req.Complete(true)
		globalProbeTCPDebugState.recordFailureWithRoute("create_failed", targetAddr, probeLocalTunnelRouteDecision{}, errors.New(createErr.String()))
		return
	}
	req.Complete(false)

	inbound := gonet.NewTCPConn(&wq, ep)
	outbound, route, openErr := openProbeLocalTUNOutboundTCP(targetAddr)
	if openErr != nil {
		_ = inbound.Close()
		globalProbeTCPDebugState.recordFailureWithRoute("open_failed", targetAddr, route, openErr)
		logProbeWarnf("probe local tun tcp open failed: target=%s route=%s group=%s node=%s err=%v", targetAddr, route.TargetAddr, route.Group, route.TunnelNodeID, openErr)
		return
	}

	relay := globalProbeTCPDebugState.beginRelayWithRoute(targetAddr, route)
	go n.pipeAndCloseTCP(outbound, inbound, relay, "up")
	go n.pipeAndCloseTCP(inbound, outbound, relay, "down")
}

func (n *probeLocalTUNNetstack) pipeAndCloseTCP(dst net.Conn, src net.Conn, relay *probeTCPDebugRelay, direction string) {
	defer closeProbeLocalConnWrite(dst)
	defer closeProbeLocalConnRead(src)
	if relay != nil {
		defer relay.releaseSide()
	}
	writer := io.Writer(dst)
	if relay != nil {
		writer = &probeTCPDebugWriter{dst: dst, relay: relay, direction: direction}
	}
	_, err := io.Copy(writer, src)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		if relay != nil {
			globalProbeTCPDebugState.recordRelayFailure(relay, err)
		} else {
			globalProbeTCPDebugState.recordFailure("relay_failed", "", err)
		}
	}
}

func openProbeLocalTUNOutboundTCP(targetAddr string) (net.Conn, probeLocalTunnelRouteDecision, error) {
	route, err := decideProbeLocalRouteForTarget(targetAddr)
	if err != nil {
		return nil, probeLocalTunnelRouteDecision{}, err
	}
	if route.Reject {
		return nil, route, &probeLocalRouteRejectError{Group: route.Group}
	}
	if route.Direct {
		conn, dialErr := net.DialTimeout("tcp", strings.TrimSpace(route.TargetAddr), probeLocalTUNTCPDialTimeout)
		if dialErr != nil {
			return nil, route, dialErr
		}
		return conn, route, nil
	}
	conn, openErr := openProbeLocalTunnelConnWithGroupRuntime("tcp", route.TargetAddr, route.GroupRuntime, nil)
	if openErr != nil {
		return nil, route, openErr
	}
	return conn, route, nil
}

func (c *probeLocalDirectBypassManagedConn) Close() error {
	if c == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		if c.release != nil {
			c.release()
		}
		if c.Conn != nil {
			err = c.Conn.Close()
		}
	})
	return err
}

func (n *probeLocalTUNNetstack) handleUDPForwarder(req *udp.ForwarderRequest) {
	if req == nil {
		return
	}
	id := req.ID()
	targetAddr, err := probeLocalTransportIDToTarget(id.LocalAddress, id.LocalPort)
	if err != nil {
		return
	}

	var wq waiter.Queue
	ep, createErr := req.CreateEndpoint(&wq)
	if createErr != nil {
		reason := classifyProbeLocalTUNError("create_failed", errors.New(createErr.String()))
		logProbeWarnf("probe local tun udp create endpoint failed: target=%s reason=%s err=%s", targetAddr, reason, createErr.String())
		return
	}
	inbound := gonet.NewUDPConn(&wq, ep)

	outbound, route, openErr := openProbeLocalTUNOutboundUDP(id, targetAddr)
	if openErr != nil {
		_ = inbound.Close()
		reason := classifyProbeLocalTUNError("open_failed", openErr)
		logProbeWarnf("probe local tun udp open failed: target=%s route=%s group=%s node=%s reason=%s err=%v", targetAddr, route.TargetAddr, route.Group, route.TunnelNodeID, reason, openErr)
		return
	}

	bridge := &probeLocalTUNUDPBridge{
		inbound:  inbound,
		outbound: outbound,
		timeout:  probeLocalTUNUDPAssociationTimeout,
	}
	bridge.start()
}

func openProbeLocalTUNOutboundUDP(id stack.TransportEndpointID, targetAddr string) (io.ReadWriteCloser, probeLocalTunnelRouteDecision, error) {
	route, err := decideProbeLocalRouteForTarget(targetAddr)
	if err != nil {
		return nil, probeLocalTunnelRouteDecision{}, err
	}
	if route.Reject {
		return nil, route, &probeLocalRouteRejectError{Group: route.Group}
	}

	srcIP := strings.TrimSpace(id.RemoteAddress.String())
	dstIP := strings.TrimSpace(id.LocalAddress.String())
	sourceKey, sourceRefs, releaseSource := acquireProbeLocalTUNUDPSource(srcIP, uint16(id.RemotePort))
	if releaseSource == nil {
		releaseSource = func() {}
	}

	if route.Direct {
		udpAddr, resolveErr := net.ResolveUDPAddr("udp", route.TargetAddr)
		if resolveErr != nil {
			releaseSource()
			return nil, route, resolveErr
		}

		var localAddr *net.UDPAddr
		if parsed := net.ParseIP(strings.TrimSpace(strings.Trim(srcIP, "[]"))); parsed != nil {
			localAddr = &net.UDPAddr{IP: parsed, Port: int(uint16(id.RemotePort))}
		}
		conn, dialErr := net.DialUDP("udp", localAddr, udpAddr)
		natMode := probeChainUDPAssociationNATModeDefault
		if dialErr != nil && localAddr != nil && shouldFallbackProbeLocalUDPBind(dialErr) {
			logProbeWarnf("probe local tun udp bind conflict, fallback to ephemeral local addr: src=%s:%d target=%s err=%v", srcIP, uint16(id.RemotePort), route.TargetAddr, dialErr)
			conn, dialErr = net.DialUDP("udp", nil, udpAddr)
			natMode = probeLocalTUNUDPNATModeFallbackEphemeral
		}
		if dialErr != nil {
			releaseSource()
			return nil, route, dialErr
		}
		_ = natMode
		return &probeLocalTUNUDPManagedOutbound{ReadWriteCloser: conn, releaseSource: releaseSource}, route, nil
	}

	association := &probeChainAssociationV2Meta{
		Version:          2,
		Transport:        "udp",
		RouteGroup:       strings.TrimSpace(route.Group),
		RouteNodeID:      firstNonEmpty(strings.TrimSpace(route.TunnelNodeID), formatProbeLocalLegacyTunnelNodeID(route.SelectedChainID)),
		RouteTarget:      strings.TrimSpace(route.TargetAddr),
		RouteFingerprint: strings.ToLower(strings.TrimSpace(route.TargetAddr)),
		NATMode:          probeChainUDPAssociationNATModeDefault,
		TTLProfile:       probeChainUDPAssociationTTLProfileDefault,
		IdleTimeoutMS:    probeLocalTUNUDPAssociationTimeout.Milliseconds(),
		GCIntervalMS:     probeLocalTUNUDPAssociationGCInterval.Milliseconds(),
		CreatedAtUnixMS:  time.Now().UnixMilli(),
		SrcIP:            srcIP,
		SrcPort:          uint16(id.RemotePort),
		DstIP:            dstIP,
		DstPort:          uint16(id.LocalPort),
		SourceKey:        sourceKey,
		SourceRefs:       sourceRefs,
	}
	if ip := net.ParseIP(srcIP); ip != nil {
		if ip.To4() != nil {
			association.IPFamily = 4
		} else {
			association.IPFamily = 6
		}
	}
	assocKey := strings.ToLower(strings.TrimSpace(targetAddr)) + "|" + srcIP + ":" + strconv.Itoa(int(id.RemotePort)) + "->" + dstIP + ":" + strconv.Itoa(int(id.LocalPort))
	association.AssocKeyV2 = assocKey
	association.FlowID = assocKey

	stream, openErr := openProbeLocalTunnelConnWithGroupRuntime("udp", route.TargetAddr, route.GroupRuntime, association)
	if openErr != nil {
		releaseSource()
		return nil, route, openErr
	}
	return &probeLocalTUNUDPManagedOutbound{ReadWriteCloser: newProbeLocalTUNTunnelUDPConn(stream), releaseSource: releaseSource}, route, nil
}

func (b *probeLocalTUNUDPBridge) start() {
	go b.forwardInboundToOutbound()
	go b.forwardOutboundToInbound()
}

func (b *probeLocalTUNUDPBridge) forwardInboundToOutbound() {
	buf := make([]byte, probeLocalTUNUDPReadBufferSize)
	for {
		if b.timeout > 0 {
			_ = b.inbound.SetReadDeadline(time.Now().Add(b.timeout))
		}
		n, err := b.inbound.Read(buf)
		if n > 0 {
			if _, writeErr := b.outbound.Write(buf[:n]); writeErr != nil {
				b.close()
				return
			}
		}
		if err != nil {
			b.close()
			return
		}
	}
}

func (b *probeLocalTUNUDPBridge) forwardOutboundToInbound() {
	buf := make([]byte, probeLocalTUNUDPReadBufferSize)
	for {
		if b.timeout > 0 {
			if deadliner, ok := b.outbound.(probeLocalTUNReadDeadliner); ok {
				_ = deadliner.SetReadDeadline(time.Now().Add(b.timeout))
			}
		}
		n, err := b.outbound.Read(buf)
		if n > 0 {
			if _, writeErr := b.inbound.Write(buf[:n]); writeErr != nil {
				b.close()
				return
			}
		}
		if err != nil {
			b.close()
			return
		}
	}
}

func (b *probeLocalTUNUDPBridge) close() {
	b.closeOnce.Do(func() {
		if b.inbound != nil {
			_ = b.inbound.Close()
		}
		if b.outbound != nil {
			_ = b.outbound.Close()
		}
	})
}

func newProbeLocalTUNTunnelUDPConn(stream net.Conn) *probeLocalTUNTunnelUDPConn {
	return &probeLocalTUNTunnelUDPConn{
		stream: stream,
		reader: bufio.NewReader(stream),
	}
}

func (c *probeLocalTUNTunnelUDPConn) Read(payload []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, io.ErrClosedPipe
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	frame, err := readProbeChainFramedPacket(c.reader)
	if err != nil {
		return 0, err
	}
	if len(frame) == 0 {
		return 0, nil
	}
	n := copy(payload, frame)
	return n, nil
}

func (c *probeLocalTUNTunnelUDPConn) Write(payload []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, io.ErrClosedPipe
	}
	if len(payload) == 0 {
		return 0, nil
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := writeProbeChainFramedPacket(c.stream, payload); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func (c *probeLocalTUNTunnelUDPConn) Close() error {
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

func (c *probeLocalTUNTunnelUDPConn) SetReadDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return io.ErrClosedPipe
	}
	return c.stream.SetReadDeadline(t)
}

func (c *probeLocalTUNUDPManagedOutbound) Close() error {
	if c == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		if c.ReadWriteCloser != nil {
			err = c.ReadWriteCloser.Close()
		}
		if c.releaseSource != nil {
			c.releaseSource()
		}
	})
	return err
}

func acquireProbeLocalTUNUDPSource(srcIP string, srcPort uint16) (string, int64, func()) {
	cleanIP := strings.TrimSpace(strings.Trim(srcIP, "[]"))
	if cleanIP == "" || srcPort == 0 {
		return "", 0, func() {}
	}
	sourceKey := cleanIP + ":" + strconv.Itoa(int(srcPort))
	probeLocalTUNUDPSourceState.mu.Lock()
	if probeLocalTUNUDPSourceState.refs == nil {
		probeLocalTUNUDPSourceState.refs = map[string]int64{}
	}
	probeLocalTUNUDPSourceState.refs[sourceKey]++
	refs := probeLocalTUNUDPSourceState.refs[sourceKey]
	probeLocalTUNUDPSourceState.mu.Unlock()

	once := sync.Once{}
	release := func() {
		once.Do(func() {
			releaseProbeLocalTUNUDPSource(sourceKey)
		})
	}
	return sourceKey, refs, release
}

func releaseProbeLocalTUNUDPSource(sourceKey string) {
	cleanKey := strings.TrimSpace(sourceKey)
	if cleanKey == "" {
		return
	}
	probeLocalTUNUDPSourceState.mu.Lock()
	defer probeLocalTUNUDPSourceState.mu.Unlock()
	if probeLocalTUNUDPSourceState.refs == nil {
		probeLocalTUNUDPSourceState.refs = map[string]int64{}
		return
	}
	remaining := probeLocalTUNUDPSourceState.refs[cleanKey] - 1
	if remaining <= 0 {
		delete(probeLocalTUNUDPSourceState.refs, cleanKey)
		return
	}
	probeLocalTUNUDPSourceState.refs[cleanKey] = remaining
}

func probeLocalTUNUDPSourceRefs(sourceKey string) int64 {
	cleanKey := strings.TrimSpace(sourceKey)
	if cleanKey == "" {
		return 0
	}
	probeLocalTUNUDPSourceState.mu.Lock()
	defer probeLocalTUNUDPSourceState.mu.Unlock()
	if probeLocalTUNUDPSourceState.refs == nil {
		return 0
	}
	return probeLocalTUNUDPSourceState.refs[cleanKey]
}

func shouldFallbackProbeLocalUDPBind(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if errno == syscall.EADDRINUSE || errno == syscall.EADDRNOTAVAIL || errno == syscall.Errno(10048) || errno == syscall.Errno(10049) {
			return true
		}
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(msg, "only one usage of each socket address") {
		return true
	}
	if strings.Contains(msg, "address already in use") {
		return true
	}
	if strings.Contains(msg, "requested address is not valid in its context") {
		return true
	}
	if strings.Contains(msg, "cannot assign requested address") {
		return true
	}
	if strings.Contains(msg, "eaddrinuse") || strings.Contains(msg, "eaddrnotavail") {
		return true
	}
	return false
}

func classifyProbeLocalTUNError(defaultReason string, err error) string {
	if err == nil {
		return strings.TrimSpace(defaultReason)
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(text, "timeout"):
		return "timeout"
	case strings.Contains(text, "refused"):
		return "connection_refused"
	case strings.Contains(text, "reset"):
		return "connection_reset"
	case strings.Contains(text, "broken pipe"):
		return "broken_pipe"
	case strings.Contains(text, "address already in use") || strings.Contains(text, "eaddrinuse"):
		return "address_in_use"
	case strings.Contains(text, "requested address is not valid in its context") || strings.Contains(text, "cannot assign requested address") || strings.Contains(text, "eaddrnotavail"):
		return "address_not_available"
	case strings.Contains(text, "eof"):
		return "eof"
	case strings.Contains(text, "closed"):
		return "closed"
	default:
		if strings.TrimSpace(defaultReason) != "" {
			return strings.TrimSpace(defaultReason)
		}
		return "tun_failed"
	}
}

func closeProbeLocalConnRead(conn net.Conn) {
	if conn == nil {
		return
	}
	if duplex, ok := conn.(probeLocalTUNDuplexConn); ok {
		_ = duplex.CloseRead()
		return
	}
	_ = conn.Close()
}

func closeProbeLocalConnWrite(conn net.Conn) {
	if conn == nil {
		return
	}
	if duplex, ok := conn.(probeLocalTUNDuplexConn); ok {
		_ = duplex.CloseWrite()
		return
	}
	_ = conn.Close()
}

func probeLocalTransportIDToTarget(addr tcpip.Address, port uint16) (string, error) {
	if port == 0 {
		return "", errors.New("transport target port is empty")
	}
	host := strings.TrimSpace(addr.String())
	if host == "" {
		return "", errors.New("transport target address is empty")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func probeLocalTCPIPErrToError(err tcpip.Error) error {
	if err == nil {
		return nil
	}
	return errors.New(err.String())
}

func probeLocalTUNProtocolFromPacket(packet []byte) (tcpip.NetworkProtocolNumber, error) {
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

func (s *probeLocalTUNSimplePacketStack) Write(packet []byte) (int, error) {
	if s == nil {
		return 0, errors.New("packet stack is nil")
	}
	if s.closed {
		return 0, errors.New("packet stack is closed")
	}
	if len(packet) == 0 {
		return 0, nil
	}
	network, targetAddr, parseErr := parseProbeLocalTUNPacketTarget(packet)
	if parseErr != nil {
		return len(packet), nil
	}
	route, routeErr := decideProbeLocalRouteForTarget(targetAddr)
	if routeErr != nil {
		var rejectErr *probeLocalRouteRejectError
		if errors.As(routeErr, &rejectErr) {
			return len(packet), nil
		}
		return 0, routeErr
	}
	if route.Reject {
		return len(packet), nil
	}
	if !route.Direct {
		if _, _, err := normalizeProbeLocalTunnelNodeID(route.TunnelNodeID); err != nil {
			return 0, err
		}
		logProbeInfof("probe local tun packet routed to tunnel: network=%s target=%s group=%s node=%s", network, route.TargetAddr, route.Group, route.TunnelNodeID)
		return len(packet), nil
	}
	return len(packet), nil
}

func (s *probeLocalTUNSimplePacketStack) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closed = true
		releaseProbeLocalAllDirectBypassRoutes()
	})
	return nil
}

func parseProbeLocalTUNPacketTarget(packet []byte) (network string, targetAddr string, err error) {
	if len(packet) == 0 {
		return "", "", errors.New("empty packet")
	}
	version := packet[0] >> 4
	switch version {
	case 4:
		return parseProbeLocalTUNIPv4Target(packet)
	case 6:
		return parseProbeLocalTUNIPv6Target(packet)
	default:
		return "", "", fmt.Errorf("unsupported ip version: %d", version)
	}
}

func parseProbeLocalTUNIPv4Target(packet []byte) (network string, targetAddr string, err error) {
	if len(packet) < 20 {
		return "", "", errors.New("ipv4 header too short")
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl+4 {
		return "", "", errors.New("invalid ipv4 header length")
	}
	proto := packet[9]
	dstIP := net.IPv4(packet[16], packet[17], packet[18], packet[19]).String()
	dstPort := uint16(packet[ihl+2])<<8 | uint16(packet[ihl+3])
	if dstPort == 0 {
		return "", "", errors.New("missing destination port")
	}
	switch proto {
	case 6:
		network = "tcp"
	case 17:
		network = "udp"
	default:
		return "", "", fmt.Errorf("unsupported ipv4 transport protocol: %d", proto)
	}
	return network, net.JoinHostPort(dstIP, strconv.Itoa(int(dstPort))), nil
}

func parseProbeLocalTUNIPv6Target(packet []byte) (network string, targetAddr string, err error) {
	if len(packet) < 44 {
		return "", "", errors.New("ipv6 packet too short")
	}
	nextHeader := packet[6]
	dstIP := net.IP(packet[24:40]).String()
	dstPort := uint16(packet[42])<<8 | uint16(packet[43])
	if strings.TrimSpace(dstIP) == "" || dstPort == 0 {
		return "", "", errors.New("invalid ipv6 target")
	}
	switch nextHeader {
	case 6:
		network = "tcp"
	case 17:
		network = "udp"
	default:
		return "", "", fmt.Errorf("unsupported ipv6 next header: %d", nextHeader)
	}
	return network, net.JoinHostPort(dstIP, strconv.Itoa(int(dstPort))), nil
}

func ensureProbeLocalDirectBypassForTarget(targetAddr string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return err
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return nil
	}
	ipText := ip.String()

	probeLocalDirectBypassState.mu.Lock()
	if probeLocalDirectBypassState.ref == nil {
		probeLocalDirectBypassState.ref = map[string]int{}
	}
	if probeLocalDirectBypassState.hosts == nil {
		probeLocalDirectBypassState.hosts = map[string]string{}
	}
	if probeLocalDirectBypassState.targets == nil {
		probeLocalDirectBypassState.targets = map[string]map[string]struct{}{}
	}
	if probeLocalDirectBypassState.routes == nil {
		probeLocalDirectBypassState.routes = map[string]probeLocalWindowsDirectBypassRouteTarget{}
	}
	probeLocalDirectBypassState.ref[ipText]++
	probeLocalDirectBypassState.hosts[ipText] = ipText
	if _, ok := probeLocalDirectBypassState.targets[ipText]; !ok {
		probeLocalDirectBypassState.targets[ipText] = map[string]struct{}{}
	}
	probeLocalDirectBypassState.targets[ipText][strings.TrimSpace(targetAddr)] = struct{}{}
	needCreate := probeLocalDirectBypassState.ref[ipText] == 1
	probeLocalDirectBypassState.mu.Unlock()

	if !needCreate {
		return nil
	}
	release, acqErr := probeLocalAcquireDirectBypassRoute(ipText)
	if acqErr != nil {
		probeLocalDirectBypassState.mu.Lock()
		probeLocalDirectBypassState.ref[ipText]--
		if probeLocalDirectBypassState.ref[ipText] <= 0 {
			delete(probeLocalDirectBypassState.ref, ipText)
			delete(probeLocalDirectBypassState.hosts, ipText)
			delete(probeLocalDirectBypassState.targets, ipText)
			delete(probeLocalDirectBypassState.routes, ipText)
		}
		probeLocalDirectBypassState.mu.Unlock()
		return acqErr
	}
	if release != nil {
		release()
	}
	return nil
}

func resolveProbeLocalWindowsDirectBypassRouteTarget() (probeLocalWindowsDirectBypassRouteTarget, error) {
	routeTarget, err := resolveProbeLocalWindowsRouteTarget()
	if err != nil {
		return probeLocalWindowsDirectBypassRouteTarget{}, err
	}
	return probeLocalResolveWindowsPrimaryEgressRoute(routeTarget.InterfaceIndex)
}

func acquireProbeLocalTUNDirectBypassRoute(host string) (func(), error) {
	ip := strings.TrimSpace(host)
	if parsed := net.ParseIP(ip); parsed == nil || parsed.To4() == nil {
		return nil, fmt.Errorf("invalid bypass host: %s", host)
	}
	routeTarget, ok := currentProbeLocalWindowsDirectBypassRouteTarget()
	if !ok {
		if err := prepareProbeLocalWindowsDirectBypassRouteTarget(); err != nil {
			return nil, errors.New("direct bypass route target is not prepared")
		}
		routeTarget, ok = currentProbeLocalWindowsDirectBypassRouteTarget()
		if !ok {
			return nil, errors.New("direct bypass route target is not prepared")
		}
	}
	routeDef := probeLocalWindowsRouteDef{Prefix: ip, Mask: "255.255.255.255", Gateway: routeTarget.NextHop, IfIndex: routeTarget.InterfaceIndex}
	if _, err := probeLocalCreateWindowsRouteEntry(routeDef); err != nil {
		return nil, err
	}
	probeLocalDirectBypassState.mu.Lock()
	if probeLocalDirectBypassState.routes == nil {
		probeLocalDirectBypassState.routes = map[string]probeLocalWindowsDirectBypassRouteTarget{}
	}
	probeLocalDirectBypassState.routes[ip] = routeTarget
	probeLocalDirectBypassState.mu.Unlock()
	return func() {}, nil
}

func releaseProbeLocalTUNDirectBypassRoute(host string) {
	ip := strings.TrimSpace(host)
	if parsed := net.ParseIP(ip); parsed == nil || parsed.To4() == nil {
		return
	}
	probeLocalDirectBypassState.mu.Lock()
	routeTarget, ok := probeLocalDirectBypassState.routes[ip]
	if ok {
		delete(probeLocalDirectBypassState.routes, ip)
	}
	probeLocalDirectBypassState.mu.Unlock()
	if !ok {
		return
	}
	routeDef := probeLocalWindowsRouteDef{Prefix: ip, Mask: "255.255.255.255", Gateway: routeTarget.NextHop, IfIndex: routeTarget.InterfaceIndex}
	if delErr := probeLocalDeleteWindowsRouteEntry(routeDef); delErr != nil {
		logProbeWarnf("probe local bypass route delete failed: host=%s err=%v", ip, delErr)
	}
}

func releaseProbeLocalDirectBypassForHost(host string) {
	ip := strings.TrimSpace(host)
	if parsed := net.ParseIP(ip); parsed == nil || parsed.To4() == nil {
		return
	}
	probeLocalDirectBypassState.mu.Lock()
	if probeLocalDirectBypassState.ref == nil {
		probeLocalDirectBypassState.ref = map[string]int{}
	}
	count := probeLocalDirectBypassState.ref[ip]
	if count <= 1 {
		delete(probeLocalDirectBypassState.ref, ip)
		delete(probeLocalDirectBypassState.hosts, ip)
		delete(probeLocalDirectBypassState.targets, ip)
		probeLocalDirectBypassState.mu.Unlock()
		probeLocalReleaseDirectBypassRoute(ip)
		return
	}
	probeLocalDirectBypassState.ref[ip] = count - 1
	probeLocalDirectBypassState.mu.Unlock()
}

func releaseProbeLocalAllDirectBypassRoutes() {
	probeLocalDirectBypassState.mu.Lock()
	hosts := make([]string, 0, len(probeLocalDirectBypassState.hosts))
	for host := range probeLocalDirectBypassState.hosts {
		hosts = append(hosts, host)
	}
	probeLocalDirectBypassState.mu.Unlock()
	for _, host := range hosts {
		probeLocalReleaseDirectBypassRoute(host)
	}
	probeLocalDirectBypassState.mu.Lock()
	probeLocalDirectBypassState.ref = map[string]int{}
	probeLocalDirectBypassState.hosts = map[string]string{}
	probeLocalDirectBypassState.targets = map[string]map[string]struct{}{}
	probeLocalDirectBypassState.routes = map[string]probeLocalWindowsDirectBypassRouteTarget{}
	probeLocalDirectBypassState.mu.Unlock()
}

func resetProbeLocalDirectBypassStateForTest() {
	probeLocalDirectBypassState.mu.Lock()
	probeLocalDirectBypassState.ref = map[string]int{}
	probeLocalDirectBypassState.hosts = map[string]string{}
	probeLocalDirectBypassState.targets = map[string]map[string]struct{}{}
	probeLocalDirectBypassState.routes = map[string]probeLocalWindowsDirectBypassRouteTarget{}
	probeLocalDirectBypassState.mu.Unlock()
	clearProbeLocalWindowsDirectBypassRouteTarget()

	probeLocalTUNUDPSourceState.mu.Lock()
	probeLocalTUNUDPSourceState.refs = map[string]int64{}
	probeLocalTUNUDPSourceState.mu.Unlock()

	probeLocalAcquireDirectBypassRoute = acquireProbeLocalTUNDirectBypassRoute
	probeLocalReleaseDirectBypassRoute = releaseProbeLocalTUNDirectBypassRoute
}
