package core

import _ "embed"

var (
	//go:embed install_scripts/install_probe_controller_service.sh
	probeControllerInstallScriptLinux string

	//go:embed install_scripts/install_probe_node_service.sh
	probeNodeInstallScriptLinux string

	//go:embed install_scripts/install_probe_node_service_windows.ps1
	probeNodeInstallScriptWindows string

	//go:embed install_scripts/install_probe_exit_node_service.sh
	probeExitNodeInstallScriptLinux string

	//go:embed install_scripts/install_probe_router_service.sh
	probeRouterInstallScriptLinux string
)
