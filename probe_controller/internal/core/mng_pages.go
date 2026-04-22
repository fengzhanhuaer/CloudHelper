package core

import _ "embed"

var (
	//go:embed mng_pages/entry.html
	mngEntryPageHTML string

	//go:embed mng_pages/panel.html
	mngPanelPageHTML string

	//go:embed mng_pages/settings.html
	mngSettingsPageHTML string

	//go:embed mng_pages/probe.html
	mngProbePageHTML string

	//go:embed mng_pages/link.html
	mngLinkPageHTML string

	//go:embed mng_pages/cloudflare.html
	mngCloudflarePageHTML string

	//go:embed mng_pages/tg.html
	mngTGPageHTML string
)
