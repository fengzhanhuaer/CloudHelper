package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const probeLocalTUNEgressAPIVersion = "tun-egress-mode-candidate-v2"

type probeLocalTUNEgressRouteTargetOption struct {
	CandidateID     string `json:"candidate_id"`
	InterfaceIndex  int    `json:"interface_index"`
	InterfaceLUID   uint64 `json:"interface_luid,omitempty"`
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
		copy.CandidateID = probeLocalTUNEgressCandidateID(copy.InterfaceIndex, copy.NextHop)
	}
	return &copy
}

func probeLocalTUNEgressCandidateID(interfaceIndex int, nextHop string) string {
	return strings.ToLower(strings.TrimSpace(strings.Join([]string{
		strconv.Itoa(interfaceIndex),
		strings.TrimSpace(nextHop),
	}, "|")))
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
			NextHop:        strings.TrimSpace(candidate.NextHop),
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
		payload.TUNEgress.NextHop = ""
		payload.TUNEgress.Label = ""
	}
	payload.TUNEgress.Mode = mode
	payload.TUNEgress.CandidateID = strings.TrimSpace(payload.TUNEgress.CandidateID)
	payload.TUNEgress.NextHop = strings.TrimSpace(payload.TUNEgress.NextHop)
	payload.TUNEgress.Label = strings.TrimSpace(payload.TUNEgress.Label)
	if strings.TrimSpace(payload.TUNEgress.UpdatedAt) == "" {
		payload.TUNEgress.UpdatedAt = payload.UpdatedAt
	}
}
