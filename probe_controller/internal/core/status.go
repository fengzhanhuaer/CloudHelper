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
	NodeNo   int                `json:"node_no"`
	NodeName string             `json:"node_name"`
	Online   bool               `json:"online"`
	LastSeen string             `json:"last_seen"`
	System   probeSystemMetrics `json:"system"`
}

func publicDashboardProbeMetrics() []dashboardPublicProbeItem {
	// Security note: /dashboard/* is public. Do not expose node_id/ip/version here.
	runtimes := listProbeRuntimes()
	type nodeMeta struct {
		no   int
		name string
	}
	metaMap := map[string]nodeMeta{}
	if ProbeStore != nil {
		ProbeStore.mu.RLock()
		for _, node := range loadProbeNodesLocked() {
			normalizedID := normalizeProbeNodeID(strconv.Itoa(node.NodeNo))
			metaMap[normalizedID] = nodeMeta{
				no:   node.NodeNo,
				name: strings.TrimSpace(node.NodeName),
			}
		}
		ProbeStore.mu.RUnlock()
	}

	out := make([]dashboardPublicProbeItem, 0, len(runtimes))
	for _, rt := range runtimes {
		normalizedID := normalizeProbeNodeID(rt.NodeID)
		meta, ok := metaMap[normalizedID]
		nodeNo := 0
		nodeName := ""
		if ok {
			nodeNo = meta.no
			nodeName = meta.name
		}
		if nodeNo <= 0 {
			if n, err := strconv.Atoi(normalizedID); err == nil && n > 0 {
				nodeNo = n
			}
		}
		out = append(out, dashboardPublicProbeItem{
			NodeNo:   nodeNo,
			NodeName: nodeName,
			Online:   rt.Online,
			LastSeen: strings.TrimSpace(rt.LastSeen),
			System:   rt.System,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		leftNo := out[i].NodeNo
		rightNo := out[j].NodeNo
		switch {
		case leftNo > 0 && rightNo > 0 && leftNo != rightNo:
			return leftNo < rightNo
		case leftNo > 0 && rightNo <= 0:
			return true
		case leftNo <= 0 && rightNo > 0:
			return false
		}
		if out[i].NodeName != out[j].NodeName {
			return out[i].NodeName < out[j].NodeName
		}
		return i < j
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
