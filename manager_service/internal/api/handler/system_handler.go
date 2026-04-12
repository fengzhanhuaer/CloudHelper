package handler

import (
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/cloudhelper/manager_service/internal/api/response"
)

// SystemHandler handles system-level endpoints.
type SystemHandler struct {
	buildVersion string
}

// NewSystemHandler constructs a SystemHandler.
func NewSystemHandler(buildVersion string) *SystemHandler {
	return &SystemHandler{buildVersion: buildVersion}
}

// Healthz handles GET /healthz — no auth required.
func (h *SystemHandler) Healthz(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	response.OK(w, rid, map[string]string{"status": "ok"})
}

// Version handles GET /api/system/version — auth required.
func (h *SystemHandler) Version(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	version := h.resolveVersion()
	response.OK(w, rid, map[string]string{"version": version})
}

func (h *SystemHandler) resolveVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimSpace(bi.Main.Version); v != "" && v != "(devel)" {
			return v
		}
	}
	if v := strings.TrimSpace(h.buildVersion); v != "" && !strings.EqualFold(v, "dev") {
		return v
	}
	return "dev"
}
