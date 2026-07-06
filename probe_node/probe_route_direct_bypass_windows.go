//go:build windows

package main

import (
	"strings"
	"sync"
	"time"
)

type probeRouteWindowsDirectRouteTarget struct {
	InterfaceIndex int    `json:"interface_index"`
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
	logProbeInfof("probe route direct route route target prepared: if_index=%d next_hop=%s", routeTarget.InterfaceIndex, strings.TrimSpace(routeTarget.NextHop))
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
	if routeTarget, _, _, ok := currentProbeRouteWindowsDirectManualRouteTarget(); ok {
		if routeTarget.InterfaceIndex > 0 && strings.TrimSpace(routeTarget.NextHop) != "" {
			return routeTarget, nil
		}
	}
	routeTarget, err := resolveProbeRouteWindowsTUNRouteTarget()
	if err != nil {
		return probeRouteWindowsDirectRouteTarget{}, err
	}
	return probeLocalResolveWindowsPrimaryEgressRoute(routeTarget.InterfaceIndex)
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
