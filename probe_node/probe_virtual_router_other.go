//go:build !windows && !linux

package main

func ensureProbeVirtualRouterPlatformInterfaceIP(_ string) error {
	return nil
}
