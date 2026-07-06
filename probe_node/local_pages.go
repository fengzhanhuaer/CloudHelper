package main

import _ "embed"

var (
	//go:embed local_pages/login.html
	probeLocalLoginPageHTML string

	//go:embed local_pages/panel.html
	probeLocalPanelPageHTML string

	//go:embed local_pages/logs.html
	probeLocalLogsPageHTML string

	//go:embed local_pages/system.html
	probeLocalSystemPageHTML string

	//go:embed local_pages/sync.html
	probeLocalSyncPageHTML string

	//go:embed local_pages/shell.html
	probeLocalShellPageHTML string
)
