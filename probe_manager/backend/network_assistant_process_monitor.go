package backend

import (
	"sort"
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
	NetworkProcessEventDNS  NetworkProcessEventKind = "dns"
	NetworkProcessEventTCP  NetworkProcessEventKind = "tcp"
	NetworkProcessEventUDP  NetworkProcessEventKind = "udp"
)

// NetworkProcessEvent 进程网络事件。
type NetworkProcessEvent struct {
	Kind      NetworkProcessEventKind `json:"kind"`
	Timestamp int64                   `json:"timestamp"` // Unix ms
	Domain    string                  `json:"domain,omitempty"`
	TargetIP  string                  `json:"target_ip,omitempty"`
	TargetPort uint16                 `json:"target_port,omitempty"`
	Direct    bool                    `json:"direct"`
	NodeID    string                  `json:"node_id,omitempty"`
	Group     string                  `json:"group,omitempty"`
	ResolvedIPs []string              `json:"resolved_ips,omitempty"`
}

const maxProcessMonitorEvents = 500

// processMonitor 跟踪指定进程名的网络事件。
type processMonitor struct {
	mu          sync.Mutex
	active      bool
	processName string // 小写，精确匹配
	events      []NetworkProcessEvent
}

func newProcessMonitor() *processMonitor {
	return &processMonitor{}
}

func (m *processMonitor) Start(processName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = true
	m.processName = strings.ToLower(strings.TrimSpace(processName))
	m.events = nil
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

func (m *processMonitor) ProcessName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.processName
}

func (m *processMonitor) appendEvent(ev NetworkProcessEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active {
		return
	}
	m.events = append(m.events, ev)
	if len(m.events) > maxProcessMonitorEvents {
		// 保留最新的事件
		m.events = m.events[len(m.events)-maxProcessMonitorEvents:]
	}
}

// RecordDNSEvent 记录一次 DNS 查询事件（如果进程名匹配）。
func (m *processMonitor) RecordDNSEvent(srcPort uint16, domain string, resolvedIPs []string, direct bool, nodeID, group string) {
	if !m.IsActive() {
		return
	}
	procName := pidNameByPort(srcPort, false)
	if procName == "" || !strings.EqualFold(procName, m.ProcessName()) {
		return
	}
	m.appendEvent(NetworkProcessEvent{
		Kind:        NetworkProcessEventDNS,
		Timestamp:   time.Now().UnixMilli(),
		Domain:      domain,
		ResolvedIPs: resolvedIPs,
		Direct:      direct,
		NodeID:      nodeID,
		Group:       group,
	})
}

// RecordTCPEvent 记录一次 TCP 连接事件（如果进程名匹配）。
func (m *processMonitor) RecordTCPEvent(srcPort uint16, targetIP string, targetPort uint16, direct bool, nodeID, group string) {
	if !m.IsActive() {
		return
	}
	procName := pidNameByPort(srcPort, false)
	if procName == "" || !strings.EqualFold(procName, m.ProcessName()) {
		return
	}
	m.appendEvent(NetworkProcessEvent{
		Kind:       NetworkProcessEventTCP,
		Timestamp:  time.Now().UnixMilli(),
		TargetIP:   targetIP,
		TargetPort: targetPort,
		Direct:     direct,
		NodeID:     nodeID,
		Group:      group,
	})
}

// RecordUDPEvent 记录一次 UDP 连接事件（如果进程名匹配）。
func (m *processMonitor) RecordUDPEvent(srcPort uint16, targetIP string, targetPort uint16, direct bool, nodeID, group string) {
	if !m.IsActive() {
		return
	}
	procName := pidNameByPort(srcPort, false)
	if procName == "" || !strings.EqualFold(procName, m.ProcessName()) {
		return
	}
	m.appendEvent(NetworkProcessEvent{
		Kind:       NetworkProcessEventUDP,
		Timestamp:  time.Now().UnixMilli(),
		TargetIP:   targetIP,
		TargetPort: targetPort,
		Direct:     direct,
		NodeID:     nodeID,
		Group:      group,
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
