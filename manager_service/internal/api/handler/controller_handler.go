package handler

import (
	"net/http"

	"github.com/cloudhelper/manager_service/internal/adapter/controller"
	"github.com/cloudhelper/manager_service/internal/api/response"
)

// ControllerHandler handles controller session proxies.
type ControllerHandler struct {
	session *controller.Session
}

func NewControllerHandler(session *controller.Session) *ControllerHandler {
	return &ControllerHandler{session: session}
}

// Login handles POST /api/controller/session/login
func (h *ControllerHandler) Login(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	// For W2/W3, we mock the local signFn as we removed the wails local_settings access, 
	// or we can implement it if required, but the frontend currently doesn't rely on it extensively.
	// We'll just return a disabled message for now, as W3 doesn't actively use this endpoint since auth is password-based.
	
	// Actually, network assistant needs no controller session login in backend
	// but the architecture doc requires POST /api/controller/session/login
	response.OK(w, rid, map[string]string{
		"message": "controller proxy login is deprecated in W3 HTTP scope",
	})
}
