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
	m := newMeter(func(rx bool, size int) {
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
	meters.Lock()
	defer meters.Unlock()
	if _, exists := meters.items[m]; exists {
		t.Fatal("closed connection remained registered")
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
