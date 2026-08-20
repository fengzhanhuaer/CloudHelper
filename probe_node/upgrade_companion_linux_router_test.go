//go:build linux_router

package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestLinuxRouterPairedUpgradeManifestMatchesCurrentArchitecture(t *testing.T) {
	name, err := currentProbeMihomoUpgradeManifestAsset()
	if err != nil {
		t.Fatal(err)
	}
	wantName := "cloudhelper-probe-router-linux-" + runtime.GOARCH + "-manifest.json"
	if name != wantName {
		t.Fatalf("manifest asset=%q want=%q", name, wantName)
	}
	manifest := probeMihomoUpgradeManifest{SchemaVersion: 1, Version: "v2.3.4", BuildKind: probeBuildKindLinuxRouter, OS: "linux", Arch: runtime.GOARCH}
	manifest.CompatibleProgramVersions.Min = "v2.3.4"
	manifest.CompatibleProgramVersions.Max = "v2.3.4"
	manifest.Program.Asset = "cloudhelper-probe-router-linux-" + runtime.GOARCH
	manifest.Program.SHA256 = strings.Repeat("ab", 32)
	manifest.Mihomo.Version = "v1.19.29"
	manifest.Mihomo.Asset = "mihomo-linux-" + runtime.GOARCH + "-v1.19.29.gz"
	manifest.Mihomo.URL = "https://github.com/MetaCubeX/mihomo/releases/download/v1.19.29/" + manifest.Mihomo.Asset
	manifest.Mihomo.SHA256 = strings.Repeat("cd", 32)
	if err := validateProbeMihomoUpgradeManifest(manifest, "v2.3.4"); err != nil {
		t.Fatalf("valid router manifest rejected: %v", err)
	}
	manifest.Arch = "wrong"
	if err := validateProbeMihomoUpgradeManifest(manifest, "v2.3.4"); err == nil {
		t.Fatal("wrong router architecture accepted")
	}
}
