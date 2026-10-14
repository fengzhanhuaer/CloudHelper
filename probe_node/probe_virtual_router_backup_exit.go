package main

import (
	"strings"
	"sync"
	"time"
)

const (
	probeVirtualRouterBackupExitProbeInterval = 15 * time.Second
	probeVirtualRouterBackupExitFailureWindow = 45 * time.Second
	probeVirtualRouterBackupExitFailureLimit  = 2
)

var probeVirtualRouterBackupExitProbeState = struct {
	mu   sync.Mutex
	next map[string]time.Time
}{next: make(map[string]time.Time)}

func probeVirtualRouterRuleAllowsExitNode(rule probeVirtualRouterRouteRule, nodeID string) bool {
	if sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) != "probe_exit" {
		return false
	}
	nodeID = normalizeProbeRouteNodeID(nodeID)
	return nodeID != "" && (nodeID == normalizeProbeRouteNodeID(rule.ExitNodeID) || nodeID == normalizeProbeRouteNodeID(rule.BackupExitNodeID))
}

func probeVirtualRouterFakeIPEntryAllowsExitNode(entry probeVirtualRouterFakeIPEntry, nodeID string) bool {
	nodeID = normalizeProbeRouteNodeID(nodeID)
	if nodeID == "" {
		return false
	}
	if nodeID == normalizeProbeRouteNodeID(entry.ExitNodeID) {
		return true
	}
	rule, ok := currentProbeVirtualRouterRouteRuleForDomain(entry.Domain)
	if !ok || normalizeProbeRouteNodeID(rule.ExitNodeID) != normalizeProbeRouteNodeID(entry.ExitNodeID) {
		return false
	}
	if entry.RuleID != "" && strings.TrimSpace(rule.ID) != strings.TrimSpace(entry.RuleID) {
		return false
	}
	return probeVirtualRouterRuleAllowsExitNode(rule, nodeID)
}

func currentProbeVirtualRouterExitPathForRule(fromNodeID string, rule probeVirtualRouterRouteRule) []string {
	primary := normalizeProbeRouteNodeID(rule.ExitNodeID)
	primaryPath := currentProbeVirtualRouterPathBetweenNodes(fromNodeID, primary)
	backup := normalizeProbeRouteNodeID(rule.BackupExitNodeID)
	if backup == "" || backup == primary || sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) != "probe_exit" {
		return primaryPath
	}
	backupPath := currentProbeVirtualRouterPathBetweenNodes(fromNodeID, backup)
	scheduleProbeVirtualRouterBackupExitPathProbe(primaryPath)
	scheduleProbeVirtualRouterBackupExitPathProbe(backupPath)
	if probeVirtualRouterExitPathReady(primaryPath) {
		return primaryPath
	}
	if probeVirtualRouterExitPathReady(backupPath) {
		return backupPath
	}
	if len(primaryPath) > 0 {
		return primaryPath
	}
	return backupPath
}

func probeVirtualRouterExitPathReady(path []string) bool {
	if len(path) == 0 {
		return false
	}
	if len(path) == 1 {
		return true
	}
	if probeVirtualRouterPathShouldAvoid(path) {
		return false
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(path[1])
	if rt == nil || !probeVirtualRouterRuntimeHasBridgeSession(rt, direction) {
		return false
	}
	if len(path) < 3 {
		return true
	}
	key := probeVirtualRouterPathKey(path)
	probeVirtualRouterPathRTTState.mu.RLock()
	item, ok := probeVirtualRouterPathRTTState.items[key]
	probeVirtualRouterPathRTTState.mu.RUnlock()
	return !ok || strings.TrimSpace(item.LastError) == "" || item.ConsecutiveFailureCount < probeVirtualRouterBackupExitFailureLimit || time.Since(item.LastAt) > probeVirtualRouterBackupExitFailureWindow
}

func scheduleProbeVirtualRouterBackupExitPathProbe(path []string) {
	if len(path) < 3 || normalizeProbeRouteNodeID(path[0]) != currentProbeVirtualRouterLocalNodeID() {
		return
	}
	key := probeVirtualRouterPathKey(path)
	if key == "" {
		return
	}
	now := time.Now()
	probeVirtualRouterBackupExitProbeState.mu.Lock()
	if now.Before(probeVirtualRouterBackupExitProbeState.next[key]) {
		probeVirtualRouterBackupExitProbeState.mu.Unlock()
		return
	}
	if len(probeVirtualRouterBackupExitProbeState.next) > 2048 {
		for staleKey, next := range probeVirtualRouterBackupExitProbeState.next {
			if now.After(next.Add(probeVirtualRouterBackupExitProbeInterval)) {
				delete(probeVirtualRouterBackupExitProbeState.next, staleKey)
			}
		}
	}
	probeVirtualRouterBackupExitProbeState.next[key] = now.Add(probeVirtualRouterBackupExitProbeInterval)
	probeVirtualRouterBackupExitProbeState.mu.Unlock()
	pathCopy := append([]string(nil), path...)
	go func() { _, _ = probeVirtualRouterQueryPathRTT(pathCopy) }()
}
