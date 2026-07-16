package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const probeLocalTUNEgressAPIVersion = "tun-egress-stable-adapter-v3"

type probeLocalTUNEgressRouteTargetOption struct {
	CandidateID     string `json:"candidate_id"`
	InterfaceIndex  int    `json:"interface_index"`
	InterfaceLUID   uint64 `json:"interface_luid,omitempty"`
	InterfaceGUID   string `json:"interface_guid,omitempty"`
	NextHop         string `json:"next_hop"`
	Name            string `json:"name,omitempty"`
	Description     string `json:"description,omitempty"`
	RouteMetric     uint32 `json:"route_metric,omitempty"`
	InterfaceMetric uint32 `json:"interface_metric,omitempty"`
	TotalMetric     uint64 `json:"total_metric,omitempty"`
}

type probeLocalTUNEgressStatus struct {
	APIVersion     string                                 `json:"api_version,omitempty"`
	Supported      bool                                   `json:"supported"`
	Mode           string                                 `json:"mode"`
	ManualEnabled  bool                                   `json:"manual_enabled"`
	ManualValid    bool                                   `json:"manual_valid"`
	ManualError    string                                 `json:"manual_error,omitempty"`
	ManualSelected *probeLocalTUNEgressRouteTargetOption  `json:"manual_selected,omitempty"`
	Selected       *probeLocalTUNEgressRouteTargetOption  `json:"selected,omitempty"`
	Candidates     []probeLocalTUNEgressRouteTargetOption `json:"candidates,omitempty"`
	UpdatedAt      string                                 `json:"updated_at,omitempty"`
}

type probeLocalTUNEgressUpdateRequest struct {
	Mode           string `json:"mode"`
	CandidateID    string `json:"candidate_id,omitempty"`
	InterfaceIndex int    `json:"interface_index,omitempty"`
}

func probeLocalTUNEgressHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		status, err := probeLocalTUNEgressSnapshot()
		if err != nil {
			writeProbeLocalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	case http.MethodPost:
		body := http.MaxBytesReader(w, r.Body, probeLocalRouteReadBodyMaxLen)
		defer body.Close()
		decoder := json.NewDecoder(body)
		decoder.DisallowUnknownFields()
		var req probeLocalTUNEgressUpdateRequest
		if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body: multiple json values"})
			return
		}
		status, err := probeLocalTUNEgressUpdate(req)
		if err != nil {
			writeProbeLocalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func probeLocalTUNEgressSelectedLabel(option *probeLocalTUNEgressRouteTargetOption) string {
	if option == nil {
		return "-"
	}
	parts := make([]string, 0, 4)
	if name := strings.TrimSpace(option.Name); name != "" {
		parts = append(parts, name)
	}
	if hop := strings.TrimSpace(option.NextHop); hop != "" {
		parts = append(parts, hop)
	}
	if option.InterfaceIndex > 0 {
		parts = append(parts, "ifIndex="+strconv.Itoa(option.InterfaceIndex))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " / ")
}

func probeLocalTUNEgressOptionFromCandidate(option probeLocalTUNEgressRouteTargetOption) *probeLocalTUNEgressRouteTargetOption {
	copy := option
	if strings.TrimSpace(copy.CandidateID) == "" {
		copy.CandidateID = probeLocalTUNEgressCandidateID(copy.InterfaceGUID, copy.InterfaceLUID, copy.InterfaceIndex, copy.NextHop)
	}
	return &copy
}

func probeLocalTUNEgressCandidateID(interfaceGUID string, interfaceLUID uint64, interfaceIndex int, nextHop string) string {
	identity := ""
	if guid := normalizeProbeLocalTUNEgressInterfaceGUID(interfaceGUID); guid != "" {
		identity = "guid:" + guid
	} else if interfaceLUID > 0 {
		identity = fmt.Sprintf("luid:%d", interfaceLUID)
	} else {
		identity = "ifindex:" + strconv.Itoa(interfaceIndex)
	}
	return strings.ToLower(strings.TrimSpace(strings.Join([]string{identity, strings.TrimSpace(nextHop)}, "|")))
}

func normalizeProbeLocalTUNEgressInterfaceGUID(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), "{}"))
}

func probeLocalTUNEgressModeValue(manual bool, supported bool) string {
	if !supported {
		return "unsupported"
	}
	if manual {
		return "manual"
	}
	return "auto"
}

func persistProbeLocalTUNEgressManualState(candidate probeLocalTUNEgressRouteTargetOption) error {
	now := time.Now().UTC().Format(time.RFC3339)
	state := probeLocalTUNEgressStateFile{
		Version:   1,
		UpdatedAt: now,
		TUNEgress: probeLocalTUNEgressPersistentState{
			Mode:           "manual",
			CandidateID:    strings.TrimSpace(candidate.CandidateID),
			InterfaceIndex: candidate.InterfaceIndex,
			InterfaceLUID:  candidate.InterfaceLUID,
			InterfaceGUID:  strings.TrimSpace(candidate.InterfaceGUID),
			NextHop:        strings.TrimSpace(candidate.NextHop),
			Name:           strings.TrimSpace(candidate.Name),
			Description:    strings.TrimSpace(candidate.Description),
			Label:          probeLocalTUNEgressSelectedLabel(&candidate),
			UpdatedAt:      now,
		},
	}
	return persistProbeLocalTUNEgressStateFile(state)
}

func persistProbeLocalTUNEgressAutoState() error {
	now := time.Now().UTC().Format(time.RFC3339)
	state := probeLocalTUNEgressStateFile{
		Version:   1,
		UpdatedAt: now,
		TUNEgress: probeLocalTUNEgressPersistentState{
			Mode:      "auto",
			UpdatedAt: now,
		},
	}
	return persistProbeLocalTUNEgressStateFile(state)
}

func currentProbeLocalTUNEgressPersistentStateBestEffort() probeLocalTUNEgressPersistentState {
	state, err := loadProbeLocalTUNEgressStateFile()
	if err != nil {
		logProbeWarnf("probe local tun egress persistent state load failed: %v", err)
		return probeLocalTUNEgressPersistentState{Mode: "auto"}
	}
	return state.TUNEgress
}

func loadProbeLocalTUNEgressStateFile() (probeLocalTUNEgressStateFile, error) {
	path, err := resolveProbeLocalTUNEgressStatePath()
	if err != nil {
		return probeLocalTUNEgressStateFile{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			now := time.Now().UTC().Format(time.RFC3339)
			return probeLocalTUNEgressStateFile{
				Version:   1,
				UpdatedAt: now,
				TUNEgress: probeLocalTUNEgressPersistentState{
					Mode:      "auto",
					UpdatedAt: now,
				},
			}, nil
		}
		return probeLocalTUNEgressStateFile{}, err
	}
	payload := probeLocalTUNEgressStateFile{}
	if err := decodeProbeLocalJSONStrict(raw, &payload); err != nil {
		return probeLocalTUNEgressStateFile{}, err
	}
	normalizeProbeLocalTUNEgressStateFile(&payload)
	return payload, nil
}

func persistProbeLocalTUNEgressStateFile(payload probeLocalTUNEgressStateFile) error {
	normalizeProbeLocalTUNEgressStateFile(&payload)
	path, err := resolveProbeLocalTUNEgressStatePath()
	if err != nil {
		return err
	}
	return persistProbeLocalJSONFile(path, payload)
}

func normalizeProbeLocalTUNEgressStateFile(payload *probeLocalTUNEgressStateFile) {
	if payload == nil {
		return
	}
	if payload.Version <= 0 {
		payload.Version = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if strings.TrimSpace(payload.UpdatedAt) == "" {
		payload.UpdatedAt = now
	}
	mode := strings.ToLower(strings.TrimSpace(payload.TUNEgress.Mode))
	if mode != "manual" {
		mode = "auto"
		payload.TUNEgress.CandidateID = ""
		payload.TUNEgress.InterfaceIndex = 0
		payload.TUNEgress.InterfaceLUID = 0
		payload.TUNEgress.InterfaceGUID = ""
		payload.TUNEgress.NextHop = ""
		payload.TUNEgress.Name = ""
		payload.TUNEgress.Description = ""
		payload.TUNEgress.Label = ""
	}
	payload.TUNEgress.Mode = mode
	payload.TUNEgress.CandidateID = strings.TrimSpace(payload.TUNEgress.CandidateID)
	payload.TUNEgress.InterfaceGUID = strings.TrimSpace(payload.TUNEgress.InterfaceGUID)
	payload.TUNEgress.NextHop = strings.TrimSpace(payload.TUNEgress.NextHop)
	payload.TUNEgress.Name = strings.TrimSpace(payload.TUNEgress.Name)
	payload.TUNEgress.Description = strings.TrimSpace(payload.TUNEgress.Description)
	payload.TUNEgress.Label = strings.TrimSpace(payload.TUNEgress.Label)
	if strings.TrimSpace(payload.TUNEgress.UpdatedAt) == "" {
		payload.TUNEgress.UpdatedAt = payload.UpdatedAt
	}
}
