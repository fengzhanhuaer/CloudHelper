//go:build windows

package main

import (
	"errors"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

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
	options, err := probeLocalWindowsPrimaryEgressRouteOptions(excludedIfIndex)
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
		options, err := probeLocalWindowsPrimaryEgressRouteOptions(excludedIfIndex)
		if err != nil {
			return probeLocalTUNEgressStatus{}, err
		}
		candidate, ok := probeLocalTUNEgressFindCandidateByRequest(options, req)
		if !ok {
			return probeLocalTUNEgressStatus{}, &probeLocalHTTPError{Status: 404, Message: "candidate not found"}
		}
		setProbeRouteWindowsDirectManualRouteTarget(
			probeRouteWindowsDirectRouteTarget{
				InterfaceIndex: candidate.InterfaceIndex,
				NextHop:        candidate.NextHop,
			},
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
	if egress.InterfaceIndex <= 0 || strings.TrimSpace(egress.NextHop) == "" {
		logProbeWarnf("probe local tun egress persisted manual target ignored: if_index=%d next_hop=%s", egress.InterfaceIndex, strings.TrimSpace(egress.NextHop))
		clearProbeRouteWindowsDirectManualRouteTarget()
		return
	}
	setProbeRouteWindowsDirectManualRouteTarget(
		probeRouteWindowsDirectRouteTarget{
			InterfaceIndex: egress.InterfaceIndex,
			NextHop:        strings.TrimSpace(egress.NextHop),
		},
		strings.TrimSpace(egress.CandidateID),
		strings.TrimSpace(egress.Label),
	)
	logProbeInfof("probe local tun egress restored manual target: if_index=%d next_hop=%s", egress.InterfaceIndex, strings.TrimSpace(egress.NextHop))
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
		candidateID := probeLocalTUNEgressCandidateID(int(row.InterfaceIndex), nextHop)
		if _, exists := seen[candidateID]; exists {
			continue
		}
		seen[candidateID] = struct{}{}
		options = append(options, probeLocalTUNEgressRouteTargetOption{
			CandidateID:     candidateID,
			InterfaceIndex:  int(row.InterfaceIndex),
			InterfaceLUID:   adapter.InterfaceLUID,
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
	for _, option := range options {
		if cleanID != "" && strings.EqualFold(strings.TrimSpace(option.CandidateID), cleanID) {
			return option, true
		}
		if option.InterfaceIndex == target.InterfaceIndex && strings.EqualFold(strings.TrimSpace(option.NextHop), strings.TrimSpace(target.NextHop)) {
			return option, true
		}
	}
	return probeLocalTUNEgressRouteTargetOption{}, false
}
