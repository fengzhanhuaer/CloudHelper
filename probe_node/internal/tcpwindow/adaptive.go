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
const minimumShare = 32 << 10

type direction struct {
	limit       int
	applied     int
	target      int
	lowSince    time.Time
	activeUntil time.Time
}

func (d *direction) update(bytes uint64, elapsed time.Duration, now time.Time) bool {
	old := d.limit
	demand := float64(bytes) * float64(interval) / float64(elapsed)
	if demand >= minimumShare/4 {
		d.activeUntil = now.Add(time.Second)
	}
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
	pool          *meterPool
}

type meterPool struct {
	sync.Mutex
	items map[*meter]struct{}
}

var endpointMeters, carrierMeters, egressMeters meterPool

func newMeter(pool *meterPool, set func(bool, int)) *meter {
	now := time.Now()
	m := &meter{last: now, set: set, pool: pool, rx: direction{limit: InitialPayload}, tx: direction{limit: InitialPayload}}
	pool.Lock()
	if pool.items == nil {
		pool.items = make(map[*meter]struct{})
		go pool.sample()
	}
	pool.items[m] = struct{}{}
	pool.applyLocked(now)
	pool.Unlock()
	return m
}

func (p *meterPool) sample() {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		p.Lock()
		if len(p.items) == 0 {
			p.items = nil
			p.Unlock()
			return
		}
		now := time.Now()
		for m := range p.items {
			elapsed := now.Sub(m.last)
			if elapsed < interval/2 {
				continue
			}
			m.rx.update(m.read.Swap(0), elapsed, now)
			m.tx.update(m.written.Swap(0), elapsed, now)
			m.last = now
		}
		p.applyLocked(now)
		p.Unlock()
	}
}

func (p *meterPool) applyLocked(now time.Time) {
	if len(p.items) == 0 {
		return
	}
	// Both directions share the layer's budget. Inactive connections keep a small allowance
	// under contention. Remaining capacity is split proportionally among active directions.
	floor := min(minimumShare, Payload/(2*len(p.items)))
	var active, idle int64
	for m := range p.items {
		for _, d := range [...]*direction{&m.rx, &m.tx} {
			extra := int64(d.limit - floor)
			if now.Before(d.activeUntil) {
				active += extra
			} else {
				idle += extra
			}
		}
	}
	remaining := int64(Payload - (2 * len(p.items) * floor))
	activeShare := min(remaining, active)
	idleShare := min(remaining-activeShare, idle)
	for m := range p.items {
		for _, d := range [...]*direction{&m.rx, &m.tx} {
			demand, share := idle, idleShare
			if now.Before(d.activeUntil) {
				demand, share = active, activeShare
			}
			d.target = floor
			if demand > 0 {
				d.target += int(int64(d.limit-floor) * share / demand)
			}
		}
	}
	// Shrink before increasing other sockets so reallocating a budget does not create a transient spike.
	for _, shrink := range []bool{true, false} {
		for m := range p.items {
			m.applyDirection(true, &m.rx, shrink)
			m.applyDirection(false, &m.tx, shrink)
		}
	}
}

func (m *meter) applyDirection(receive bool, d *direction, shrink bool) {
	if d.applied != d.target && (d.target < d.applied) == shrink {
		m.set(receive, d.target)
		d.applied = d.target
	}
}

func (m *meter) stop() {
	m.pool.Lock()
	delete(m.pool.items, m)
	m.pool.Unlock()
}

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
func (c *measuredConn) Close() error {
	err := c.Conn.Close()
	c.m.stop()
	return err
}

// Retain half-close support for relay io.Copy loops.
type measuredEndpoint struct {
	*measuredConn
	tcp interface {
		CloseRead() error
		CloseWrite() error
	}
}

func (c *measuredEndpoint) CloseWrite() error { return c.tcp.CloseWrite() }
func (c *measuredEndpoint) CloseRead() error  { return c.tcp.CloseRead() }

func Endpoint(conn *gonet.TCPConn, ep tcpip.Endpoint) net.Conn {
	opts := ep.SocketOptions()
	m := newMeter(&endpointMeters, func(receive bool, payload int) {
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

// Egress accounts for native sockets to the final peer separately from the virtual
// endpoint and the carrier. Use Control on the dialer to negotiate window scaling.
func Egress(conn net.Conn) net.Conn {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return conn
	}
	set := nativeSetter(tcp)
	if set == nil {
		return conn
	}
	return &measuredEndpoint{&measuredConn{conn, newMeter(&egressMeters, set)}, tcp}
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
	return &measuredConn{conn, newMeter(&carrierMeters, set)}
}
