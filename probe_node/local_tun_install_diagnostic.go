package main

import "strings"

const (
	probeLocalTUNInstallCodeUnsupported          = "TUN_UNSUPPORTED"
	probeLocalTUNInstallCodeWintunLibraryMissing = "TUN_WINTUN_LIBRARY_MISSING"
	probeLocalTUNInstallCodeElevationRequired    = "TUN_ELEVATION_REQUIRED"
	probeLocalTUNInstallCodeElevationTimeout     = "TUN_ELEVATION_TIMEOUT"
	probeLocalTUNInstallCodeAdapterCreateFailed  = "TUN_ADAPTER_CREATE_FAILED"
	probeLocalTUNInstallCodeAdapterNotDetected   = "TUN_ADAPTER_NOT_DETECTED"
	probeLocalTUNInstallCodeRouteTargetFailed    = "TUN_ROUTE_TARGET_FAILED"
	probeLocalTUNInstallCodeIfIndexInvalid       = "TUN_IFINDEX_INVALID"
)

type probeLocalTUNInstallDiagnostic struct {
	Code    string   `json:"code,omitempty"`
	Stage   string   `json:"stage,omitempty"`
	Hint    string   `json:"hint,omitempty"`
	Details string   `json:"details,omitempty"`
	Steps   []string `json:"steps,omitempty"`
}

type probeLocalTUNInstallError struct {
	Diagnostic probeLocalTUNInstallDiagnostic
	cause      error
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

func newProbeLocalTUNInstallError(code string, stage string, hint string, cause error, steps []string) *probeLocalTUNInstallError {
	details := ""
	if cause != nil {
		details = strings.TrimSpace(cause.Error())
	}
	return &probeLocalTUNInstallError{
		Diagnostic: probeLocalTUNInstallDiagnostic{
			Code:    strings.TrimSpace(code),
			Stage:   strings.TrimSpace(stage),
			Hint:    strings.TrimSpace(hint),
			Details: details,
			Steps:   cloneProbeLocalTUNInstallSteps(steps),
		},
		cause: cause,
	}
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
