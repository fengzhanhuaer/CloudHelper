//go:build !windows

package backend

import "errors"

func (s *networkAssistantService) startLocalTUNPacketStack() error {
	return errors.New("local tun packet stack is only supported on windows")
}

func (s *networkAssistantService) stopLocalTUNPacketStack() error {
	return nil
}
