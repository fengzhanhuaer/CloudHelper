//go:build windows

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
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
	probeLocalTUNNetstackNICID             = tcpip.NICID(1)
	probeLocalTUNNetstackQueueSize         = 4096
	probeLocalTUNNetstackMTU               = 1500
	probeLocalTUNTCPDialTimeout            = 10 * time.Second
	probeLocalTUNTCPDirectFailureCacheTTL  = 30 * time.Second
	probeLocalTUNTCPDirectFailureCacheMax  = 512
	probeLocalTUNTCPRelayIdleTimeout       = 5 * time.Minute
	probeLocalTUNTCPOpenConcurrencyLimit   = 256
	probeLocalTUNTCPFailureLogInterval     = 5 * time.Second
	probeLocalTUNTCPFailureLogCacheMax     = 512
	probeLocalTUNTCPForwarderWindow        = 0
	probeLocalTUNTCPForwarderInFlight      = 2048
	probeLocalTUNUDPAssociationTimeout     = 30 * time.Second
	probeLocalTUNUDPQUICAssociationTimeout = 60 * time.Second
	probeLocalTUNUDPShortAssociationTTL    = 10 * time.Second
	probeLocalTUNUDPNoResponseTunnelTTL    = 15 * time.Second
	probeLocalTUNUDPNoResponseDirectTTL    = 5 * time.Second
	probeLocalTUNUDPAssociationGCInterval  = 15 * time.Second
	probeLocalTUNUDPReadBufferSize         = 65535
	probeLocalTUNUDPZeroReadBackoff        = 10 * time.Millisecond

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

type probeLocalTUNDeadlineRefreshWriter struct {
	writer io.Writer
	src    net.Conn
	dst    net.Conn
	idle   time.Duration
}

type probeLocalTUNTCPDirectFailureCacheEntry struct {
	errText   string
	expiresAt time.Time
}

type probeLocalTUNTCPDirectFailureCacheStats struct {
	Active int   `json:"active"`
	Hits   int64 `json:"hits"`
	Stored int64 `json:"stored"`
}

type probeLocalTUNTCPFailureLogEntry struct {
	nextAt     time.Time
	suppressed int64
}

func (w *probeLocalTUNDeadlineRefreshWriter) Write(payload []byte) (int, error) {
	if w == nil || w.writer == nil {
		return 0, io.ErrClosedPipe
	}
	n, err := w.writer.Write(payload)
	if n > 0 && w.idle > 0 {
		deadline := time.Now().Add(w.idle)
		if w.src != nil {
			_ = w.src.SetReadDeadline(deadline)
		}
		if w.dst != nil {
			_ = w.dst.SetReadDeadline(deadline)
		}
	}
	return n, err
}

type probeLocalTUNUDPBridge struct {
	inbound  *gonet.UDPConn
	outbound io.ReadWriteCloser
	timeout  time.Duration
	target   string
	route    probeLocalTunnelRouteDecision
	monitor  *probeLocalTUNUDPBridgeMonitorItemState

	closeOnce sync.Once
}

type probeLocalTUNUDPBridgeMonitorStats struct {
	Active int64                               `json:"active"`
	Direct int64                               `json:"direct"`
	Tunnel int64                               `json:"tunnel"`
	Opened int64                               `json:"opened"`
	Closed int64                               `json:"closed"`
	Items  []probeLocalTUNUDPBridgeMonitorItem `json:"items,omitempty"`
}

type probeLocalTUNUDPBridgeMonitorItem struct {
	ID          string `json:"id"`
	Target      string `json:"target,omitempty"`
	RouteTarget string `json:"route_target,omitempty"`
	Group       string `json:"group,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	Direct      bool   `json:"direct"`
	TimeoutMS   int64  `json:"timeout_ms"`
	OpenedAt    string `json:"opened_at,omitempty"`
	LastActive  string `json:"last_active,omitempty"`
	AgeMS       int64  `json:"age_ms"`
	IdleMS      int64  `json:"idle_ms"`
	BytesUp     int64  `json:"bytes_up,omitempty"`
	BytesDown   int64  `json:"bytes_down,omitempty"`
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

var errProbeLocalTUNFallbackBypassInstalled = errors.New("fallback direct bypass installed; close current tun flow and let application reconnect via system route")

func init() {
	probeLocalDNSEnsureDirectBypassForTarget = ensureProbeLocalDirectBypassForTarget
}

func ensureProbeLocalExplicitDirectBypassForTarget(targetAddr string) error {
	return ensureProbeLocalDirectBypassForTarget(targetAddr)
}

func ensureProbeLocalFallbackDirectBypassForTarget(targetAddr string) error {
	return ensureProbeLocalDirectBypassForRoutedTarget(targetAddr)
}

type probeLocalWindowsDirectBypassRouteTarget struct {
	InterfaceIndex int    `json:"interface_index"`
	NextHop        string `json:"next_hop"`
}

var probeLocalDirectBypassState = struct {
	mu          sync.Mutex
	ref         map[string]int
	hosts       map[string]string
	targets     map[string]map[string]struct{}
	routes      map[string]probeLocalWindowsDirectBypassRouteTarget
	managedRefs map[string]int
}{
	ref:         map[string]int{},
	hosts:       map[string]string{},
	targets:     map[string]map[string]struct{}{},
	routes:      map[string]probeLocalWindowsDirectBypassRouteTarget{},
	managedRefs: map[string]int{},
}

var probeLocalDirectBypassRouteTargetState = struct {
	mu          sync.Mutex
	routeTarget probeLocalWindowsDirectBypassRouteTarget
	ready       bool
}{}

var probeLocalTUNTCPDirectFailureCacheState = struct {
	mu     sync.Mutex
	hits   atomic.Int64
	stored atomic.Int64
	items  map[string]probeLocalTUNTCPDirectFailureCacheEntry
}{items: map[string]probeLocalTUNTCPDirectFailureCacheEntry{}}

var probeLocalTUNTCPOpenSemaphore = make(chan struct{}, probeLocalTUNTCPOpenConcurrencyLimit)

var probeLocalTUNTCPFailureLogState = struct {
	mu    sync.Mutex
	items map[string]probeLocalTUNTCPFailureLogEntry
}{items: map[string]probeLocalTUNTCPFailureLogEntry{}}

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

var probeLocalTUNUDPBridgeMonitorState = struct {
	mu     sync.Mutex
	seq    atomic.Uint64
	active atomic.Int64
	opened atomic.Int64
	closed atomic.Int64
	items  map[string]*probeLocalTUNUDPBridgeMonitorItemState
}{items: map[string]*probeLocalTUNUDPBridgeMonitorItemState{}}

type probeLocalTUNUDPBridgeMonitorItemState struct {
	id       string
	target   string
	route    probeLocalTunnelRouteDecision
	openedAt time.Time
	timeout  time.Duration

	lastActiveUnix atomic.Int64
	bytesUp        atomic.Int64
	bytesDown      atomic.Int64
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
	if shouldDropProbeLocalTUNTCPFlow(targetAddr) {
		req.Complete(true)
		return
	}
	if !acquireProbeLocalTUNTCPOpenSlot() {
		req.Complete(true)
		globalProbeTCPDebugState.recordFailureWithRoute("open_limited", targetAddr, probeLocalTunnelRouteDecision{}, errors.New("tcp open concurrency limit reached"))
		return
	}
	defer releaseProbeLocalTUNTCPOpenSlot()

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
		if errors.Is(openErr, errProbeLocalTUNFallbackBypassInstalled) {
			return
		}
		if shouldReportProbeLocalTUNTCPFailure("open_failed", targetAddr, route, openErr) {
			globalProbeTCPDebugState.recordFailureWithRoute("open_failed", targetAddr, route, openErr)
			logProbeWarnf("probe local tun tcp outbound open failed: inbound=tun outbound=%s target=%s route=%s group=%s node=%s err=%v", probeLocalTUNRouteOutboundPath(route), targetAddr, route.TargetAddr, route.Group, route.TunnelNodeID, openErr)
		}
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
	if probeLocalTUNTCPRelayIdleTimeout > 0 {
		deadline := time.Now().Add(probeLocalTUNTCPRelayIdleTimeout)
		_ = src.SetReadDeadline(deadline)
		_ = dst.SetReadDeadline(deadline)
		writer = &probeLocalTUNDeadlineRefreshWriter{writer: writer, src: src, dst: dst, idle: probeLocalTUNTCPRelayIdleTimeout}
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
		if shouldInstallProbeLocalFallbackDirectBypassAndFail(route) {
			if bypassErr := ensureProbeLocalFallbackDirectBypassForTarget(route.TargetAddr); bypassErr != nil {
				return nil, route, bypassErr
			}
			return nil, route, errProbeLocalTUNFallbackBypassInstalled
		}
		if cachedErr := lookupProbeLocalTUNTCPDirectFailure(route.TargetAddr); cachedErr != nil {
			return nil, route, cachedErr
		}
		if bypassErr := ensureProbeLocalDirectBypassForRoutedTarget(route.TargetAddr); bypassErr != nil {
			return nil, route, bypassErr
		}
		conn, dialErr := net.DialTimeout("tcp", strings.TrimSpace(route.TargetAddr), probeLocalTUNTCPDialTimeout)
		if dialErr != nil {
			rememberProbeLocalTUNTCPDirectFailure(route.TargetAddr, dialErr)
			return nil, route, dialErr
		}
		clearProbeLocalTUNTCPDirectFailure(route.TargetAddr)
		return conn, route, nil
	}
	var lastErr error
	for _, target := range probeLocalTunnelRouteTargetCandidates(route) {
		conn, openErr := openProbeLocalTunnelConnWithGroupRuntime("tcp", target, route.GroupRuntime, nil)
		if openErr == nil {
			route.TargetAddr = target
			return conn, route, nil
		}
		lastErr = openErr
	}
	if lastErr != nil {
		return nil, route, lastErr
	}
	return nil, route, errors.New("tunnel route has no target candidates")
}

func probeLocalTUNRouteOutboundPath(route probeLocalTunnelRouteDecision) string {
	if route.Direct {
		if isProbeLocalFallbackDirectRoute(route) {
			return "fallback_bypass"
		}
		return "direct"
	}
	if route.Reject {
		return "reject"
	}
	return "tunnel"
}

func acquireProbeLocalTUNTCPOpenSlot() bool {
	select {
	case probeLocalTUNTCPOpenSemaphore <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseProbeLocalTUNTCPOpenSlot() {
	select {
	case <-probeLocalTUNTCPOpenSemaphore:
	default:
	}
}

func shouldReportProbeLocalTUNTCPFailure(kind string, targetAddr string, route probeLocalTunnelRouteDecision, err error) bool {
	key := strings.Join([]string{
		strings.TrimSpace(kind),
		strings.TrimSpace(probeLocalTUNRouteOutboundPath(route)),
		strings.TrimSpace(route.Group),
		strings.TrimSpace(route.TunnelNodeID),
		strings.TrimSpace(targetAddr),
		strings.TrimSpace(route.TargetAddr),
		classifyProbeTCPDebugError(kind, err),
	}, "|")
	if strings.TrimSpace(key) == "" {
		return true
	}
	now := time.Now()
	probeLocalTUNTCPFailureLogState.mu.Lock()
	defer probeLocalTUNTCPFailureLogState.mu.Unlock()
	if probeLocalTUNTCPFailureLogState.items == nil {
		probeLocalTUNTCPFailureLogState.items = map[string]probeLocalTUNTCPFailureLogEntry{}
	}
	entry, ok := probeLocalTUNTCPFailureLogState.items[key]
	if ok && now.Before(entry.nextAt) {
		entry.suppressed++
		probeLocalTUNTCPFailureLogState.items[key] = entry
		return false
	}
	if len(probeLocalTUNTCPFailureLogState.items) >= probeLocalTUNTCPFailureLogCacheMax {
		for itemKey, item := range probeLocalTUNTCPFailureLogState.items {
			if now.After(item.nextAt) {
				delete(probeLocalTUNTCPFailureLogState.items, itemKey)
			}
		}
	}
	if len(probeLocalTUNTCPFailureLogState.items) >= probeLocalTUNTCPFailureLogCacheMax {
		for itemKey := range probeLocalTUNTCPFailureLogState.items {
			delete(probeLocalTUNTCPFailureLogState.items, itemKey)
			break
		}
	}
	probeLocalTUNTCPFailureLogState.items[key] = probeLocalTUNTCPFailureLogEntry{nextAt: now.Add(probeLocalTUNTCPFailureLogInterval)}
	return true
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
	if shouldDropProbeLocalTUNUDPFlow(targetAddr) {
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
		timeout:  resolveProbeLocalTUNUDPBridgeTimeout(targetAddr, route),
		target:   targetAddr,
		route:    route,
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
	if shouldInstallProbeLocalFallbackDirectBypassAndFail(route) {
		if bypassErr := ensureProbeLocalFallbackDirectBypassForTarget(route.TargetAddr); bypassErr != nil {
			return nil, route, bypassErr
		}
		return nil, route, errProbeLocalTUNFallbackBypassInstalled
	}

	srcIP := strings.TrimSpace(id.RemoteAddress.String())
	dstIP := strings.TrimSpace(id.LocalAddress.String())
	sourceKey, sourceRefs, releaseSource := acquireProbeLocalTUNUDPSource(srcIP, uint16(id.RemotePort))
	if releaseSource == nil {
		releaseSource = func() {}
	}

	if route.Direct {
		if bypassErr := ensureProbeLocalDirectBypassForRoutedTarget(route.TargetAddr); bypassErr != nil {
			releaseSource()
			return nil, route, bypassErr
		}
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
		TTLProfile:       resolveProbeLocalTUNUDPTTLProfile(route.TargetAddr),
		IdleTimeoutMS:    resolveProbeLocalTUNUDPBridgeTimeout(route.TargetAddr, route).Milliseconds(),
		GCIntervalMS:     probeChainUDPAssociationEffectiveGCInterval(resolveProbeLocalTUNUDPBridgeTimeout(route.TargetAddr, route)).Milliseconds(),
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
	probeLocalTUNUDPBridgeMonitorState.active.Add(1)
	probeLocalTUNUDPBridgeMonitorState.opened.Add(1)
	b.monitor = beginProbeLocalTUNUDPBridgeMonitorItem(strings.TrimSpace(b.target), b.route, b.timeout)
	go b.forwardInboundToOutbound()
	go b.forwardOutboundToInbound()
}

func (b *probeLocalTUNUDPBridge) forwardInboundToOutbound() {
	buf := make([]byte, probeLocalTUNUDPReadBufferSize)
	for {
		readTimeout := b.currentReadTimeout()
		if readTimeout > 0 {
			_ = b.inbound.SetReadDeadline(time.Now().Add(readTimeout))
		}
		n, err := b.inbound.Read(buf)
		if n > 0 {
			touchProbeLocalTUNUDPBridgeMonitorItem(b.monitor, "up", n)
			if _, writeErr := b.outbound.Write(buf[:n]); writeErr != nil {
				b.close()
				return
			}
		}
		if n == 0 && err == nil {
			time.Sleep(probeLocalTUNUDPZeroReadBackoff)
			continue
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
		readTimeout := b.currentReadTimeout()
		if readTimeout > 0 {
			if deadliner, ok := b.outbound.(probeLocalTUNReadDeadliner); ok {
				_ = deadliner.SetReadDeadline(time.Now().Add(readTimeout))
			}
		}
		n, err := b.outbound.Read(buf)
		if n > 0 {
			touchProbeLocalTUNUDPBridgeMonitorItem(b.monitor, "down", n)
			if _, writeErr := b.inbound.Write(buf[:n]); writeErr != nil {
				b.close()
				return
			}
		}
		if n == 0 && err == nil {
			time.Sleep(probeLocalTUNUDPZeroReadBackoff)
			continue
		}
		if err != nil {
			b.close()
			return
		}
	}
}

func (b *probeLocalTUNUDPBridge) currentReadTimeout() time.Duration {
	if b == nil {
		return 0
	}
	if b.shouldUseNoResponseTimeout() {
		if b.route.Direct {
			return probeLocalTUNUDPNoResponseDirectTTL
		}
		return probeLocalTUNUDPNoResponseTunnelTTL
	}
	return b.timeout
}

func (b *probeLocalTUNUDPBridge) shouldUseNoResponseTimeout() bool {
	if b == nil || b.monitor == nil {
		return false
	}
	return b.monitor.bytesUp.Load() > 0 && b.monitor.bytesDown.Load() == 0
}

func (b *probeLocalTUNUDPBridge) close() {
	b.closeOnce.Do(func() {
		probeLocalTUNUDPBridgeMonitorState.active.Add(-1)
		probeLocalTUNUDPBridgeMonitorState.closed.Add(1)
		endProbeLocalTUNUDPBridgeMonitorItem(b.monitor)
		if b.inbound != nil {
			_ = b.inbound.Close()
		}
		if b.outbound != nil {
			_ = b.outbound.Close()
		}
	})
}

func snapshotProbeLocalTUNUDPBridgeMonitorStats() probeLocalTUNUDPBridgeMonitorStats {
	stats := probeLocalTUNUDPBridgeMonitorStats{
		Active: probeLocalTUNUDPBridgeMonitorState.active.Load(),
		Opened: probeLocalTUNUDPBridgeMonitorState.opened.Load(),
		Closed: probeLocalTUNUDPBridgeMonitorState.closed.Load(),
	}
	now := time.Now().UTC()
	probeLocalTUNUDPBridgeMonitorState.mu.Lock()
	items := make([]*probeLocalTUNUDPBridgeMonitorItemState, 0, len(probeLocalTUNUDPBridgeMonitorState.items))
	for _, item := range probeLocalTUNUDPBridgeMonitorState.items {
		if item != nil {
			items = append(items, item)
		}
	}
	probeLocalTUNUDPBridgeMonitorState.mu.Unlock()
	for _, item := range items {
		view := probeLocalTUNUDPBridgeMonitorItem{
			ID:          strings.TrimSpace(item.id),
			Target:      strings.TrimSpace(item.target),
			RouteTarget: firstNonEmpty(strings.TrimSpace(item.route.TargetAddr), strings.TrimSpace(item.target)),
			Group:       strings.TrimSpace(item.route.Group),
			NodeID:      strings.TrimSpace(item.route.TunnelNodeID),
			Direct:      item.route.Direct,
			TimeoutMS:   item.timeout.Milliseconds(),
			OpenedAt:    item.openedAt.UTC().Format(time.RFC3339),
			AgeMS:       now.Sub(item.openedAt).Milliseconds(),
			BytesUp:     item.bytesUp.Load(),
			BytesDown:   item.bytesDown.Load(),
		}
		if lastActive := item.lastActiveUnix.Load(); lastActive > 0 {
			lastActiveAt := time.Unix(lastActive, 0).UTC()
			view.LastActive = lastActiveAt.Format(time.RFC3339)
			view.IdleMS = now.Sub(lastActiveAt).Milliseconds()
		}
		if view.Direct {
			stats.Direct++
		} else {
			stats.Tunnel++
		}
		stats.Items = append(stats.Items, view)
	}
	sort.Slice(stats.Items, func(i, j int) bool {
		if stats.Items[i].IdleMS == stats.Items[j].IdleMS {
			return stats.Items[i].Target < stats.Items[j].Target
		}
		return stats.Items[i].IdleMS > stats.Items[j].IdleMS
	})
	if len(stats.Items) > 16 {
		stats.Items = append([]probeLocalTUNUDPBridgeMonitorItem(nil), stats.Items[:16]...)
	}
	return stats
}

func beginProbeLocalTUNUDPBridgeMonitorItem(target string, route probeLocalTunnelRouteDecision, timeout time.Duration) *probeLocalTUNUDPBridgeMonitorItemState {
	now := time.Now().UTC()
	id := "probe-udp-" + strconv.FormatInt(now.UnixNano(), 10) + "-" + strconv.FormatUint(probeLocalTUNUDPBridgeMonitorState.seq.Add(1), 10)
	item := &probeLocalTUNUDPBridgeMonitorItemState{
		id:       id,
		target:   strings.TrimSpace(target),
		route:    route,
		openedAt: now,
		timeout:  timeout,
	}
	item.lastActiveUnix.Store(now.Unix())
	probeLocalTUNUDPBridgeMonitorState.mu.Lock()
	if probeLocalTUNUDPBridgeMonitorState.items == nil {
		probeLocalTUNUDPBridgeMonitorState.items = map[string]*probeLocalTUNUDPBridgeMonitorItemState{}
	}
	probeLocalTUNUDPBridgeMonitorState.items[id] = item
	probeLocalTUNUDPBridgeMonitorState.mu.Unlock()
	return item
}

func touchProbeLocalTUNUDPBridgeMonitorItem(item *probeLocalTUNUDPBridgeMonitorItemState, direction string, n int) {
	if item == nil || n <= 0 {
		return
	}
	item.lastActiveUnix.Store(time.Now().UTC().Unix())
	if strings.EqualFold(strings.TrimSpace(direction), "down") {
		item.bytesDown.Add(int64(n))
		return
	}
	item.bytesUp.Add(int64(n))
}

func endProbeLocalTUNUDPBridgeMonitorItem(item *probeLocalTUNUDPBridgeMonitorItemState) {
	if item == nil {
		return
	}
	probeLocalTUNUDPBridgeMonitorState.mu.Lock()
	delete(probeLocalTUNUDPBridgeMonitorState.items, item.id)
	probeLocalTUNUDPBridgeMonitorState.mu.Unlock()
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

func resolveProbeLocalTUNUDPAssociationTimeout(targetAddr string) time.Duration {
	if isProbeLocalTUNUDPQUICTarget(targetAddr) {
		return probeLocalTUNUDPQUICAssociationTimeout
	}
	return probeLocalTUNUDPAssociationTimeout
}

func resolveProbeLocalTUNUDPBridgeTimeout(targetAddr string, route probeLocalTunnelRouteDecision) time.Duration {
	if shouldUseProbeLocalTUNUDPShortTTL(targetAddr, route) {
		return probeLocalTUNUDPShortAssociationTTL
	}
	return resolveProbeLocalTUNUDPAssociationTimeout(targetAddr)
}

func resolveProbeLocalTUNUDPTTLProfile(targetAddr string) string {
	if isProbeLocalTUNUDPQUICTarget(targetAddr) {
		return probeChainUDPAssociationTTLProfileQUICStable
	}
	return probeChainUDPAssociationTTLProfileDefault
}

func isProbeLocalTUNUDPQUICTarget(targetAddr string) bool {
	_, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return false
	}
	switch strings.TrimSpace(port) {
	case "443", "8443":
		return true
	default:
		return false
	}
}

func shouldUseProbeLocalTUNUDPShortTTL(targetAddr string, route probeLocalTunnelRouteDecision) bool {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return false
	}
	cleanPort := strings.TrimSpace(port)
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	ip := net.ParseIP(cleanHost)
	if cleanPort == "53" || cleanPort == "137" || cleanPort == "5353" || cleanPort == "1900" || cleanPort == "7680" {
		return true
	}
	if ip == nil {
		return false
	}
	return route.Direct && isProbeLocalTUNLocalOrDiscoveryIP(ip)
}

func shouldDropProbeLocalTUNUDPFlow(targetAddr string) bool {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return false
	}
	cleanPort := strings.TrimSpace(port)
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	ip := net.ParseIP(cleanHost)
	if ip == nil {
		return false
	}
	if ip.IsMulticast() || ip.Equal(net.IPv4bcast) || isProbeLocalTUNFakeIPBroadcast(ip) {
		return true
	}
	if cleanPort == "137" || cleanPort == "1900" || cleanPort == "5353" {
		return true
	}
	return false
}

func shouldDropProbeLocalTUNTCPFlow(targetAddr string) bool {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return false
	}
	if strings.TrimSpace(port) != "7680" {
		return false
	}
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(host, "[]")))
	if ip == nil {
		return false
	}
	return isProbeLocalTUNLocalOrDiscoveryIP(ip)
}

func shouldInstallProbeLocalFallbackDirectBypassAndFail(route probeLocalTunnelRouteDecision) bool {
	if !isProbeLocalFallbackDirectRoute(route) {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(route.TargetAddr))
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(host, "[]")))
	if ip == nil || ip.To4() == nil {
		return false
	}
	return !isProbeLocalTUNLocalOrDiscoveryIP(ip)
}

func isProbeLocalFallbackDirectRoute(route probeLocalTunnelRouteDecision) bool {
	return route.Direct && !route.Reject && strings.EqualFold(strings.TrimSpace(route.Group), "fallback")
}

func lookupProbeLocalTUNTCPDirectFailure(targetAddr string) error {
	key := normalizeProbeLocalTUNTCPDirectFailureKey(targetAddr)
	if key == "" {
		return nil
	}
	now := time.Now()
	probeLocalTUNTCPDirectFailureCacheState.mu.Lock()
	entry, ok := probeLocalTUNTCPDirectFailureCacheState.items[key]
	if !ok {
		probeLocalTUNTCPDirectFailureCacheState.mu.Unlock()
		return nil
	}
	if !entry.expiresAt.After(now) {
		delete(probeLocalTUNTCPDirectFailureCacheState.items, key)
		probeLocalTUNTCPDirectFailureCacheState.mu.Unlock()
		return nil
	}
	probeLocalTUNTCPDirectFailureCacheState.mu.Unlock()
	probeLocalTUNTCPDirectFailureCacheState.hits.Add(1)
	return fmt.Errorf("cached direct tcp dial failure for %s: %s", key, strings.TrimSpace(entry.errText))
}

func rememberProbeLocalTUNTCPDirectFailure(targetAddr string, err error) {
	if !shouldCacheProbeLocalTUNTCPDirectFailure(err) {
		return
	}
	key := normalizeProbeLocalTUNTCPDirectFailureKey(targetAddr)
	if key == "" {
		return
	}
	now := time.Now()
	probeLocalTUNTCPDirectFailureCacheState.mu.Lock()
	if probeLocalTUNTCPDirectFailureCacheState.items == nil {
		probeLocalTUNTCPDirectFailureCacheState.items = map[string]probeLocalTUNTCPDirectFailureCacheEntry{}
	}
	if len(probeLocalTUNTCPDirectFailureCacheState.items) >= probeLocalTUNTCPDirectFailureCacheMax {
		pruneProbeLocalTUNTCPDirectFailureCacheLocked(now)
	}
	if len(probeLocalTUNTCPDirectFailureCacheState.items) >= probeLocalTUNTCPDirectFailureCacheMax {
		dropKey := ""
		for itemKey := range probeLocalTUNTCPDirectFailureCacheState.items {
			dropKey = itemKey
			break
		}
		if dropKey != "" {
			delete(probeLocalTUNTCPDirectFailureCacheState.items, dropKey)
		}
	}
	probeLocalTUNTCPDirectFailureCacheState.items[key] = probeLocalTUNTCPDirectFailureCacheEntry{
		errText:   strings.TrimSpace(err.Error()),
		expiresAt: now.Add(probeLocalTUNTCPDirectFailureCacheTTL),
	}
	probeLocalTUNTCPDirectFailureCacheState.mu.Unlock()
	probeLocalTUNTCPDirectFailureCacheState.stored.Add(1)
}

func clearProbeLocalTUNTCPDirectFailure(targetAddr string) {
	key := normalizeProbeLocalTUNTCPDirectFailureKey(targetAddr)
	if key == "" {
		return
	}
	probeLocalTUNTCPDirectFailureCacheState.mu.Lock()
	delete(probeLocalTUNTCPDirectFailureCacheState.items, key)
	probeLocalTUNTCPDirectFailureCacheState.mu.Unlock()
}

func snapshotProbeLocalTUNTCPDirectFailureCacheStats() probeLocalTUNTCPDirectFailureCacheStats {
	now := time.Now()
	probeLocalTUNTCPDirectFailureCacheState.mu.Lock()
	pruneProbeLocalTUNTCPDirectFailureCacheLocked(now)
	active := len(probeLocalTUNTCPDirectFailureCacheState.items)
	probeLocalTUNTCPDirectFailureCacheState.mu.Unlock()
	return probeLocalTUNTCPDirectFailureCacheStats{
		Active: active,
		Hits:   probeLocalTUNTCPDirectFailureCacheState.hits.Load(),
		Stored: probeLocalTUNTCPDirectFailureCacheState.stored.Load(),
	}
}

func pruneProbeLocalTUNTCPDirectFailureCacheLocked(now time.Time) {
	for key, entry := range probeLocalTUNTCPDirectFailureCacheState.items {
		if !entry.expiresAt.After(now) {
			delete(probeLocalTUNTCPDirectFailureCacheState.items, key)
		}
	}
}

func shouldCacheProbeLocalTUNTCPDirectFailure(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "timeout") ||
		strings.Contains(text, "no route to host") ||
		strings.Contains(text, "network is unreachable") ||
		strings.Contains(text, "host is unreachable")
}

func normalizeProbeLocalTUNTCPDirectFailureKey(targetAddr string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return ""
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	port = strings.TrimSpace(port)
	if host == "" || port == "" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return net.JoinHostPort(host, port)
}

func resetProbeLocalTUNTCPDirectFailureCacheForTest() {
	probeLocalTUNTCPDirectFailureCacheState.mu.Lock()
	probeLocalTUNTCPDirectFailureCacheState.items = map[string]probeLocalTUNTCPDirectFailureCacheEntry{}
	probeLocalTUNTCPDirectFailureCacheState.mu.Unlock()
	probeLocalTUNTCPDirectFailureCacheState.hits.Store(0)
	probeLocalTUNTCPDirectFailureCacheState.stored.Store(0)
}

func isProbeLocalTUNFakeIPBroadcast(ip net.IP) bool {
	if ip == nil {
		return false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	return ip4[0] == 198 && ip4[1] == 19 && ip4[2] == 255 && ip4[3] == 255
}

func isProbeLocalTUNLocalOrDiscoveryIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		case ip4[0] == 169 && ip4[1] == 254:
			return true
		default:
			return false
		}
	}
	return ip.IsPrivate()
}

func ensureProbeLocalDirectBypassForRoutedTarget(targetAddr string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
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
	if probeLocalDirectBypassState.managedRefs == nil {
		probeLocalDirectBypassState.managedRefs = map[string]int{}
	}
	if probeLocalDirectBypassState.managedRefs[ipText] > 0 {
		probeLocalDirectBypassState.mu.Unlock()
		return nil
	}
	probeLocalDirectBypassState.mu.Unlock()

	if err := ensureProbeLocalDirectBypassForTarget(net.JoinHostPort(ipText, port)); err != nil {
		return err
	}
	probeLocalDirectBypassState.mu.Lock()
	if probeLocalDirectBypassState.managedRefs == nil {
		probeLocalDirectBypassState.managedRefs = map[string]int{}
	}
	probeLocalDirectBypassState.managedRefs[ipText] = 1
	probeLocalDirectBypassState.mu.Unlock()
	return nil
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
	if probeLocalDirectBypassState.managedRefs == nil {
		probeLocalDirectBypassState.managedRefs = map[string]int{}
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
		delete(probeLocalDirectBypassState.managedRefs, ip)
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
	probeLocalDirectBypassState.managedRefs = map[string]int{}
	probeLocalDirectBypassState.mu.Unlock()
}

func releaseProbeLocalManagedDirectBypassRoutes() {
	probeLocalDirectBypassState.mu.Lock()
	refs := make(map[string]int, len(probeLocalDirectBypassState.managedRefs))
	for ip, count := range probeLocalDirectBypassState.managedRefs {
		if count > 0 {
			refs[ip] = count
		}
	}
	probeLocalDirectBypassState.managedRefs = map[string]int{}
	probeLocalDirectBypassState.mu.Unlock()

	for ip, count := range refs {
		for i := 0; i < count; i++ {
			releaseProbeLocalDirectBypassForHost(ip)
		}
	}
}

func resetProbeLocalDirectBypassStateForTest() {
	probeLocalDirectBypassState.mu.Lock()
	probeLocalDirectBypassState.ref = map[string]int{}
	probeLocalDirectBypassState.hosts = map[string]string{}
	probeLocalDirectBypassState.targets = map[string]map[string]struct{}{}
	probeLocalDirectBypassState.routes = map[string]probeLocalWindowsDirectBypassRouteTarget{}
	probeLocalDirectBypassState.managedRefs = map[string]int{}
	probeLocalDirectBypassState.mu.Unlock()
	clearProbeLocalWindowsDirectBypassRouteTarget()

	probeLocalTUNUDPSourceState.mu.Lock()
	probeLocalTUNUDPSourceState.refs = map[string]int64{}
	probeLocalTUNUDPSourceState.mu.Unlock()
	resetProbeLocalTUNTCPDirectFailureCacheForTest()
	probeLocalTUNTCPFailureLogState.mu.Lock()
	probeLocalTUNTCPFailureLogState.items = map[string]probeLocalTUNTCPFailureLogEntry{}
	probeLocalTUNTCPFailureLogState.mu.Unlock()

	probeLocalAcquireDirectBypassRoute = acquireProbeLocalTUNDirectBypassRoute
	probeLocalReleaseDirectBypassRoute = releaseProbeLocalTUNDirectBypassRoute
}
