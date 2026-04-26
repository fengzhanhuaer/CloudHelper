//go:build !windows

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

func writeProbeLocalTUNPacket(_ []byte) error {
	return fmt.Errorf("%w: %s", errProbeLocalProxyUnsupported, runtime.GOOS)
}

func resetProbeLocalTUNDataPlaneHooksForTest() {}
