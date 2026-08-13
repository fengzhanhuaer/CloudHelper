//go:build mihomo_exit

package main

func buildProbeProductProfile() probeProductProfile {
	return probeProductProfile{
		BuildKind:          probeBuildKindMihomoExit,
		ServiceName:        "probe_exit_node",
		UpgradeAssetPrefix: "cloudhelper-probe-exit-node",
		RuntimeLogDir:      "log",
		RuntimeLogFile:     "probe_exit_node.runtime.log",
		DataDir:            "data",
		TempDir:            "temp",
		LinuxAMD64Only:     true,
	}
}
