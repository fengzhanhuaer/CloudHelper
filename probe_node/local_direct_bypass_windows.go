//go:build windows

package main

import (
	"strings"
	"sync"
	"time"
)

type probeLocalWindowsDirectBypassRouteTarget struct {
	InterfaceIndex int    `json:"interface_index"`
	NextHop        string `json:"next_hop"`
}

var probeLocalDirectBypassRouteTargetState = struct {
	mu          sync.Mutex
	routeTarget probeLocalWindowsDirectBypassRouteTarget
	manual      bool
	manualID    string
	manualLabel string
	updatedAt   string
	ready       bool
}{}

func prepareProbeLocalWindowsDirectBypassRouteTarget() error {
	routeTarget, err := resolveProbeLocalWindowsDirectBypassRouteTarget()
	if err != nil {
		return err
	}
	setProbeLocalWindowsDirectBypassRouteTarget(routeTarget)
	logProbeInfof("probe local tun direct bypass route target prepared: if_index=%d next_hop=%s", routeTarget.InterfaceIndex, strings.TrimSpace(routeTarget.NextHop))
	return nil
}

func setProbeLocalWindowsDirectBypassManualRouteTarget(routeTarget probeLocalWindowsDirectBypassRouteTarget, candidateID string, label string) {
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	probeLocalDirectBypassRouteTargetState.routeTarget = routeTarget
	probeLocalDirectBypassRouteTargetState.manual = true
	probeLocalDirectBypassRouteTargetState.manualID = strings.TrimSpace(candidateID)
	probeLocalDirectBypassRouteTargetState.manualLabel = strings.TrimSpace(label)
	probeLocalDirectBypassRouteTargetState.updatedAt = time.Now().UTC().Format(time.RFC3339)
	probeLocalDirectBypassRouteTargetState.ready = true
	probeLocalDirectBypassRouteTargetState.mu.Unlock()
}

func clearProbeLocalWindowsDirectBypassManualRouteTarget() {
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	probeLocalDirectBypassRouteTargetState.manual = false
	probeLocalDirectBypassRouteTargetState.manualID = ""
	probeLocalDirectBypassRouteTargetState.manualLabel = ""
	probeLocalDirectBypassRouteTargetState.updatedAt = time.Now().UTC().Format(time.RFC3339)
	probeLocalDirectBypassRouteTargetState.ready = false
	probeLocalDirectBypassRouteTargetState.mu.Unlock()
}

func currentProbeLocalWindowsDirectBypassManualRouteTarget() (probeLocalWindowsDirectBypassRouteTarget, string, string, bool) {
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	defer probeLocalDirectBypassRouteTargetState.mu.Unlock()
	if !probeLocalDirectBypassRouteTargetState.manual {
		return probeLocalWindowsDirectBypassRouteTarget{}, "", "", false
	}
	return probeLocalDirectBypassRouteTargetState.routeTarget, probeLocalDirectBypassRouteTargetState.manualID, probeLocalDirectBypassRouteTargetState.manualLabel, true
}

func currentProbeLocalWindowsDirectBypassRouteTarget() (probeLocalWindowsDirectBypassRouteTarget, bool) {
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	defer probeLocalDirectBypassRouteTargetState.mu.Unlock()
	if !probeLocalDirectBypassRouteTargetState.ready {
		return probeLocalWindowsDirectBypassRouteTarget{}, false
	}
	return probeLocalDirectBypassRouteTargetState.routeTarget, true
}

func setProbeLocalWindowsDirectBypassRouteTarget(routeTarget probeLocalWindowsDirectBypassRouteTarget) {
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	probeLocalDirectBypassRouteTargetState.routeTarget = routeTarget
	probeLocalDirectBypassRouteTargetState.ready = true
	if !probeLocalDirectBypassRouteTargetState.manual {
		probeLocalDirectBypassRouteTargetState.manualID = ""
		probeLocalDirectBypassRouteTargetState.manualLabel = ""
		probeLocalDirectBypassRouteTargetState.updatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeLocalDirectBypassRouteTargetState.mu.Unlock()
}

func clearProbeLocalWindowsDirectBypassRouteTarget() {
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	if !probeLocalDirectBypassRouteTargetState.manual {
		probeLocalDirectBypassRouteTargetState.routeTarget = probeLocalWindowsDirectBypassRouteTarget{}
		probeLocalDirectBypassRouteTargetState.manualID = ""
		probeLocalDirectBypassRouteTargetState.manualLabel = ""
	}
	probeLocalDirectBypassRouteTargetState.ready = false
	probeLocalDirectBypassRouteTargetState.mu.Unlock()
}

func resolveProbeLocalWindowsDirectBypassRouteTarget() (probeLocalWindowsDirectBypassRouteTarget, error) {
	if routeTarget, _, _, ok := currentProbeLocalWindowsDirectBypassManualRouteTarget(); ok {
		if routeTarget.InterfaceIndex > 0 && strings.TrimSpace(routeTarget.NextHop) != "" {
			return routeTarget, nil
		}
	}
	routeTarget, err := resolveProbeLocalWindowsRouteTarget()
	if err != nil {
		return probeLocalWindowsDirectBypassRouteTarget{}, err
	}
	return probeLocalResolveWindowsPrimaryEgressRoute(routeTarget.InterfaceIndex)
}

func resetProbeLocalDirectBypassStateForTest() {
	probeLocalDirectBypassRouteTargetState.mu.Lock()
	probeLocalDirectBypassRouteTargetState.routeTarget = probeLocalWindowsDirectBypassRouteTarget{}
	probeLocalDirectBypassRouteTargetState.manual = false
	probeLocalDirectBypassRouteTargetState.manualID = ""
	probeLocalDirectBypassRouteTargetState.manualLabel = ""
	probeLocalDirectBypassRouteTargetState.updatedAt = ""
	probeLocalDirectBypassRouteTargetState.ready = false
	probeLocalDirectBypassRouteTargetState.mu.Unlock()
}
