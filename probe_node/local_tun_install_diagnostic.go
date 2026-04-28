package main

import (
	"strings"
	"sync"
)

const (
	probeLocalTUNInstallCodeUnsupported                = "TUN_UNSUPPORTED"
	probeLocalTUNInstallCodeWintunLibraryMissing       = "TUN_WINTUN_LIBRARY_MISSING"
	probeLocalTUNInstallCodeElevationRequired          = "TUN_ELEVATION_REQUIRED"
	probeLocalTUNInstallCodeElevationTimeout           = "TUN_ELEVATION_TIMEOUT"
	probeLocalTUNInstallCodeAdapterCreateFailed        = "TUN_ADAPTER_CREATE_FAILED"
	probeLocalTUNInstallCodeAdapterNotDetected         = "TUN_ADAPTER_NOT_DETECTED"
	probeLocalTUNInstallCodeRouteTargetFailed          = "TUN_ROUTE_TARGET_FAILED"
	probeLocalTUNInstallCodeIfIndexInvalid             = "TUN_IFINDEX_INVALID"
	probeLocalTUNInstallCodeAdapterPhantomOnly         = "TUN_ADAPTER_PHANTOM_ONLY"
	probeLocalTUNInstallCodeAdapterJointVisibilityMiss = "TUN_ADAPTER_JOINT_VISIBILITY_MISSING"
)

type probeLocalTUNInstallObservationDriver struct {
	PackageExists bool   `json:"package_exists"`
	PackagePath   string `json:"package_path"`
}

type probeLocalTUNInstallObservationCreate struct {
	Called        bool   `json:"called"`
	HandleNonZero bool   `json:"handle_nonzero"`
	RawError      string `json:"raw_error"`
}

type probeLocalTUNInstallObservationVisibility struct {
	DetectVisible   bool `json:"detect_visible"`
	IfIndexResolved bool `json:"ifindex_resolved"`
	IfIndexValue    int  `json:"ifindex_value"`
}

type probeLocalTUNInstallObservationFinal struct {
	Success    bool   `json:"success"`
	ReasonCode string `json:"reason_code"`
	Reason     string `json:"reason"`
}

type probeLocalTUNInstallObservationDiagnostic struct {
	Code     string `json:"code"`
	RawError string `json:"raw_error"`
}

type probeLocalTUNInstallObservation struct {
	Driver     probeLocalTUNInstallObservationDriver     `json:"driver"`
	Create     probeLocalTUNInstallObservationCreate     `json:"create"`
	Visibility probeLocalTUNInstallObservationVisibility `json:"visibility"`
	Final      probeLocalTUNInstallObservationFinal      `json:"final"`
	Diagnostic probeLocalTUNInstallObservationDiagnostic `json:"diagnostic"`
}

func newProbeLocalTUNInstallObservation() probeLocalTUNInstallObservation {
	return probeLocalTUNInstallObservation{}
}

func normalizeProbeLocalTUNInstallObservation(obs probeLocalTUNInstallObservation) probeLocalTUNInstallObservation {
	obs.Driver.PackagePath = strings.TrimSpace(obs.Driver.PackagePath)
	obs.Create.RawError = strings.TrimSpace(obs.Create.RawError)
	obs.Final.ReasonCode = strings.TrimSpace(obs.Final.ReasonCode)
	obs.Final.Reason = strings.TrimSpace(obs.Final.Reason)
	obs.Diagnostic.Code = strings.TrimSpace(obs.Diagnostic.Code)
	obs.Diagnostic.RawError = strings.TrimSpace(obs.Diagnostic.RawError)
	return obs
}

func cloneProbeLocalTUNInstallObservationPointer(observation *probeLocalTUNInstallObservation) *probeLocalTUNInstallObservation {
	if observation == nil {
		return nil
	}
	clone := normalizeProbeLocalTUNInstallObservation(*observation)
	return &clone
}

var probeLocalTUNInstallObservationRuntimeState = struct {
	mu          sync.RWMutex
	observation *probeLocalTUNInstallObservation
}{}

func setProbeLocalTUNInstallObservation(observation probeLocalTUNInstallObservation) {
	normalized := normalizeProbeLocalTUNInstallObservation(observation)
	probeLocalTUNInstallObservationRuntimeState.mu.Lock()
	probeLocalTUNInstallObservationRuntimeState.observation = &normalized
	probeLocalTUNInstallObservationRuntimeState.mu.Unlock()
}

func currentProbeLocalTUNInstallObservation() (probeLocalTUNInstallObservation, bool) {
	probeLocalTUNInstallObservationRuntimeState.mu.RLock()
	observation := probeLocalTUNInstallObservationRuntimeState.observation
	probeLocalTUNInstallObservationRuntimeState.mu.RUnlock()
	if observation == nil {
		return probeLocalTUNInstallObservation{}, false
	}
	return normalizeProbeLocalTUNInstallObservation(*observation), true
}

func clearProbeLocalTUNInstallObservation() {
	probeLocalTUNInstallObservationRuntimeState.mu.Lock()
	probeLocalTUNInstallObservationRuntimeState.observation = nil
	probeLocalTUNInstallObservationRuntimeState.mu.Unlock()
}

type probeLocalTUNInstallDiagnostic struct {
	Code     string   `json:"code,omitempty"`
	RawError string   `json:"raw_error,omitempty"`
	Stage    string   `json:"stage,omitempty"`
	Hint     string   `json:"hint,omitempty"`
	Details  string   `json:"details,omitempty"`
	Steps    []string `json:"steps,omitempty"`
}

type probeLocalTUNInstallError struct {
	Diagnostic  probeLocalTUNInstallDiagnostic
	Observation *probeLocalTUNInstallObservation
	cause       error
}

func (e *probeLocalTUNInstallError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Diagnostic.Details) != "" {
		return strings.TrimSpace(e.Diagnostic.Details)
	}
	if e.cause != nil {
		return strings.TrimSpace(e.cause.Error())
	}
	if strings.TrimSpace(e.Diagnostic.Hint) != "" {
		return strings.TrimSpace(e.Diagnostic.Hint)
	}
	return "tun install failed"
}

func (e *probeLocalTUNInstallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *probeLocalTUNInstallError) InstallObservation() (probeLocalTUNInstallObservation, bool) {
	if e == nil || e.Observation == nil {
		return probeLocalTUNInstallObservation{}, false
	}
	return normalizeProbeLocalTUNInstallObservation(*e.Observation), true
}

func newProbeLocalTUNInstallError(code string, stage string, hint string, cause error, steps []string, observations ...probeLocalTUNInstallObservation) *probeLocalTUNInstallError {
	rawError := ""
	if cause != nil {
		rawError = strings.TrimSpace(cause.Error())
	}
	installErr := &probeLocalTUNInstallError{
		Diagnostic: probeLocalTUNInstallDiagnostic{
			Code:     strings.TrimSpace(code),
			RawError: rawError,
			Stage:    strings.TrimSpace(stage),
			Hint:     strings.TrimSpace(hint),
			Details:  rawError,
			Steps:    cloneProbeLocalTUNInstallSteps(steps),
		},
		cause: cause,
	}
	if len(observations) > 0 {
		observation := normalizeProbeLocalTUNInstallObservation(observations[0])
		if observation.Final.Success {
			observation.Final.Success = false
		}
		if observation.Final.ReasonCode == "" {
			observation.Final.ReasonCode = strings.TrimSpace(code)
		}
		if observation.Final.Reason == "" {
			observation.Final.Reason = strings.TrimSpace(hint)
		}
		if observation.Diagnostic.Code == "" {
			observation.Diagnostic.Code = strings.TrimSpace(code)
		}
		if observation.Diagnostic.RawError == "" {
			observation.Diagnostic.RawError = rawError
		}
		setProbeLocalTUNInstallObservation(observation)
		installErr.Observation = cloneProbeLocalTUNInstallObservationPointer(&observation)
	}
	return installErr
}

func cloneProbeLocalTUNInstallSteps(steps []string) []string {
	if len(steps) == 0 {
		return nil
	}
	out := make([]string, 0, len(steps))
	for _, step := range steps {
		clean := strings.TrimSpace(step)
		if clean == "" {
			continue
		}
		out = append(out, clean)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
