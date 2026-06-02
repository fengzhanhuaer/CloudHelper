//go:build !windows

package main

import (
	"bufio"
	"io"
	"net"
	"sync"
	"time"
)

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

type probeLocalTUNTCPDirectFailureCacheStats struct {
	Active int   `json:"active"`
	Hits   int64 `json:"hits"`
	Stored int64 `json:"stored"`
}

type probeLocalTUNTunnelUDPConn struct {
	stream  net.Conn
	reader  *bufio.Reader
	readMu  sync.Mutex
	writeMu sync.Mutex
}

func startProbeLocalTUNPacketStack() error { return nil }
func stopProbeLocalTUNPacketStack() error  { return nil }

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

func snapshotProbeLocalTUNUDPBridgeMonitorStats() probeLocalTUNUDPBridgeMonitorStats {
	return probeLocalTUNUDPBridgeMonitorStats{}
}

func snapshotProbeLocalTUNTCPDirectFailureCacheStats() probeLocalTUNTCPDirectFailureCacheStats {
	return probeLocalTUNTCPDirectFailureCacheStats{}
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
	return readProbeChainFramedPacketInto(c.reader, payload)
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
	if c == nil || c.stream == nil {
		return nil
	}
	return c.stream.Close()
}

func (c *probeLocalTUNTunnelUDPConn) SetReadDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return io.ErrClosedPipe
	}
	return c.stream.SetReadDeadline(t)
}
