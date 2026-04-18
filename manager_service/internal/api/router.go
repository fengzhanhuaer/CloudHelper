package api

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/cloudhelper/manager_service/internal/adapter/controller"
	"github.com/cloudhelper/manager_service/internal/adapter/netassist"
	"github.com/cloudhelper/manager_service/internal/adapter/node"
	"github.com/cloudhelper/manager_service/internal/api/handler"
	"github.com/cloudhelper/manager_service/internal/api/middleware"
	"github.com/cloudhelper/manager_service/internal/auth"
	"github.com/cloudhelper/manager_service/web"
	"github.com/gin-gonic/gin"
)

type RouterOptions struct {
	AuthSvc           *auth.Service
	NodeStore         *node.Store
	ControllerSession *controller.Session
	NetAssistClient   *netassist.Client
	BuildVersion      string
	LogDir            string
}

func NewRouter(opts RouterOptions) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	mux := gin.New()
	mux.Use(gin.Recovery())
	mux.Use(middleware.RequestID())
	mux.Use(middleware.Logger())

	sysH := handler.NewSystemHandler(opts.BuildVersion)
	authH := handler.NewAuthHandler(opts.AuthSvc)
	nodeH := handler.NewNodeHandler(opts.NodeStore)
	upgradeH := handler.NewUpgradeHandler(opts.LogDir)

	var ctrlH *handler.ControllerHandler
	if opts.ControllerSession != nil {
		ctrlH = handler.NewControllerHandler(opts.ControllerSession, opts.AuthSvc)
	}
	var netAssistH *handler.NetAssistHandler
	if opts.NetAssistClient != nil {
		netAssistH = handler.NewNetAssistHandler(opts.NetAssistClient)
	}

	mux.GET("/healthz", sysH.Healthz)

	apiGroup := mux.Group("/api")
	{
		apiGroup.POST("/auth/login", authH.Login)
		apiGroup.POST("/auth/password/reset-local", middleware.LocalhostOnly(), authH.ResetLocal)

		authGroup := apiGroup.Group("")
		authGroup.Use(middleware.Auth(opts.AuthSvc))
		{
			authGroup.GET("/system/version", sysH.Version)
			authGroup.POST("/auth/logout", authH.Logout)
			authGroup.POST("/auth/password/change", authH.ChangePassword)

			if ctrlH != nil {
				authGroup.POST("/controller/session/login", ctrlH.Login)
				// SetSession is localhost-only: allows local operator to supply a controller token.
				authGroup.POST("/controller/session/set", middleware.LocalhostOnly(), ctrlH.SetSession)
			}
			if netAssistH != nil {
				na := authGroup.Group("/network-assistant")
				{
					na.GET("/status", netAssistH.GetStatus)
					na.POST("/mode", netAssistH.SwitchMode)
					na.GET("/logs", netAssistH.GetLogs)
					na.GET("/rules", netAssistH.GetRuleConfig)
					na.POST("/rules/policy", netAssistH.SetRulePolicy)
					na.GET("/dns/cache", netAssistH.GetDNSCache)
					na.GET("/processes", netAssistH.GetProcesses)
					na.POST("/monitor/start", netAssistH.StartMonitor)
					na.POST("/monitor/stop", netAssistH.StopMonitor)
					na.POST("/monitor/clear", netAssistH.ClearMonitorEvents)
					na.GET("/monitor/events", netAssistH.GetMonitorEvents)
					na.POST("/tun/install", netAssistH.InstallTUN)
					na.POST("/tun/enable", netAssistH.EnableTUN)
					na.POST("/direct/restore", netAssistH.RestoreDirect)
				}
			}

			authGroup.GET("/probe/nodes", nodeH.List)
			authGroup.POST("/probe/nodes", nodeH.Create)
			authGroup.PUT("/probe/nodes/:node_no", nodeH.Update)
			authGroup.POST("/probe/link/test", nodeH.TestLink)

			// ── R2-BE: Probe proxy endpoints (require controller session) ─────
			if opts.ControllerSession != nil {
				probeProxyH := handler.NewProbeProxyHandler(opts.ControllerSession)
				// Fixed-segment routes MUST be registered before wildcard /:node_no routes.
				authGroup.GET("/probe/nodes/status", probeProxyH.GetNodesStatus)
				authGroup.GET("/probe/nodes/report-interval", probeProxyH.GetReportInterval)
				authGroup.POST("/probe/nodes/report-interval", probeProxyH.SetReportInterval)
				authGroup.POST("/probe/nodes/upgrade-all", probeProxyH.UpgradeAllNodes)
				authGroup.GET("/probe/nodes/shell/shortcuts", probeProxyH.GetShellShortcuts)
				authGroup.POST("/probe/nodes/shell/shortcuts", probeProxyH.SaveShellShortcut)
				authGroup.DELETE("/probe/nodes/shell/shortcuts/:name", probeProxyH.DeleteShellShortcut)
				// Node-scoped wildcard routes.
				authGroup.DELETE("/probe/nodes/:node_no", probeProxyH.DeleteNode)
				authGroup.POST("/probe/nodes/:node_no/restore", probeProxyH.RestoreNode)
				authGroup.POST("/probe/nodes/:node_no/upgrade", probeProxyH.UpgradeNode)
				authGroup.GET("/probe/nodes/:node_no/logs", probeProxyH.GetNodeLogs)
				authGroup.POST("/probe/nodes/:node_no/shell/start", probeProxyH.StartShell)
				authGroup.POST("/probe/nodes/:node_no/shell/exec", probeProxyH.ExecShell)
				authGroup.POST("/probe/nodes/:node_no/shell/stop", probeProxyH.StopShell)
			}

			// ── R4-BE: Link management proxy endpoints ────────────────────────
			if opts.ControllerSession != nil {
				linkH := handler.NewLinkProxyHandler(opts.ControllerSession)
				authGroup.GET("/link/chains", linkH.GetChains)
				authGroup.POST("/link/chains", linkH.UpsertChain)
				authGroup.DELETE("/link/chains/:chain_id", linkH.DeleteChain)
				authGroup.GET("/link/users", linkH.GetUsers)
				authGroup.GET("/link/users/:username/pubkey", linkH.GetUserPublicKey)
				authGroup.POST("/link/nodes/update", linkH.UpdateNodeLink)
				authGroup.POST("/link/test/start", linkH.StartLinkTest)
				authGroup.POST("/link/test/stop", linkH.StopLinkTest)
				authGroup.POST("/link/dns/refresh", linkH.RefreshProbeDNS)
			}

			// ── R5-BE: Cloudflare proxy endpoints ────────────────────────────
			if opts.ControllerSession != nil {
				cfH := handler.NewCloudflareProxyHandler(opts.ControllerSession)
				authGroup.GET("/cloudflare/api-key", cfH.GetAPIKey)
				authGroup.POST("/cloudflare/api-key", cfH.SetAPIKey)
				authGroup.GET("/cloudflare/zone", cfH.GetZone)
				authGroup.POST("/cloudflare/zone", cfH.SetZone)
				authGroup.GET("/cloudflare/ddns/records", cfH.GetDDNSRecords)
				authGroup.POST("/cloudflare/ddns/apply", cfH.ApplyDDNS)
				authGroup.GET("/cloudflare/zerotrust/whitelist", cfH.GetZeroTrustWhitelist)
				authGroup.POST("/cloudflare/zerotrust/whitelist", cfH.SetZeroTrustWhitelist)
				authGroup.POST("/cloudflare/zerotrust/sync", cfH.RunZeroTrustSync)
			}

			// ── R6-BE: TG assistant proxy endpoints ───────────────────────────
			if opts.ControllerSession != nil {
				tgH := handler.NewTGProxyHandler(opts.ControllerSession)
				// API key
				authGroup.GET("/tg/api-key", tgH.GetAPIKey)
				authGroup.POST("/tg/api-key", tgH.SetAPIKey)
				// Accounts
				authGroup.GET("/tg/accounts", tgH.ListAccounts)
				authGroup.POST("/tg/accounts/refresh", tgH.RefreshAccounts)
				authGroup.POST("/tg/accounts/add", tgH.AddAccount)
				authGroup.POST("/tg/accounts/send-code", tgH.SendCode)
				authGroup.POST("/tg/accounts/sign-in", tgH.SignIn)
				authGroup.POST("/tg/accounts/logout", tgH.LogoutAccount)
				authGroup.POST("/tg/accounts/remove", tgH.RemoveAccount)
				// Schedules (fixed routes before :id wildcard)
				authGroup.GET("/tg/schedules", tgH.ListSchedules)
				authGroup.POST("/tg/schedules", tgH.AddSchedule)
				authGroup.GET("/tg/schedules/pending", tgH.GetPendingTasks)
				authGroup.GET("/tg/schedules/history", tgH.GetHistory)
				authGroup.PUT("/tg/schedules/:id", tgH.UpdateSchedule)
				authGroup.DELETE("/tg/schedules/:id", tgH.RemoveSchedule)
				authGroup.POST("/tg/schedules/:id/enable", tgH.EnableSchedule)
				authGroup.POST("/tg/schedules/:id/disable", tgH.DisableSchedule)
				authGroup.POST("/tg/schedules/:id/send-now", tgH.SendNow)
				// Targets
				authGroup.GET("/tg/targets", tgH.ListTargets)
				authGroup.POST("/tg/targets/refresh", tgH.RefreshTargets)
				// Bot
				authGroup.GET("/tg/bot", tgH.GetBot)
				authGroup.POST("/tg/bot", tgH.SetBot)
				authGroup.POST("/tg/bot/test-send", tgH.TestBotSend)
			}

			// ── R8-BE: System/backup proxy endpoints ─────────────────────────
			if opts.ControllerSession != nil {
				sysH := handler.NewSystemProxyHandler(opts.ControllerSession)
				// R8-BE: backup settings
				authGroup.GET("/system/backup-settings", sysH.GetBackupSettings)
				authGroup.POST("/system/backup-settings", sysH.SetBackupSettings)
				authGroup.POST("/system/backup-settings/test", sysH.TestBackupSettings)
				// W4: controller logs
				authGroup.GET("/system/controller-logs", sysH.GetControllerLogs)
				// W4: controller version & upgrade
				authGroup.GET("/system/controller-version", sysH.GetControllerVersion)
				authGroup.POST("/system/controller-upgrade", sysH.UpgradeController)
				authGroup.GET("/system/controller-upgrade-progress", sysH.GetControllerUpgradeProgress)
				// W4: rule-routes backup sync
				authGroup.POST("/system/rule-routes/upload", sysH.UploadRuleRoutes)
				authGroup.POST("/system/rule-routes/download", sysH.DownloadRuleRoutes)
			}

			authGroup.GET("/upgrade/release", upgradeH.GetRelease)
			authGroup.POST("/upgrade/manager", upgradeH.UpgradeManager)
			authGroup.GET("/logs/manager", upgradeH.GetLogs)
		}
	}
	// Serve embedded frontend SPA.
	// All non-/api, non-/healthz paths → index.html (SPA client-side routing).
	distFS, err := fs.Sub(web.FS, "dist")
	if err == nil {
		fileServer := http.FileServer(http.FS(distFS))
		mux.NoRoute(func(c *gin.Context) {
			path := strings.TrimPrefix(c.Request.URL.Path, "/")
			// Try to serve the static asset; fall back to index.html for SPA routes.
			if path != "" {
				if _, err := fs.Stat(distFS, path); err == nil {
					fileServer.ServeHTTP(c.Writer, c.Request)
					return
				}
			}
			c.Request.URL.Path = "/"
			fileServer.ServeHTTP(c.Writer, c.Request)
		})
	}
	return mux
}
