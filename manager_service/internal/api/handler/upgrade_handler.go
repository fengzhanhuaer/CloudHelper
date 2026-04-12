package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudhelper/manager_service/internal/adapter/logview"
	"github.com/cloudhelper/manager_service/internal/adapter/upgrade"
	"github.com/cloudhelper/manager_service/internal/api/response"
)

// UpgradeHandler handles upgrade release query and log viewing endpoints.
// PKG-W2-04 / RQ-004
type UpgradeHandler struct {
	logDir string
}

// NewUpgradeHandler constructs an UpgradeHandler.
// logDir is the directory containing manager_service.log.
func NewUpgradeHandler(logDir string) *UpgradeHandler {
	return &UpgradeHandler{logDir: logDir}
}

// GetRelease handles GET /api/upgrade/release?project=...
func (h *UpgradeHandler) GetRelease(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	project := strings.TrimSpace(r.URL.Query().Get("project"))

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	info, err := upgrade.GetLatestRelease(ctx, project)
	if err != nil {
		response.Internal(w, rid, "failed to fetch release: "+err.Error())
		return
	}
	response.OK(w, rid, info)
}

// GetLogs handles GET /api/logs/manager?lines=200&since_minutes=0&min_level=normal
func (h *UpgradeHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	q := r.URL.Query()
	lines, _ := strconv.Atoi(q.Get("lines"))
	sinceMinutes, _ := strconv.Atoi(q.Get("since_minutes"))
	minLevel := q.Get("min_level")

	resp, err := logview.ReadManagerLogs(h.logDir, lines, sinceMinutes, minLevel)
	if err != nil {
		response.Internal(w, rid, "failed to read logs: "+err.Error())
		return
	}
	response.OK(w, rid, resp)
}

// UpgradeManager handles POST /api/upgrade/manager
// RQ-008: manager_service does not upgrade itself.
func (h *UpgradeHandler) UpgradeManager(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	// Since upgrading execute path is frozen inside probe_manager (RQ-008),
	// this endpoint just informs the client that web-based upgrading is discontinued
	// and they should use the install script.
	response.BadRequest(w, rid, "Self-upgrading in Web UI is no longer supported in manager_service. Please use the installation script.")
}
