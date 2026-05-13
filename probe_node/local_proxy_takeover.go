//go:build !windows && !linux

package main

import (
	"fmt"
	"runtime"
)

func applyProbeLocalProxyTakeover() error {
	return fmt.Errorf("%w: %s", errProbeLocalProxyUnsupported, runtime.GOOS)
}

func restoreProbeLocalProxyDirect() error {
	return nil
}

func currentProbeLocalTUNDNSListenHost() string {
	return ""
}

func currentProbeLocalSystemDNSServers() []string {
	return nil
}

func applyProbeLocalTUNPrimaryDNS() error {
	return nil
}

func restoreProbeLocalTUNPrimaryDNS() error {
	return nil
}

func uninstallProbeLocalTUNDriver() error {
	return fmt.Errorf("%w: %s", errProbeLocalTUNUnsupported, runtime.GOOS)
}
