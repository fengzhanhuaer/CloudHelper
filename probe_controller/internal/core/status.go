package core

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudhelper/probe_controller/internal/dashboard"
)

var serverStartTime time.Time

func PingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, statusPayload())
}

func dashboardStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dashboard/status" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, statusPayload())
}

func dashboardProbesHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dashboard/probes" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	items := publicDashboardProbeMetrics()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": items,
	})
}

type dashboardPublicProbeItem struct {
	NodeName string             `json:"node_name"`
	Online   bool               `json:"online"`
	LastSeen string             `json:"last_seen"`
	System   probeSystemMetrics `json:"system"`
}

func publicDashboardProbeMetrics() []dashboardPublicProbeItem {
	// Security note: /dashboard/* is public. Do not expose node_id/ip/version here.
	runtimes := listProbeRuntimes()
	nameMap := map[string]string{}
	if ProbeStore != nil {
		ProbeStore.mu.RLock()
		for _, node := range loadProbeNodesLocked() {
			nameMap[normalizeProbeNodeID(strconv.Itoa(node.NodeNo))] = strings.TrimSpace(node.NodeName)
		}
		ProbeStore.mu.RUnlock()
	}

	out := make([]dashboardPublicProbeItem, 0, len(runtimes))
	for _, rt := range runtimes {
		nodeName := strings.TrimSpace(nameMap[normalizeProbeNodeID(rt.NodeID)])
		out = append(out, dashboardPublicProbeItem{
			NodeName: nodeName,
			Online:   rt.Online,
			LastSeen: strings.TrimSpace(rt.LastSeen),
			System:   rt.System,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Online == out[j].Online {
			return i < j
		}
		return out[i].Online && !out[j].Online
	})
	return out
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dashboard" {
		http.NotFound(w, r)
		return
	}
	dashboard.Handler(w, r)
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func statusPayload() map[string]interface{} {
	return map[string]interface{}{
		"uptime": int(time.Since(serverStartTime).Seconds()),
	}
}

func SetServerStartTimeForTest(ts time.Time) {
	serverStartTime = ts
}
