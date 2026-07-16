//go:build !windows

package main

func probeVRouteSystemProxyRequired() bool {
	return false
}

func setProbeVRouteSystemProxy(string, string) error {
	return nil
}

func restoreProbeVRouteSystemProxy() error {
	return nil
}

func snapshotProbeVRouteSystemProxy() (bool, string, string) {
	return false, "", ""
}
