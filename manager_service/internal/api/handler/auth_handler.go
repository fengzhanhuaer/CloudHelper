// Package handler implements HTTP handlers for manager_service API endpoints.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/cloudhelper/manager_service/internal/auth"
)

const tokenHeader = "X-Session-Token"

// AuthHandler handles authentication-related endpoints.
type AuthHandler struct {
	svc *auth.Service
}

// NewAuthHandler constructs an AuthHandler.
func NewAuthHandler(svc *auth.Service) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// Login handles POST /api/auth/login
// Body: { "username": "...", "password": "..." }
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, rid, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		response.BadRequest(w, rid, "username and password are required")
		return
	}

	token, err := h.svc.Login(req.Username, req.Password)
	if err != nil {
		response.Unauthorized(w, rid)
		return
	}

	response.OK(w, rid, map[string]string{
		"token":    token,
		"username": req.Username,
	})
}

// Logout handles POST /api/auth/logout
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	token := r.Header.Get(tokenHeader)
	h.svc.Logout(token)
	response.OK(w, rid, nil)
}

// ChangePassword handles POST /api/auth/password/change
// Body: { "old_password": "...", "new_username": "...", "new_password": "..." }
// RQ-006: allow changing username and password after login.
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")

	var req struct {
		OldPassword string `json:"old_password"`
		NewUsername string `json:"new_username"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, rid, "invalid request body")
		return
	}
	if req.OldPassword == "" || req.NewPassword == "" {
		response.BadRequest(w, rid, "old_password and new_password are required")
		return
	}

	if err := h.svc.ChangeCredentials(req.OldPassword, req.NewUsername, req.NewPassword); err != nil {
		if err == auth.ErrInvalidCredentials {
			response.Unauthorized(w, rid)
			return
		}
		response.BadRequest(w, rid, err.Error())
		return
	}

	response.OK(w, rid, map[string]string{
		"message": "credentials updated, please re-login",
	})
}

// ResetLocal handles POST /api/auth/password/reset-local
// Only accessible from 127.0.0.1 (enforced by LocalhostOnly middleware).
func (h *AuthHandler) ResetLocal(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	if err := h.svc.ResetLocal(); err != nil {
		response.Internal(w, rid, "reset failed")
		return
	}
	response.OK(w, rid, map[string]string{
		"message": "credentials reset to default, please re-login",
	})
}
