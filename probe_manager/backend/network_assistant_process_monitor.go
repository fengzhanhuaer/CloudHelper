package backend

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NetworkProcessInfo 描述一个正在运行的进程。
type NetworkProcessInfo struct {
	PID     uint32 `json:"pid"`
	Name    string `json:"name"`
	ExePath string `json:"exe_path"`
}

// NetworkProcessEventKind 事件类型。
type NetworkProcessEventKind string

const (
	NetworkProcessEventDNS NetworkProcessEventKind = "dns"
	NetworkProcessEventTCP NetworkProcessEventKind = "tcp"
	NetworkProcessEventUDP NetworkProcessEventKind = "udp"
)

// NetworkProcessEvent 进程网络事件（已做进程内去重聚合）。
type NetworkProcessEvent struct {
	Kind        NetworkProcessEventKind `json:"kind"`
	Timestamp   int64                   `json:"timestamp"` // Unix ms
	ProcessName string                  `json:"process_name,omitempty"`
	Domain      string                  `json:"domain,omitempty"`
	TargetIP    string                  `json:"target_ip,omitempty"`
	TargetPort  uint16                  `json:"target_port,omitempty"`
	Direct      bool                    `json:"direct"`
	NodeID      string                  `json:"node_id,omitempty"`
	Group       string                  `json:"group,omitempty"`
	ResolvedIPs []string                `json:"resolved_ips,omitempty"`
	Count       int                     `json:"count"`
}

const maxProcessMonitorEvents = 500

// processMonitor 跟踪所有进程的网络事件。
type processMonitor struct {
	mu     sync.Mutex
	active bool
	events []NetworkProcessEvent
	index  map[string]int
}

func newProcessMonitor() *processMonitor {
	return &processMonitor{index: make(map[string]int)}
}

func (m *processMonitor) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = true
	m.events = nil
	m.index = make(map[string]int)
}

func (m *processMonitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = false
}

func (m *processMonitor) IsActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

func normalizeProcessName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

func eventKey(ev NetworkProcessEvent) string {
	processName := strings.ToLower(normalizeProcessName(ev.ProcessName))
	kind := strings.ToLower(strings.TrimSpace(string(ev.Kind)))
	domain := strings.ToLower(strings.TrimSpace(ev.Domain))
	targetIP := strings.TrimSpace(ev.TargetIP)
	nodeID := strings.ToLower(strings.TrimSpace(ev.NodeID))
	group := strings.ToLower(strings.TrimSpace(ev.Group))

	ips := make([]string, 0, len(ev.ResolvedIPs))
	for _, ip := range ev.ResolvedIPs {
		normalized := strings.TrimSpace(ip)
		if normalized == "" {
			continue
		}
		ips = append(ips, normalized)
	}
	sort.Strings(ips)
	ipsKey := strings.Join(ips, ",")

	routeFlag := "proxy"
	if ev.Direct {
		routeFlag = "direct"
	}

	return strings.Join([]string{
		processName,
		kind,
		domain,
		targetIP,
		strconv.FormatUint(uint64(ev.TargetPort), 10),
		routeFlag,
		nodeID,
		group,
		ipsKey,
	}, "|")
}

func (m *processMonitor) rebuildIndexLocked() {
	m.index = make(map[string]int, len(m.events))
	for i, ev := range m.events {
		m.index[eventKey(ev)] = i
	}
}

func (m *processMonitor) appendEvent(ev NetworkProcessEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active {
		return
	}

	ev.ProcessName = normalizeProcessName(ev.ProcessName)
	if ev.Count <= 0 {
		ev.Count = 1
	}
	key := eventKey(ev)
	if idx, ok := m.index[key]; ok && idx >= 0 && idx < len(m.events) {
		current := m.events[idx]
		current.Count += ev.Count
		current.Timestamp = ev.Timestamp
		if current.Domain == "" {
			current.Domain = ev.Domain
		}
		if current.TargetIP == "" {
			current.TargetIP = ev.TargetIP
		}
		if current.TargetPort == 0 {
			current.TargetPort = ev.TargetPort
		}
		if len(current.ResolvedIPs) == 0 && len(ev.ResolvedIPs) > 0 {
			current.ResolvedIPs = append([]string(nil), ev.ResolvedIPs...)
		}
		m.events[idx] = current
		return
	}

	m.events = append(m.events, ev)
	m.index[key] = len(m.events) - 1
	if len(m.events) > maxProcessMonitorEvents {
		m.events = m.events[len(m.events)-maxProcessMonitorEvents:]
		m.rebuildIndexLocked()
	}
}

// RecordDNSEvent 记录一次 DNS 查询事件（记录所有进程）。
func (m *processMonitor) RecordDNSEvent(srcPort uint16, domain string, resolvedIPs []string, direct bool, nodeID, group string) {
	if !m.IsActive() {
		return
	}
	procName := pidNameByPort(srcPort, false)
	m.appendEvent(NetworkProcessEvent{
		Kind:        NetworkProcessEventDNS,
		Timestamp:   time.Now().UnixMilli(),
		ProcessName: procName,
		Domain:      domain,
		ResolvedIPs: resolvedIPs,
		Direct:      direct,
		NodeID:      nodeID,
		Group:       group,
		Count:       1,
	})
}

// RecordTCPEvent 记录一次 TCP 连接事件（记录所有进程）。
func (m *processMonitor) RecordTCPEvent(srcPort uint16, targetIP string, targetPort uint16, direct bool, nodeID, group string) {
	if !m.IsActive() {
		return
	}
	procName := pidNameByPort(srcPort, false)
	m.appendEvent(NetworkProcessEvent{
		Kind:        NetworkProcessEventTCP,
		Timestamp:   time.Now().UnixMilli(),
		ProcessName: procName,
		TargetIP:    targetIP,
		TargetPort:  targetPort,
		Direct:      direct,
		NodeID:      nodeID,
		Group:       group,
		Count:       1,
	})
}

// RecordUDPEvent 记录一次 UDP 连接事件（记录所有进程）。
func (m *processMonitor) RecordUDPEvent(srcPort uint16, targetIP string, targetPort uint16, direct bool, nodeID, group string) {
	if !m.IsActive() {
		return
	}
	procName := pidNameByPort(srcPort, false)
	m.appendEvent(NetworkProcessEvent{
		Kind:        NetworkProcessEventUDP,
		Timestamp:   time.Now().UnixMilli(),
		ProcessName: procName,
		TargetIP:    targetIP,
		TargetPort:  targetPort,
		Direct:      direct,
		NodeID:      nodeID,
		Group:       group,
		Count:       1,
	})
}

func (m *processMonitor) GetEvents() []NetworkProcessEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return []NetworkProcessEvent{}
	}
	result := make([]NetworkProcessEvent, len(m.events))
	copy(result, m.events)
	return result
}

// QueryEvents 返回 timestamp >= sinceMs 的事件；sinceMs<=0 时返回全部。
func (m *processMonitor) QueryEvents(sinceMs int64) []NetworkProcessEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []NetworkProcessEvent
	for _, ev := range m.events {
		if sinceMs <= 0 || ev.Timestamp >= sinceMs {
			result = append(result, ev)
		}
	}
	if result == nil {
		return []NetworkProcessEvent{}
	}
	return result
}

func (m *processMonitor) ClearEvents() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = nil
	m.index = make(map[string]int)
}

// listRunningProcesses 返回去重后的进程名列表（已排序）。
func listRunningProcesses() []NetworkProcessInfo {
	return listRunningProcessesPlatform()
}

// deduplicateProcessList 按进程名去重并排序。
func deduplicateProcessList(procs []NetworkProcessInfo) []NetworkProcessInfo {
	seen := make(map[string]bool)
	var result []NetworkProcessInfo
	for _, p := range procs {
		key := strings.ToLower(p.Name)
		if !seen[key] {
			seen[key] = true
			result = append(result, p)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result
}
