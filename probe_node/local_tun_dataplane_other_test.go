//go:build !windows

package main

type fakeProbeLocalTUNDataPlane struct {
	stats      probeLocalTUNDataPlaneStats
	closeErr   error
	writeErr   error
	closeCalls int
	writeCalls int
}

func (f *fakeProbeLocalTUNDataPlane) Close() error {
	f.closeCalls++
	return f.closeErr
}

func (f *fakeProbeLocalTUNDataPlane) Stats() probeLocalTUNDataPlaneStats {
	return f.stats
}

func (f *fakeProbeLocalTUNDataPlane) WritePacket(_ []byte) error {
	f.writeCalls++
	return f.writeErr
}

var (
	probeLocalEnsureWintunLibraryForDataPlane = func() error { return nil }
	probeLocalResolveWintunPathForDataPlane   = func() (string, error) { return "", nil }
	probeLocalCreateWintunAdapterForDataPlane = func(_, _, _ string) (uintptr, error) { return 0, nil }
	probeLocalCloseWintunAdapterForDataPlane  = func(_ string, _ uintptr) error { return nil }
	probeLocalNewTUNDataPlaneRunner           = func(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (probeLocalTUNDataPlane, error) {
		return nil, nil
	}
)
