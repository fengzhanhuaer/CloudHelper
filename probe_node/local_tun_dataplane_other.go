//go:build !windows && !linux

package main

import (
	"fmt"
	"runtime"
)

func startProbeLocalTUNDataPlane() error {
	return fmt.Errorf("%w: %s", errProbeLocalProxyUnsupported, runtime.GOOS)
}

func stopProbeLocalTUNDataPlane() error {
	return nil
}

func probeLocalTUNDataPlaneStatsSnapshot() probeLocalTUNDataPlaneStats {
	return probeLocalTUNDataPlaneStats{}
}

func probeLocalTUNDataPlaneRunning() bool {
	return false
}

func writeProbeLocalTUNPacket(_ []byte) error {
	return fmt.Errorf("%w: %s", errProbeLocalProxyUnsupported, runtime.GOOS)
}

func resetProbeLocalTUNDataPlaneHooksForTest() {}
