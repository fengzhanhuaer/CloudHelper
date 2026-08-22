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

const probeLocalLinuxEgressGuardianInterval = 5 * time.Second

var probeRouteLinuxEgressState = struct {
	mu          sync.Mutex
	manual      bool
	target      probeVirtualRouterLinuxRouteTarget
	candidateID string
	label       string
}{}

var probeLocalLinuxInterfaceByName = net.InterfaceByName

var probeLocalLinuxEgressGuardianState = struct {
	mu     sync.Mutex
	stopCh chan struct{}
	doneCh chan struct{}
	failed bool
}{}

func startProbeLocalTUNEgressGuardian() {
	probeLocalLinuxEgressGuardianState.mu.Lock()
	if probeLocalLinuxEgressGuardianState.stopCh != nil {
		probeLocalLinuxEgressGuardianState.mu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	probeLocalLinuxEgressGuardianState.stopCh = stopCh
	probeLocalLinuxEgressGuardianState.doneCh = doneCh
	probeLocalLinuxEgressGuardianState.failed = false
	probeLocalLinuxEgressGuardianState.mu.Unlock()
	go runProbeLocalLinuxEgressGuardian(stopCh, doneCh)
}

func stopProbeLocalTUNEgressGuardian() {
	probeLocalLinuxEgressGuardianState.mu.Lock()
	stopCh := probeLocalLinuxEgressGuardianState.stopCh
	doneCh := probeLocalLinuxEgressGuardianState.doneCh
	probeLocalLinuxEgressGuardianState.stopCh = nil
	probeLocalLinuxEgressGuardianState.doneCh = nil
	probeLocalLinuxEgressGuardianState.failed = false
	probeLocalLinuxEgressGuardianState.mu.Unlock()
	if stopCh == nil {
		return
	}
	close(stopCh)
	if doneCh != nil {
		<-doneCh
	}
}

func runProbeLocalLinuxEgressGuardian(stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)
	ticker := time.NewTicker(probeLocalLinuxEgressGuardianInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			recordProbeLocalLinuxEgressGuardianResult(reconcileProbeLocalLinuxManualEgress())
		}
	}
}

func recordProbeLocalLinuxEgressGuardianResult(err error) {
	probeLocalLinuxEgressGuardianState.mu.Lock()
	wasFailed := probeLocalLinuxEgressGuardianState.failed
	probeLocalLinuxEgressGuardianState.failed = err != nil
	probeLocalLinuxEgressGuardianState.mu.Unlock()
	if err != nil && !wasFailed {
		logProbeWarnf("probe local linux tun egress recovery pending: %v", err)
	}
}

func reconcileProbeLocalLinuxManualEgress() error {
	state := currentProbeLocalTUNEgressPersistentStateBestEffort()
	if !strings.EqualFold(strings.TrimSpace(state.Mode), "manual") {
		return nil
	}
	options, err := probeLocalLinuxPrimaryEgressRouteOptions(probeRouteLinuxTUNDeviceName())
	if err != nil {
		return err
	}
	target := probeVirtualRouterLinuxRouteTarget{Dev: strings.TrimSpace(state.Name), Gateway: strings.TrimSpace(state.NextHop)}
	if target.Dev == "" {
		target.Dev = probeLocalLinuxEgressDevFromCandidateID(state.CandidateID)
	}
	if candidate, ok := probeLocalLinuxFindEgressCandidate(options, state.CandidateID, target); ok && probeLocalLinuxManualEgressIsEffective(options, candidate) {
		setProbeRouteLinuxManualEgressTarget(probeLocalLinuxEgressTarget(candidate), candidate.CandidateID, probeLocalTUNEgressSelectedLabel(&candidate))
		return nil
	}
	candidate, ok := probeLocalLinuxValidatedManualEgress(target, probeRouteLinuxTUNDeviceName())
	if !ok {
		return errors.New("selected linux egress route is unavailable")
	}
	if err := probeLocalLinuxForceManualEgressDefaultRoute(candidate, options); err != nil {
		return err
	}
	setProbeRouteLinuxManualEgressTarget(probeLocalLinuxEgressTarget(candidate), candidate.CandidateID, probeLocalTUNEgressSelectedLabel(&candidate))
	if err := rebindProbeRouteLinuxDirectBypasses(probeLocalLinuxEgressTarget(candidate)); err != nil {
		return err
	}
	if probeVirtualRouterLocalDNSEnabled() {
		if err := applyProbeVirtualRouterSystemDNS(); err != nil {
			return fmt.Errorf("apply router system DNS: %w", err)
		}
	}
	return nil
}

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
		} else if candidate, ok := probeLocalLinuxValidatedManualEgress(manualTarget, probeRouteLinuxTUNDeviceName()); ok {
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
		target := probeVirtualRouterLinuxRouteTarget{Dev: dev, Gateway: strings.TrimSpace(state.NextHop)}
		setProbeRouteLinuxManualEgressTarget(
			target,
			strings.TrimSpace(state.CandidateID),
			strings.TrimSpace(state.Label),
		)
		if candidate, valid := probeLocalLinuxValidatedManualEgress(target, probeRouteLinuxTUNDeviceName()); valid {
			if forceErr := probeLocalLinuxForceManualEgressDefaultRoute(candidate, options); forceErr != nil {
				logProbeWarnf("probe local linux tun egress restore force default route failed: %v", forceErr)
			}
		}
		return
	}
	setProbeRouteLinuxManualEgressTarget(probeLocalLinuxEgressTarget(candidate), candidate.CandidateID, probeLocalTUNEgressSelectedLabel(&candidate))
	if !probeLocalLinuxManualEgressIsEffective(options, candidate) {
		if forceErr := probeLocalLinuxForceManualEgressDefaultRoute(candidate, options); forceErr != nil {
			logProbeWarnf("probe local linux tun egress restore force selected default route failed: %v", forceErr)
		}
	}
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
			if !probeLocalLinuxManualEgressIsEffective(options, candidate) {
				if err := probeLocalLinuxForceManualEgressDefaultRoute(candidate, options); err != nil {
					return probeVirtualRouterLinuxRouteTarget{}, err
				}
			}
			return probeLocalLinuxEgressTarget(candidate), nil
		}
		if candidate, ok := probeLocalLinuxValidatedManualEgress(target, excludedDev); ok {
			if err := probeLocalLinuxForceManualEgressDefaultRoute(candidate, options); err != nil {
				return probeVirtualRouterLinuxRouteTarget{}, err
			}
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

func probeLocalLinuxForceManualEgressDefaultRoute(candidate probeLocalTUNEgressRouteTargetOption, defaults []probeLocalTUNEgressRouteTargetOption) error {
	dev := strings.TrimSpace(candidate.Name)
	gateway := strings.TrimSpace(candidate.NextHop)
	if dev == "" || net.ParseIP(gateway).To4() == nil {
		return errors.New("manual linux egress target is invalid")
	}
	metric := probeLocalLinuxManualEgressDefaultRouteMetric(defaults)
	args := []string{"-4", "route", "replace", "default", "via", gateway, "dev", dev}
	if metric > 0 {
		args = append(args, "metric", strconv.FormatUint(uint64(metric), 10))
	}
	if _, err := probeLocalLinuxRunCommand(5*time.Second, "ip", args...); err != nil {
		return fmt.Errorf("force selected linux egress default route via %s dev %s: %w", gateway, dev, err)
	}
	return nil
}

func probeLocalLinuxManualEgressDefaultRouteMetric(defaults []probeLocalTUNEgressRouteTargetOption) uint32 {
	if len(defaults) > 0 {
		return defaults[0].RouteMetric
	}
	return 0
}

func probeLocalLinuxManualEgressIsEffective(defaults []probeLocalTUNEgressRouteTargetOption, selected probeLocalTUNEgressRouteTargetOption) bool {
	if len(defaults) == 0 {
		return false
	}
	bestMetric := defaults[0].RouteMetric
	selectedFound := false
	for _, candidate := range defaults {
		if candidate.RouteMetric != bestMetric {
			break
		}
		if candidate.CandidateID == selected.CandidateID {
			selectedFound = true
			continue
		}
		return false
	}
	return selectedFound
}

func probeLocalLinuxValidatedManualEgress(target probeVirtualRouterLinuxRouteTarget, excludedDev string) (probeLocalTUNEgressRouteTargetOption, bool) {
	dev := strings.TrimSpace(target.Dev)
	gateway := strings.TrimSpace(target.Gateway)
	if dev == "" || dev == strings.TrimSpace(excludedDev) || net.ParseIP(gateway).To4() == nil {
		return probeLocalTUNEgressRouteTargetOption{}, false
	}
	iface, err := probeLocalLinuxInterfaceByName(dev)
	if err != nil || iface == nil {
		return probeLocalTUNEgressRouteTargetOption{}, false
	}
	output, err := probeLocalLinuxRunCommand(5*time.Second, "ip", "-4", "route", "get", gateway, "oif", dev)
	if err != nil || !probeLocalLinuxRouteGetUsesInterface(output, gateway, dev) {
		return probeLocalTUNEgressRouteTargetOption{}, false
	}
	return probeLocalLinuxEgressOption(target), true
}

func probeLocalLinuxRouteGetUsesInterface(output, gateway, dev string) bool {
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) == 0 || fields[0] == "local" {
		return false
	}
	selectedDev := ""
	sourceIP := ""
	viaGateway := ""
	for index := 0; index+1 < len(fields); index++ {
		switch fields[index] {
		case "dev":
			selectedDev = strings.TrimSpace(fields[index+1])
		case "src":
			sourceIP = strings.TrimSpace(fields[index+1])
		case "via":
			viaGateway = strings.TrimSpace(fields[index+1])
		}
	}
	return selectedDev == strings.TrimSpace(dev) && viaGateway == "" && !strings.EqualFold(sourceIP, strings.TrimSpace(gateway))
}

func refreshProbeRouteLinuxSelectedEgress() error {
	target, err := resolveProbeRouteLinuxSelectedEgressRoute(probeRouteLinuxTUNDeviceName())
	if err != nil {
		return err
	}
	if err := rebindProbeRouteLinuxDirectBypasses(target); err != nil {
		return err
	}
	if probeProductVRouteTakeoverEnabled() && probeVirtualRouterLocalEntryEnabled() {
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
