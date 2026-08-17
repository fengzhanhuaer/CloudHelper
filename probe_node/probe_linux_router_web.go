//go:build linux_router

package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeLinuxRouterWebListenEnv     = "PROBE_ROUTER_WEB_LISTEN"
	probeLinuxRouterWebListenDefault = "0.0.0.0:18080"
	probeLinuxRouterWebBodyLimit     = 64 * 1024
)

//go:embed linux_router_pages/index.html
var probeLinuxRouterWebPageHTML string

var probeLinuxRouterWebState = struct {
	sync.Mutex
	server     *http.Server
	listenAddr string
	startedAt  time.Time
}{}

func init() {
	probeProductLocalWebStart = startProbeLinuxRouterWeb
	probeProductLocalWebStop = stopProbeLinuxRouterWeb
}

func resolveProbeLinuxRouterWebListenAddr() (string, error) {
	addr := strings.TrimSpace(os.Getenv(probeLinuxRouterWebListenEnv))
	if addr == "" {
		addr = probeLinuxRouterWebListenDefault
	}
	host, portRaw, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid router web listen address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid router web listen port %q", portRaw)
	}
	if host != "0.0.0.0" && !isProbeLinuxRouterPrivateIPv4(host) {
		return "", errors.New("router web listen host must be 0.0.0.0, loopback, or a private IPv4 address")
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func startProbeLinuxRouterWeb(nodeID string) error {
	if strings.TrimSpace(nodeID) == "" {
		return errors.New("router web requires a node identity")
	}
	authManager, err := ensureProbeLocalAuthManager()
	if err != nil {
		return err
	}
	if !authManager.registered() {
		if _, err := ensureProbeLocalSetupToken(); err != nil {
			return err
		}
	}
	addr, err := resolveProbeLinuxRouterWebListenAddr()
	if err != nil {
		return err
	}

	probeLinuxRouterWebState.Lock()
	if probeLinuxRouterWebState.server != nil {
		probeLinuxRouterWebState.Unlock()
		return nil
	}
	listener, err := net.Listen("tcp4", addr)
	if err != nil {
		probeLinuxRouterWebState.Unlock()
		return err
	}
	server := &http.Server{
		Handler:           buildProbeLinuxRouterWebHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	probeLinuxRouterWebState.server = server
	probeLinuxRouterWebState.listenAddr = listener.Addr().String()
	probeLinuxRouterWebState.startedAt = time.Now()
	listenAddr := probeLinuxRouterWebState.listenAddr
	probeLinuxRouterWebState.Unlock()

	logProbeInfof("linux router local web listening on http://%s (private IPv4 access only)", listenAddr)
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logProbeErrorf("linux router local web exited: listen=%s err=%v", listenAddr, err)
		}
		probeLinuxRouterWebState.Lock()
		if probeLinuxRouterWebState.server == server {
			probeLinuxRouterWebState.server = nil
			probeLinuxRouterWebState.listenAddr = ""
			probeLinuxRouterWebState.startedAt = time.Time{}
		}
		probeLinuxRouterWebState.Unlock()
	}()
	return nil
}

func stopProbeLinuxRouterWeb() {
	probeLinuxRouterWebState.Lock()
	server := probeLinuxRouterWebState.server
	probeLinuxRouterWebState.server = nil
	probeLinuxRouterWebState.listenAddr = ""
	probeLinuxRouterWebState.startedAt = time.Time{}
	probeLinuxRouterWebState.Unlock()
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		_ = server.Close()
		logProbeWarnf("linux router local web forced closed: %v", err)
	}
}

func buildProbeLinuxRouterWebHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", probeLinuxRouterWebRootHandler)
	mux.HandleFunc("/local/router", probeLinuxRouterWebPageHandler)
	mux.HandleFunc("/local/api/auth/bootstrap", probeLocalAuthBootstrapHandler)
	mux.HandleFunc("/local/api/auth/register", probeLocalAuthRegisterHandler)
	mux.HandleFunc("/local/api/auth/login", probeLocalAuthLoginHandler)
	mux.HandleFunc("/local/api/auth/logout", probeLocalAuthLogoutHandler)
	mux.HandleFunc("/local/api/auth/session", probeLocalAuthSessionHandler)
	mux.HandleFunc("/local/router/api/status", probeLinuxRouterWebStatusHandler)
	mux.HandleFunc("/local/router/api/config", probeLinuxRouterWebConfigHandler)
	mux.HandleFunc("/local/router/api/fail-open", probeLinuxRouterWebFailOpenHandler)
	mux.HandleFunc("/local/router/api/resume", probeLinuxRouterWebResumeHandler)
	mux.HandleFunc("/local/router/api/logs", probeLinuxRouterWebLogsHandler)
	return probeLinuxRouterLANOnlyMiddleware(mux)
}

func probeLinuxRouterLANOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if err != nil || !isProbeLinuxRouterPrivateIPv4(remoteHost) {
		return false
	}
	host := strings.TrimSpace(r.Host)
	if parsedHost, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = parsedHost
	}
	return isProbeLinuxRouterPrivateIPv4(host)
}

func isProbeLinuxRouterPrivateIPv4(raw string) bool {
	addr, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(raw), "[]"))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.Is4() && (addr.IsPrivate() || addr.IsLoopback())
}

func probeLinuxRouterWebRootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.Redirect(w, r, "/local/router", http.StatusFound)
}

func probeLinuxRouterWebPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/local/router" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(probeLinuxRouterWebPageHTML))
}

func probeLinuxRouterWebStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	desired, manualFailOpen, localOverride, nodeID := currentProbeLinuxRouterLocalState()
	report := currentProbeLinuxRouterReport()
	probeReporterRPCState.mu.Lock()
	controllerConnected := probeReporterRPCState.stream != nil && probeReporterRPCState.encoder != nil
	probeReporterRPCState.mu.Unlock()
	probeLinuxRouterWebState.Lock()
	listenAddr := probeLinuxRouterWebState.listenAddr
	startedAt := probeLinuxRouterWebState.startedAt
	probeLinuxRouterWebState.Unlock()
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
		"local_override":       localOverride,
		"config":               desired,
		"runtime":              report,
	})
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
	var config probeLinuxRouterGatewayConfig
	if err := decodeProbeLinuxRouterWebJSON(body, &config); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := applyProbeLinuxRouterLocalGatewayConfig(config); err != nil {
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
