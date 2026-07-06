//go:build !windows

package main

type fakeProbeVirtualRouterTUNDataPlane struct {
	stats      probeVirtualRouterTUNDataPlaneStats
	closeErr   error
	writeErr   error
	closeCalls int
	writeCalls int
}

func (f *fakeProbeVirtualRouterTUNDataPlane) Close() error {
	f.closeCalls++
	return f.closeErr
}

func (f *fakeProbeVirtualRouterTUNDataPlane) Stats() probeVirtualRouterTUNDataPlaneStats {
	return f.stats
}

func (f *fakeProbeVirtualRouterTUNDataPlane) WritePacket(_ []byte) error {
	f.writeCalls++
	return f.writeErr
}

var (
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane   = func() (string, error) { return "", nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) { return 0, nil }
	probeLocalCloseWintunAdapterForDataPlane  = func(_ string, _ uintptr) error { return nil }
	probeVirtualRouterNewTUNDataPlaneRunner   = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeVirtualRouterTUNDataPlane, error) {
		return nil, nil
	}
)

func stubProbeLocalConsoleTUNRouteTargetForTest(_ interface{ Helper() }) {}
