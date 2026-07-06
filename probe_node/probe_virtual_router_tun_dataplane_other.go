//go:build !windows && !linux

package main

import (
	"fmt"
	"runtime"
)

func startProbeVirtualRouterTUNDataPlane() error {
	return fmt.Errorf("%w: %s", errprobeLocalRouteUnsupported, runtime.GOOS)
}

func stopProbeVirtualRouterTUNDataPlane() error {
	return nil
}

func probeVirtualRouterTUNDataPlaneStatsSnapshot() probeVirtualRouterTUNDataPlaneStats {
	return probeVirtualRouterTUNDataPlaneStats{}
}

func probeVirtualRouterTUNDataPlaneRunning() bool {
	return false
}

func writeProbeVirtualRouterTUNPacket(_ []byte) error {
	return fmt.Errorf("%w: %s", errprobeLocalRouteUnsupported, runtime.GOOS)
}

func resetProbeVirtualRouterTUNDataPlaneHooksForTest() {}
