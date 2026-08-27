//go:build linux_router

package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const probeLinuxRouterWebBodyLimit = 64 * 1024

//go:embed linux_router_pages/index.html
var probeLinuxRouterWebPageHTML string

func init() {
	probeProductRegisterLocalConsoleRoutes = registerProbeLinuxRouterLocalConsoleRoutes
	probeProductDecorateLocalConsolePage = decorateProbeLinuxRouterLocalConsolePage
	probeProductWrapLocalConsoleHandler = probeLinuxRouterLANOnlyMiddleware
	probeProductLocalAuthSetupTokenRequired = func() bool { return false }
}

func registerProbeLinuxRouterLocalConsoleRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("/local/router", probeLinuxRouterWebPageHandler)
	mux.HandleFunc("/local/router/one-arm", probeLinuxRouterWebPageHandler)
	mux.HandleFunc("/local/router/api/status", probeLinuxRouterWebStatusHandler)
	mux.HandleFunc("/local/router/api/config", probeLinuxRouterWebConfigHandler)
	mux.HandleFunc("/local/router/api/network/auto", probeLinuxRouterWebNetworkAutoHandler)
	mux.HandleFunc("/local/router/api/fail-open", probeLinuxRouterWebFailOpenHandler)
	mux.HandleFunc("/local/router/api/resume", probeLinuxRouterWebResumeHandler)
	mux.HandleFunc("/local/router/api/logs", probeLinuxRouterWebLogsHandler)
	mux.HandleFunc("/local/router/api/upgrade", probeLocalSystemUpgradeHandler)
	mux.HandleFunc("/local/router/api/upgrade/check", probeLocalSystemUpgradeCheckHandler)
	mux.HandleFunc("/local/router/api/upgrade/status", probeLocalSystemUpgradeStatusHandler)
}

func decorateProbeLinuxRouterLocalConsolePage(path, pageHTML string) string {
	if path != "/local/virtual-router" {
		return pageHTML
	}
	const marker = "<!-- product-virtual-router-subtabs -->"
	const tab = `<a class="subtab" href="/local/router" role="tab" aria-selected="false">旁路由</a><a class="subtab" href="/local/router/one-arm" role="tab" aria-selected="false">单臂路由</a>`
	return strings.Replace(pageHTML, marker, tab, 1)
}

func probeLinuxRouterLANOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isProbeLocalConsoleTrusted(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
		if !isProbeLinuxRouterLANRequest(r) {
			http.Error(w, "local network access only", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func isProbeLinuxRouterLANRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	remoteHost, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil || !isProbeLinuxRouterLocalIPv4(remoteHost) {
		return false
	}
	host := strings.TrimSpace(r.Host)
	if parsedHost, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = parsedHost
	}
	return isProbeLinuxRouterLocalIPv4(host)
}

func isProbeLinuxRouterLocalIPv4(raw string) bool {
	addr, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(raw), "[]"))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	if !addr.Is4() {
		return false
	}
	if addr.IsPrivate() || addr.IsLoopback() {
		return true
	}
	fakeCIDR, err := netip.ParsePrefix(currentProbeVirtualRouterFakeIPCIDR())
	return err == nil && fakeCIDR.Contains(addr)
}

func probeLinuxRouterWebPageHandler(w http.ResponseWriter, r *http.Request) {
	path := "/local/router"
	if r != nil && r.URL.Path == "/local/router/one-arm" {
		path = "/local/router/one-arm"
	}
	serveProbeLocalHTMLPage(w, r, path, probeLinuxRouterWebPageHTML)
}

func probeLinuxRouterWebStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	desired, manualFailOpen, nodeID := currentProbeLinuxRouterLocalState()
	report := currentProbeLinuxRouterReport()
	probeReporterRPCState.mu.Lock()
	controllerConnected := probeReporterRPCState.stream != nil && probeReporterRPCState.encoder != nil
	probeReporterRPCState.mu.Unlock()
	listenAddr, startedAt := currentProbeLocalConsoleRuntime()
	hostname, _ := os.Hostname()
	uptimeSeconds := int64(0)
	if !startedAt.IsZero() {
		uptimeSeconds = int64(time.Since(startedAt).Seconds())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id":              nodeID,
		"hostname":             strings.TrimSpace(hostname),
		"version":              BuildVersion,
		"architecture":         runtime.GOARCH,
		"listen_addr":          listenAddr,
		"uptime_seconds":       uptimeSeconds,
		"controller_connected": controllerConnected,
		"manual_fail_open":     manualFailOpen,
		"config":               desired,
		"runtime":              report,
		"connections":          probeLocalVirtualRouterPathStatusPayloads(),
		"interfaces":           listProbeLinuxRouterWebInterfaces(),
	})
}

func listProbeLinuxRouterWebInterfaces() []map[string]any {
	interfaces, err := net.Interfaces()
	if err != nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(interfaces))
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || strings.TrimSpace(iface.Name) == "" {
			continue
		}
		addresses := make([]string, 0)
		if values, addrErr := iface.Addrs(); addrErr == nil {
			for _, value := range values {
				addresses = append(addresses, value.String())
			}
		}
		out = append(out, map[string]any{"name": iface.Name, "addresses": addresses})
	}
	return out
}

func probeLinuxRouterWebConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLinuxRouterWebBodyLimit)
	defer body.Close()
	var config probeLinuxRouterLocalConfig
	if err := decodeProbeLinuxRouterWebJSON(body, &config); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := applyProbeLinuxRouterLocalConfig(config); err != nil {
		var configErr *probeLinuxRouterLocalConfigError
		if errors.As(err, &configErr) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": configErr.Error()})
			return
		}
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func probeLinuxRouterWebNetworkAutoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLinuxRouterWebBodyLimit)
	defer body.Close()
	var request struct {
		Interface string `json:"interface"`
	}
	if err := decodeProbeLinuxRouterWebJSON(body, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	interfaceName := strings.TrimSpace(request.Interface)
	if interfaceName == "" {
		interfaceName = "auto"
	}
	if interfaceName != "auto" && !probeLinuxRouterInterfacePattern.MatchString(interfaceName) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid router interface"})
		return
	}
	resolvedInterface, changed, err := probeLinuxRouterPlatformRestoreInterfaceAuto(interfaceName)
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"interface":           resolvedInterface,
		"changed":             changed,
		"reconnect_scheduled": changed,
	})
}

func probeLinuxRouterWebFailOpenHandler(w http.ResponseWriter, r *http.Request) {
	probeLinuxRouterWebAction(w, r, true)
}

func probeLinuxRouterWebResumeHandler(w http.ResponseWriter, r *http.Request) {
	probeLinuxRouterWebAction(w, r, false)
}

func probeLinuxRouterWebAction(w http.ResponseWriter, r *http.Request, failOpen bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if err := setProbeLinuxRouterManualFailOpen(failOpen); err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "manual_fail_open": failOpen})
}

func probeLinuxRouterWebLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	lines := 200
	if value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("lines"))); err == nil && value > 0 {
		lines = value
	}
	if lines > 500 {
		lines = 500
	}
	source, _, content, entries, err := collectProbeLocalLogsForView(lines, 0, "", "")
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": source, "content": content, "entries": entries})
}

func decodeProbeLinuxRouterWebJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}
