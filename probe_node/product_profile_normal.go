//go:build !mihomo_exit && !linux_router

package main

func buildProbeProductProfile() probeProductProfile {
	return probeProductProfile{
		BuildKind:                     probeBuildKindNormal,
		ServiceName:                   "probe_node",
		UpgradeAssetPrefix:            "cloudhelper-probe-node",
		RuntimeLogDir:                 "logs",
		RuntimeLogFile:                "probe_node.runtime.log",
		DataDir:                       "data",
		TempDir:                       ".cloudhelper-upgrade",
		AllowLocalTUNInstall:          true,
		EnableLocalConsole:            true,
		EnableLocalProxy:              true,
		EnableSystemDNS:               true,
		EnableSyncScheduler:           true,
		EnableDDNSScheduler:           true,
		EnableLocalTUNStartupRecovery: true,
		EnableVRoutePlatformInterface: true,
		EnableVRouteTakeoverRoutes:    true,
	}
}
