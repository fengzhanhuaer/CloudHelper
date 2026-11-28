package externalspeed

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sync"

	"github.com/cloudhelper/probe_node/internal/tcpwindow"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

// PacketConn owns one temporary TCP stack. It sends normal IP business frames;
// only the reverse tuple belongs to this test. No global route is modified.
type PacketConn struct {
	mu            sync.Mutex
	stack         *stack.Stack
	link          *channel.Endpoint
	local, remote netip.Addr
	port          uint16
	remotePort    uint16
	closed        bool
	cancel        context.CancelFunc
	done          chan struct{}
	releasePort   func()
}

func NewPacketConn(ctx context.Context, local, remote string, send func([]byte) error) (*PacketConn, error) {
	return NewPacketConnWithPort(ctx, local, remote, 443, send)
}

func NewPacketConnWithPort(ctx context.Context, local, remote string, remotePort uint16, send func([]byte) error) (*PacketConn, error) {
	if remotePort != 80 && remotePort != 443 {
		return nil, errors.New("invalid test destination port")
	}
	a, e := netip.ParseAddr(local)
	if e != nil || !a.Is4() {
		return nil, errors.New("invalid test source IPv4")
	}
	b, e := netip.ParseAddr(remote)
	if e != nil || !b.Is4() {
		return nil, errors.New("invalid test destination IPv4")
	}
	s := stack.New(stack.Options{NetworkProtocols: []stack.NetworkProtocolFactory{ipv4.NewProtocol}, TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol}})
	if err := tcpwindow.ConfigureStack(s); err != nil {
		s.Destroy()
		return nil, err
	}
	link := channel.New(256, 1400, "")
	if err := s.CreateNIC(1, link); err != nil {
		s.Destroy()
		return nil, errors.New(err.String())
	}
	if err := s.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: tcpip.AddressWithPrefix{Address: tcpip.AddrFrom4(a.As4()), PrefixLen: 32}}, stack.AddressProperties{}); err != nil {
		s.Destroy()
		return nil, errors.New(err.String())
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})
	child, cancel := context.WithCancel(ctx)
	port, release, err := reservePort()
	if err != nil {
		cancel()
		s.Destroy()
		link.Close()
		return nil, err
	}
	p := &PacketConn{stack: s, link: link, local: a, remote: b, port: port, remotePort: remotePort, releasePort: release, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		for {
			pkt := link.ReadContext(child)
			if pkt == nil {
				return
			}
			v := pkt.ToView()
			data := append([]byte(nil), v.AsSlice()...)
			v.Release()
			pkt.DecRef()
			if err := send(data); err != nil {
				cancel()
				return
			}
		}
	}()
	return p, nil
}

func (p *PacketConn) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	return gonet.DialTCPWithBind(ctx, p.stack, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(p.local.As4()), Port: p.port}, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(p.remote.As4()), Port: p.remotePort}, ipv4.ProtocolNumber)
}

func (p *PacketConn) Feed(data []byte) bool {
	if len(data) < 40 || data[0]>>4 != 4 || data[9] != 6 {
		return false
	}
	ihl := int(data[0]&15) * 4
	if ihl < 20 || ihl+20 > len(data) || binary.BigEndian.Uint16(data[6:8])&0x3fff != 0 {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.port == 0 || netip.AddrFrom4([4]byte(data[12:16])) != p.remote || netip.AddrFrom4([4]byte(data[16:20])) != p.local || binary.BigEndian.Uint16(data[ihl:]) != p.remotePort || binary.BigEndian.Uint16(data[ihl+2:]) != p.port {
		return false
	}
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(data)})
	p.link.InjectInbound(ipv4.ProtocolNumber, pkt)
	pkt.DecRef()
	return true
}

func (p *PacketConn) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	p.cancel()
	<-p.done
	p.stack.Destroy()
	p.link.Close()
	p.releasePort()
}
