//go:build !windows

package main

func ensureProbeVirtualRouterPlatformInterfaceIP(_ string) error {
	return nil
}
