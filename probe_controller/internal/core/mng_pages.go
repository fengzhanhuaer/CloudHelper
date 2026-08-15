package core

import (
	_ "embed"
	"html"
	"net/http"
	"strings"
)

const mngQuickNavPlaceholder = "<!-- mng-quick-nav -->"

const mngQuickNavStyle = `<style>
  .topbar { display:flex; flex-direction:row; align-items:center; gap:12px; flex-wrap:wrap; }
  .topbar > :first-child { flex:0 0 auto; }
  .topbar > :last-child { display:flex; align-items:center; gap:6px; flex:0 0 auto; flex-wrap:wrap; margin-left:auto; }
  .quick-nav { display:flex; align-items:center; justify-content:center; flex:1 1 620px; flex-wrap:wrap; gap:6px; min-width:0; }
  .quick-link { display:inline-flex; align-items:center; justify-content:center; min-height:32px; padding:0 10px; border:1px solid #30363d; border-radius:8px; background:#161b22; color:#c9d1d9; font-size:13px; line-height:1.2; text-decoration:none; white-space:nowrap; box-sizing:border-box; transition:border-color .16s ease, background .16s ease, color .16s ease, transform .16s ease; }
  .quick-link:hover { border-color:#58a6ff; background:#21262d; color:#fff; transform:translateY(-1px); }
  .quick-link[aria-current="page"] { border-color:#58a6ff; background:rgba(31,111,235,.14); color:#58a6ff; }
  @media (max-width:620px) {
    .topbar { flex-direction:row; align-items:flex-start; }
    .topbar > :first-child { width:100%; }
    .quick-nav { order:3; display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); width:100%; flex-basis:100%; }
    .quick-link { min-width:0; white-space:normal; text-align:center; }
    .topbar > :last-child { margin-left:auto; }
  }
</style>`

type mngQuickNavItem struct {
	Path  string
	Label string
}

var mngQuickNavItems = []mngQuickNavItem{
	{Path: "/mng/settings", Label: "系统设置"},
	{Path: "/mng/probe", Label: "探针管理"},
	{Path: "/mng/backup", Label: "备份管理"},
	{Path: "/mng/notepad", Label: "记事本"},
	{Path: "/mng/controller-logs", Label: "主控日志"},
	{Path: "/mng/route", Label: "路由管理"},
	{Path: "/dashboard", Label: "探针面板"},
	{Path: "/mng/cloudflare", Label: "Cloudflare 管理"},
	{Path: "/mng/tg", Label: "TG 助手"},
}

func renderMngPageHTML(pageHTML, currentPath string) string {
	var nav strings.Builder
	nav.WriteString(`<nav class="quick-nav" aria-label="磁贴快捷入口">`)
	for _, item := range mngQuickNavItems {
		nav.WriteString(`<a class="quick-link" href="`)
		nav.WriteString(html.EscapeString(item.Path))
		nav.WriteString(`"`)
		if currentPath == item.Path {
			nav.WriteString(` aria-current="page"`)
		}
		nav.WriteString(`>`)
		nav.WriteString(html.EscapeString(item.Label))
		nav.WriteString(`</a>`)
	}
	nav.WriteString(`</nav>`)
	rendered := strings.Replace(pageHTML, mngQuickNavPlaceholder, nav.String(), 1)
	return strings.Replace(rendered, "</head>", mngQuickNavStyle+"\n</head>", 1)
}

func writeMngPageHTML(w http.ResponseWriter, r *http.Request, pageHTML string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(renderMngPageHTML(pageHTML, r.URL.Path)))
}

var (
	//go:embed mng_pages/entry.html
	mngEntryPageHTML string

	//go:embed mng_pages/panel.html
	mngPanelPageHTML string

	//go:embed mng_pages/settings.html
	mngSettingsPageHTML string

	//go:embed mng_pages/backup.html
	mngBackupPageHTML string

	//go:embed mng_pages/notepad.html
	mngNotepadPageHTML string

	//go:embed mng_pages/probe.html
	mngProbePageHTML string

	//go:embed mng_pages/route.html
	mngRoutePageHTML string

	//go:embed mng_pages/cloudflare.html
	mngCloudflarePageHTML string

	//go:embed mng_pages/tg.html
	mngTGPageHTML string

	//go:embed mng_pages/tg_session.html
	mngTGSessionPageHTML string

	//go:embed mng_pages/controller_logs.html
	mngControllerLogsPageHTML string
)
