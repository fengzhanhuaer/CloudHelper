package main

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	probeBuildKindNormal     = "normal"
	probeBuildKindMihomoExit = "mihomo_exit"
)

type probeProductProfile struct {
	BuildKind                     string
	ServiceName                   string
	UpgradeAssetPrefix            string
	RuntimeLogDir                 string
	RuntimeLogFile                string
	DataDir                       string
	TempDir                       string
	AllowLocalTUNInstall          bool
	EnableLocalConsole            bool
	EnableLocalProxy              bool
	EnableSystemDNS               bool
	EnableSyncScheduler           bool
	EnableDDNSScheduler           bool
	EnableLocalTUNStartupRecovery bool
	EnableVRoutePlatformInterface bool
	LinuxAMD64Only                bool
}

type probeProductUpgradeCompanion struct {
	CandidatePath string
	TargetPath    string
	BackupPath    string
}

var activeProbeProductProfile = buildProbeProductProfile()

func currentProbeBuildKind() string {
	return strings.TrimSpace(activeProbeProductProfile.BuildKind)
}

func validateProbeProductPlatform(goos string, goarch string) error {
	if !activeProbeProductProfile.LinuxAMD64Only {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(goos), "linux") && strings.EqualFold(strings.TrimSpace(goarch), "amd64") {
		return nil
	}
	return errors.New("mihomo exit probe supports linux amd64 only")
}

func validateProbeExpectedNodeKind(expected string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected == "" || expected == currentProbeBuildKind() {
		return nil
	}
	return errors.New("probe build kind does not match controller expectation")
}

func resolveProbeProductWorkingPath(name string) (string, error) {
	cleanName := strings.TrimSpace(name)
	if cleanName == "" {
		return "", errors.New("product working path is empty")
	}
	return filepath.Abs(filepath.Join(".", cleanName))
}

func probeProductUpgradeWorkspacePrefix() string {
	prefix := strings.TrimSpace(activeProbeProductProfile.UpgradeAssetPrefix)
	if prefix == "" {
		prefix = "cloudhelper-probe-node"
	}
	return prefix + "-upgrade-"
}

func probeProductPlatformError() error {
	return validateProbeProductPlatform(runtime.GOOS, runtime.GOARCH)
}
