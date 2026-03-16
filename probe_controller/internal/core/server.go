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
	initProbeReportIntervalControl()
	if err := autoBackupControllerData(); err != nil {
		log.Printf("warning: failed to backup controller data: %v", err)
	}

	mux := NewMux()
	handler := enforceProbeScopeMiddleware(mux)

	log.Println("CloudHelper Probe Controller is running at http://" + listenAddr)
	if err := http.ListenAndServe(listenAddr, handler); err != nil {
		log.Fatal(err)
	}
}

func NewMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ping", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(PingHandler))))
	mux.HandleFunc("/api/auth/nonce", corsMiddleware(requireHTTPSMiddleware(NonceHandler)))
	mux.HandleFunc("/api/auth/login", corsMiddleware(requireHTTPSMiddleware(LoginHandler)))
	mux.HandleFunc("/api/admin/ws", AdminWSHandler)
	mux.HandleFunc("/api/probe/proxy/github/latest", ProbeProxyGitHubLatestHandler)
	mux.HandleFunc("/api/probe/proxy/download", ProbeProxyDownloadHandler)
	mux.HandleFunc("/api/probe", ProbeWSHandler)
	mux.HandleFunc("/api/ws/tunnel/cloudserver", NetworkAssistantTunnelWSHandler)
	mux.HandleFunc("/dashboard/status", dashboardStatusHandler)
	mux.HandleFunc("/dashboard/probes", dashboardProbesHandler)
	mux.HandleFunc("/dashboard", dashboardHandler)
	mux.HandleFunc("/", rootHandler)
	return mux
}
