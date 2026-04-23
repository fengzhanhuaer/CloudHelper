package main

import _ "embed"

var (
	//go:embed local_pages/login.html
	probeLocalLoginPageHTML string

	//go:embed local_pages/panel.html
	probeLocalPanelPageHTML string
)
