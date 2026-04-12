// Package api wires up all HTTP routes for manager_service.
package api

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/cloudhelper/manager_service/internal/adapter/node"
	"github.com/cloudhelper/manager_service/internal/api/handler"
	"github.com/cloudhelper/manager_service/internal/api/middleware"
	"github.com/cloudhelper/manager_service/internal/auth"
)

// RouterOptions carries runtime dependencies for the router.
type RouterOptions struct {
	AuthSvc      *auth.Service
	NodeStore    *node.Store
	BuildVersion string
	LogDir       string
}

// NewRouter creates and returns the fully configured HTTP router.
//
// W1 routes (auth + system):
//
//	GET  /healthz                              — public
//	GET  /api/system/version                   — auth
//	POST /api/auth/login                       — public
//	POST /api/auth/logout                      — auth
//	POST /api/auth/password/change             — auth
//	POST /api/auth/password/reset-local        — localhost only
//
// W2 routes (probe node, link test, upgrade, logs):
//
//	GET  /api/probe/nodes                      — auth
//	POST /api/probe/nodes                      — auth
//	PUT  /api/probe/nodes/{node_no}            — auth
//	POST /api/probe/link/test                  — auth
//	GET  /api/upgrade/release                  — auth
//	GET  /api/logs/manager                     — auth
func NewRouter(opts RouterOptions) http.Handler {
	mux := http.NewServeMux()

	sysH := handler.NewSystemHandler(opts.BuildVersion)
	authH := handler.NewAuthHandler(opts.AuthSvc)
	nodeH := handler.NewNodeHandler(opts.NodeStore)
	upgradeH := handler.NewUpgradeHandler(opts.LogDir)

	requireAuth := middleware.Auth(opts.AuthSvc)

	// ---- Public ----
	mux.HandleFunc("GET /healthz", sysH.Healthz)
	mux.HandleFunc("POST /api/auth/login", authH.Login)

	// Localhost-only reset.
	mux.Handle("POST /api/auth/password/reset-local",
		middleware.LocalhostOnly(http.HandlerFunc(authH.ResetLocal)),
	)

	// ---- Authenticated — W1 ----
	mux.Handle("GET /api/system/version", requireAuth(http.HandlerFunc(sysH.Version)))
	mux.Handle("POST /api/auth/logout", requireAuth(http.HandlerFunc(authH.Logout)))
	mux.Handle("POST /api/auth/password/change", requireAuth(http.HandlerFunc(authH.ChangePassword)))

	// ---- Authenticated — W2: Probe nodes ----
	mux.Handle("GET /api/probe/nodes", requireAuth(http.HandlerFunc(nodeH.List)))
	mux.Handle("POST /api/probe/nodes", requireAuth(http.HandlerFunc(nodeH.Create)))
	mux.Handle("PUT /api/probe/nodes/{node_no}", requireAuth(http.HandlerFunc(nodeH.Update)))

	// ---- Authenticated — W2: Link test ----
	mux.Handle("POST /api/probe/link/test", requireAuth(http.HandlerFunc(nodeH.TestLink)))

	// ---- Authenticated — W2: Upgrade & logs ----
	mux.Handle("GET /api/upgrade/release", requireAuth(http.HandlerFunc(upgradeH.GetRelease)))
	mux.Handle("GET /api/logs/manager", requireAuth(http.HandlerFunc(upgradeH.GetLogs)))

	// Global middleware: RequestID → Logger → mux.
	var h http.Handler = mux
	h = middleware.Logger(h)
	h = middleware.RequestID(h)
	return h
}

// resolveLogDir returns the log directory path based on the executable location.
func resolveLogDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "log")
	}
	return filepath.Join(".", "log")
}

