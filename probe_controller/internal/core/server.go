package core

import (
	"log"
	"net/http"
	"time"
)

func Run() {
	initControllerLogger()
	if handled, err := runControllerUpgradeVerifyModeFromArgs(); handled {
		if err != nil {
			log.Fatalf("controller upgrade verification failed: %v", err)
		}
		return
	}
	serverStartTime = time.Now()

	initStore()
	initProbeCertificateManager()
	initControllerScheduler()
	initTGAssistantScheduleEngine()
	initTGAssistantBotEngine()
	if err := cleanupControllerStaleExecutables(); err != nil {
		log.Printf("warning: failed to cleanup stale controller executable files: %v", err)
	}
	initAuth()
	initProbeReportIntervalControl()
	triggerAutoBackupControllerDataAsync("startup")

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
	mux.HandleFunc("/api/probe/proxy/probe-node/install-script", ProbeProxyInstallScriptHandler)
	mux.HandleFunc("/api/probe/link/config", ProbeLinkChainsHandler)
	mux.HandleFunc("/api/probe/link/chains", ProbeLinkChainsHandler)
	mux.HandleFunc("/api/probe/certificate", ProbeCertificateHandler)
	mux.HandleFunc("/api/probe", ProbeWSHandler)
	mux.HandleFunc("/api/tg/", TGAssistantBotWebhookHandler)
	mux.HandleFunc("/api/ws/tunnel/cloudserver", NetworkAssistantTunnelWSHandler)
	mux.HandleFunc("/dashboard/status", dashboardStatusHandler)
	mux.HandleFunc("/dashboard/probes", dashboardProbesHandler)
	mux.HandleFunc("/dashboard", dashboardHandler)
	mux.HandleFunc("/dashboard/favicon.svg", faviconSVGHandler)
	mux.HandleFunc("/dashboard/favicon.ico", faviconICOHandler)
	mux.HandleFunc("/favicon.svg", faviconSVGHandler)
	mux.HandleFunc("/favicon.ico", faviconICOHandler)
	mux.HandleFunc("/", rootHandler)
	return mux
}
