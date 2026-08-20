//go:build !mihomo_exit && !linux_router

package main

import "context"

func prepareProbeProductUpgradeCompanion(context.Context, string, releaseInfo, string, nodeIdentity, string, string) (probeProductUpgradeCompanion, error) {
	return probeProductUpgradeCompanion{}, nil
}

func replaceProbeProductUpgradeCompanion(value probeProductUpgradeCompanion) (probeProductUpgradeCompanion, error) {
	return value, nil
}

func rollbackProbeProductUpgradeCompanion(probeProductUpgradeCompanion) error { return nil }
