package core

import (
	"log"
	"net/http"
	"time"
)

func Run() {
	initControllerLogger()
	serverStartTime = time.Now()

	initStore()
	if err := cleanupControllerStaleExecutables(); err != nil {
		log.Printf("warning: failed to cleanup stale controller executable files: %v", err)
	}
	initAuth()
	if err := autoBackupControllerData(); err != nil {
		log.Printf("warning: failed to backup controller data: %v", err)
	}

	mux := NewMux()

	log.Println("CloudHelper Probe Controller is running at http://" + listenAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatal(err)
	}
}

func NewMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ping", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(PingHandler))))
	mux.HandleFunc("/api/auth/nonce", corsMiddleware(requireHTTPSMiddleware(NonceHandler)))
	mux.HandleFunc("/api/auth/login", corsMiddleware(requireHTTPSMiddleware(LoginHandler)))
	mux.HandleFunc("/api/admin/status", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminStatusHandler))))
	mux.HandleFunc("/api/admin/logs", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminLogsHandler))))
	mux.HandleFunc("/api/admin/version", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminVersionHandler))))
	mux.HandleFunc("/api/admin/upgrade", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminUpgradeHandler))))
	mux.HandleFunc("/api/admin/upgrade/progress", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminUpgradeProgressHandler))))
	mux.HandleFunc("/api/admin/proxy/github/latest", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminProxyGitHubLatestHandler))))
	mux.HandleFunc("/api/admin/proxy/download", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminProxyDownloadHandler))))
	mux.HandleFunc("/api/admin/probe/upgrade", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminUpgradeProbeNodeHandler))))
	mux.HandleFunc("/api/admin/probe/upgrade/all", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminUpgradeAllProbeNodesHandler))))
	mux.HandleFunc("/api/admin/probe/nodes", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminGetProbeNodesHandler))))
	mux.HandleFunc("/api/admin/probe/nodes/sync", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminSyncProbeNodesHandler))))
	mux.HandleFunc("/api/admin/probe/secret", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminUpsertProbeSecretHandler))))
	mux.HandleFunc("/api/admin/tunnel/nodes", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminTunnelNodesHandler))))
	mux.HandleFunc("/api/admin/ws/status", AdminStatusWSHandler)
	mux.HandleFunc("/api/probe/nonce", ProbeNonceHandler)
	mux.HandleFunc("/api/probe/proxy/github/latest", ProbeProxyGitHubLatestHandler)
	mux.HandleFunc("/api/probe/proxy/download", ProbeProxyDownloadHandler)
	mux.HandleFunc("/api/probe", ProbeWSHandler)
	mux.HandleFunc("/api/ws/tunnel/cloudserver", NetworkAssistantTunnelWSHandler)
	mux.HandleFunc("/dashboard/status", dashboardStatusHandler)
	mux.HandleFunc("/dashboard", dashboardHandler)
	mux.HandleFunc("/", rootHandler)
	return mux
}
