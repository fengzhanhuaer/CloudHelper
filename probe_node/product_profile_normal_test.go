//go:build !mihomo_exit

package main

import "testing"

func TestNormalProductProfilePreservesExistingCapabilities(t *testing.T) {
	profile := activeProbeProductProfile
	if profile.BuildKind != probeBuildKindNormal || profile.RuntimeLogDir != "logs" || profile.UpgradeAssetPrefix != "cloudhelper-probe-node" {
		t.Fatalf("unexpected normal profile: %+v", profile)
	}
	if !profile.AllowLocalTUNInstall || !profile.EnableLocalConsole || !profile.EnableLocalProxy || !profile.EnableSystemDNS || !profile.EnableSyncScheduler || !profile.EnableDDNSScheduler || !profile.EnableLocalTUNStartupRecovery || !profile.EnableVRoutePlatformInterface {
		t.Fatalf("normal profile lost an existing capability: %+v", profile)
	}
	if err := validateProbeExpectedNodeKind(probeBuildKindNormal); err != nil {
		t.Fatalf("normal expected node kind rejected: %v", err)
	}
	if err := validateProbeExpectedNodeKind(probeBuildKindMihomoExit); err == nil {
		t.Fatal("normal build accepted mihomo_exit expected node kind")
	}
	if err := validateProbeUpgradeVerifyBuildKind(probeBuildKindNormal); err != nil {
		t.Fatalf("normal candidate rejected: %v", err)
	}
	if err := validateProbeUpgradeVerifyBuildKind(probeBuildKindMihomoExit); err == nil {
		t.Fatal("normal candidate accepted mihomo_exit expected build kind")
	}
}
