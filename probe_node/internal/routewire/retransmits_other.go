//go:build !linux

package routewire

import "net"

func CarrierTCPRetransmits(net.Conn) *uint64 { return nil }
