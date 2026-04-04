//go:build !windows

package backend

func (s *networkAssistantService) applyPlatformTUNSystemRouting(_ tunControlPlaneTargets) error {
	s.mu.Lock()
	s.tunRouteState = tunSystemRouteState{}
	s.mu.Unlock()
	return nil
}

func (s *networkAssistantService) clearPlatformTUNDynamicBypassRoutes() error {
	s.mu.Lock()
	s.tunDynamicBypass = make(map[string]int)
	s.mu.Unlock()
	return nil
}

func (s *networkAssistantService) clearPlatformTUNSystemRouting() error {
	s.mu.Lock()
	s.tunRouteState = tunSystemRouteState{}
	s.tunDynamicBypass = make(map[string]int)
	s.mu.Unlock()
	return nil
}

func (s *networkAssistantService) acquireTUNDirectBypassRoute(_ string) (func(), error) {
	return func() {}, nil
}

func (s *networkAssistantService) releaseTUNDirectBypassRoute(_ string) {
}
