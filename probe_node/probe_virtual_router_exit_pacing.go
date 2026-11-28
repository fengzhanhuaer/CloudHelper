package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"

	"github.com/cloudhelper/probe_node/internal/tcpwindow"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

const (
	probeExitPacingInitial   = 4_000_000
	probeExitPacingMinimum   = 500_000
	probeExitPacingMaximum   = tcpwindow.Payload * 1_000 / 300
	probeExitPacingGrowth    = 100 * time.Millisecond
	probeExitPacingBackoff   = 20 * time.Millisecond
	probeExitPacingBatch     = 64 << 10
	probeExitPacingQueueHigh = 64
)

type probeExitTCPPacer struct {
	bytesPerSecond int
	nextWrite      time.Time
	nextGrowth     time.Time
	nextBackoff    time.Time
}

func (p *probeExitTCPPacer) update(now time.Time, pressured, busy bool) {
	if p.bytesPerSecond == 0 {
		p.bytesPerSecond = probeExitPacingInitial
		p.nextGrowth = now.Add(probeExitPacingGrowth)
	}
	if pressured {
		if !now.Before(p.nextBackoff) {
			p.bytesPerSecond = max(probeExitPacingMinimum, p.bytesPerSecond*3/4)
			p.nextBackoff = now.Add(probeExitPacingBackoff)
		}
		p.nextGrowth = now.Add(probeExitPacingGrowth)
		return
	}
	if busy && !now.Before(p.nextGrowth) {
		p.bytesPerSecond = min(probeExitPacingMaximum, p.bytesPerSecond*3/2)
		p.nextGrowth = now.Add(probeExitPacingGrowth)
	}
}

func (p *probeExitTCPPacer) wait(now time.Time) time.Duration {
	return max(0, p.nextWrite.Sub(now))
}

func (p *probeExitTCPPacer) recordSend(now time.Time, bytes int) {
	// A late scheduler tick can catch up within one batch, without saving idle credit.
	if p.nextWrite.IsZero() || now.Sub(p.nextWrite) > time.Millisecond {
		p.nextWrite = now
	}
	p.nextWrite = p.nextWrite.Add(time.Duration(bytes) * time.Second / time.Duration(p.bytesPerSecond))
}

type probeExitPacedConn struct {
	net.Conn
	netstack     *probeVirtualRouterExitNetstack
	shard        *probeAdaptiveQueue[probeVirtualRouterExitNetstackOutputPacket]
	closed       chan struct{}
	closeOnce    sync.Once
	pacer        probeExitTCPPacer
	timer        *time.Timer
	writeBlocked bool
}

func (n *probeVirtualRouterExitNetstack) paceTCPReply(conn net.Conn, id stack.TransportEndpointID) net.Conn {
	if len(n.outputDispatch) == 0 || id.LocalAddress.Len() != 4 || id.RemoteAddress.Len() != 4 {
		return conn
	}
	// Match the IPv4 flow hash used by output dispatch.
	var packet [40]byte
	packet[0], packet[9] = 0x45, 6
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	copy(packet[12:16], id.LocalAddress.AsSlice())
	copy(packet[16:20], id.RemoteAddress.AsSlice())
	binary.BigEndian.PutUint16(packet[20:22], id.LocalPort)
	binary.BigEndian.PutUint16(packet[22:24], id.RemotePort)
	shard := probeVirtualRouterPacketDispatchShard(packet[:], len(n.outputDispatch))
	return &probeExitPacedConn{Conn: conn, netstack: n, shard: n.outputDispatch[shard], closed: make(chan struct{})}
}

func (c *probeExitPacedConn) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		now := time.Now()
		pressure := c.shard.Len() >= probeExitPacingQueueHigh || c.netstack.linkEP.NumQueued() >= probeExitPacingQueueHigh
		delay := c.pacer.wait(now)
		c.pacer.update(now, pressure || c.writeBlocked, delay > 0 || len(data) >= 16<<10)
		if pressure {
			delay = max(delay, 2*time.Millisecond)
		}
		if delay > 0 {
			if c.timer == nil {
				c.timer = time.NewTimer(delay)
			} else {
				c.timer.Reset(delay)
			}
			select {
			case <-c.timer.C:
			case <-c.closed:
				c.timer.Stop()
				return written, net.ErrClosed
			case <-c.netstack.doneCh:
				c.timer.Stop()
				return written, context.Canceled
			}
		}
		batch := min(len(data), probeExitPacingBatch, max(16<<10, c.pacer.bytesPerSecond/500))
		started := time.Now()
		n, err := c.Conn.Write(data[:batch])
		c.writeBlocked = time.Since(started) > probeExitPacingBackoff
		c.pacer.recordSend(started, n)
		written += n
		if err != nil {
			return written, err
		}
		if n != batch {
			return written, io.ErrShortWrite
		}
		data = data[n:]
	}
	return written, nil
}

func (c *probeExitPacedConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func (c *probeExitPacedConn) CloseRead() error {
	shutdownProbeVirtualRouterExitConnRead(c.Conn)
	return nil
}

func (c *probeExitPacedConn) CloseWrite() error {
	shutdownProbeVirtualRouterExitConnWrite(c.Conn)
	return nil
}
