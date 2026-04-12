import os

def rewrite_response():
    with open('internal/api/response/response.go', 'w') as f:
        f.write("""package response

import (
	"net/http"
	"github.com/gin-gonic/gin"
)

type Envelope struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

const (
	CodeOK           = 0
	CodeUnauthorized = 401
	CodeForbidden    = 403
	CodeBadRequest   = 400
	CodeInternal     = 500
)

func OK(c *gin.Context, requestID string, data any) {
	c.JSON(http.StatusOK, Envelope{Code: CodeOK, Message: "ok", Data: data, RequestID: requestID})
}

func Unauthorized(c *gin.Context, requestID string) {
	c.JSON(http.StatusUnauthorized, Envelope{Code: CodeUnauthorized, Message: "unauthorized", RequestID: requestID})
}

func BadRequest(c *gin.Context, requestID, message string) {
	c.JSON(http.StatusBadRequest, Envelope{Code: CodeBadRequest, Message: message, RequestID: requestID})
}

func Internal(c *gin.Context, requestID, message string) {
	c.JSON(http.StatusInternalServerError, Envelope{Code: CodeInternal, Message: message, RequestID: requestID})
}
""")

def rewrite_system():
    with open('internal/api/handler/system_handler.go', 'w') as f:
        f.write("""package handler

import (
	"github.com/cloudhelper/internal/api/response"
	"github.com/gin-gonic/gin"
)

type SystemHandler struct {
	buildVersion string
}

func NewSystemHandler(version string) *SystemHandler {
	return &SystemHandler{buildVersion: version}
}

func (h *SystemHandler) Healthz(c *gin.Context) {
	rid := c.GetString("RequestID")
	response.OK(c, rid, "ok")
}

func (h *SystemHandler) Version(c *gin.Context) {
	rid := c.GetString("RequestID")
	response.OK(c, rid, map[string]string{"version": h.buildVersion})
}
""")

def rewrite_auth():
    with open('internal/api/handler/auth_handler.go', 'w') as f:
        f.write("""package handler

import (
	"github.com/cloudhelper/internal/api/response"
	"github.com/cloudhelper/internal/auth"
	"github.com/gin-gonic/gin"
)

const tokenHeader = "X-Session-Token"

type AuthHandler struct {
	svc *auth.Service
}

func NewAuthHandler(svc *auth.Service) *AuthHandler {
	return &AuthHandler{svc: svc}
}

func (h *AuthHandler) Login(c *gin.Context) {
	rid := c.GetString("RequestID")
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		response.BadRequest(c, rid, "username and password are required")
		return
	}
	token, err := h.svc.Login(req.Username, req.Password)
	if err != nil {
		response.Unauthorized(c, rid)
		return
	}
	response.OK(c, rid, map[string]string{"token": token, "username": req.Username})
}

func (h *AuthHandler) Logout(c *gin.Context) {
	rid := c.GetString("RequestID")
	token := c.GetHeader(tokenHeader)
	h.svc.Logout(token)
	response.OK(c, rid, nil)
}

func (h *AuthHandler) ChangePassword(c *gin.Context) {
	rid := c.GetString("RequestID")
	var req struct {
		OldPassword string `json:"old_password"`
		NewUsername string `json:"new_username"`
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	if req.OldPassword == "" || req.NewPassword == "" {
		response.BadRequest(c, rid, "old_password and new_password are required")
		return
	}
	if err := h.svc.ChangeCredentials(req.OldPassword, req.NewUsername, req.NewPassword); err != nil {
		if err == auth.ErrInvalidCredentials {
			response.Unauthorized(c, rid)
			return
		}
		response.BadRequest(c, rid, err.Error())
		return
	}
	response.OK(c, rid, map[string]string{"message": "credentials updated, please re-login"})
}

func (h *AuthHandler) ResetLocal(c *gin.Context) {
	rid := c.GetString("RequestID")
	if err := h.svc.ResetLocal(); err != nil {
		response.Internal(c, rid, "reset failure: "+err.Error())
		return
	}
	response.OK(c, rid, map[string]string{"message": "local admin credentials reset securely"})
}
""")

def rewrite_node():
    with open('internal/api/handler/node_handler.go', 'w') as f:
        f.write("""package handler

import (
	"strconv"
	"strings"
	"time"

	"github.com/cloudhelper/internal/adapter/node"
	"github.com/cloudhelper/internal/api/response"
	"github.com/gin-gonic/gin"
)

type NodeHandler struct {
	store *node.Store
}

func NewNodeHandler(store *node.Store) *NodeHandler {
	return &NodeHandler{store: store}
}

func (h *NodeHandler) List(c *gin.Context) {
	rid := c.GetString("RequestID")
	nodes, err := h.store.List()
	if err != nil {
		response.Internal(c, rid, "failed to load nodes: "+err.Error())
		return
	}
	response.OK(c, rid, nodes)
}

func (h *NodeHandler) Create(c *gin.Context) {
	rid := c.GetString("RequestID")
	var req struct {
		NodeName string `json:"node_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	n, err := h.store.Create(req.NodeName)
	if err != nil {
		response.BadRequest(c, rid, err.Error())
		return
	}
	response.OK(c, rid, n)
}

func (h *NodeHandler) Update(c *gin.Context) {
	rid := c.GetString("RequestID")
	nodeNoStr := c.Param("node_no")
	nodeNo, err := strconv.Atoi(nodeNoStr)
	if err != nil || nodeNo <= 0 {
		response.BadRequest(c, rid, "invalid node_no")
		return
	}
	var req struct {
		NodeName      string `json:"node_name"`
		Remark        string `json:"remark"`
		TargetSystem  string `json:"target_system"`
		DirectConnect bool   `json:"direct_connect"`
		PaymentCycle  string `json:"payment_cycle"`
		Cost          string `json:"cost"`
		ExpireAt      string `json:"expire_at"`
		VendorName    string `json:"vendor_name"`
		VendorURL     string `json:"vendor_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	updated, err := h.store.Update(nodeNo, node.UpdateSettings(req))
	if err != nil {
		response.BadRequest(c, rid, err.Error())
		return
	}
	response.OK(c, rid, updated)
}

func (h *NodeHandler) TestLink(c *gin.Context) {
	rid := c.GetString("RequestID")
	var req struct {
		NodeID       string `json:"node_id"`
		EndpointType string `json:"endpoint_type"`
		Scheme       string `json:"scheme"`
		Host         string `json:"host"`
		Port         int    `json:"port"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	ctx, cancel := c.Request.Context(), func() {}
	result, err := node.TestLink(ctx, req.NodeID, req.EndpointType, req.Scheme, req.Host, req.Port)
	if err != nil {
		response.BadRequest(c, rid, err.Error())
		return
	}
	defer cancel()
	response.OK(c, rid, result)
}
""")

def rewrite_upgrade():
    with open('internal/api/handler/upgrade_handler.go', 'w') as f:
        f.write("""package handler

import (
	"strconv"
	"strings"

	"github.com/cloudhelper/internal/adapter/logview"
	"github.com/cloudhelper/internal/adapter/upgrade"
	"github.com/cloudhelper/internal/api/response"
	"github.com/gin-gonic/gin"
)

type UpgradeHandler struct {
	logDir string
}

func NewUpgradeHandler(logDir string) *UpgradeHandler {
	return &UpgradeHandler{logDir: logDir}
}

func (h *UpgradeHandler) GetRelease(c *gin.Context) {
	rid := c.GetString("RequestID")
	project := strings.TrimSpace(c.Query("project"))
	ctx := c.Request.Context()
	info, err := upgrade.GetLatestRelease(ctx, project)
	if err != nil {
		response.Internal(c, rid, "failed to fetch release: "+err.Error())
		return
	}
	response.OK(c, rid, info)
}

func (h *UpgradeHandler) GetLogs(c *gin.Context) {
	rid := c.GetString("RequestID")
	lines, _ := strconv.Atoi(c.Query("lines"))
	sinceMinutes, _ := strconv.Atoi(c.Query("since_minutes"))
	minLevel := c.Query("min_level")

	resp, err := logview.ReadManagerLogs(h.logDir, lines, sinceMinutes, minLevel)
	if err != nil {
		response.Internal(c, rid, "failed to read logs: "+err.Error())
		return
	}
	response.OK(c, rid, resp)
}

func (h *UpgradeHandler) UpgradeManager(c *gin.Context) {
	rid := c.GetString("RequestID")
	response.BadRequest(c, rid, "Self-upgrading in Web UI is no longer supported in manager_service. Please use the installation script.")
}
""")

def rewrite_controller():
    with open('internal/api/handler/controller_handler.go', 'w') as f:
        f.write("""package handler

import (
	"github.com/cloudhelper/internal/adapter/controller"
	"github.com/cloudhelper/internal/api/response"
	"github.com/gin-gonic/gin"
)

type ControllerHandler struct {
	session *controller.Session
}

func NewControllerHandler(session *controller.Session) *ControllerHandler {
	return &ControllerHandler{session: session}
}

func (h *ControllerHandler) Login(c *gin.Context) {
	rid := c.GetString("RequestID")
	response.OK(c, rid, map[string]string{
		"message": "controller proxy login is deprecated in W3 HTTP scope",
	})
}
""")

def rewrite_netassist():
    with open('internal/api/handler/netassist_handler.go', 'w') as f:
        f.write("""package handler

import (
	"encoding/json"
	"net/http"

	"github.com/cloudhelper/internal/adapter/netassist"
	"github.com/cloudhelper/internal/api/response"
	"github.com/gin-gonic/gin"
)

type NetAssistHandler struct {
	client *netassist.Client
}

func NewNetAssistHandler(client *netassist.Client) *NetAssistHandler {
	return &NetAssistHandler{client: client}
}

func (h *NetAssistHandler) GetStatus(c *gin.Context) {
	rid := c.GetString("RequestID")
	raw, err := h.client.GetStatus(c.Request.Context(), "")
	if err != nil {
		response.Internal(c, rid, "failed to fetch net assist status: "+err.Error())
		return
	}
	
	envelope := response.Envelope{Code: response.CodeOK, Message: "success", RequestID: rid}
	var data interface{}
	if err := json.Unmarshal(raw, &data); err == nil {
		envelope.Data = data
	} else {
		envelope.Data = json.RawMessage(raw)
	}
	c.JSON(http.StatusOK, envelope)
}

func (h *NetAssistHandler) SwitchMode(c *gin.Context) {
	rid := c.GetString("RequestID")
	var req struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	raw, err := h.client.SwitchMode(c.Request.Context(), "", req.Mode)
	if err != nil {
		response.Internal(c, rid, "failed to switch mode: "+err.Error())
		return
	}
	
	envelope := response.Envelope{Code: response.CodeOK, Message: "success", RequestID: rid}
	var data interface{}
	if err := json.Unmarshal(raw, &data); err == nil {
		envelope.Data = data
	} else {
		envelope.Data = json.RawMessage(raw)
	}
	c.JSON(http.StatusOK, envelope)
}
""")

def rewrite_middleware():
    with open('internal/api/middleware/audit.go', 'w') as f:
        f.write("""package middleware

import (
	"fmt"
	"strings"
	"time"

	"github.com/cloudhelper/internal/auth"
	"github.com/cloudhelper/internal/logging"
	"github.com/gin-gonic/gin"
)

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := fmt.Sprintf("req-%d", time.Now().UnixNano())
		c.Set("RequestID", rid)
		c.Header("X-Request-ID", rid)
		c.Next()
	}
}

func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		latency := time.Since(start)
		rid := c.GetString("RequestID")
		status := c.Writer.Status()
		logging.Infof("[API] rid=%s method=%s path=%s status=%d latency=%v clientIP=%s",
			rid, c.Request.Method, c.Request.URL.Path, status, latency, c.ClientIP())
	}
}

func Auth(svc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetString("RequestID")
		authHeader := c.GetHeader("Authorization")
		token := c.GetHeader("X-Session-Token")
		if token == "" && strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		}
		if token == "" {
			c.JSON(401, gin.H{"code": 401, "message": "unauthorized", "request_id": rid})
			c.Abort()
			return
		}
		session, err := svc.Validate(token)
		if err != nil {
			c.JSON(401, gin.H{"code": 401, "message": "invalid session", "request_id": rid})
			c.Abort()
			return
		}
		c.Set("session", session)
		c.Next()
	}
}

func LocalhostOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetString("RequestID")
		ip := strings.Split(c.Request.RemoteAddr, ":")[0]
		if ip != "127.0.0.1" && ip != "::1" && ip != "localhost" {
			c.JSON(403, gin.H{"code": 403, "message": "forbidden: localhost only", "request_id": rid})
			c.Abort()
			return
		}
		c.Next()
	}
}
""")
    # Delete logger.go if it exists since we consolidated it into audit.go
    import os
    try:
        os.remove('internal/api/middleware/logger.go')
    except:
        pass

def rewrite_router():
    with open('internal/api/router.go', 'w') as f:
        f.write("""package api

import (
	"github.com/cloudhelper/internal/adapter/controller"
	"github.com/cloudhelper/internal/adapter/netassist"
	"github.com/cloudhelper/internal/adapter/node"
	"github.com/cloudhelper/internal/api/handler"
	"github.com/cloudhelper/internal/api/middleware"
	"github.com/cloudhelper/internal/auth"
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
""")

if __name__ == "__main__":
    rewrite_response()
    rewrite_system()
    rewrite_auth()
    rewrite_node()
    rewrite_upgrade()
    rewrite_controller()
    rewrite_netassist()
    rewrite_middleware()
    rewrite_router()
    print("DONE REWRITING")
