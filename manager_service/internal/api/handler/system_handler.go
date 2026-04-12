package handler

import (
	"github.com/cloudhelper/manager_service/internal/api/response"
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
