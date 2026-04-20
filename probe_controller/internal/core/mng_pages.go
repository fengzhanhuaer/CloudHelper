package core

import _ "embed"

var (
	//go:embed mng_pages/entry.html
	mngEntryPageHTML string

	//go:embed mng_pages/panel.html
	mngPanelPageHTML string

	//go:embed mng_pages/settings.html
	mngSettingsPageHTML string
)
