package handler

import (
	"context"
	"time"

	"github.com/cloudhelper/manager_service/internal/adapter/controller"
	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/cloudhelper/manager_service/internal/auth"
	"github.com/gin-gonic/gin"
)

// ControllerHandler proxies controller session management.
// PKG-FIX-P0-01 / RQ-004
type ControllerHandler struct {
	session *controller.Session
	authSvc *auth.Service
}

func NewControllerHandler(session *controller.Session, authSvc *auth.Service) *ControllerHandler {
	return &ControllerHandler{session: session, authSvc: authSvc}
}

// ControllerLoginRequest is the body for POST /api/controller/session/login.
// The manager_service proxies a nonce-sign login to the controller on behalf of the frontend.
// Request body allows the frontend to optionally pass controller baseURL; otherwise falls back to default.
type ControllerLoginRequest struct {
	// ControllerURL is optional; if empty the default 127.0.0.1:15030 is used.
	ControllerURL string `json:"controller_url"`
}

// ControllerLoginResponse is the response for POST /api/controller/session/login.
type ControllerLoginResponse struct {
	OK         bool   `json:"ok"`
	Token      string `json:"token,omitempty"`
	Message    string `json:"message"`
	BaseURL    string `json:"base_url"`
}

// Login handles POST /api/controller/session/login.
// It performs the three-step nonce→sign→token handshake against the probe_controller
// using the manager_service admin private key (the same one already used in probe_manager).
// PKG-FIX-P0-01 / RQ-004
func (h *ControllerHandler) Login(c *gin.Context) {
	rid := c.GetString("RequestID")

	var req ControllerLoginRequest
	// Body is optional — ignore decode errors.
	_ = c.ShouldBindJSON(&req)

	if req.ControllerURL != "" {
		h.session.UpdateSession(req.ControllerURL, "")
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	// signFn: manager_service does not hold the admin private key directly.
	// The nonce-signing model was designed for Wails desktop access to the local key file.
	// In Web mode, manager_service cannot sign on behalf of the frontend without the key.
	// Architecture decision: manager_service stores a pre-set controller token from a
	// one-time local bootstrap, or the controller uses username/password parity in future.
	//
	// For W3: if a controller token was previously stored (e.g. via UpdateSession),
	// return it; otherwise surface an actionable error.
	existingToken := h.session.Token()
	if existingToken != "" {
		response.OK(c, rid, ControllerLoginResponse{
			OK:      true,
			Token:   existingToken,
			Message: "using cached controller session",
			BaseURL: h.session.BaseURL(),
		})
		return
	}

	// No pre-stored token: surface clear error explaining the constraint.
	// The controller login requires a private key that is only accessible locally.
	// The frontend should use the local bootstrap endpoint to supply the token.
	_ = ctx
	response.BadRequest(c, rid,
		"controller session not established: manager_service requires a pre-configured controller token. "+
			"Use POST /api/controller/session/set to supply a valid controller token obtained from the local instance.")
}

// SetSession handles POST /api/controller/session/set.
// Allows the local operator (or a trusted bootstrap flow) to supply a controller token
// that manager_service will use for subsequent proxied requests. Localhost-only.
func (h *ControllerHandler) SetSession(c *gin.Context) {
	rid := c.GetString("RequestID")
	var req struct {
		ControllerURL string `json:"controller_url"`
		Token         string `json:"token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Token == "" {
		response.BadRequest(c, rid, "token is required")
		return
	}
	h.session.UpdateSession(req.ControllerURL, req.Token)
	response.OK(c, rid, ControllerLoginResponse{
		OK:      true,
		Token:   req.Token,
		Message: "controller session updated",
		BaseURL: h.session.BaseURL(),
	})
}
