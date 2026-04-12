package handler

import (
	"strconv"
	"strings"

	"github.com/cloudhelper/manager_service/internal/adapter/logview"
	"github.com/cloudhelper/manager_service/internal/adapter/upgrade"
	"github.com/cloudhelper/manager_service/internal/api/response"
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
