//go:build !windows

package main

import (
	"time"
)

func probeLocalTUNEgressSnapshot() (probeLocalTUNEgressStatus, error) {
	return probeLocalTUNEgressStatus{
		Supported: false,
		Mode:      "unsupported",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func probeLocalTUNEgressUpdate(req probeLocalTUNEgressUpdateRequest) (probeLocalTUNEgressStatus, error) {
	return probeLocalTUNEgressStatus{}, &probeLocalHTTPError{Status: 501, Message: "manual egress selection is only supported on windows"}
}

func applyProbeLocalTUNEgressPersistentState(state probeLocalProxyStateFile) {
}
