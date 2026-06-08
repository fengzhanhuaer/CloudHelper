package core

import _ "embed"

var (
	//go:embed install_scripts/install_probe_node_service.sh
	probeNodeInstallScriptLinux string

	//go:embed install_scripts/install_probe_node_service_windows.ps1
	probeNodeInstallScriptWindows string
)
