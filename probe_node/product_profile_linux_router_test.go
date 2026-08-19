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
	if !profile.EnableLocalConsole || !profile.EnableLocalConsoleByDefault || profile.LocalConsoleDefaultListen != "0.0.0.0:16032" || !profile.AllowInsecureLocalConsoleHTTP || !profile.PreferLocalConsoleConfig || !profile.EnableVRoutePlatformInterface || !profile.LinuxAMD64OrARM64Only {
		t.Fatalf("router platform flags are incomplete: %+v", profile)
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

func TestLinuxRouterSavedListenIPOverridesLegacyEnvironment(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	t.Setenv("PROBE_LOCAL_LISTEN", "0.0.0.0:16032")
	if err := persistProbeLocalAuthState(probeLocalAuthState{ListenIP: "127.0.0.1", ListenPort: 16032}); err != nil {
		t.Fatal(err)
	}
	if got := resolveProbeLocalListenAddr(""); got != "127.0.0.1:16032" {
		t.Fatalf("saved router listen should override legacy environment, got=%q", got)
	}
}
