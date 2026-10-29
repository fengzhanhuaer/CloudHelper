// Package transportnet filters literal addresses the local network cannot use.
package transportnet

import (
	"net"
	"strings"
	"sync"
	"time"
)

var ipv6 = struct {
	sync.Mutex
	until  time.Time
	usable bool
}{}

// Usable rejects public IPv6 when this host has no IPv6 source or route. The
// UDP connect only asks the kernel to select a route; it sends no packets.
// IPv4, names, loopback and scoped LAN addresses keep their existing behavior.
func Usable(address string) bool {
	host := address
	if h, _, err := net.SplitHostPort(address); err == nil {
		host = h
	}
	ip := net.ParseIP(strings.SplitN(host, "%", 2)[0])
	if ip == nil || ip.To4() != nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	if !HasIPv6Source() {
		return false
	}
	conn, err := net.DialUDP("udp6", nil, &net.UDPAddr{IP: ip, Port: 9})
	if err != nil {
		return false
	}
	defer conn.Close()
	source := conn.LocalAddr().(*net.UDPAddr).IP
	return source.To4() == nil && source.IsGlobalUnicast()
}

// HasIPv6Source is cached briefly so network recovery is detected automatically.
func HasIPv6Source() bool {
	ipv6.Lock()
	defer ipv6.Unlock()
	if time.Now().Before(ipv6.until) {
		return ipv6.usable
	}
	addrs, err := net.InterfaceAddrs()
	// If interface enumeration fails, let route selection decide instead.
	ipv6.usable = err != nil || containsIPv6Source(addrs)
	ipv6.until = time.Now().Add(3 * time.Second)
	return ipv6.usable
}

func containsIPv6Source(addrs []net.Addr) bool {
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err == nil && ip.To4() == nil && ip.IsGlobalUnicast() {
			return true
		}
	}
	return false
}
