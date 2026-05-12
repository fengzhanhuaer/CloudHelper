package main

import _ "embed"

var (
	//go:embed local_pages/login.html
	probeLocalLoginPageHTML string

	//go:embed local_pages/panel.html
	probeLocalPanelPageHTML string

	//go:embed local_pages/proxy.html
	probeLocalProxyPageHTML string

	//go:embed local_pages/dns.html
	probeLocalDNSPageHTML string

	//go:embed local_pages/logs.html
	probeLocalLogsPageHTML string

	//go:embed local_pages/system.html
	probeLocalSystemPageHTML string
)
