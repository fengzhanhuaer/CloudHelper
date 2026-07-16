//go:build !windows

package main

func setProbeVRouteSystemProxy(string, string) error {
	return nil
}

func restoreProbeVRouteSystemProxy() error {
	return nil
}

func snapshotProbeVRouteSystemProxy() (bool, string, string) {
	return false, "", ""
}
