//go:build !windows && !linux

package main

import (
	"time"
)

func startProbeLocalTUNEgressGuardian() {}

func stopProbeLocalTUNEgressGuardian() {}

func probeLocalTUNEgressSnapshot() (probeLocalTUNEgressStatus, error) {
	return probeLocalTUNEgressStatus{
		APIVersion: probeLocalTUNEgressAPIVersion,
		Supported:  false,
		Mode:       "unsupported",
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func probeLocalTUNEgressUpdate(req probeLocalTUNEgressUpdateRequest) (probeLocalTUNEgressStatus, error) {
	return probeLocalTUNEgressStatus{}, &probeLocalHTTPError{Status: 501, Message: "manual egress selection is not supported on this platform"}
}

func applyProbeLocalTUNEgressPersistentState(state probeLocalTUNEgressPersistentState) {
	_ = state
}
