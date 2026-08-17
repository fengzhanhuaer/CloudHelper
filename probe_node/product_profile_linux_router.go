//go:build linux_router

package main

func buildProbeProductProfile() probeProductProfile {
	return probeProductProfile{
		BuildKind:                     probeBuildKindLinuxRouter,
		ServiceName:                   "probe_router",
		UpgradeAssetPrefix:            "cloudhelper-probe-router",
		RuntimeLogDir:                 "log",
		RuntimeLogFile:                "probe_router.runtime.log",
		DataDir:                       "data",
		TempDir:                       "temp",
		EnableProductLocalWeb:         true,
		EnableVRoutePlatformInterface: true,
		LinuxAMD64OrARM64Only:         true,
	}
}
