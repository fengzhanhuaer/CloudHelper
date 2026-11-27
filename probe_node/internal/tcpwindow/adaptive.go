package tcpwindow

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
)

const interval = 300 * time.Millisecond
const quietPeriod = 30 * time.Second

type direction struct {
	limit    int
	lowSince time.Time
}

func (d *direction) update(bytes uint64, elapsed time.Duration, now time.Time) bool {
	old := d.limit
	demand := float64(bytes) * float64(interval) / float64(elapsed)
	switch {
	case demand >= float64(old)/4:
		// Leave headroom for another RTT of growth.
		d.limit = min(Payload, max(old*2, int(min(4*demand, float64(Payload)))))
		d.lowSince = time.Time{}
	case demand <= float64(old)/8 && old > InitialPayload:
		if d.lowSince.IsZero() {
			d.lowSince = now
		} else if now.Sub(d.lowSince) >= quietPeriod {
			d.limit = max(InitialPayload, old/2)
			d.lowSince = now
		}
	default:
		d.lowSince = time.Time{}
	}
	return old != d.limit
}

// One sampler per process, no goroutine or timer per idle socket. The read
// and write paths only count bytes; socket options change on budget changes.
type meter struct {
	read, written atomic.Uint64
	rx, tx        direction
	last          time.Time
	set           func(receive bool, payload int)
}

var meters struct {
	sync.Mutex
	items map[*meter]struct{}
}

func newMeter(set func(bool, int)) *meter {
	m := &meter{last: time.Now(), set: set, rx: direction{limit: InitialPayload}, tx: direction{limit: InitialPayload}}
	set(true, InitialPayload)
	set(false, InitialPayload)
	meters.Lock()
	if meters.items == nil {
		meters.items = make(map[*meter]struct{})
		go sample()
	}
	meters.items[m] = struct{}{}
	meters.Unlock()
	return m
}

func sample() {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		meters.Lock()
		if len(meters.items) == 0 {
			meters.items = nil
			meters.Unlock()
			return
		}
		now := time.Now()
		for m := range meters.items {
			elapsed := now.Sub(m.last)
			if elapsed < interval/2 {
				continue
			}
			if m.rx.update(m.read.Swap(0), elapsed, now) {
				m.set(true, m.rx.limit)
			}
			if m.tx.update(m.written.Swap(0), elapsed, now) {
				m.set(false, m.tx.limit)
			}
			m.last = now
		}
		meters.Unlock()
	}
}

func (m *meter) stop() { meters.Lock(); delete(meters.items, m); meters.Unlock() }

type measuredConn struct {
	net.Conn
	m *meter
}

func (c *measuredConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.m.read.Add(uint64(n))
	return n, err
}
func (c *measuredConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.m.written.Add(uint64(n))
	return n, err
}
func (c *measuredConn) Close() error { c.m.stop(); return c.Conn.Close() }

// Retain half-close support for relay io.Copy loops.
type measuredEndpoint struct {
	*measuredConn
	tcp *gonet.TCPConn
}

func (c *measuredEndpoint) CloseWrite() error { return c.tcp.CloseWrite() }
func (c *measuredEndpoint) CloseRead() error  { return c.tcp.CloseRead() }

func Endpoint(conn *gonet.TCPConn, ep tcpip.Endpoint) net.Conn {
	opts := ep.SocketOptions()
	m := newMeter(func(receive bool, payload int) {
		// After scale negotiation the sampler owns the budget. Notify gVisor
		// so growth can reopen the advertised window and wake blocked writers.
		if receive {
			opts.SetReceiveBufferSize(int64(2*payload), true)
		} else {
			opts.SetSendBufferSize(int64(2*payload), true)
		}
	})
	return &measuredEndpoint{&measuredConn{conn, m}, conn}
}

func Carrier(conn, underlying net.Conn) net.Conn {
	for {
		w, ok := underlying.(interface{ NetConn() net.Conn })
		if !ok {
			break
		}
		underlying = w.NetConn()
	}
	tcp, ok := underlying.(*net.TCPConn)
	if !ok {
		return conn
	}
	set := nativeSetter(tcp)
	if set == nil {
		return conn
	}
	return &measuredConn{conn, newMeter(set)}
}
