//go:build windows

package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func startProbeLocalTUNEgressGuardian() {}

func stopProbeLocalTUNEgressGuardian() {}

var probeLocalWindowsEgressRouteOptions = probeLocalWindowsPrimaryEgressRouteOptions

func probeLocalTUNEgressSnapshot() (probeLocalTUNEgressStatus, error) {
	applyProbeLocalTUNEgressPersistentState(currentProbeLocalTUNEgressPersistentStateBestEffort())
	status := probeLocalTUNEgressStatus{
		APIVersion: probeLocalTUNEgressAPIVersion,
		Supported:  true,
		Mode:       "auto",
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	excludedIfIndex := 0
	if routeTarget, err := resolveProbeRouteWindowsTUNRouteTarget(); err == nil {
		excludedIfIndex = routeTarget.InterfaceIndex
	}
	options, err := probeLocalWindowsEgressRouteOptions(excludedIfIndex)
	if err != nil {
		return status, err
	}
	status.Candidates = append(status.Candidates, options...)
	if len(options) > 0 {
		status.Selected = probeLocalTUNEgressOptionFromCandidate(options[0])
	}

	manualTarget, manualID, manualLabel, manualOK := currentProbeRouteWindowsDirectManualRouteTarget()
	if manualOK {
		status.ManualEnabled = true
		status.Mode = "manual"
		status.ManualSelected = probeLocalTUNEgressOptionFromCandidate(probeLocalTUNEgressRouteTargetOption{
			CandidateID:    manualID,
			InterfaceIndex: manualTarget.InterfaceIndex,
			InterfaceLUID:  manualTarget.InterfaceLUID,
			InterfaceGUID:  manualTarget.InterfaceGUID,
			NextHop:        strings.TrimSpace(manualTarget.NextHop),
		})
		if matched, ok := probeLocalTUNEgressFindCandidate(options, manualID, manualTarget); ok {
			status.ManualValid = true
			status.Selected = probeLocalTUNEgressOptionFromCandidate(matched)
			status.Mode = "manual"
		} else {
			status.ManualValid = false
			status.ManualError = "manual candidate not found in current default routes"
			if len(options) > 0 {
				status.Selected = probeLocalTUNEgressOptionFromCandidate(options[0])
			}
		}
		if strings.TrimSpace(manualLabel) != "" && status.ManualSelected != nil {
			status.ManualSelected.Name = firstNonEmpty(strings.TrimSpace(status.ManualSelected.Name), strings.TrimSpace(manualLabel))
		}
	}

	if !manualOK {
		status.Mode = "auto"
	}
	if len(options) == 0 {
		status.Selected = nil
	}
	return status, nil
}

func probeLocalTUNEgressUpdate(req probeLocalTUNEgressUpdateRequest) (probeLocalTUNEgressStatus, error) {
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	excludedIfIndex := 0
	if routeTarget, err := resolveProbeRouteWindowsTUNRouteTarget(); err == nil {
		excludedIfIndex = routeTarget.InterfaceIndex
	}
	switch mode {
	case "", "auto":
		clearProbeRouteWindowsDirectManualRouteTarget()
		if err := persistProbeLocalTUNEgressAutoState(); err != nil {
			return probeLocalTUNEgressStatus{}, err
		}
		if err := prepareProbeRouteWindowsDirectRouteTarget(); err != nil {
			logProbeWarnf("probe local tun egress auto refresh failed: %v", err)
		}
		return probeLocalTUNEgressSnapshot()
	case "manual":
		options, err := probeLocalWindowsEgressRouteOptions(excludedIfIndex)
		if err != nil {
			return probeLocalTUNEgressStatus{}, err
		}
		candidate, ok := probeLocalTUNEgressFindCandidateByRequest(options, req)
		if !ok {
			return probeLocalTUNEgressStatus{}, &probeLocalHTTPError{Status: 404, Message: "candidate not found"}
		}
		setProbeRouteWindowsDirectManualRouteTarget(
			probeRouteWindowsDirectRouteTargetFromEgressOption(candidate),
			candidate.CandidateID,
			probeLocalTUNEgressSelectedLabel(&candidate),
		)
		if err := persistProbeLocalTUNEgressManualState(candidate); err != nil {
			return probeLocalTUNEgressStatus{}, err
		}
		if err := prepareProbeRouteWindowsDirectRouteTarget(); err != nil {
			logProbeWarnf("probe local tun egress manual refresh failed: %v", err)
		}
		return probeLocalTUNEgressSnapshot()
	default:
		return probeLocalTUNEgressStatus{}, &probeLocalHTTPError{Status: 400, Message: "mode must be auto or manual"}
	}
}

func applyProbeLocalTUNEgressPersistentState(egress probeLocalTUNEgressPersistentState) {
	if !strings.EqualFold(strings.TrimSpace(egress.Mode), "manual") {
		clearProbeRouteWindowsDirectManualRouteTarget()
		return
	}
	if egress.InterfaceIndex <= 0 && egress.InterfaceLUID == 0 && normalizeProbeLocalTUNEgressInterfaceGUID(egress.InterfaceGUID) == "" {
		logProbeWarnf("probe local tun egress persisted manual target ignored: missing adapter identity next_hop=%s", strings.TrimSpace(egress.NextHop))
		clearProbeRouteWindowsDirectManualRouteTarget()
		return
	}
	if strings.TrimSpace(egress.NextHop) == "" {
		logProbeWarnf("probe local tun egress persisted manual target ignored: adapter identity has no next hop")
		clearProbeRouteWindowsDirectManualRouteTarget()
		return
	}

	candidateID := strings.TrimSpace(egress.CandidateID)
	label := strings.TrimSpace(egress.Label)
	target := probeRouteWindowsDirectRouteTarget{
		InterfaceIndex: egress.InterfaceIndex,
		InterfaceLUID:  egress.InterfaceLUID,
		InterfaceGUID:  strings.TrimSpace(egress.InterfaceGUID),
		NextHop:        strings.TrimSpace(egress.NextHop),
	}
	excludedIfIndex := 0
	if routeTarget, err := resolveProbeRouteWindowsTUNRouteTarget(); err == nil {
		excludedIfIndex = routeTarget.InterfaceIndex
	}
	if options, err := probeLocalWindowsEgressRouteOptions(excludedIfIndex); err == nil {
		if candidate, ok := probeLocalTUNEgressFindPersistentCandidate(options, egress); ok {
			target = probeRouteWindowsDirectRouteTargetFromEgressOption(candidate)
			candidateID = candidate.CandidateID
			label = probeLocalTUNEgressSelectedLabel(&candidate)
			if !probeLocalTUNEgressPersistentStateMatchesCandidate(egress, candidate) {
				if persistErr := persistProbeLocalTUNEgressManualState(candidate); persistErr != nil {
					logProbeWarnf("probe local tun egress stable adapter migration persist failed: %v", persistErr)
				} else {
					logProbeInfof("probe local tun egress stable adapter migrated: old_if_index=%d new_if_index=%d interface_guid=%s next_hop=%s", egress.InterfaceIndex, candidate.InterfaceIndex, strings.TrimSpace(candidate.InterfaceGUID), strings.TrimSpace(candidate.NextHop))
				}
			}
		}
	} else {
		logProbeWarnf("probe local tun egress current candidates unavailable while restoring manual target: %v", err)
	}
	setProbeRouteWindowsDirectManualRouteTarget(
		target,
		candidateID,
		label,
	)
	logProbeInfof("probe local tun egress restored manual target: if_index=%d interface_guid=%s next_hop=%s", target.InterfaceIndex, strings.TrimSpace(target.InterfaceGUID), strings.TrimSpace(target.NextHop))
}

func probeLocalWindowsPrimaryEgressRouteOptions(excludedIfIndex int) ([]probeLocalTUNEgressRouteTargetOption, error) {
	var tablePtr uintptr
	ret, _, callErr := probeLocalProcGetIpForwardTable2Net.Call(uintptr(windows.AF_INET), uintptr(unsafe.Pointer(&tablePtr)))
	if ret != 0 {
		return nil, probeLocalWindowsNetapiCallErr("GetIpForwardTable2", ret, callErr)
	}
	if tablePtr == 0 {
		return nil, errors.New("GetIpForwardTable2 returned empty table")
	}
	defer probeLocalProcFreeMibTableNet.Call(tablePtr)

	header := (*probeLocalMIBIPForwardTable2Header)(unsafe.Pointer(tablePtr))
	rowsBase := tablePtr + unsafe.Sizeof(probeLocalMIBIPForwardTable2Header{})
	rows := unsafe.Slice((*probeLocalMIBIPForwardRow2)(unsafe.Pointer(rowsBase)), int(header.NumEntries))
	adapters, err := windowsListAdaptersIPv4()
	if err != nil {
		return nil, err
	}
	adapterByIfIndex := make(map[int]windowsAdapterInfo, len(adapters))
	for _, adapter := range adapters {
		if adapter.InterfaceIndex > 0 {
			adapterByIfIndex[adapter.InterfaceIndex] = adapter
		}
	}
	options := make([]probeLocalTUNEgressRouteTargetOption, 0, len(rows))
	seen := map[string]struct{}{}
	for _, row := range rows {
		if int(row.InterfaceIndex) <= 0 || int(row.InterfaceIndex) == excludedIfIndex {
			continue
		}
		if row.DestinationPrefix.PrefixLength != 0 {
			continue
		}
		prefixIP := decodeProbeLocalSockaddrInetIPv4(row.DestinationPrefix.Prefix)
		if prefixIP != "0.0.0.0" {
			continue
		}
		nextHop := decodeProbeLocalSockaddrInetIPv4(row.NextHop)
		if nextHop == "" || nextHop == "0.0.0.0" {
			continue
		}
		adapter, ok := adapterByIfIndex[int(row.InterfaceIndex)]
		if !ok || adapter.OperStatus != windows.IfOperStatusUp {
			continue
		}
		candidateID := probeLocalTUNEgressCandidateID(adapter.AdapterGUID, adapter.InterfaceLUID, int(row.InterfaceIndex), nextHop)
		if _, exists := seen[candidateID]; exists {
			continue
		}
		seen[candidateID] = struct{}{}
		options = append(options, probeLocalTUNEgressRouteTargetOption{
			CandidateID:     candidateID,
			InterfaceIndex:  int(row.InterfaceIndex),
			InterfaceLUID:   adapter.InterfaceLUID,
			InterfaceGUID:   adapter.AdapterGUID,
			NextHop:         nextHop,
			Name:            strings.TrimSpace(adapter.Name),
			Description:     strings.TrimSpace(adapter.Description),
			RouteMetric:     row.Metric,
			InterfaceMetric: adapter.IPv4Metric,
			TotalMetric:     uint64(row.Metric) + uint64(adapter.IPv4Metric),
		})
	}
	sort.SliceStable(options, func(i, j int) bool {
		left := options[i]
		right := options[j]
		switch {
		case left.TotalMetric != right.TotalMetric:
			return left.TotalMetric < right.TotalMetric
		case left.RouteMetric != right.RouteMetric:
			return left.RouteMetric < right.RouteMetric
		case left.InterfaceMetric != right.InterfaceMetric:
			return left.InterfaceMetric < right.InterfaceMetric
		case left.InterfaceIndex != right.InterfaceIndex:
			return left.InterfaceIndex < right.InterfaceIndex
		default:
			return strings.ToLower(left.NextHop) < strings.ToLower(right.NextHop)
		}
	})
	return options, nil
}

func probeLocalTUNEgressFindCandidateByRequest(options []probeLocalTUNEgressRouteTargetOption, req probeLocalTUNEgressUpdateRequest) (probeLocalTUNEgressRouteTargetOption, bool) {
	if len(options) == 0 {
		return probeLocalTUNEgressRouteTargetOption{}, false
	}
	candidateID := strings.ToLower(strings.TrimSpace(req.CandidateID))
	if candidateID != "" {
		for _, option := range options {
			if strings.EqualFold(strings.TrimSpace(option.CandidateID), candidateID) {
				return option, true
			}
		}
	}
	if req.InterfaceIndex > 0 {
		for _, option := range options {
			if option.InterfaceIndex == req.InterfaceIndex {
				return option, true
			}
		}
	}
	return probeLocalTUNEgressRouteTargetOption{}, false
}

func probeLocalTUNEgressFindCandidate(options []probeLocalTUNEgressRouteTargetOption, candidateID string, target probeRouteWindowsDirectRouteTarget) (probeLocalTUNEgressRouteTargetOption, bool) {
	cleanID := strings.ToLower(strings.TrimSpace(candidateID))
	cleanGUID := normalizeProbeLocalTUNEgressInterfaceGUID(target.InterfaceGUID)
	if cleanGUID != "" {
		for _, option := range options {
			if normalizeProbeLocalTUNEgressInterfaceGUID(option.InterfaceGUID) == cleanGUID {
				return option, true
			}
		}
	}
	if target.InterfaceLUID > 0 {
		for _, option := range options {
			if option.InterfaceLUID == target.InterfaceLUID {
				return option, true
			}
		}
	}
	for _, option := range options {
		if cleanID != "" && strings.EqualFold(strings.TrimSpace(option.CandidateID), cleanID) {
			return option, true
		}
		if option.InterfaceIndex == target.InterfaceIndex && strings.EqualFold(strings.TrimSpace(option.NextHop), strings.TrimSpace(target.NextHop)) {
			return option, true
		}
	}
	nextHop := strings.TrimSpace(target.NextHop)
	if nextHop != "" {
		matches := make([]probeLocalTUNEgressRouteTargetOption, 0, 1)
		for _, option := range options {
			if strings.EqualFold(strings.TrimSpace(option.NextHop), nextHop) {
				matches = append(matches, option)
			}
		}
		if len(matches) == 1 {
			return matches[0], true
		}
	}
	return probeLocalTUNEgressRouteTargetOption{}, false
}

func probeLocalTUNEgressFindPersistentCandidate(options []probeLocalTUNEgressRouteTargetOption, egress probeLocalTUNEgressPersistentState) (probeLocalTUNEgressRouteTargetOption, bool) {
	target := probeRouteWindowsDirectRouteTarget{
		InterfaceIndex: egress.InterfaceIndex,
		InterfaceLUID:  egress.InterfaceLUID,
		InterfaceGUID:  egress.InterfaceGUID,
		NextHop:        egress.NextHop,
	}
	if candidate, ok := probeLocalTUNEgressFindCandidate(options, egress.CandidateID, target); ok {
		return candidate, true
	}
	legacyName := strings.TrimSpace(egress.Name)
	if legacyName == "" {
		legacyName = probeLocalTUNEgressNameFromLabel(egress.Label)
	}
	if legacyName != "" {
		for _, option := range options {
			if strings.EqualFold(strings.TrimSpace(option.Name), legacyName) && strings.EqualFold(strings.TrimSpace(option.NextHop), strings.TrimSpace(egress.NextHop)) {
				return option, true
			}
		}
	}
	return probeLocalTUNEgressRouteTargetOption{}, false
}

func probeLocalTUNEgressNameFromLabel(label string) string {
	parts := strings.Split(strings.TrimSpace(label), " / ")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

func probeRouteWindowsDirectRouteTargetFromEgressOption(candidate probeLocalTUNEgressRouteTargetOption) probeRouteWindowsDirectRouteTarget {
	return probeRouteWindowsDirectRouteTarget{
		InterfaceIndex: candidate.InterfaceIndex,
		InterfaceLUID:  candidate.InterfaceLUID,
		InterfaceGUID:  strings.TrimSpace(candidate.InterfaceGUID),
		NextHop:        strings.TrimSpace(candidate.NextHop),
	}
}

func probeLocalTUNEgressPersistentStateMatchesCandidate(state probeLocalTUNEgressPersistentState, candidate probeLocalTUNEgressRouteTargetOption) bool {
	return strings.EqualFold(strings.TrimSpace(state.CandidateID), strings.TrimSpace(candidate.CandidateID)) &&
		state.InterfaceIndex == candidate.InterfaceIndex &&
		state.InterfaceLUID == candidate.InterfaceLUID &&
		normalizeProbeLocalTUNEgressInterfaceGUID(state.InterfaceGUID) == normalizeProbeLocalTUNEgressInterfaceGUID(candidate.InterfaceGUID) &&
		strings.EqualFold(strings.TrimSpace(state.NextHop), strings.TrimSpace(candidate.NextHop)) &&
		strings.EqualFold(strings.TrimSpace(state.Name), strings.TrimSpace(candidate.Name)) &&
		strings.EqualFold(strings.TrimSpace(state.Description), strings.TrimSpace(candidate.Description))
}

func describeProbeLocalTUNEgressTarget(target probeRouteWindowsDirectRouteTarget) string {
	return fmt.Sprintf("if_index=%d luid=%d guid=%s next_hop=%s", target.InterfaceIndex, target.InterfaceLUID, strings.TrimSpace(target.InterfaceGUID), strings.TrimSpace(target.NextHop))
}
