package tcpwindow

import (
	"io"
	"net"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

func TestStackBudget(t *testing.T) {
	s := stack.New(stack.Options{NetworkProtocols: []stack.NetworkProtocolFactory{ipv4.NewProtocol}, TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol}})
	defer s.Destroy()
	if err := ConfigureStack(s); err != nil {
		t.Fatal(err)
	}
	send, recv := tcp.GetTCPSendBufferLimits(s), tcp.GetTCPReceiveBufferLimits(s)
	if send.Default != InitialBuffer || recv.Default != InitialBuffer || send.Max != 37_500_000 || recv.Max != send.Max {
		t.Fatalf("unexpected stack limits: send %+v, receive %+v", send, recv)
	}
}

func TestMeterTracksWritesAndReleasesOnClose(t *testing.T) {
	writer, reader := net.Pipe()
	defer reader.Close()
	growth := make(chan int, 1)
	m := newMeter(&endpointMeters, func(rx bool, size int) {
		if !rx && size > InitialPayload {
			select {
			case growth <- size:
			default:
			}
		}
	})
	c := &measuredConn{writer, m}
	defer c.Close()
	go io.Copy(io.Discard, reader)
	if _, err := c.Write(make([]byte, InitialPayload)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-growth:
	case <-time.After(3 * time.Second):
		t.Fatal("active write direction did not grow")
	}
	c.Close()
	endpointMeters.Lock()
	defer endpointMeters.Unlock()
	if _, exists := endpointMeters.items[m]; exists {
		t.Fatal("closed connection remained registered")
	}
}

func TestWindowBudgetIsSharedAcrossConnectionsAndDirections(t *testing.T) {
	now := time.Unix(100, 0)
	var sum, peak int
	makeSocket := func() *meter {
		applied := map[bool]int{}
		return &meter{
			rx: direction{limit: InitialPayload},
			tx: direction{limit: Payload, activeUntil: now.Add(time.Second)},
			set: func(receive bool, size int) {
				sum += size - applied[receive]
				applied[receive] = size
				peak = max(peak, sum)
			},
		}
	}
	one, two := makeSocket(), makeSocket()
	pool := meterPool{items: map[*meter]struct{}{one: {}, two: {}}}
	pool.applyLocked(now)
	if sum > Payload || one.tx.applied != two.tx.applied || one.tx.applied < Payload/3 {
		t.Fatalf("concurrent flows did not share budget: sum=%d tx=%d,%d", sum, one.tx.applied, two.tx.applied)
	}
	// Transfer the budget from one busy connection to the other.
	one.tx.activeUntil = time.Time{}
	pool.applyLocked(now)
	if peak > Payload || two.tx.applied < Payload*9/10 || one.tx.applied > InitialPayload {
		t.Fatalf("busy flow could not reuse idle budget: peak=%d tx=%d,%d", peak, one.tx.applied, two.tx.applied)
	}
}

func TestBurstAndIdle(t *testing.T) {
	d := direction{limit: InitialPayload}
	now := time.Unix(100, 0)
	step := func(delta time.Duration, bytes uint64, want int) {
		t.Helper()
		now = now.Add(delta)
		d.update(bytes, delta, now)
		if d.limit != want {
			t.Fatalf("after %v, %d bytes: limit %d, want %d", delta, bytes, d.limit, want)
		}
	}
	step(interval, InitialPayload/4, 2*InitialPayload)
	step(interval, Payload, Payload)
	step(interval, 0, Payload)
	step(quietPeriod-interval, 0, Payload)
	step(interval, Payload/2, Payload) // A new burst resets the quiet period.
	step(interval, 0, Payload)
	for old := Payload; old > InitialPayload; {
		old = max(InitialPayload, old/2)
		step(quietPeriod, 0, old)
	}
	step(quietPeriod, 0, InitialPayload)
	step(interval, Payload, Payload)
}
