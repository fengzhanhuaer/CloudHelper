//go:build !windows && !linux

package main

func ensureProbeVirtualRouterPlatformInterfaceIP(_ string) error {
	return nil
}

func cleanupProbeVirtualRouterPlatformRoutes() error {
	return nil
}
