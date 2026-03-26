//go:build !windows

package backend

func (s *networkAssistantService) applyPlatformTUNSystemRouting(_ tunControlPlaneTargets) error {
	s.mu.Lock()
	s.tunRouteState = tunSystemRouteState{}
	s.mu.Unlock()
	return nil
}

func (s *networkAssistantService) clearPlatformTUNSystemRouting() error {
	s.mu.Lock()
	s.tunRouteState = tunSystemRouteState{}
	s.mu.Unlock()
	return nil
}

func (s *networkAssistantService) acquireTUNDirectBypassRoute(_ string) (func(), error) {
	return func() {}, nil
}

func (s *networkAssistantService) releaseTUNDirectBypassRoute(_ string) {
}
