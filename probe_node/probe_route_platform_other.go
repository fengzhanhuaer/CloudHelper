//go:build !windows && !linux

package main

import (
	"fmt"
	"runtime"
)

func ensureProbeRouteDirectBypass(string) error {
	return nil
}

func currentProbeLocalSystemDNSServers() []string {
	return nil
}

func uninstallProbeLocalTUNDriver() error {
	return fmt.Errorf("%w: %s", errProbeLocalTUNUnsupported, runtime.GOOS)
}
