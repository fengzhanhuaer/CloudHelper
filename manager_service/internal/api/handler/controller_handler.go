package handler

import (
	"github.com/cloudhelper/manager_service/internal/adapter/controller"
	"github.com/cloudhelper/manager_service/internal/api/response"
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
