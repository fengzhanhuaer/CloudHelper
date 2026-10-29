package transportnet

import (
	"net"
	"testing"
)

func TestIPv6RequiresSourceBeyondLoopbackAndLinkLocal(t *testing.T) {
	addresses := func(cidrs ...string) []net.Addr {
		var result []net.Addr
		for _, cidr := range cidrs {
			ip, network, err := net.ParseCIDR(cidr)
			if err != nil {
				t.Fatal(err)
			}
			network.IP = ip
			result = append(result, network)
		}
		return result
	}
	for _, fixture := range []struct {
		name  string
		addrs []net.Addr
		want  bool
	}{
		{"ipv4-only", addresses("192.168.52.172/24"), false},
		{"windows-link-local-only", addresses("::1/128", "fe80::1/64", "192.168.52.172/24"), false},
		{"public-ipv6", addresses("2001:db8::2/64"), true},
		{"private-ipv6", addresses("fd12:3456::2/64"), true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if got := containsIPv6Source(fixture.addrs); got != fixture.want {
				t.Fatalf("source support=%v want=%v", got, fixture.want)
			}
		})
	}
	for _, address := range []string{"127.0.0.1:80", "[::1]:80", "[fe80::1%12]:80", "192.168.52.1:12040"} {
		if !Usable(address) {
			t.Fatalf("local/IPv4 address rejected: %s", address)
		}
	}
}
