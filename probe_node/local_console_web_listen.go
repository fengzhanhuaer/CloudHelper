package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const probeLocalWebListenBodyMaxLen = 16 * 1024

type probeLocalWebListenRequest struct {
	ListenIP string `json:"listen_ip"`
}

type probeLocalWebListenOption struct {
	IP        string `json:"ip"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
}

var probeLocalApplyWebListenRestart = func() {
	prepareProbeLocalProcessRestart()
	if err := probeLocalRestartProcess(""); err != nil {
		logProbeErrorf("probe local web listen restart failed: %v", err)
	}
}

func normalizeProbeLocalWebListenIP(raw string) string {
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(raw, "[]")))
	if ip == nil || ip.To4() == nil {
		return ""
	}
	return ip.To4().String()
}

func listProbeLocalWebListenOptions(configuredIP string) []probeLocalWebListenOption {
	items := make([]probeLocalWebListenOption, 0, 8)
	seen := make(map[string]struct{}, 8)
	add := func(ip, label string, available bool) {
		ip = normalizeProbeLocalWebListenIP(ip)
		if ip == "" {
			return
		}
		if _, ok := seen[ip]; ok {
			return
		}
		seen[ip] = struct{}{}
		items = append(items, probeLocalWebListenOption{IP: ip, Label: strings.TrimSpace(label), Available: available})
	}

	if probeLocalConsoleAllowsNonLoopbackHTTP() {
		add("0.0.0.0", "全部 IPv4 网卡 (0.0.0.0)", true)
	}
	add("127.0.0.1", "仅本机 (127.0.0.1)", true)

	if probeLocalConsoleAllowsNonLoopbackHTTP() {
		ifaces, err := net.Interfaces()
		if err == nil {
			for _, iface := range ifaces {
				if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
					continue
				}
				addrs, addrErr := iface.Addrs()
				if addrErr != nil {
					continue
				}
				for _, addr := range addrs {
					ip := probeReportIPFromAddr(addr)
					if ip == nil || ip.To4() == nil || !probeReportIPIsLAN(ip) {
						continue
					}
					add(ip.String(), strings.TrimSpace(iface.Name)+" ("+ip.String()+")", true)
				}
			}
		}
	}

	configuredIP = normalizeProbeLocalWebListenIP(configuredIP)
	if configuredIP != "" {
		add(configuredIP, "当前配置 ("+configuredIP+")", false)
	}
	sort.SliceStable(items, func(i, j int) bool {
		priority := func(ip string) int {
			switch ip {
			case "0.0.0.0":
				return 0
			case "127.0.0.1":
				return 1
			default:
				return 2
			}
		}
		left, right := priority(items[i].IP), priority(items[j].IP)
		if left != right {
			return left < right
		}
		return items[i].Label < items[j].Label
	})
	return items
}

func probeLocalWebListenPayload(state probeLocalAuthState, restarting bool) map[string]any {
	defaultHost, defaultPort := probeLocalConsoleDefaultHostPort()
	listenIP := normalizeProbeLocalWebListenIP(state.ListenIP)
	if listenIP == "" {
		listenIP = defaultHost
	}
	listenPort := state.ListenPort
	if listenPort <= 0 || listenPort > 65535 {
		listenPort = defaultPort
	}
	activeListen, _ := currentProbeLocalConsoleRuntime()
	environmentOverride := ""
	if !activeProbeProductProfile.PreferLocalConsoleConfig {
		environmentOverride = normalizeProbeLocalListenAddr(os.Getenv("PROBE_LOCAL_LISTEN"))
	}
	return map[string]any{
		"ok":                   true,
		"listen_ip":            listenIP,
		"listen_port":          listenPort,
		"configured_listen":    net.JoinHostPort(listenIP, strconv.Itoa(listenPort)),
		"active_listen":        activeListen,
		"default_listen_ip":    defaultHost,
		"options":              listProbeLocalWebListenOptions(listenIP),
		"environment_override": environmentOverride,
		"editable":             environmentOverride == "",
		"restarting":           restarting,
	}
}

func updateProbeLocalAuthManagerListenConfig(listenIP string, listenPort int) {
	probeLocalAuthInitMu.Lock()
	mgr := probeLocalAuthInstance
	probeLocalAuthInitMu.Unlock()
	if mgr == nil {
		return
	}
	mgr.mu.Lock()
	mgr.state.ListenIP = listenIP
	mgr.state.ListenPort = listenPort
	mgr.mu.Unlock()
}

func probeLocalSystemWebListenHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	state, _, err := loadProbeLocalAuthStateRaw()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, probeLocalWebListenPayload(state, false))
	case http.MethodPost:
		if !activeProbeProductProfile.PreferLocalConsoleConfig && normalizeProbeLocalListenAddr(os.Getenv("PROBE_LOCAL_LISTEN")) != "" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "PROBE_LOCAL_LISTEN overrides the local Web listen setting"})
			return
		}
		body := http.MaxBytesReader(w, r.Body, probeLocalWebListenBodyMaxLen)
		defer body.Close()
		decoder := json.NewDecoder(body)
		decoder.DisallowUnknownFields()
		req := probeLocalWebListenRequest{}
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		listenIP := normalizeProbeLocalWebListenIP(req.ListenIP)
		selectable := false
		for _, option := range listProbeLocalWebListenOptions(state.ListenIP) {
			if option.Available && option.IP == listenIP {
				selectable = true
				break
			}
		}
		if !selectable {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "listen_ip must be an available local IPv4 address"})
			return
		}
		_, defaultPort := probeLocalConsoleDefaultHostPort()
		listenPort := state.ListenPort
		if listenPort <= 0 || listenPort > 65535 || activeProbeProductProfile.PreferLocalConsoleConfig {
			listenPort = defaultPort
		}
		changed := normalizeProbeLocalWebListenIP(state.ListenIP) != listenIP || state.ListenPort != listenPort
		state.ListenIP = listenIP
		state.ListenPort = listenPort
		if err := persistProbeLocalAuthState(state); err != nil {
			writeProbeLocalError(w, err)
			return
		}
		updateProbeLocalAuthManagerListenConfig(listenIP, listenPort)
		writeJSON(w, http.StatusOK, probeLocalWebListenPayload(state, changed))
		if changed {
			go func() {
				time.Sleep(300 * time.Millisecond)
				probeLocalApplyWebListenRestart()
			}()
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func resetProbeLocalWebListenHooksForTest() {
	probeLocalApplyWebListenRestart = func() {
		prepareProbeLocalProcessRestart()
		if err := probeLocalRestartProcess(""); err != nil {
			logProbeErrorf("probe local web listen restart failed: %v", err)
		}
	}
}
