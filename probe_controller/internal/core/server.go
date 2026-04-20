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
			logControllerErrorf("controller upgrade verification failed: %v", err)
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
	initCloudflareZeroTrustSyncEngine()
	if err := cleanupControllerStaleExecutables(); err != nil {
		logControllerWarnf("failed to cleanup stale controller executable files: %v", err)
	}
	initAuth()
	initMngAuth()
	initProbeReportIntervalControl()
	triggerAutoBackupControllerDataAsync("startup")

	mux := NewMux()
	handler := enforceProbeScopeMiddleware(mux)

	logControllerInfof("CloudHelper Probe Controller is running at http://%s", listenAddr)
	if err := http.ListenAndServe(listenAddr, handler); err != nil {
		logControllerErrorf("controller http server stopped: %v", err)
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
	mux.HandleFunc("/mng/api/bootstrap", mngBootstrapHandler)
	mux.HandleFunc("/mng/api/register", mngRegisterHandler)
	mux.HandleFunc("/mng/api/login", mngLoginHandler)
	mux.HandleFunc("/mng/api/logout", mngLogoutHandler)
	mux.HandleFunc("/mng/api/session", mngSessionHandler)
	mux.HandleFunc("/mng/api/panel/summary", mngAuthRequiredMiddleware(mngPanelSummaryHandler))
	mux.HandleFunc("/mng/api/system/version", mngAuthRequiredMiddleware(mngSystemVersionHandler))
	mux.HandleFunc("/mng/api/system/upgrade", mngAuthRequiredMiddleware(mngSystemUpgradeHandler))
	mux.HandleFunc("/mng/api/system/upgrade/progress", mngAuthRequiredMiddleware(mngSystemUpgradeProgressHandler))
	mux.HandleFunc("/mng/api/system/reconnect/check", mngSystemReconnectCheckHandler)
	mux.HandleFunc("/mng/api/probe/nodes", mngAuthRequiredMiddleware(mngProbeNodesHandler))
	mux.HandleFunc("/mng/api/probe/node/create", mngAuthRequiredMiddleware(mngProbeNodeCreateHandler))
	mux.HandleFunc("/mng/api/probe/node/update", mngAuthRequiredMiddleware(mngProbeNodeUpdateHandler))
	mux.HandleFunc("/mng/api/probe/node/delete", mngAuthRequiredMiddleware(mngProbeNodeDeleteHandler))
	mux.HandleFunc("/mng/api/probe/node/restore", mngAuthRequiredMiddleware(mngProbeNodeRestoreHandler))
	mux.HandleFunc("/mng/api/probe/status", mngAuthRequiredMiddleware(mngProbeStatusHandler))
	mux.HandleFunc("/mng/api/probe/logs", mngAuthRequiredMiddleware(mngProbeLogsHandler))
	mux.HandleFunc("/mng/api/probe/upgrade", mngAuthRequiredMiddleware(mngProbeUpgradeHandler))
	mux.HandleFunc("/mng/api/probe/upgrade/all", mngAuthRequiredMiddleware(mngProbeUpgradeAllHandler))
	mux.HandleFunc("/mng/api/probe/shell/session/start", mngAuthRequiredMiddleware(mngProbeShellSessionStartHandler))
	mux.HandleFunc("/mng/api/probe/shell/session/exec", mngAuthRequiredMiddleware(mngProbeShellSessionExecHandler))
	mux.HandleFunc("/mng/api/probe/shell/session/stop", mngAuthRequiredMiddleware(mngProbeShellSessionStopHandler))
	mux.HandleFunc("/mng/api/probe/shell/shortcuts", mngAuthRequiredMiddleware(mngProbeShellShortcutsGetHandler))
	mux.HandleFunc("/mng/api/probe/shell/shortcuts/upsert", mngAuthRequiredMiddleware(mngProbeShellShortcutsUpsertHandler))
	mux.HandleFunc("/mng/api/probe/shell/shortcuts/delete", mngAuthRequiredMiddleware(mngProbeShellShortcutsDeleteHandler))
	mux.HandleFunc("/mng", mngEntryHandler)
	mux.HandleFunc("/mng/panel", mngAuthRequiredMiddleware(mngPanelHandler))
	mux.HandleFunc("/mng/settings", mngAuthRequiredMiddleware(mngSettingsHandler))
	mux.HandleFunc("/mng/probe", mngAuthRequiredMiddleware(mngProbePageHandler))
	mux.HandleFunc("/dashboard/favicon.svg", faviconSVGHandler)
	mux.HandleFunc("/dashboard/favicon.ico", faviconICOHandler)
	mux.HandleFunc("/favicon.svg", faviconSVGHandler)
	mux.HandleFunc("/favicon.ico", faviconICOHandler)
	mux.HandleFunc("/", rootHandler)
	return mux
}
