//go:build windows

package backend

import (
	"context"
	"errors"
	"io"
	"net"
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
	localTUNNetstackNICID         = tcpip.NICID(1)
	localTUNNetstackQueueSize     = 4096
	localTUNNetstackMTU           = 1500
	localTUNTCPDialTimeout        = 10 * time.Second
	localTUNUDPAssociationTimeout = 60 * time.Second
	localTUNUDPReadBufferSize     = 65535
	localTUNTCPForwarderWindow    = 0
	localTUNTCPForwarderInFlight  = 2048
)

type localTUNDuplexConn interface {
	net.Conn
	CloseWrite() error
	CloseRead() error
}

type localTUNReadDeadliner interface {
	SetReadDeadline(time.Time) error
}

type localTUNNetstack struct {
	service *networkAssistantService

	stack  *stack.Stack
	linkEP *channel.Endpoint

	cancel context.CancelFunc
	doneCh chan struct{}

	closeOnce sync.Once
	closed    atomic.Bool
}

type localTUNUDPBridge struct {
	service  *networkAssistantService
	inbound  *gonet.UDPConn
	outbound io.ReadWriteCloser
	timeout  time.Duration

	closeOnce sync.Once
}

type tunnelStreamNetAddr struct {
	network string
	address string
}

func (a tunnelStreamNetAddr) Network() string {
	return a.network
}

func (a tunnelStreamNetAddr) String() string {
	return a.address
}

type tunnelStreamNetConn struct {
	stream *tunnelMuxStream

	localAddr  net.Addr
	remoteAddr net.Addr

	readMu    sync.Mutex
	writeMu   sync.Mutex
	closeOnce sync.Once
	closed    atomic.Bool
	pending   []byte
}

type directBypassManagedConn struct {
	net.Conn
	release   func()
	closeOnce sync.Once
}

func (c *directBypassManagedConn) Close() error {
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

func (s *networkAssistantService) startLocalTUNPacketStack() error {
	s.mu.Lock()
	if s.tunPacketStack != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	netstackRunner, err := newLocalTUNNetstack(s)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if s.tunPacketStack != nil {
		s.mu.Unlock()
		_ = netstackRunner.Close()
		return nil
	}
	s.tunPacketStack = netstackRunner
	s.tunUDPHandler = nil
	s.mu.Unlock()

	s.logf("local tun gvisor netstack started")
	return nil
}

func (s *networkAssistantService) stopLocalTUNPacketStack() error {
	s.mu.Lock()
	stackRunner := s.tunPacketStack
	s.tunPacketStack = nil
	s.tunUDPHandler = nil
	s.mu.Unlock()

	if stackRunner == nil {
		return nil
	}
	return stackRunner.Close()
}

func newLocalTUNNetstack(service *networkAssistantService) (*localTUNNetstack, error) {
	if service == nil {
		return nil, errors.New("nil network assistant service")
	}

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

	linkEP := channel.New(localTUNNetstackQueueSize, localTUNNetstackMTU, "")
	if err := tcpipErrToError(gStack.CreateNIC(localTUNNetstackNICID, linkEP)); err != nil {
		return nil, err
	}
	if err := tcpipErrToError(gStack.SetPromiscuousMode(localTUNNetstackNICID, true)); err != nil {
		return nil, err
	}
	if err := tcpipErrToError(gStack.SetSpoofing(localTUNNetstackNICID, true)); err != nil {
		return nil, err
	}

	gStack.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: localTUNNetstackNICID},
		{Destination: header.IPv6EmptySubnet, NIC: localTUNNetstackNICID},
	})

	ctx, cancel := context.WithCancel(context.Background())
	netstackRunner := &localTUNNetstack{
		service: service,
		stack:   gStack,
		linkEP:  linkEP,
		cancel:  cancel,
		doneCh:  make(chan struct{}),
	}

	tcpForwarder := tcp.NewForwarder(gStack, localTUNTCPForwarderWindow, localTUNTCPForwarderInFlight, netstackRunner.handleTCPForwarder)
	udpForwarder := udp.NewForwarder(gStack, netstackRunner.handleUDPForwarder)
	gStack.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)
	gStack.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder.HandlePacket)

	go netstackRunner.outputLoop(ctx)
	return netstackRunner, nil
}

func (n *localTUNNetstack) outputLoop(ctx context.Context) {
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

		n.service.mu.RLock()
		dataPlane := n.service.tunDataPlane
		n.service.mu.RUnlock()
		if dataPlane == nil {
			continue
		}
		if err := dataPlane.WritePacket(payload); err != nil {
			n.service.logf("local tun netstack write packet failed: %v", err)
		}
	}
}

func (n *localTUNNetstack) Write(packet []byte) (int, error) {
	if len(packet) == 0 {
		return 0, nil
	}
	if n == nil || n.closed.Load() {
		return 0, io.ErrClosedPipe
	}

	protocol, err := localTUNProtocolFromPacket(packet)
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

func (n *localTUNNetstack) Close() error {
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
	})
	return nil
}

func (n *localTUNNetstack) handleTCPForwarder(req *tcp.ForwarderRequest) {
	if req == nil {
		return
	}

	id := req.ID()
	targetAddr, err := transportIDToTarget(id.LocalAddress, id.LocalPort)
	if err != nil {
		req.Complete(true)
		return
	}

	var wq waiter.Queue
	ep, createErr := req.CreateEndpoint(&wq)
	if createErr != nil {
		req.Complete(true)
		n.service.logfRateLimited(
			"tun:tcp:create_failed:"+strings.ToLower(strings.TrimSpace(targetAddr)),
			3*time.Second,
			"local tun tcp create endpoint failed: target=%s reason=%s err=%s",
			targetAddr,
			classifyTUNEndpointCreateFailure(createErr.String()),
			createErr.String(),
		)
		return
	}
	req.Complete(false)

	inbound := gonet.NewTCPConn(&wq, ep)
	outbound, route, openErr := n.openOutboundTCP(targetAddr)
	if openErr != nil {
		_ = inbound.Close()
		reason := classifyTUNRouteOpenFailure(route, openErr)
		n.service.logfRateLimited(
			"tun:tcp:open_failed:"+strings.ToLower(strings.TrimSpace(targetAddr)),
			3*time.Second,
			"local tun tcp route open failed: target=%s routed=%s direct=%v node=%s group=%s reason=%s timeout=%s err=%v",
			targetAddr,
			route.TargetAddr,
			route.Direct,
			route.NodeID,
			route.Group,
			reason,
			localTUNTCPDialTimeout,
			openErr,
		)
		return
	}

	n.service.logfRateLimited(
		"tun:tcp:connected:"+strings.ToLower(strings.TrimSpace(targetAddr)),
		2*time.Second,
		"local tun tcp relay connected: target=%s routed=%s direct=%v node=%s group=%s",
		targetAddr,
		route.TargetAddr,
		route.Direct,
		route.NodeID,
		route.Group,
	)
	n.relayTCP(inbound, outbound)
}

func (n *localTUNNetstack) handleUDPForwarder(req *udp.ForwarderRequest) {
	if req == nil {
		return
	}

	id := req.ID()
	targetAddr, err := transportIDToTarget(id.LocalAddress, id.LocalPort)
	if err != nil {
		return
	}

	var wq waiter.Queue
	ep, createErr := req.CreateEndpoint(&wq)
	if createErr != nil {
		n.service.logfRateLimited(
			"tun:udp:create_failed:"+strings.ToLower(strings.TrimSpace(targetAddr)),
			3*time.Second,
			"local tun udp create endpoint failed: target=%s reason=%s err=%s",
			targetAddr,
			classifyTUNEndpointCreateFailure(createErr.String()),
			createErr.String(),
		)
		return
	}
	inbound := gonet.NewUDPConn(&wq, ep)

	outbound, route, openErr := n.openOutboundUDP(targetAddr)
	if openErr != nil {
		_ = inbound.Close()
		reason := classifyTUNRouteOpenFailure(route, openErr)
		n.service.logfRateLimited(
			"tun:udp:open_failed:"+strings.ToLower(strings.TrimSpace(targetAddr)),
			3*time.Second,
			"local tun udp route open failed: target=%s routed=%s direct=%v node=%s group=%s reason=%s err=%v",
			targetAddr,
			route.TargetAddr,
			route.Direct,
			route.NodeID,
			route.Group,
			reason,
			openErr,
		)
		return
	}

	bridge := &localTUNUDPBridge{
		service:  n.service,
		inbound:  inbound,
		outbound: outbound,
		timeout:  localTUNUDPAssociationTimeout,
	}
	bridge.start()
	n.service.logfRateLimited(
		"tun:udp:associated:"+strings.ToLower(strings.TrimSpace(targetAddr)),
		2*time.Second,
		"local tun udp association created: target=%s routed=%s direct=%v node=%s group=%s",
		targetAddr,
		route.TargetAddr,
		route.Direct,
		route.NodeID,
		route.Group,
	)
}

func (n *localTUNNetstack) openOutboundTCP(targetAddr string) (net.Conn, tunnelRouteDecision, error) {
	route, err := n.service.decideRouteForTarget(targetAddr)
	if err != nil {
		return nil, tunnelRouteDecision{}, err
	}

	if route.Direct {
		release, bypassErr := n.service.acquireTUNDirectBypassRoute(route.TargetAddr)
		if bypassErr != nil {
			return nil, route, bypassErr
		}
		conn, dialErr := net.DialTimeout("tcp", route.TargetAddr, localTUNTCPDialTimeout)
		if dialErr != nil {
			release()
			return nil, route, dialErr
		}
		return &directBypassManagedConn{Conn: conn, release: release}, route, nil
	}

	stream, openErr := n.service.openTunnelStreamForNode("tcp", route.TargetAddr, route.NodeID)
	if openErr != nil {
		return nil, route, openErr
	}
	return newTunnelStreamNetConn(stream, route.TargetAddr), route, nil
}

func (n *localTUNNetstack) openOutboundUDP(targetAddr string) (io.ReadWriteCloser, tunnelRouteDecision, error) {
	route, err := n.service.decideRouteForTarget(targetAddr)
	if err != nil {
		return nil, tunnelRouteDecision{}, err
	}

	if route.Direct {
		release, bypassErr := n.service.acquireTUNDirectBypassRoute(route.TargetAddr)
		if bypassErr != nil {
			return nil, route, bypassErr
		}
		udpAddr, resolveErr := net.ResolveUDPAddr("udp", route.TargetAddr)
		if resolveErr != nil {
			release()
			return nil, route, resolveErr
		}
		conn, dialErr := dialUDPWithRetry("udp", nil, udpAddr)
		if dialErr != nil {
			release()
			return nil, route, dialErr
		}
		return &directBypassManagedConn{Conn: conn, release: release}, route, nil
	}

	stream, openErr := n.service.openTunnelStreamForNode("udp", route.TargetAddr, route.NodeID)
	if openErr != nil {
		return nil, route, openErr
	}
	return newTunnelStreamNetConn(stream, route.TargetAddr), route, nil
}

func (n *localTUNNetstack) relayTCP(inbound net.Conn, outbound net.Conn) {
	go n.pipeAndCloseTCP(outbound, inbound)
	go n.pipeAndCloseTCP(inbound, outbound)
}

func (n *localTUNNetstack) pipeAndCloseTCP(dst net.Conn, src net.Conn) {
	defer closeConnWrite(dst)
	defer closeConnRead(src)
	_, _ = io.Copy(dst, src)
}

func (b *localTUNUDPBridge) start() {
	go b.forwardInboundToOutbound()
	go b.forwardOutboundToInbound()
}

func (b *localTUNUDPBridge) forwardInboundToOutbound() {
	buf := make([]byte, localTUNUDPReadBufferSize)
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

func (b *localTUNUDPBridge) forwardOutboundToInbound() {
	buf := make([]byte, localTUNUDPReadBufferSize)
	for {
		if b.timeout > 0 {
			if deadliner, ok := b.outbound.(localTUNReadDeadliner); ok {
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

func (b *localTUNUDPBridge) close() {
	b.closeOnce.Do(func() {
		if b.inbound != nil {
			_ = b.inbound.Close()
		}
		if b.outbound != nil {
			_ = b.outbound.Close()
		}
	})
}

func closeConnRead(conn net.Conn) {
	if conn == nil {
		return
	}
	if duplex, ok := conn.(localTUNDuplexConn); ok {
		_ = duplex.CloseRead()
		return
	}
	_ = conn.Close()
}

func classifyTUNEndpointCreateFailure(rawErr string) string {
	errText := strings.ToLower(strings.TrimSpace(rawErr))
	switch {
	case strings.Contains(errText, "port is in use"):
		return "local_port_in_use"
	case strings.Contains(errText, "no buffer") || strings.Contains(errText, "queue was full"):
		return "socket_buffer_exhausted"
	default:
		return "endpoint_create_failed"
	}
}

func classifyTUNRouteOpenFailure(route tunnelRouteDecision, err error) string {
	if err == nil {
		return "unknown"
	}
	if isRuleRouteRejectErr(err) {
		return "rule_reject"
	}
	errText := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(errText, "i/o timeout") || strings.Contains(errText, "context deadline exceeded"):
		if route.Direct {
			return "direct_dial_timeout"
		}
		return "tunnel_open_timeout"
	case strings.Contains(errText, "only one usage of each socket address") || strings.Contains(errText, "address already in use"):
		return "local_ephemeral_port_exhausted"
	case strings.Contains(errText, "queue was full") || strings.Contains(errText, "no space left") || strings.Contains(errText, "sufficient buffer space") || strings.Contains(errText, "wsaenobufs"):
		return "socket_buffer_exhausted"
	case strings.Contains(errText, "network is unreachable") || strings.Contains(errText, "no route to host"):
		return "route_unreachable"
	case strings.Contains(errText, "connection refused"):
		return "target_refused"
	case strings.Contains(errText, "chain target config not found"):
		return "chain_target_missing"
	case strings.Contains(errText, "auth rejected") || strings.Contains(errText, "auth failed"):
		return "chain_auth_failed"
	case strings.Contains(errText, "missing controller") || strings.Contains(errText, "session token"):
		return "control_plane_not_ready"
	default:
		if route.Direct {
			return "direct_route_open_failed"
		}
		return "tunnel_route_open_failed"
	}
}

func closeConnWrite(conn net.Conn) {
	if conn == nil {
		return
	}
	if duplex, ok := conn.(localTUNDuplexConn); ok {
		_ = duplex.CloseWrite()
		return
	}
	_ = conn.Close()
}

func transportIDToTarget(addr tcpip.Address, port uint16) (string, error) {
	if port == 0 {
		return "", errors.New("transport target port is empty")
	}
	host := strings.TrimSpace(addr.String())
	if host == "" {
		return "", errors.New("transport target address is empty")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func tcpipErrToError(err tcpip.Error) error {
	if err == nil {
		return nil
	}
	return errors.New(err.String())
}

func localTUNProtocolFromPacket(packet []byte) (tcpip.NetworkProtocolNumber, error) {
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

func newTunnelStreamNetConn(stream *tunnelMuxStream, remoteAddr string) *tunnelStreamNetConn {
	return &tunnelStreamNetConn{
		stream:     stream,
		localAddr:  tunnelStreamNetAddr{network: "tunnel", address: "local"},
		remoteAddr: tunnelStreamNetAddr{network: "tunnel", address: strings.TrimSpace(remoteAddr)},
		pending:    make([]byte, 0),
	}
}

func (c *tunnelStreamNetConn) Read(data []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	if len(c.pending) > 0 {
		n := copy(data, c.pending)
		if n >= len(c.pending) {
			c.pending = c.pending[:0]
		} else {
			c.pending = append(c.pending[:0], c.pending[n:]...)
		}
		return n, nil
	}

	for {
		if c.closed.Load() {
			return 0, io.EOF
		}
		select {
		case payload := <-c.stream.readCh:
			if len(payload) == 0 {
				continue
			}
			n := copy(data, payload)
			if n < len(payload) {
				c.pending = append(c.pending[:0], payload[n:]...)
			}
			return n, nil
		case err := <-c.stream.errCh:
			if err == nil {
				err = io.EOF
			}
			return 0, err
		}
	}
}

func (c *tunnelStreamNetConn) Write(data []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	if err := c.stream.write(data); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (c *tunnelStreamNetConn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		if c.stream != nil {
			c.stream.close()
		}
	})
	return nil
}

func (c *tunnelStreamNetConn) CloseWrite() error {
	return c.Close()
}

func (c *tunnelStreamNetConn) CloseRead() error {
	return c.Close()
}

func (c *tunnelStreamNetConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *tunnelStreamNetConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (c *tunnelStreamNetConn) SetDeadline(_ time.Time) error {
	return nil
}

func (c *tunnelStreamNetConn) SetReadDeadline(_ time.Time) error {
	return nil
}

func (c *tunnelStreamNetConn) SetWriteDeadline(_ time.Time) error {
	return nil
}
