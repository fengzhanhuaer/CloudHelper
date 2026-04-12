package handler

import (
	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/cloudhelper/manager_service/internal/auth"
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
