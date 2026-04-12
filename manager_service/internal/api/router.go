package api

import (
	"github.com/cloudhelper/manager_service/internal/adapter/controller"
	"github.com/cloudhelper/manager_service/internal/adapter/netassist"
	"github.com/cloudhelper/manager_service/internal/adapter/node"
	"github.com/cloudhelper/manager_service/internal/api/handler"
	"github.com/cloudhelper/manager_service/internal/api/middleware"
	"github.com/cloudhelper/manager_service/internal/auth"
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
		ctrlH = handler.NewControllerHandler(opts.ControllerSession)
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
			}
			if netAssistH != nil {
				authGroup.GET("/network-assistant/status", netAssistH.GetStatus)
				authGroup.POST("/network-assistant/mode", netAssistH.SwitchMode)
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
	return mux
}
