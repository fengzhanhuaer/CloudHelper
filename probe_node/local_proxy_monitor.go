package main

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const probeLocalProxyMonitorInterval = 30 * time.Second

type probeLocalTUNGroupRuntimeMonitorStats struct {
	Total        int            `json:"total"`
	Selected     int            `json:"selected"`
	Connected    int            `json:"connected"`
	FailureTotal int            `json:"failure_total"`
	Statuses     map[string]int `json:"statuses"`
}

type probeLocalDNSMonitorStats struct {
	Loaded       bool `json:"loaded"`
	Service      bool `json:"service"`
	TUNService   bool `json:"tun_service"`
	Cache        int  `json:"cache"`
	FakeIP       int  `json:"fake_ip"`
	RouteHints   int  `json:"route_hints"`
	RouteIPHints int  `json:"route_ip_hints"`
}

type probeProcessCPUSample struct {
	At        time.Time
	Total     time.Duration
	Available bool
}

type probeLocalProxyMonitorMemorySnapshot struct {
	AllocMB     float64 `json:"alloc_mb"`
	HeapAllocMB float64 `json:"heap_alloc_mb"`
	HeapInuseMB float64 `json:"heap_inuse_mb"`
	HeapObjects uint64  `json:"heap_objects"`
	NumGC       uint32  `json:"num_gc"`
}

type probeLocalProxyMonitorTUNSnapshot struct {
	Running   bool   `json:"running"`
	RXPackets uint64 `json:"rx_packets"`
	RXBytes   uint64 `json:"rx_bytes"`
}

type probeLocalProxyMonitorSnapshot struct {
	FetchedAt       string                                `json:"fetched_at"`
	Reason          string                                `json:"reason"`
	UptimeSeconds   int64                                 `json:"uptime_seconds"`
	Enabled         bool                                  `json:"enabled"`
	Mode            string                                `json:"mode"`
	Goroutines      int                                   `json:"goroutines"`
	GoMaxProcs      int                                   `json:"gomaxprocs"`
	NumCPU          int                                   `json:"num_cpu"`
	CPUPercent      *float64                              `json:"cpu_percent,omitempty"`
	CPUPercentText  string                                `json:"cpu_percent_text"`
	CPUTotalMS      *int64                                `json:"cpu_total_ms,omitempty"`
	Memory          probeLocalProxyMonitorMemorySnapshot  `json:"memory"`
	TUN             probeLocalProxyMonitorTUNSnapshot     `json:"tun"`
	GroupRuntimes   probeLocalTUNGroupRuntimeMonitorStats `json:"group_runtimes"`
	TCPActive       int                                   `json:"tcp_active"`
	TCPFailures     int                                   `json:"tcp_failures"`
	UDPAssociations int                                   `json:"udp_associations"`
	ChainRuntimes   int                                   `json:"chain_runtimes"`
	DNS             probeLocalDNSMonitorStats             `json:"dns"`
}

func (s probeLocalProxyMonitorSnapshot) clone() probeLocalProxyMonitorSnapshot {
	if s.CPUPercent != nil {
		value := *s.CPUPercent
		s.CPUPercent = &value
	}
	if s.CPUTotalMS != nil {
		value := *s.CPUTotalMS
		s.CPUTotalMS = &value
	}
	if s.GroupRuntimes.Statuses != nil {
		statuses := make(map[string]int, len(s.GroupRuntimes.Statuses))
		for key, value := range s.GroupRuntimes.Statuses {
			statuses[key] = value
		}
		s.GroupRuntimes.Statuses = statuses
	}
	return s
}

var probeLocalProxyMonitorState = struct {
	mu          sync.Mutex
	stopCh      chan struct{}
	startedAt   time.Time
	previousCPU probeProcessCPUSample
	latest      probeLocalProxyMonitorSnapshot
}{}

func startProbeLocalProxyMonitor() {
	probeLocalProxyMonitorState.mu.Lock()
	if probeLocalProxyMonitorState.stopCh != nil {
		probeLocalProxyMonitorState.mu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	probeLocalProxyMonitorState.stopCh = stopCh
	startedAt := time.Now().UTC()
	probeLocalProxyMonitorState.startedAt = startedAt
	probeLocalProxyMonitorState.previousCPU = probeProcessCPUSample{}
	probeLocalProxyMonitorState.mu.Unlock()

	go runProbeLocalProxyMonitor(stopCh, startedAt)
}

func stopProbeLocalProxyMonitor() {
	probeLocalProxyMonitorState.mu.Lock()
	stopCh := probeLocalProxyMonitorState.stopCh
	if stopCh == nil {
		probeLocalProxyMonitorState.mu.Unlock()
		return
	}
	probeLocalProxyMonitorState.stopCh = nil
	close(stopCh)
	probeLocalProxyMonitorState.mu.Unlock()
}

func resetProbeLocalProxyMonitorForTest() {
	stopProbeLocalProxyMonitor()
	probeLocalProxyMonitorState.mu.Lock()
	probeLocalProxyMonitorState.startedAt = time.Time{}
	probeLocalProxyMonitorState.previousCPU = probeProcessCPUSample{}
	probeLocalProxyMonitorState.latest = probeLocalProxyMonitorSnapshot{}
	probeLocalProxyMonitorState.mu.Unlock()
}

func runProbeLocalProxyMonitor(stopCh <-chan struct{}, startedAt time.Time) {
	defer func() {
		probeLocalProxyMonitorState.mu.Lock()
		if probeLocalProxyMonitorState.stopCh == stopCh {
			probeLocalProxyMonitorState.stopCh = nil
		}
		probeLocalProxyMonitorState.mu.Unlock()
	}()

	updateProbeLocalProxyMonitorSnapshot("start", startedAt)

	ticker := time.NewTicker(probeLocalProxyMonitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			status := probeLocalControl.proxyStatus()
			if !status.Enabled {
				updateProbeLocalProxyMonitorSnapshot("proxy_disabled", startedAt)
				return
			}
			updateProbeLocalProxyMonitorSnapshot("tick", startedAt)
		case <-stopCh:
			updateProbeLocalProxyMonitorSnapshot("stop", startedAt)
			return
		}
	}
}

func currentProbeLocalProxyMonitorSnapshot() probeLocalProxyMonitorSnapshot {
	probeLocalProxyMonitorState.mu.Lock()
	startedAt := probeLocalProxyMonitorState.startedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
		probeLocalProxyMonitorState.startedAt = startedAt
	}
	probeLocalProxyMonitorState.mu.Unlock()
	return updateProbeLocalProxyMonitorSnapshot("request", startedAt)
}

func updateProbeLocalProxyMonitorSnapshot(reason string, startedAt time.Time) probeLocalProxyMonitorSnapshot {
	status := probeLocalControl.proxyStatus()
	currentCPU := currentProbeProcessCPUSample()
	probeLocalProxyMonitorState.mu.Lock()
	previousCPU := probeLocalProxyMonitorState.previousCPU
	probeLocalProxyMonitorState.previousCPU = currentCPU
	probeLocalProxyMonitorState.mu.Unlock()

	mem := runtime.MemStats{}
	runtime.ReadMemStats(&mem)
	tunStats := probeLocalTUNDataPlaneStatsSnapshot()
	groupStats := snapshotProbeLocalTUNGroupRuntimeMonitorStats()
	dnsStats := snapshotProbeLocalDNSMonitorStats()
	tcpActive, tcpFailures := snapshotProbeLocalTCPDebugMonitorStats()
	udpAssociations := snapshotProbeUDPAssociationMonitorCount()
	chainRuntimes := snapshotProbeChainRuntimeMonitorCount()

	cpuPercent := probeLocalProxyMonitorCPUPercent(previousCPU, currentCPU)
	cpuTotalMS := probeLocalProxyMonitorCPUTotalMS(currentCPU)
	snapshot := probeLocalProxyMonitorSnapshot{
		FetchedAt:       time.Now().UTC().Format(time.RFC3339),
		Reason:          firstNonEmpty(reason, "sample"),
		UptimeSeconds:   int64(time.Since(startedAt).Seconds()),
		Enabled:         status.Enabled,
		Mode:            status.Mode,
		Goroutines:      runtime.NumGoroutine(),
		GoMaxProcs:      runtime.GOMAXPROCS(0),
		NumCPU:          runtime.NumCPU(),
		CPUPercent:      cpuPercent,
		CPUPercentText:  formatProbeLocalProxyMonitorCPUPercent(cpuPercent),
		CPUTotalMS:      cpuTotalMS,
		Memory:          probeLocalProxyMonitorMemorySnapshot{AllocMB: bytesToMiB(mem.Alloc), HeapAllocMB: bytesToMiB(mem.HeapAlloc), HeapInuseMB: bytesToMiB(mem.HeapInuse), HeapObjects: mem.HeapObjects, NumGC: mem.NumGC},
		TUN:             probeLocalProxyMonitorTUNSnapshot{Running: tunStats.Running, RXPackets: tunStats.RXPackets, RXBytes: tunStats.RXBytes},
		GroupRuntimes:   groupStats,
		TCPActive:       tcpActive,
		TCPFailures:     tcpFailures,
		UDPAssociations: udpAssociations,
		ChainRuntimes:   chainRuntimes,
		DNS:             dnsStats,
	}
	probeLocalProxyMonitorState.mu.Lock()
	probeLocalProxyMonitorState.latest = snapshot.clone()
	probeLocalProxyMonitorState.mu.Unlock()
	return snapshot
}

func snapshotProbeLocalTUNGroupRuntimeMonitorStats() probeLocalTUNGroupRuntimeMonitorStats {
	stats := probeLocalTUNGroupRuntimeMonitorStats{Statuses: map[string]int{}}
	probeLocalTUNGroupRuntimeRegistry.mu.RLock()
	items := make([]*probeLocalTUNGroupRuntime, 0, len(probeLocalTUNGroupRuntimeRegistry.items))
	for _, rt := range probeLocalTUNGroupRuntimeRegistry.items {
		if rt != nil {
			items = append(items, rt)
		}
	}
	probeLocalTUNGroupRuntimeRegistry.mu.RUnlock()

	stats.Total = len(items)
	for _, rt := range items {
		snapshot := rt.snapshot()
		if strings.TrimSpace(snapshot.SelectedChainID) != "" {
			stats.Selected++
		}
		if snapshot.Connected {
			stats.Connected++
		}
		stats.FailureTotal += snapshot.FailureCount
		status := firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "unknown")
		stats.Statuses[status]++
	}
	return stats
}

func snapshotProbeLocalDNSMonitorStats() probeLocalDNSMonitorStats {
	probeLocalDNSState.mu.Lock()
	defer probeLocalDNSState.mu.Unlock()
	return probeLocalDNSMonitorStats{
		Loaded:       probeLocalDNSState.cacheLoaded,
		Service:      probeLocalDNSState.status.Enabled,
		TUNService:   probeLocalDNSState.tunStatus.Enabled,
		Cache:        len(probeLocalDNSState.cache),
		FakeIP:       len(probeLocalDNSState.fakeIPToEntry),
		RouteHints:   len(probeLocalDNSState.routeHints),
		RouteIPHints: len(probeLocalDNSState.routeIPHints),
	}
}

func snapshotProbeLocalTCPDebugMonitorStats() (active int, failures int) {
	state := globalProbeTCPDebugState
	if state == nil {
		return 0, 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.active), len(state.failures)
}

func snapshotProbeUDPAssociationMonitorCount() int {
	pool := globalProbeChainUDPAssociationPool
	if pool == nil {
		return 0
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return len(pool.items)
}

func snapshotProbeChainRuntimeMonitorCount() int {
	probeChainRuntimeState.mu.Lock()
	defer probeChainRuntimeState.mu.Unlock()
	return len(probeChainRuntimeState.runtimes)
}

func probeLocalProxyMonitorCPUPercent(previous, current probeProcessCPUSample) *float64 {
	if !previous.Available || !current.Available {
		return nil
	}
	wall := current.At.Sub(previous.At)
	cpu := current.Total - previous.Total
	if wall <= 0 || cpu < 0 {
		return nil
	}
	percent := (float64(cpu) / float64(wall) / float64(runtime.NumCPU())) * 100
	if percent < 0 {
		percent = 0
	}
	return &percent
}

func probeLocalProxyMonitorCPUTotalMS(current probeProcessCPUSample) *int64 {
	if !current.Available {
		return nil
	}
	total := current.Total.Milliseconds()
	return &total
}

func formatProbeLocalProxyMonitorCPUPercent(percent *float64) string {
	if percent == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.2f", *percent)
}

func formatProbeLocalProxyMonitorCounts(items map[string]int) string {
	if len(items) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", key, items[key]))
	}
	return strings.Join(parts, ",")
}

func bytesToMiB(value uint64) float64 {
	return float64(value) / 1024 / 1024
}
