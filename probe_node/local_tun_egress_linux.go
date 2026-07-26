//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var probeRouteLinuxEgressState = struct {
	mu          sync.Mutex
	manual      bool
	target      probeVirtualRouterLinuxRouteTarget
	candidateID string
	label       string
}{}

var probeLocalLinuxInterfaceByName = net.InterfaceByName

func probeLocalTUNEgressSnapshot() (probeLocalTUNEgressStatus, error) {
	applyProbeLocalTUNEgressPersistentState(currentProbeLocalTUNEgressPersistentStateBestEffort())
	status := probeLocalTUNEgressStatus{
		APIVersion: probeLocalTUNEgressAPIVersion,
		Supported:  true,
		Mode:       "auto",
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	options, err := probeLocalLinuxPrimaryEgressRouteOptions(probeRouteLinuxTUNDeviceName())
	if err != nil {
		return status, err
	}
	status.Candidates = append(status.Candidates, options...)
	if len(options) > 0 {
		status.Selected = probeLocalTUNEgressOptionFromCandidate(options[0])
	}

	manualTarget, manualID, manualLabel, manual := currentProbeRouteLinuxManualEgressTarget()
	if manual {
		status.ManualEnabled = true
		status.Mode = "manual"
		manualOption := probeLocalLinuxEgressOption(manualTarget)
		manualOption.CandidateID = manualID
		manualOption.Name = firstNonEmpty(manualOption.Name, probeLocalTUNEgressNameFromLinuxLabel(manualLabel))
		status.ManualSelected = probeLocalTUNEgressOptionFromCandidate(manualOption)
		if candidate, ok := probeLocalLinuxFindEgressCandidate(options, manualID, manualTarget); ok {
			status.ManualValid = true
			status.Selected = probeLocalTUNEgressOptionFromCandidate(candidate)
		} else {
			status.ManualError = "manual candidate not found in current default routes"
		}
	}
	if len(options) == 0 {
		status.Selected = nil
	}
	return status, nil
}

func probeLocalTUNEgressUpdate(req probeLocalTUNEgressUpdateRequest) (probeLocalTUNEgressStatus, error) {
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	switch mode {
	case "", "auto":
		clearProbeRouteLinuxManualEgressTarget()
		if err := persistProbeLocalTUNEgressAutoState(); err != nil {
			return probeLocalTUNEgressStatus{}, err
		}
	case "manual":
		options, err := probeLocalLinuxPrimaryEgressRouteOptions(probeRouteLinuxTUNDeviceName())
		if err != nil {
			return probeLocalTUNEgressStatus{}, err
		}
		candidate, ok := probeLocalLinuxFindEgressCandidateByRequest(options, req)
		if !ok {
			return probeLocalTUNEgressStatus{}, &probeLocalHTTPError{Status: 404, Message: "candidate not found"}
		}
		setProbeRouteLinuxManualEgressTarget(probeLocalLinuxEgressTarget(candidate), candidate.CandidateID, probeLocalTUNEgressSelectedLabel(&candidate))
		if err := persistProbeLocalTUNEgressManualState(candidate); err != nil {
			return probeLocalTUNEgressStatus{}, err
		}
	default:
		return probeLocalTUNEgressStatus{}, &probeLocalHTTPError{Status: 400, Message: "mode must be auto or manual"}
	}
	if err := refreshProbeRouteLinuxSelectedEgress(); err != nil {
		return probeLocalTUNEgressStatus{}, err
	}
	if probeVirtualRouterLocalDNSEnabled() {
		if err := applyProbeVirtualRouterSystemDNS(); err != nil {
			return probeLocalTUNEgressStatus{}, err
		}
	}
	return probeLocalTUNEgressSnapshot()
}

func applyProbeLocalTUNEgressPersistentState(state probeLocalTUNEgressPersistentState) {
	if !strings.EqualFold(strings.TrimSpace(state.Mode), "manual") {
		clearProbeRouteLinuxManualEgressTarget()
		return
	}
	options, err := probeLocalLinuxPrimaryEgressRouteOptions(probeRouteLinuxTUNDeviceName())
	if err != nil {
		logProbeWarnf("probe local linux tun egress restore candidates failed: %v", err)
		clearProbeRouteLinuxManualEgressTarget()
		return
	}
	candidate, ok := probeLocalLinuxFindPersistentEgressCandidate(options, state)
	if !ok {
		dev := strings.TrimSpace(state.Name)
		if dev == "" {
			dev = probeLocalLinuxEgressDevFromCandidateID(state.CandidateID)
		}
		setProbeRouteLinuxManualEgressTarget(
			probeVirtualRouterLinuxRouteTarget{Dev: dev, Gateway: strings.TrimSpace(state.NextHop)},
			strings.TrimSpace(state.CandidateID),
			strings.TrimSpace(state.Label),
		)
		return
	}
	setProbeRouteLinuxManualEgressTarget(probeLocalLinuxEgressTarget(candidate), candidate.CandidateID, probeLocalTUNEgressSelectedLabel(&candidate))
	if !probeLocalLinuxPersistentEgressMatchesCandidate(state, candidate) {
		if err := persistProbeLocalTUNEgressManualState(candidate); err != nil {
			logProbeWarnf("probe local linux tun egress target migration persist failed: %v", err)
		}
	}
}

func probeLocalLinuxPrimaryEgressRouteOptions(excludedDev string) ([]probeLocalTUNEgressRouteTargetOption, error) {
	output, err := probeLocalLinuxRunCommand(5*time.Second, "ip", "-4", "route", "show", "default")
	if err != nil {
		return nil, fmt.Errorf("list linux default routes failed: %w", err)
	}
	excluded := strings.TrimSpace(excludedDev)
	options := make([]probeLocalTUNEgressRouteTargetOption, 0)
	seen := make(map[string]struct{})
	for _, line := range strings.Split(output, "\n") {
		target, ok := parseProbeVirtualRouterLinuxDefaultRouteLine(line)
		if !ok || target.Dev == excluded {
			continue
		}
		option := probeLocalLinuxEgressOption(target)
		option.RouteMetric = probeLocalLinuxDefaultRouteMetric(line)
		option.TotalMetric = uint64(option.RouteMetric)
		if _, ok := seen[option.CandidateID]; ok {
			continue
		}
		seen[option.CandidateID] = struct{}{}
		options = append(options, option)
	}
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].TotalMetric != options[j].TotalMetric {
			return options[i].TotalMetric < options[j].TotalMetric
		}
		if options[i].Name != options[j].Name {
			return options[i].Name < options[j].Name
		}
		return options[i].NextHop < options[j].NextHop
	})
	return options, nil
}

func probeLocalLinuxDefaultRouteMetric(line string) uint32 {
	fields := strings.Fields(strings.TrimSpace(line))
	for index := 0; index+1 < len(fields); index++ {
		if fields[index] != "metric" {
			continue
		}
		value, err := strconv.ParseUint(fields[index+1], 10, 32)
		if err == nil {
			return uint32(value)
		}
	}
	return 0
}

func probeLocalLinuxEgressOption(target probeVirtualRouterLinuxRouteTarget) probeLocalTUNEgressRouteTargetOption {
	dev := strings.TrimSpace(target.Dev)
	nextHop := strings.TrimSpace(target.Gateway)
	ifIndex := 0
	if iface, err := probeLocalLinuxInterfaceByName(dev); err == nil && iface != nil {
		ifIndex = iface.Index
	}
	return probeLocalTUNEgressRouteTargetOption{
		CandidateID:    probeLocalLinuxEgressCandidateID(dev, nextHop),
		InterfaceIndex: ifIndex,
		NextHop:        nextHop,
		Name:           dev,
		Description:    "Linux network interface",
	}
}

func probeLocalLinuxEgressCandidateID(dev, nextHop string) string {
	return strings.ToLower("dev:" + strings.TrimSpace(dev) + "|" + strings.TrimSpace(nextHop))
}

func probeLocalLinuxEgressDevFromCandidateID(candidateID string) string {
	clean := strings.TrimSpace(candidateID)
	if !strings.HasPrefix(strings.ToLower(clean), "dev:") {
		return ""
	}
	dev, _, _ := strings.Cut(clean[len("dev:"):], "|")
	return strings.TrimSpace(dev)
}

func probeLocalLinuxEgressTarget(option probeLocalTUNEgressRouteTargetOption) probeVirtualRouterLinuxRouteTarget {
	return probeVirtualRouterLinuxRouteTarget{Dev: strings.TrimSpace(option.Name), Gateway: strings.TrimSpace(option.NextHop)}
}

func probeLocalLinuxFindEgressCandidateByRequest(options []probeLocalTUNEgressRouteTargetOption, req probeLocalTUNEgressUpdateRequest) (probeLocalTUNEgressRouteTargetOption, bool) {
	for _, option := range options {
		if strings.TrimSpace(req.CandidateID) != "" && strings.EqualFold(option.CandidateID, strings.TrimSpace(req.CandidateID)) {
			return option, true
		}
		if req.InterfaceIndex > 0 && option.InterfaceIndex == req.InterfaceIndex {
			return option, true
		}
	}
	return probeLocalTUNEgressRouteTargetOption{}, false
}

func probeLocalLinuxFindEgressCandidate(options []probeLocalTUNEgressRouteTargetOption, candidateID string, target probeVirtualRouterLinuxRouteTarget) (probeLocalTUNEgressRouteTargetOption, bool) {
	for _, option := range options {
		if strings.TrimSpace(candidateID) != "" && strings.EqualFold(option.CandidateID, strings.TrimSpace(candidateID)) {
			return option, true
		}
		if option.Name == target.Dev && option.NextHop == target.Gateway {
			return option, true
		}
	}
	return probeLocalTUNEgressRouteTargetOption{}, false
}

func probeLocalLinuxFindPersistentEgressCandidate(options []probeLocalTUNEgressRouteTargetOption, state probeLocalTUNEgressPersistentState) (probeLocalTUNEgressRouteTargetOption, bool) {
	target := probeVirtualRouterLinuxRouteTarget{Dev: strings.TrimSpace(state.Name), Gateway: strings.TrimSpace(state.NextHop)}
	if candidate, ok := probeLocalLinuxFindEgressCandidate(options, state.CandidateID, target); ok {
		return candidate, true
	}
	if state.InterfaceIndex > 0 {
		for _, option := range options {
			if option.InterfaceIndex == state.InterfaceIndex && option.NextHop == strings.TrimSpace(state.NextHop) {
				return option, true
			}
		}
	}
	return probeLocalTUNEgressRouteTargetOption{}, false
}

func probeLocalLinuxPersistentEgressMatchesCandidate(state probeLocalTUNEgressPersistentState, candidate probeLocalTUNEgressRouteTargetOption) bool {
	return strings.EqualFold(strings.TrimSpace(state.CandidateID), candidate.CandidateID) &&
		state.InterfaceIndex == candidate.InterfaceIndex &&
		strings.EqualFold(strings.TrimSpace(state.NextHop), candidate.NextHop) &&
		strings.EqualFold(strings.TrimSpace(state.Name), candidate.Name)
}

func probeLocalTUNEgressNameFromLinuxLabel(label string) string {
	parts := strings.Split(strings.TrimSpace(label), " / ")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

func setProbeRouteLinuxManualEgressTarget(target probeVirtualRouterLinuxRouteTarget, candidateID, label string) {
	probeRouteLinuxEgressState.mu.Lock()
	probeRouteLinuxEgressState.manual = true
	probeRouteLinuxEgressState.target = target
	probeRouteLinuxEgressState.candidateID = strings.TrimSpace(candidateID)
	probeRouteLinuxEgressState.label = strings.TrimSpace(label)
	probeRouteLinuxEgressState.mu.Unlock()
}

func clearProbeRouteLinuxManualEgressTarget() {
	probeRouteLinuxEgressState.mu.Lock()
	probeRouteLinuxEgressState.manual = false
	probeRouteLinuxEgressState.target = probeVirtualRouterLinuxRouteTarget{}
	probeRouteLinuxEgressState.candidateID = ""
	probeRouteLinuxEgressState.label = ""
	probeRouteLinuxEgressState.mu.Unlock()
}

func currentProbeRouteLinuxManualEgressTarget() (probeVirtualRouterLinuxRouteTarget, string, string, bool) {
	probeRouteLinuxEgressState.mu.Lock()
	defer probeRouteLinuxEgressState.mu.Unlock()
	return probeRouteLinuxEgressState.target, probeRouteLinuxEgressState.candidateID, probeRouteLinuxEgressState.label, probeRouteLinuxEgressState.manual
}

func resolveProbeRouteLinuxSelectedEgressRoute(excludedDev string) (probeVirtualRouterLinuxRouteTarget, error) {
	target, candidateID, _, manual := currentProbeRouteLinuxManualEgressTarget()
	if manual {
		options, err := probeLocalLinuxPrimaryEgressRouteOptions(excludedDev)
		if err != nil {
			return probeVirtualRouterLinuxRouteTarget{}, err
		}
		if candidate, ok := probeLocalLinuxFindEgressCandidate(options, candidateID, target); ok {
			return probeLocalLinuxEgressTarget(candidate), nil
		}
		return probeVirtualRouterLinuxRouteTarget{}, errors.New("selected linux egress route is unavailable")
	}
	options, err := probeLocalLinuxPrimaryEgressRouteOptions(excludedDev)
	if err != nil {
		return probeVirtualRouterLinuxRouteTarget{}, err
	}
	if len(options) == 0 {
		return probeVirtualRouterLinuxRouteTarget{}, errors.New("usable linux virtual router ipv4 default route not found")
	}
	return probeLocalLinuxEgressTarget(options[0]), nil
}

func refreshProbeRouteLinuxSelectedEgress() error {
	target, err := resolveProbeRouteLinuxSelectedEgressRoute(probeRouteLinuxTUNDeviceName())
	if err != nil {
		return err
	}
	if err := rebindProbeRouteLinuxDirectBypasses(target); err != nil {
		return err
	}
	if probeVirtualRouterLocalEntryEnabled() {
		localIP := currentProbeVirtualRouterLocalIP()
		if localIP != "" {
			return ensureProbeVirtualRouterLinuxTakeoverRoutes(probeRouteLinuxTUNDeviceName(), localIP)
		}
	}
	return nil
}

func rebindProbeRouteLinuxDirectBypasses(target probeVirtualRouterLinuxRouteTarget) error {
	probeRouteLinuxDirectBypassState.mu.Lock()
	routes := make([]probeVirtualRouterLinuxRouteDef, 0, len(probeRouteLinuxDirectBypassState.routes))
	for _, routeDef := range probeRouteLinuxDirectBypassState.routes {
		routes = append(routes, routeDef)
	}
	probeRouteLinuxDirectBypassState.mu.Unlock()
	var allErr error
	for _, oldRoute := range routes {
		newRoute := oldRoute
		newRoute.Dev = target.Dev
		newRoute.Gateway = target.Gateway
		if err := ensureProbeVirtualRouterLinuxRoute(newRoute); err != nil {
			allErr = errors.Join(allErr, err)
			continue
		}
		probeRouteLinuxDirectBypassState.mu.Lock()
		probeRouteLinuxDirectBypassState.routes[newRoute.Prefix] = newRoute
		probeRouteLinuxDirectBypassState.mu.Unlock()
	}
	return allErr
}

func resetProbeRouteLinuxEgressStateForTest() {
	clearProbeRouteLinuxManualEgressTarget()
}
