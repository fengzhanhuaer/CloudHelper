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
