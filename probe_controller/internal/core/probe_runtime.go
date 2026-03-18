package core

import (
	"sort"
	"strings"
	"sync"
	"time"
)

type probeSystemMetrics struct {
	CPUPercent        float64 `json:"cpu_percent"`
	MemoryTotalBytes  uint64  `json:"memory_total_bytes"`
	MemoryUsedBytes   uint64  `json:"memory_used_bytes"`
	MemoryUsedPercent float64 `json:"memory_used_percent"`
	SwapTotalBytes    uint64  `json:"swap_total_bytes"`
	SwapUsedBytes     uint64  `json:"swap_used_bytes"`
	SwapUsedPercent   float64 `json:"swap_used_percent"`
	DiskTotalBytes    uint64  `json:"disk_total_bytes"`
	DiskUsedBytes     uint64  `json:"disk_used_bytes"`
	DiskUsedPercent   float64 `json:"disk_used_percent"`
}

type probeRuntimeStatus struct {
	NodeID      string             `json:"node_id"`
	Online      bool               `json:"online"`
	LastSeen    string             `json:"last_seen"`
	IPv4        []string           `json:"ipv4,omitempty"`
	IPv6        []string           `json:"ipv6,omitempty"`
	IPLocations map[string]string  `json:"ip_locations,omitempty"`
	Version     string             `json:"version,omitempty"`
	System      probeSystemMetrics `json:"system"`
}

var probeRuntimeStore = struct {
	mu   sync.RWMutex
	data map[string]probeRuntimeStatus
}{data: make(map[string]probeRuntimeStatus)}

func setProbeRuntimeOnline(nodeID string, online bool) {
	nodeID = normalizeProbeNodeID(nodeID)
	if nodeID == "" {
		return
	}
	probeRuntimeStore.mu.Lock()
	defer probeRuntimeStore.mu.Unlock()
	current := probeRuntimeStore.data[nodeID]
	current.NodeID = nodeID
	current.Online = online
	if online {
		current.LastSeen = time.Now().UTC().Format(time.RFC3339)
	}
	probeRuntimeStore.data[nodeID] = current
}

func updateProbeRuntimeReport(nodeID string, ipv4 []string, ipv6 []string, metrics probeSystemMetrics, version string) {
	nodeID = normalizeProbeNodeID(nodeID)
	if nodeID == "" {
		return
	}

	nextIPv4 := compactStrings(ipv4)
	nextIPv6 := compactStrings(ipv6)
	var previousIPv4 []string
	var previousIPv6 []string
	nextIPLocations := map[string]string{}
	pendingResolveIPs := make([]string, 0)
	seenIPs := map[string]struct{}{}

	probeRuntimeStore.mu.Lock()
	if current, ok := probeRuntimeStore.data[nodeID]; ok {
		previousIPv4 = append(previousIPv4, current.IPv4...)
		previousIPv6 = append(previousIPv6, current.IPv6...)
		for _, ip := range append(append([]string{}, nextIPv4...), nextIPv6...) {
			if _, seen := seenIPs[ip]; seen {
				continue
			}
			seenIPs[ip] = struct{}{}
			if current.IPLocations != nil {
				if label := strings.TrimSpace(current.IPLocations[ip]); label != "" {
					nextIPLocations[ip] = label
					continue
				}
			}
			if localLabel := detectLocalProbeIPLocation(ip); localLabel != "" {
				nextIPLocations[ip] = localLabel
				continue
			}
			if cached := getCachedProbeIPLocation(ip); cached != "" {
				nextIPLocations[ip] = cached
				continue
			}
			pendingResolveIPs = append(pendingResolveIPs, ip)
		}
	} else {
		for _, ip := range append(append([]string{}, nextIPv4...), nextIPv6...) {
			if _, seen := seenIPs[ip]; seen {
				continue
			}
			seenIPs[ip] = struct{}{}
			if localLabel := detectLocalProbeIPLocation(ip); localLabel != "" {
				nextIPLocations[ip] = localLabel
				continue
			}
			if cached := getCachedProbeIPLocation(ip); cached != "" {
				nextIPLocations[ip] = cached
				continue
			}
			pendingResolveIPs = append(pendingResolveIPs, ip)
		}
	}
	probeRuntimeStore.data[nodeID] = probeRuntimeStatus{
		NodeID:      nodeID,
		Online:      true,
		LastSeen:    time.Now().UTC().Format(time.RFC3339),
		IPv4:        nextIPv4,
		IPv6:        nextIPv6,
		IPLocations: nextIPLocations,
		Version:     strings.TrimSpace(version),
		System:      metrics,
	}
	probeRuntimeStore.mu.Unlock()

	if len(pendingResolveIPs) > 0 {
		resolveAndApplyProbeIPLocations(nodeID, pendingResolveIPs)
	}
	notifyCloudflareRuntimeChanged(nodeID, previousIPv4, previousIPv6, nextIPv4, nextIPv6)
}

func getProbeRuntime(nodeID string) (probeRuntimeStatus, bool) {
	probeRuntimeStore.mu.RLock()
	defer probeRuntimeStore.mu.RUnlock()
	v, ok := probeRuntimeStore.data[normalizeProbeNodeID(nodeID)]
	return v, ok
}

func listProbeRuntimes() []probeRuntimeStatus {
	probeRuntimeStore.mu.RLock()
	defer probeRuntimeStore.mu.RUnlock()
	out := make([]probeRuntimeStatus, 0, len(probeRuntimeStore.data))
	for _, item := range probeRuntimeStore.data {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].NodeID < out[j].NodeID
	})
	return out
}

func compactStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
