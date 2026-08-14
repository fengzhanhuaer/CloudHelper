//go:build windows

package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type probeRouteWindowsDirectRouteTarget struct {
	InterfaceIndex int    `json:"interface_index"`
	InterfaceLUID  uint64 `json:"interface_luid,omitempty"`
	InterfaceGUID  string `json:"interface_guid,omitempty"`
	NextHop        string `json:"next_hop"`
}

var probeRouteDirectRouteTargetState = struct {
	mu          sync.Mutex
	routeTarget probeRouteWindowsDirectRouteTarget
	manual      bool
	manualID    string
	manualLabel string
	updatedAt   string
	ready       bool
}{}

func prepareProbeRouteWindowsDirectRouteTarget() error {
	routeTarget, err := resolveProbeRouteWindowsDirectRouteTarget()
	if err != nil {
		return err
	}
	setProbeRouteWindowsDirectRouteTarget(routeTarget)
	if cleanupErr := cleanupProbeRouteWindowsInvalidLocalAddressBypassRoutes(); cleanupErr != nil {
		logProbeWarnf("probe route direct bypass cleanup invalid local-address routes failed: %v", cleanupErr)
	}
	logProbeInfof("probe route direct route route target prepared: %s", describeProbeLocalTUNEgressTarget(routeTarget))
	return nil
}

func setProbeRouteWindowsDirectManualRouteTarget(routeTarget probeRouteWindowsDirectRouteTarget, candidateID string, label string) {
	probeRouteDirectRouteTargetState.mu.Lock()
	probeRouteDirectRouteTargetState.routeTarget = routeTarget
	probeRouteDirectRouteTargetState.manual = true
	probeRouteDirectRouteTargetState.manualID = strings.TrimSpace(candidateID)
	probeRouteDirectRouteTargetState.manualLabel = strings.TrimSpace(label)
	probeRouteDirectRouteTargetState.updatedAt = time.Now().UTC().Format(time.RFC3339)
	probeRouteDirectRouteTargetState.ready = true
	probeRouteDirectRouteTargetState.mu.Unlock()
}

func clearProbeRouteWindowsDirectManualRouteTarget() {
	probeRouteDirectRouteTargetState.mu.Lock()
	probeRouteDirectRouteTargetState.manual = false
	probeRouteDirectRouteTargetState.manualID = ""
	probeRouteDirectRouteTargetState.manualLabel = ""
	probeRouteDirectRouteTargetState.updatedAt = time.Now().UTC().Format(time.RFC3339)
	probeRouteDirectRouteTargetState.ready = false
	probeRouteDirectRouteTargetState.mu.Unlock()
}

func currentProbeRouteWindowsDirectManualRouteTarget() (probeRouteWindowsDirectRouteTarget, string, string, bool) {
	probeRouteDirectRouteTargetState.mu.Lock()
	defer probeRouteDirectRouteTargetState.mu.Unlock()
	if !probeRouteDirectRouteTargetState.manual {
		return probeRouteWindowsDirectRouteTarget{}, "", "", false
	}
	return probeRouteDirectRouteTargetState.routeTarget, probeRouteDirectRouteTargetState.manualID, probeRouteDirectRouteTargetState.manualLabel, true
}

func currentProbeRouteWindowsDirectRouteTarget() (probeRouteWindowsDirectRouteTarget, bool) {
	probeRouteDirectRouteTargetState.mu.Lock()
	defer probeRouteDirectRouteTargetState.mu.Unlock()
	if !probeRouteDirectRouteTargetState.ready {
		return probeRouteWindowsDirectRouteTarget{}, false
	}
	return probeRouteDirectRouteTargetState.routeTarget, true
}

func setProbeRouteWindowsDirectRouteTarget(routeTarget probeRouteWindowsDirectRouteTarget) {
	probeRouteDirectRouteTargetState.mu.Lock()
	probeRouteDirectRouteTargetState.routeTarget = routeTarget
	probeRouteDirectRouteTargetState.ready = true
	if !probeRouteDirectRouteTargetState.manual {
		probeRouteDirectRouteTargetState.manualID = ""
		probeRouteDirectRouteTargetState.manualLabel = ""
		probeRouteDirectRouteTargetState.updatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeRouteDirectRouteTargetState.mu.Unlock()
}

func clearProbeRouteWindowsDirectRouteTarget() {
	probeRouteDirectRouteTargetState.mu.Lock()
	if !probeRouteDirectRouteTargetState.manual {
		probeRouteDirectRouteTargetState.routeTarget = probeRouteWindowsDirectRouteTarget{}
		probeRouteDirectRouteTargetState.manualID = ""
		probeRouteDirectRouteTargetState.manualLabel = ""
	}
	probeRouteDirectRouteTargetState.ready = false
	probeRouteDirectRouteTargetState.mu.Unlock()
}

func resolveProbeRouteWindowsDirectRouteTarget() (probeRouteWindowsDirectRouteTarget, error) {
	if routeTarget, candidateID, label, ok := currentProbeRouteWindowsDirectManualRouteTarget(); ok {
		excludedIfIndex, err := resolveProbeRouteWindowsDirectBypassExcludedIfIndex()
		if err != nil {
			return probeRouteWindowsDirectRouteTarget{}, err
		}
		options, err := probeLocalWindowsEgressRouteOptions(excludedIfIndex)
		if err != nil {
			return probeRouteWindowsDirectRouteTarget{}, fmt.Errorf("resolve current manual egress candidates failed: %w", err)
		}
		candidate, matched := probeLocalTUNEgressFindCandidate(options, candidateID, routeTarget)
		if !matched {
			return probeRouteWindowsDirectRouteTarget{}, fmt.Errorf("manual egress adapter is unavailable: %s", describeProbeLocalTUNEgressTarget(routeTarget))
		}
		refreshed := probeRouteWindowsDirectRouteTargetFromEgressOption(candidate)
		if !sameProbeRouteWindowsDirectRouteTarget(routeTarget, refreshed) || !strings.EqualFold(strings.TrimSpace(candidateID), strings.TrimSpace(candidate.CandidateID)) {
			setProbeRouteWindowsDirectManualRouteTarget(refreshed, candidate.CandidateID, probeLocalTUNEgressSelectedLabel(&candidate))
			if persistErr := persistProbeLocalTUNEgressManualState(candidate); persistErr != nil {
				logProbeWarnf("probe route direct route stable manual target persist failed: %v", persistErr)
			}
			logProbeWarnf("probe route direct route stable manual target rebound: old={%s} new={%s}", describeProbeLocalTUNEgressTarget(routeTarget), describeProbeLocalTUNEgressTarget(refreshed))
		} else if strings.TrimSpace(label) == "" {
			setProbeRouteWindowsDirectManualRouteTarget(refreshed, candidate.CandidateID, probeLocalTUNEgressSelectedLabel(&candidate))
		}
		return refreshed, nil
	}
	excludedIfIndex, err := resolveProbeRouteWindowsDirectBypassExcludedIfIndex()
	if err != nil {
		return probeRouteWindowsDirectRouteTarget{}, err
	}
	return probeLocalResolveWindowsPrimaryEgressRoute(excludedIfIndex)
}

func resolveProbeRouteWindowsDirectBypassExcludedIfIndex() (int, error) {
	if ifIndex := currentProbeVirtualRouterTUNDataPlaneIfIndex(); ifIndex > 0 {
		return ifIndex, nil
	}
	if routeTarget, err := resolveProbeRouteWindowsTUNRouteTarget(); err == nil && routeTarget.InterfaceIndex > 0 {
		return routeTarget.InterfaceIndex, nil
	}
	adapter, exists, err := probeLocalFindWintunAdapter()
	if err != nil {
		return 0, fmt.Errorf("resolve wintun adapter for direct bypass: %w", err)
	}
	if exists && adapter.InterfaceIndex > 0 {
		return adapter.InterfaceIndex, nil
	}
	return 0, nil
}

func sameProbeRouteWindowsDirectRouteTarget(left, right probeRouteWindowsDirectRouteTarget) bool {
	return left.InterfaceIndex == right.InterfaceIndex &&
		left.InterfaceLUID == right.InterfaceLUID &&
		normalizeProbeLocalTUNEgressInterfaceGUID(left.InterfaceGUID) == normalizeProbeLocalTUNEgressInterfaceGUID(right.InterfaceGUID) &&
		strings.EqualFold(strings.TrimSpace(left.NextHop), strings.TrimSpace(right.NextHop))
}

func resetProbeRouteDirectBypassStateForTest() {
	probeRouteDirectRouteTargetState.mu.Lock()
	probeRouteDirectRouteTargetState.routeTarget = probeRouteWindowsDirectRouteTarget{}
	probeRouteDirectRouteTargetState.manual = false
	probeRouteDirectRouteTargetState.manualID = ""
	probeRouteDirectRouteTargetState.manualLabel = ""
	probeRouteDirectRouteTargetState.updatedAt = ""
	probeRouteDirectRouteTargetState.ready = false
	probeRouteDirectRouteTargetState.mu.Unlock()
}
