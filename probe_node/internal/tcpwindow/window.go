// Package tcpwindow defines the TCP window budget for Probe Node and Android.
package tcpwindow

import (
	"fmt"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

const (
	Payload        = 500_000_000 / 8 * 300 / 1_000
	InitialPayload = 256 << 10
	InitialBuffer  = 2 * InitialPayload
	// gVisor accounts for packet overhead at twice cwnd*MSS.
	MaxBuffer = 2 * Payload
)

func ConfigureStack(s *stack.Stack) error {
	auto := tcpip.TCPModerateReceiveBufferOption(true)
	send := tcpip.TCPSendBufferSizeRangeOption{Min: 64 << 10, Default: InitialBuffer, Max: MaxBuffer}
	recv := tcpip.TCPReceiveBufferSizeRangeOption{Min: 64 << 10, Default: InitialBuffer, Max: MaxBuffer}
	for _, opt := range []tcpip.SettableTransportProtocolOption{&auto, &send, &recv} {
		if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, opt); err != nil {
			return fmt.Errorf("configure TCP window %T: %s", opt, err.String())
		}
	}
	return nil
}
