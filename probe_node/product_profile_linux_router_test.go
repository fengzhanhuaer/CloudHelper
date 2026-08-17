//go:build linux_router

package main

import "testing"

func TestLinuxRouterProductProfile(t *testing.T) {
	profile := buildProbeProductProfile()
	if profile.BuildKind != probeBuildKindLinuxRouter {
		t.Fatalf("build kind = %q", profile.BuildKind)
	}
	if profile.ServiceName != "probe_router" || profile.UpgradeAssetPrefix != "cloudhelper-probe-router" {
		t.Fatalf("unexpected profile: %+v", profile)
	}
	if profile.DataDir != "data" || profile.RuntimeLogDir != "log" || profile.TempDir != "temp" {
		t.Fatalf("unexpected working directories: %+v", profile)
	}
	if !profile.EnableProductLocalWeb || !profile.EnableVRoutePlatformInterface || !profile.LinuxAMD64OrARM64Only {
		t.Fatalf("router platform flags are incomplete: %+v", profile)
	}
	if profile.EnableLocalConsole {
		t.Fatalf("router product must not expose the generic probe console: %+v", profile)
	}
	if profile.EnableVRouteTakeoverRoutes {
		t.Fatalf("router product must never install host takeover routes: %+v", profile)
	}
}

func TestLinuxRouterProductPlatforms(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		if err := validateProbeProductPlatform("linux", arch); err != nil {
			t.Fatalf("linux/%s rejected: %v", arch, err)
		}
	}
	for _, item := range [][2]string{{"linux", "arm"}, {"windows", "amd64"}, {"darwin", "arm64"}} {
		if err := validateProbeProductPlatform(item[0], item[1]); err == nil {
			t.Fatalf("%s/%s unexpectedly accepted", item[0], item[1])
		}
	}
}
