package main

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	probeBuildKindNormal      = "normal"
	probeBuildKindMihomoExit  = "mihomo_exit"
	probeBuildKindLinuxRouter = "linux_router"
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
	EnableProductLocalWeb         bool
	EnableLocalProxy              bool
	EnableSystemDNS               bool
	EnableSyncScheduler           bool
	EnableDDNSScheduler           bool
	EnableLocalTUNStartupRecovery bool
	EnableVRoutePlatformInterface bool
	EnableVRouteTakeoverRoutes    bool
	LinuxAMD64Only                bool
	LinuxAMD64OrARM64Only         bool
}

var (
	probeProductLocalWebStart = func(string) error { return nil }
	probeProductLocalWebStop  = func() {}
)

type probeProductUpgradeCompanion struct {
	CandidatePath string
	TargetPath    string
	BackupPath    string
}

var activeProbeProductProfile = buildProbeProductProfile()

var probeProductLinuxRouterReport = func() probeLinuxRouterRuntimeReport {
	return probeLinuxRouterRuntimeReport{}
}

var probeLinuxRouterRouteConfigApplier = func(snapshot *probeLinuxRouterSnapshot, nodeID string) error {
	return nil
}

var probeProductAllowsForwardedTUNPacket = func(packet []byte, dstIP string, path []string) bool {
	return false
}

var probeProductHandleDirectTUNPacket = func(packet []byte, dstIP string) bool {
	return false
}

var probeProductTargetsLocalDelivery = func(dstIP string) bool {
	return false
}

func applyProbeLinuxRouterRouteConfig(snapshot *probeLinuxRouterSnapshot, nodeID string) error {
	return probeLinuxRouterRouteConfigApplier(snapshot, nodeID)
}

func probeProductVRouteTakeoverEnabled() bool {
	return activeProbeProductProfile.EnableVRouteTakeoverRoutes
}

func currentProbeBuildKind() string {
	return strings.TrimSpace(activeProbeProductProfile.BuildKind)
}

func validateProbeProductPlatform(goos string, goarch string) error {
	if !activeProbeProductProfile.LinuxAMD64Only && !activeProbeProductProfile.LinuxAMD64OrARM64Only {
		return nil
	}
	osName := strings.ToLower(strings.TrimSpace(goos))
	arch := strings.ToLower(strings.TrimSpace(goarch))
	if activeProbeProductProfile.LinuxAMD64Only && osName == "linux" && arch == "amd64" {
		return nil
	}
	if activeProbeProductProfile.LinuxAMD64OrARM64Only && osName == "linux" && (arch == "amd64" || arch == "arm64") {
		return nil
	}
	if activeProbeProductProfile.LinuxAMD64Only {
		return errors.New("mihomo exit probe supports linux amd64 only")
	}
	return errors.New("linux router probe supports linux amd64 and arm64 only")
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
