//go:build !windows && !linux

package main

func applyProbeVirtualRouterSystemDNS() error {
	return nil
}

func restoreProbeVirtualRouterSystemDNS() error {
	return nil
}
