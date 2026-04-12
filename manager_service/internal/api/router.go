package api

import (
	"io/fs"
	"net/http"

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
			path := c.Request.URL.Path
			// Try to serve the static asset; fall back to index.html for SPA routes.
			if _, err := distFS.(fs.StatFS).Stat(path[1:]); err == nil {
				fileServer.ServeHTTP(c.Writer, c.Request)
			} else {
				c.Request.URL.Path = "/"
				fileServer.ServeHTTP(c.Writer, c.Request)
			}
		})
	}
	return mux
}
