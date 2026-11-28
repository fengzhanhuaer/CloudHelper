package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"

	"github.com/cloudhelper/probe_node/internal/tcpwindow"
	"golang.org/x/time/rate"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

const (
	probeExitPacingInitial   = 4_000_000
	probeExitPacingMinimum   = 500_000
	probeExitPacingMaximum   = tcpwindow.BitsPerSecond / 8
	probeExitPacingGrowth    = 100 * time.Millisecond
	probeExitPacingBackoff   = 20 * time.Millisecond
	probeExitPacingBatch     = 64 << 10
	probeExitPacingQueueHigh = 64
	probeExitPeerReportGap   = 150 * time.Millisecond
	probeExitPeerReportTTL   = 450 * time.Millisecond
	probeExitPeerBatch       = 16 << 10
	probeExitPeerRecovery    = 2 * time.Second
)

var probeExitNodeLimiter = newProbeExitNodeLimiter()

func newProbeExitNodeLimiter() *rate.Limiter {
	return rate.NewLimiter(rate.Limit(probeExitPacingMaximum), probeExitPacingBatch)
}

type probeExitTCPPacer struct {
	bytesPerSecond int
	nextWrite      time.Time
	nextGrowth     time.Time
	nextBackoff    time.Time
	lastPeerReport int64
}

func (rt *probeVirtualRouterRuntime) recordPeerTUNPressure(now time.Time, path []string) {
	if rt == nil || len(path) != 2 || path[0] != rt.cfg.peerNodeID || path[1] != rt.cfg.localNodeID {
		return
	}
	previous := rt.peerTUNPressure.Load()
	if previous == 0 || now.UnixNano()-previous >= int64(probeExitPeerReportGap) {
		rt.peerTUNPressure.CompareAndSwap(previous, now.UnixNano())
	}
}

func (p *probeExitTCPPacer) applyPeerPressure(now time.Time, report int64) bool {
	elapsed := now.UnixNano() - report
	if report <= 0 || elapsed < 0 || elapsed >= int64(probeExitPeerReportTTL) {
		return false
	}
	if report > p.lastPeerReport {
		p.lastPeerReport = report
		if p.bytesPerSecond > probeExitPacingInitial {
			p.bytesPerSecond = max(probeExitPacingInitial, p.bytesPerSecond*15/16)
		}
		p.nextGrowth = now.Add(probeExitPeerReportTTL)
	}
	return true
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
		growth := p.bytesPerSecond / 2
		if p.lastPeerReport > 0 && now.UnixNano()-p.lastPeerReport < int64(probeExitPeerRecovery) {
			growth = p.bytesPerSecond / 8
		}
		p.bytesPerSecond = min(probeExitPacingMaximum, p.bytesPerSecond+growth)
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
	peer         *probeVirtualRouterRuntime
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
	var peer *probeVirtualRouterRuntime
	path := currentProbeVirtualRouterPathForPacket(packet[:], id.RemoteAddress.String())
	if len(path) == 2 {
		peer, _ = probeVirtualRouterRuntimeForAdjacentNode(path[1])
	}
	return &probeExitPacedConn{Conn: conn, netstack: n, shard: n.outputDispatch[shard], peer: peer, closed: make(chan struct{})}
}

func (c *probeExitPacedConn) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		now := time.Now()
		pressure := c.shard.Len() >= probeExitPacingQueueHigh || c.netstack.linkEP.NumQueued() >= probeExitPacingQueueHigh
		delay := c.pacer.wait(now)
		busy := delay > 0 || len(data) >= 16<<10
		lastPeerReport := c.pacer.lastPeerReport
		peerPressure := c.peer != nil && busy && c.pacer.applyPeerPressure(now, c.peer.peerTUNPressure.Load())
		if c.pacer.lastPeerReport != lastPeerReport {
			c.netstack.peerPressureApplied.Add(1)
		}
		c.pacer.update(now, pressure || c.writeBlocked, busy && !peerPressure)
		if pressure {
			delay = max(delay, 2*time.Millisecond)
		}
		if err := c.wait(delay); err != nil {
			return written, err
		}
		batch := min(len(data), probeExitPacingBatch, max(16<<10, c.pacer.bytesPerSecond/500))
		if peerPressure {
			batch = min(batch, probeExitPeerBatch)
		}
		reservation := probeExitNodeLimiter.ReserveN(time.Now(), batch)
		if err := c.wait(reservation.Delay()); err != nil {
			reservation.Cancel()
			return written, err
		}
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

func (c *probeExitPacedConn) wait(delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if c.timer == nil {
		c.timer = time.NewTimer(delay)
	} else {
		c.timer.Reset(delay)
	}
	select {
	case <-c.timer.C:
		return nil
	case <-c.closed:
		c.timer.Stop()
		return net.ErrClosed
	case <-c.netstack.doneCh:
		c.timer.Stop()
		return context.Canceled
	}
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
