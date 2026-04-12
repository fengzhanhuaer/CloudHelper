package handler

import (
	"strings"

	"github.com/cloudhelper/manager_service/internal/adapter/logview"
	"github.com/cloudhelper/manager_service/internal/adapter/upgrade"
	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/gin-gonic/gin"
)

// UpgradeHandler handles upgrade release query, log viewing, and upgrade task endpoints.
// PKG-W2-04 / RQ-004
type UpgradeHandler struct {
	logDir string
}

func NewUpgradeHandler(logDir string) *UpgradeHandler {
	return &UpgradeHandler{logDir: logDir}
}

// GetRelease handles GET /api/upgrade/release?project=...
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

// GetLogs handles GET /api/logs/manager?lines=200&since_minutes=0&min_level=normal
func (h *UpgradeHandler) GetLogs(c *gin.Context) {
	rid := c.GetString("RequestID")
	lines := 0
	sinceMinutes := 0
	if v := c.Query("lines"); v != "" {
		if n, err := parseInt(v); err == nil {
			lines = n
		}
	}
	if v := c.Query("since_minutes"); v != "" {
		if n, err := parseInt(v); err == nil {
			sinceMinutes = n
		}
	}
	minLevel := c.Query("min_level")

	resp, err := logview.ReadManagerLogs(h.logDir, lines, sinceMinutes, minLevel)
	if err != nil {
		response.Internal(c, rid, "failed to read logs: "+err.Error())
		return
	}
	response.OK(c, rid, resp)
}

// UpgradeManager handles POST /api/upgrade/manager.
// PKG-FIX-P0-02 / RQ-004
// Architecture decision (RQ-008): manager_service binary self-upgrade is not supported
// in the Web mode because it would require the service to replace its own executable
// while running, which is not safe or portable.
// The endpoint is available and returns a structured response describing the constraint,
// allowing the frontend to present a helpful message rather than an opaque error.
func (h *UpgradeHandler) UpgradeManager(c *gin.Context) {
	rid := c.GetString("RequestID")
	response.OK(c, rid, map[string]interface{}{
		"supported": false,
		"reason":    "self-upgrade is not supported in Web mode; please use the installation script to upgrade manager_service",
		"docs_url":  "https://github.com/yourorg/cloudhelper/blob/main/README.md#upgrade",
	})
}

func parseInt(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errNotInt
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

var errNotInt = &parseError{"not an integer"}

type parseError struct{ msg string }

func (e *parseError) Error() string { return e.msg }
