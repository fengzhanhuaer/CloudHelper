package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	runtimepprof "runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	probeLocalListenAddrDefault = "127.0.0.1:16032"
	probeLocalListenDefaultHost = "127.0.0.1"
	probeLocalListenDefaultPort = 16032

	probeLocalAuthStoreFile      = "probe_local_auth.json"
	probeLocalSessionCookieName  = "probe_local_session"
	probeLocalSessionTTL         = 8 * time.Hour
	probeLocalLoginFailThreshold = 5
	probeLocalLoginFreezeTTL     = 5 * time.Minute
	probeLocalMinPasswordLength  = 8
	probeLocalMaxPasswordLength  = 128
	probeLocalMaxUsernameLength  = 64
	probeLocalAuthReadBodyMaxLen = 64 * 1024

	probeLocalTUNStateFileName       = "tun_state.json"
	probeLocalTUNEgressStateFileName = "tun_egress.json"
	probeLocalDNSHostFileName        = "dns_host.txt"
	probeLocalRouteReadBodyMaxLen    = 512 * 1024
)

type probeLocalAuthState struct {
	Registered   bool   `json:"registered"`
	Username     string `json:"username,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
	PasswordSalt string `json:"password_salt,omitempty"`
	PasswordType string `json:"password_type,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
	// ListenIP / ListenPort configure the local console (本地界面) listen address.
	// They are read at startup; defaults are written into probe_local_auth.json so
	// they can be edited manually. ListenIP may be a non-loopback address (e.g.
	// 0.0.0.0 or a LAN IP) to expose the local UI on the network.
	ListenIP   string `json:"listen_ip,omitempty"`
	ListenPort int    `json:"listen_port,omitempty"`
}

type probeLocalSessionState struct {
	Username  string
	ExpiresAt time.Time
}

type probeLocalAuthManager struct {
	mu sync.RWMutex

	state       probeLocalAuthState
	sessions    map[string]probeLocalSessionState
	loginFailed map[string]int
	loginFrozen map[string]time.Time
}

type probeLocalHTTPError struct {
	Status  int
	Message string
	Payload map[string]any
}

func (e *probeLocalHTTPError) Error() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.Message)
}

type probeLocalTunRuntimeState struct {
	Platform               string                           `json:"platform"`
	Installed              bool                             `json:"installed"`
	Enabled                bool                             `json:"enabled"`
	DataPlane              bool                             `json:"data_plane"`
	DataPlaneRX            uint64                           `json:"data_plane_rx_packets,omitempty"`
	DataPlaneBytes         uint64                           `json:"data_plane_rx_bytes,omitempty"`
	DataPlaneTX            uint64                           `json:"data_plane_tx_packets,omitempty"`
	DataPlaneTXBytes       uint64                           `json:"data_plane_tx_bytes,omitempty"`
	LastError              string                           `json:"last_error,omitempty"`
	RecoveryStatus         string                           `json:"recovery_status,omitempty"`
	RecoveryAttempts       int                              `json:"recovery_attempts,omitempty"`
	RecoveryLastError      string                           `json:"recovery_last_error,omitempty"`
	RecoveryNextAt         string                           `json:"recovery_next_at,omitempty"`
	RecoveryUpdatedAt      string                           `json:"recovery_updated_at,omitempty"`
	InstallObservation     *probeLocalTUNInstallObservation `json:"install_observation,omitempty"`
	LastInstallObservation *probeLocalTUNInstallObservation `json:"last_install_observation,omitempty"`
	UpdatedAt              string                           `json:"updated_at,omitempty"`
}

type probeLocalRouteRuntimeState struct {
	Enabled   bool   `json:"enabled"`
	Mode      string `json:"mode"`
	LastError string `json:"last_error,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type probeLocalControlManager struct {
	mu  sync.RWMutex
	tun probeLocalTunRuntimeState
}

type probeLocalTUNPersistentState struct {
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type probeLocalTUNEgressPersistentState struct {
	Mode           string `json:"mode,omitempty"`
	CandidateID    string `json:"candidate_id,omitempty"`
	InterfaceIndex int    `json:"interface_index,omitempty"`
	InterfaceLUID  uint64 `json:"interface_luid,omitempty"`
	InterfaceGUID  string `json:"interface_guid,omitempty"`
	NextHop        string `json:"next_hop,omitempty"`
	Name           string `json:"name,omitempty"`
	Description    string `json:"description,omitempty"`
	Label          string `json:"label,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

type probeLocalTUNStateFile struct {
	Version   int                          `json:"version"`
	UpdatedAt string                       `json:"updated_at"`
	TUN       probeLocalTUNPersistentState `json:"tun"`
}

type probeLocalTUNEgressStateFile struct {
	Version   int                                `json:"version"`
	UpdatedAt string                             `json:"updated_at"`
	TUNEgress probeLocalTUNEgressPersistentState `json:"tun_egress"`
}

type probeLocalHostMapping struct {
	DNS string `json:"dns"`
	IP  string `json:"ip"`
}

type probeLocalRouteRuntimeContext struct {
	Identity          nodeIdentity
	ControllerBaseURL string
}

type probeLocalUpgradeRuntimeState struct {
	Status          string `json:"status"`
	Step            string `json:"step,omitempty"`
	Progress        int    `json:"progress"`
	Message         string `json:"message,omitempty"`
	Error           string `json:"error,omitempty"`
	Mode            string `json:"mode,omitempty"`
	ReleaseRepo     string `json:"release_repo,omitempty"`
	DownloadedBytes int64  `json:"downloaded_bytes,omitempty"`
	TotalBytes      int64  `json:"total_bytes,omitempty"`
	SpeedBPS        int64  `json:"speed_bps,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

type probeLocalUpgradeCheckResult struct {
	OK             bool   `json:"ok"`
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version,omitempty"`
	Upgradeable    bool   `json:"upgradeable"`
	Mode           string `json:"mode,omitempty"`
	ReleaseRepo    string `json:"release_repo,omitempty"`
	AssetName      string `json:"asset_name,omitempty"`
	AssetError     string `json:"asset_error,omitempty"`
	CheckedAt      string `json:"checked_at"`
}

type probeLocalRouteAuthBlacklistSaveRequest struct {
	Content string   `json:"content"`
	IPs     []string `json:"ips,omitempty"`
}

func probeLocalNoopPostInstallTUNReadyCheck() error {
	return nil
}

func defaultProbeLocalDetectTUNInstalled() (bool, error) {
	switch runtime.GOOS {
	case "linux":
		info, err := os.Stat("/dev/net/tun")
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
		return info != nil && !info.IsDir(), nil
	case "windows":
		return false, errProbeLocalTUNUnsupported
	default:
		return false, fmt.Errorf("%w: %s", errProbeLocalTUNUnsupported, runtime.GOOS)
	}
}

var (
	errProbeLocalTUNRouteFeaturePaused    = errors.New("probe local TUN route feature is paused")
	errProbeLocalTUNUnsupported           = errors.New("probe local tun install is not supported on this platform")
	probeLocalTUNRouteFeatureEnabled      = func() bool { return false }
	probeLocalInstallTUNDriver            = installProbeLocalTUNDriver
	probeLocalCheckTUNReadyAfterInstall   = probeLocalNoopPostInstallTUNReadyCheck
	probeLocalDetectTUNInstalled          = defaultProbeLocalDetectTUNInstalled
	probeLocalResetTUNDetectInstalledHook = func() { probeLocalDetectTUNInstalled = defaultProbeLocalDetectTUNInstalled }
	probeLocalUninstallTUNDriver          = uninstallProbeLocalTUNDriver
	probeLocalRouteRelaySpeedDebugFetch   = probeRouteRelayFetchSpeedDebugDefault
	probeLocalStartCFIPOptimizeTask       = func(fn func()) { go fn() }
	probeLocalRunUpgrade                  = runProbeUpgrade
	probeLocalFetchRelease                = fetchProbeRelease
	probeLocalRestartProcess              = restartCurrentProcess
	probeLocalLookupIPv4ForBypass         = lookupProbeLocalIPv4ForBypass
)

func probeLocalTUNRouteFeatureActive() bool {
	return probeLocalTUNRouteFeatureEnabled != nil && probeLocalTUNRouteFeatureEnabled()
}

var probeLocalConsoleRefreshState = struct {
	mu             sync.Mutex
	running        bool
	lastStartedAt  string
	lastFinishedAt string
	lastError      string
}{}

func newProbeLocalControlManager() *probeLocalControlManager {
	now := time.Now().UTC().Format(time.RFC3339)
	return &probeLocalControlManager{
		tun: probeLocalTunRuntimeState{
			Platform:  runtime.GOOS,
			Installed: false,
			Enabled:   false,
			UpdatedAt: now,
		},
	}
}

func lookupProbeLocalIPv4ForBypass(host string) ([]string, error) {
	ips, err := resolveProbeLocalDNSIPv4s(strings.TrimSpace(host))
	if err != nil {
		return nil, err
	}
	return dedupeProbeLocalBypassIPv4Strings(ips), nil
}

func dedupeProbeLocalBypassIPv4Strings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, raw := range items {
		ip4 := net.ParseIP(strings.TrimSpace(raw)).To4()
		if ip4 == nil {
			continue
		}
		value := ip4.String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func expandProbeLocalBootstrapBypassTargets(targets []string) ([]string, error) {
	expanded := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, rawTarget := range targets {
		host, rawPort, err := net.SplitHostPort(strings.TrimSpace(rawTarget))
		if err != nil {
			return nil, fmt.Errorf("split bypass target failed: target=%s err=%w", strings.TrimSpace(rawTarget), err)
		}
		host = strings.TrimSpace(strings.Trim(host, "[]"))
		if host == "" || strings.TrimSpace(rawPort) == "" {
			return nil, fmt.Errorf("invalid bypass target: %s", strings.TrimSpace(rawTarget))
		}
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			target := net.JoinHostPort(ip.To4().String(), rawPort)
			if _, ok := seen[target]; ok {
				continue
			}
			seen[target] = struct{}{}
			expanded = append(expanded, target)
			continue
		}
		ipv4List, lookupErr := probeLocalLookupIPv4ForBypass(host)
		if lookupErr != nil {
			return nil, fmt.Errorf("resolve bypass host failed: host=%s err=%w", host, lookupErr)
		}
		if len(ipv4List) == 0 {
			return nil, fmt.Errorf("resolve bypass host has no ipv4 result: host=%s", host)
		}
		for _, ipText := range ipv4List {
			target := net.JoinHostPort(ipText, rawPort)
			if _, ok := seen[target]; ok {
				continue
			}
			seen[target] = struct{}{}
			expanded = append(expanded, target)
		}
	}
	return expanded, nil
}

func (m *probeLocalControlManager) tunStatus() probeLocalTunRuntimeState {
	var status probeLocalTunRuntimeState
	if m.mu.TryRLock() {
		status = m.tun
		m.mu.RUnlock()
	} else {
		now := time.Now().UTC().Format(time.RFC3339)
		status.Platform = runtime.GOOS
		status.RecoveryStatus = "running"
		status.RecoveryLastError = "tun control is busy"
		status.RecoveryUpdatedAt = now
		status.UpdatedAt = now
		if state, err := loadProbeLocalTUNStateFile(); err == nil {
			status.Installed = state.TUN.Installed
			status.Enabled = state.TUN.Enabled
			status.UpdatedAt = firstNonEmpty(strings.TrimSpace(state.TUN.UpdatedAt), strings.TrimSpace(state.UpdatedAt), now)
		}
		if observation, ok := currentProbeLocalTUNInstallObservation(); ok {
			status.LastInstallObservation = cloneProbeLocalTUNInstallObservationPointer(&observation)
			if observation.Final.Success {
				status.Installed = true
			}
		}
		if status.UpdatedAt == "" {
			status.UpdatedAt = now
		}
		if status.Platform == "" {
			status.Platform = runtime.GOOS
		}
	}
	stats := probeVirtualRouterTUNDataPlaneStatsSnapshot()
	if !status.Installed && stats.Running {
		status.Installed = true
	}
	if !status.Enabled && stats.Running {
		status.Enabled = true
	}
	status.DataPlane = stats.Running
	status.DataPlaneRX = stats.RXPackets
	status.DataPlaneBytes = stats.RXBytes
	status.DataPlaneTX = stats.TXPackets
	status.DataPlaneTXBytes = stats.TXBytes
	return status
}

func markProbeLocalTUNInterfaceReady() {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	probeLocalControl.mu.Lock()
	changed := !probeLocalControl.tun.Installed || !probeLocalControl.tun.Enabled || probeLocalControl.tun.LastError != ""
	probeLocalControl.tun.Installed = true
	probeLocalControl.tun.Enabled = true
	probeLocalControl.tun.LastError = ""
	probeLocalControl.tun.UpdatedAt = now
	probeLocalControl.mu.Unlock()
	if changed {
		persistProbeLocalTUNStateBestEffort(true, true)
	}
}

func markProbeLocalTUNDataPlaneStopped() {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	probeLocalControl.mu.Lock()
	installed := probeLocalControl.tun.Installed
	changed := probeLocalControl.tun.Enabled || probeLocalControl.tun.DataPlane
	probeLocalControl.tun.Enabled = false
	probeLocalControl.tun.DataPlane = false
	probeLocalControl.tun.DataPlaneRX = 0
	probeLocalControl.tun.DataPlaneBytes = 0
	probeLocalControl.tun.DataPlaneTX = 0
	probeLocalControl.tun.DataPlaneTXBytes = 0
	probeLocalControl.tun.UpdatedAt = now
	probeLocalControl.mu.Unlock()
	if changed {
		persistProbeLocalTUNStateBestEffort(installed, false)
	}
}

func persistProbeLocalTUNStateBestEffort(installed, enabled bool) {
	if err := persistProbeLocalTUNPersistentState(installed, enabled); err != nil {
		logProbeWarnf("probe local tun persist state failed: installed=%v enabled=%v err=%v", installed, enabled, err)
	}
}

func (m *probeLocalControlManager) setTUNRecoveryStatus(status string, attempt int, nextAt time.Time, errText string) {
	status = strings.TrimSpace(strings.ToLower(status))
	errText = strings.TrimSpace(errText)
	now := time.Now().UTC().Format(time.RFC3339)
	nextText := ""
	if !nextAt.IsZero() {
		nextText = nextAt.UTC().Format(time.RFC3339)
	}
	m.mu.Lock()
	m.tun.RecoveryStatus = status
	if attempt > 0 {
		m.tun.RecoveryAttempts = attempt
	}
	m.tun.RecoveryLastError = errText
	m.tun.RecoveryNextAt = nextText
	m.tun.RecoveryUpdatedAt = now
	if errText != "" {
		m.tun.LastError = errText
	}
	m.tun.UpdatedAt = now
	m.mu.Unlock()
}

func (m *probeLocalControlManager) shouldRecoverTUNOnStartup() bool {
	state, err := loadProbeLocalTUNStateFile()
	if err != nil {
		return false
	}
	if !shouldRestoreProbeLocalTUNFromState(state.TUN) {
		return false
	}
	status := m.tunStatus()
	return !status.Installed || !status.Enabled
}

func recoverProbeLocalTUNRuntimeOnStartup() error {
	return probeLocalControl.recoverTUNOnStartup(1)
}

func startProbeLocalTUNStartupRecoveryAsync() {
	if !probeLocalControl.shouldRecoverTUNOnStartup() {
		return
	}
	probeLocalTUNStartupRecoveryLoopState.mu.Lock()
	if probeLocalTUNStartupRecoveryLoopState.running {
		probeLocalTUNStartupRecoveryLoopState.mu.Unlock()
		return
	}
	probeLocalTUNStartupRecoveryLoopState.running = true
	probeLocalTUNStartupRecoveryLoopState.mu.Unlock()

	go func() {
		if err := probeLocalControl.recoverTUNOnStartup(1); err != nil {
			logProbeWarnf("probe local tun startup recovery skipped: %v", err)
			probeLocalTUNStartupRecoveryLoopState.mu.Lock()
			probeLocalTUNStartupRecoveryLoopState.running = false
			probeLocalTUNStartupRecoveryLoopState.mu.Unlock()
			startProbeLocalTUNStartupRecoveryLoop()
			return
		}
		probeLocalTUNStartupRecoveryLoopState.mu.Lock()
		probeLocalTUNStartupRecoveryLoopState.running = false
		probeLocalTUNStartupRecoveryLoopState.mu.Unlock()
	}()
}

func startProbeLocalTUNStartupRecoveryLoop() {
	if !probeLocalControl.shouldRecoverTUNOnStartup() {
		return
	}
	probeLocalTUNStartupRecoveryLoopState.mu.Lock()
	if probeLocalTUNStartupRecoveryLoopState.running {
		probeLocalTUNStartupRecoveryLoopState.mu.Unlock()
		return
	}
	probeLocalTUNStartupRecoveryLoopState.running = true
	probeLocalTUNStartupRecoveryLoopState.mu.Unlock()

	go func() {
		defer func() {
			probeLocalTUNStartupRecoveryLoopState.mu.Lock()
			probeLocalTUNStartupRecoveryLoopState.running = false
			probeLocalTUNStartupRecoveryLoopState.mu.Unlock()
		}()
		delays := []time.Duration{
			5 * time.Second,
			10 * time.Second,
			20 * time.Second,
			30 * time.Second,
			45 * time.Second,
			60 * time.Second,
			90 * time.Second,
			120 * time.Second,
		}
		for i, delay := range delays {
			attempt := i + 2
			nextAt := time.Now().Add(delay)
			probeLocalControl.setTUNRecoveryStatus("waiting", attempt, nextAt, "")
			logProbeInfof("probe local tun startup recovery retry scheduled: attempt=%d delay=%s", attempt, delay.String())
			time.Sleep(delay)
			if !probeLocalControl.shouldRecoverTUNOnStartup() {
				probeLocalControl.setTUNRecoveryStatus("idle", attempt, time.Time{}, "")
				return
			}
			if err := probeLocalControl.recoverTUNOnStartup(attempt); err != nil {
				logProbeWarnf("probe local tun startup recovery retry failed: attempt=%d err=%v", attempt, err)
				continue
			}
			logProbeInfof("probe local tun startup recovery retry succeeded: attempt=%d", attempt)
			return
		}
		status := probeLocalControl.tunStatus()
		errText := strings.TrimSpace(status.RecoveryLastError)
		if errText == "" {
			errText = strings.TrimSpace(status.LastError)
		}
		if errText == "" {
			errText = "tun startup recovery exhausted retry attempts"
		}
		probeLocalControl.setTUNRecoveryStatus("failed", len(delays)+1, time.Time{}, errText)
		logProbeWarnf("probe local tun startup recovery exhausted: attempts=%d err=%s", len(delays)+1, errText)
	}()
}

func recoverProbeLocalTUNRuntimeAfterRouteConfigSync() {
}

func (m *probeLocalControlManager) recoverTUNOnStartup(attempt int) error {
	if attempt <= 0 {
		attempt = 1
	}
	m.setTUNRecoveryStatus("running", attempt, time.Time{}, "")
	logProbeInfof("probe local tun startup recovery attempt started: attempt=%d", attempt)
	state, err := loadProbeLocalTUNStateFile()
	if err != nil {
		m.setTUNRecoveryStatus("failed", attempt, time.Time{}, strings.TrimSpace(err.Error()))
		return err
	}
	applyProbeLocalTUNEgressPersistentState(currentProbeLocalTUNEgressPersistentStateBestEffort())

	detectedInstalled, detectErr := probeLocalDetectTUNInstalled()
	if detectErr != nil && !errors.Is(detectErr, errProbeLocalTUNUnsupported) {
		logProbeWarnf("probe local tun startup detect failed: %v", detectErr)
	}
	installed := detectedInstalled && detectErr == nil
	restoreTUN := shouldRestoreProbeLocalTUNFromState(state.TUN)
	now := time.Now().UTC().Format(time.RFC3339)
	installErrText := ""

	if restoreTUN && !errors.Is(detectErr, errProbeLocalTUNUnsupported) {
		logProbeWarnf("probe local tun startup recovery will run install/check: persisted_installed=%v persisted_enabled=%v detected_installed=%v detect_err=%v", state.TUN.Installed, state.TUN.Enabled, detectedInstalled, detectErr)
		if _, installErr := m.installTUN(); installErr != nil {
			installErrText = strings.TrimSpace(installErr.Error())
			logProbeWarnf("probe local tun startup install/check recovery failed: %v", installErr)
		}
		detectedInstalled, detectErr = probeLocalDetectTUNInstalled()
		if detectErr != nil && !errors.Is(detectErr, errProbeLocalTUNUnsupported) {
			logProbeWarnf("probe local tun startup redetect after install/check failed: %v", detectErr)
		}
		installed = detectedInstalled && detectErr == nil
		now = time.Now().UTC().Format(time.RFC3339)
	}

	m.mu.Lock()
	m.tun.Platform = runtime.GOOS
	m.tun.Installed = installed
	m.tun.Enabled = restoreTUN
	m.tun.DataPlane = false
	m.tun.DataPlaneRX = 0
	m.tun.DataPlaneBytes = 0
	m.tun.DataPlaneTX = 0
	m.tun.DataPlaneTXBytes = 0
	if installed {
		m.tun.LastError = ""
	} else if strings.TrimSpace(m.tun.LastError) == "" && detectErr != nil && !errors.Is(detectErr, errProbeLocalTUNUnsupported) {
		m.tun.LastError = strings.TrimSpace(detectErr.Error())
	} else if installErrText != "" {
		m.tun.LastError = installErrText
	} else if !detectedInstalled && state.TUN.Installed {
		m.tun.LastError = "tun adapter is not available after startup detection"
	}
	m.tun.UpdatedAt = now
	m.mu.Unlock()

	if installed != state.TUN.Installed {
		persistProbeLocalTUNStateBestEffort(installed, restoreTUN)
	}
	if !installed {
		errText := strings.TrimSpace(m.tunStatus().LastError)
		if errText == "" {
			errText = "tun adapter is not available after startup recovery"
		}
		err := errors.New(errText)
		m.setTUNRecoveryStatus("failed", attempt, time.Time{}, errText)
		logProbeWarnf("probe local tun startup recovery attempt failed: attempt=%d err=%v", attempt, err)
		return err
	}
	if !restoreTUN {
		if installed {
			logProbeInfof("probe local tun startup recovered installed state")
		}
		m.setTUNRecoveryStatus("idle", attempt, time.Time{}, "")
		return nil
	}

	now = time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	m.tun.Installed = installed
	m.tun.Enabled = restoreTUN
	m.tun.DataPlane = false
	m.tun.DataPlaneRX = 0
	m.tun.DataPlaneBytes = 0
	m.tun.DataPlaneTX = 0
	m.tun.DataPlaneTXBytes = 0
	m.tun.LastError = ""
	m.tun.UpdatedAt = now
	m.mu.Unlock()
	persistProbeLocalTUNStateBestEffort(installed, restoreTUN)
	m.setTUNRecoveryStatus("recovered", attempt, time.Time{}, "")
	logProbeInfof("probe local tun startup recovered adapter only: persisted_tun_enabled=%v", restoreTUN)
	return nil
}

func (m *probeLocalControlManager) installTUN() (probeLocalTunRuntimeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	startedAt := time.Now()
	logProbeInfof("probe local tun install/check started: platform=%s", runtime.GOOS)
	if err := probeLocalInstallTUNDriver(); err != nil {
		m.tun.LastError = strings.TrimSpace(err.Error())
		var installErr *probeLocalTUNInstallError
		if errors.As(err, &installErr) && installErr != nil {
			if len(installErr.Diagnostic.Steps) > 0 {
				logProbeWarnf("probe local tun install diagnostic steps: %s", strings.Join(installErr.Diagnostic.Steps, " | "))
			}
			logProbeErrorf(
				"probe local tun install/check failed: code=%s stage=%s hint=%s details=%s",
				strings.TrimSpace(installErr.Diagnostic.Code),
				strings.TrimSpace(installErr.Diagnostic.Stage),
				strings.TrimSpace(installErr.Diagnostic.Hint),
				strings.TrimSpace(installErr.Diagnostic.Details),
			)
		} else {
			logProbeErrorf("probe local tun install/check failed: %v", err)
		}
		logProbeWarnf("probe local tun install/check failed elapsed=%s", time.Since(startedAt).String())
		if observation, ok := currentProbeLocalTUNInstallObservation(); ok {
			m.tun.InstallObservation = cloneProbeLocalTUNInstallObservationPointer(&observation)
			m.tun.LastInstallObservation = cloneProbeLocalTUNInstallObservationPointer(&observation)
		} else {
			fallbackObservation := newProbeLocalTUNInstallObservation()
			fallbackObservation.Final.Success = false
			fallbackObservation.Final.ReasonCode = "TUN_INSTALL_FAILED"
			fallbackObservation.Final.Reason = m.tun.LastError
			fallbackObservation.Diagnostic.Code = "TUN_INSTALL_FAILED"
			fallbackObservation.Diagnostic.RawError = m.tun.LastError
			setProbeLocalTUNInstallObservation(fallbackObservation)
			m.tun.InstallObservation = cloneProbeLocalTUNInstallObservationPointer(&fallbackObservation)
			m.tun.LastInstallObservation = cloneProbeLocalTUNInstallObservationPointer(&fallbackObservation)
		}
		m.tun.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		status := http.StatusInternalServerError
		if errors.Is(err, errProbeLocalTUNUnsupported) {
			status = http.StatusNotImplemented
		}
		return m.tun, &probeLocalHTTPError{Status: status, Message: m.tun.LastError, Payload: buildProbeLocalTUNErrorPayload(err)}
	}

	if err := probeLocalCheckTUNReadyAfterInstall(); err != nil {
		wrappedErr := newProbeLocalTUNInstallError(
			probeLocalTUNInstallCodeRouteTargetFailed,
			"post_install_route_target_check",
			"TUN 网卡已安装但路由目标 IP 不可达，请检查网卡状态后重试",
			err,
			nil,
		)
		m.tun.LastError = strings.TrimSpace(wrappedErr.Error())
		if observation, ok := currentProbeLocalTUNInstallObservation(); ok {
			observation.Final.Success = false
			observation.Final.ReasonCode = probeLocalTUNInstallCodeRouteTargetFailed
			observation.Final.Reason = "TUN 网卡已安装但路由目标 IP 不可达，请检查网卡状态后重试"
			observation.Diagnostic.Code = probeLocalTUNInstallCodeRouteTargetFailed
			observation.Diagnostic.Stage = "post_install_route_target_check"
			observation.Diagnostic.Hint = "TUN 网卡已安装但路由目标 IP 不可达，请检查网卡状态后重试"
			observation.Diagnostic.RawError = strings.TrimSpace(err.Error())
			observation.Diagnostic.Details = strings.TrimSpace(err.Error())
			setProbeLocalTUNInstallObservation(observation)
			m.tun.InstallObservation = cloneProbeLocalTUNInstallObservationPointer(&observation)
			m.tun.LastInstallObservation = cloneProbeLocalTUNInstallObservationPointer(&observation)
		} else {
			fallbackObservation := newProbeLocalTUNInstallObservation()
			fallbackObservation.Final.Success = false
			fallbackObservation.Final.ReasonCode = probeLocalTUNInstallCodeRouteTargetFailed
			fallbackObservation.Final.Reason = "TUN 网卡已安装但路由目标 IP 不可达，请检查网卡状态后重试"
			fallbackObservation.Diagnostic.Code = probeLocalTUNInstallCodeRouteTargetFailed
			fallbackObservation.Diagnostic.Stage = "post_install_route_target_check"
			fallbackObservation.Diagnostic.Hint = "TUN 网卡已安装但路由目标 IP 不可达，请检查网卡状态后重试"
			fallbackObservation.Diagnostic.RawError = strings.TrimSpace(err.Error())
			fallbackObservation.Diagnostic.Details = strings.TrimSpace(err.Error())
			setProbeLocalTUNInstallObservation(fallbackObservation)
			m.tun.InstallObservation = cloneProbeLocalTUNInstallObservationPointer(&fallbackObservation)
			m.tun.LastInstallObservation = cloneProbeLocalTUNInstallObservationPointer(&fallbackObservation)
		}
		m.tun.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		logProbeWarnf(
			"probe local tun post-install ready check context: env_ifindex=%s env_gateway=%s env_dns=%s",
			strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_INDEX")),
			strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_GATEWAY")),
			strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_DNS_HOST")),
		)
		logProbeWarnf("probe local tun post-install ready check failed elapsed=%s err=%v", time.Since(startedAt).String(), err)
		return m.tun, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: m.tun.LastError, Payload: buildProbeLocalTUNErrorPayload(wrappedErr)}
	}
	if runtime.GOOS == "windows" {
		if err := startProbeVirtualRouterTUNDataPlane(); err != nil {
			wrappedErr := newProbeLocalTUNInstallError(
				probeLocalTUNInstallCodeRouteTargetFailed,
				"post_install_dataplane_start",
				"TUN 网卡已安装但数据面无法启动，请检查网卡状态后重试",
				err,
				nil,
			)
			m.tun.LastError = strings.TrimSpace(wrappedErr.Error())
			m.tun.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			logProbeWarnf("probe local tun post-install data plane start failed elapsed=%s err=%v", time.Since(startedAt).String(), err)
			return m.tun, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: m.tun.LastError, Payload: buildProbeLocalTUNErrorPayload(wrappedErr)}
		}
	}
	ensureProbeVirtualRouterLocalInterfaceIP()

	m.tun.Installed = true
	m.tun.Enabled = true
	m.tun.LastError = ""
	if observation, ok := currentProbeLocalTUNInstallObservation(); ok {
		m.tun.InstallObservation = cloneProbeLocalTUNInstallObservationPointer(&observation)
		m.tun.LastInstallObservation = cloneProbeLocalTUNInstallObservationPointer(&observation)
	} else {
		fallbackObservation := newProbeLocalTUNInstallObservation()
		fallbackObservation.Final.Success = true
		fallbackObservation.Final.ReasonCode = "TUN_INSTALL_SUCCEEDED"
		fallbackObservation.Final.Reason = "安装流程完成"
		setProbeLocalTUNInstallObservation(fallbackObservation)
		m.tun.InstallObservation = cloneProbeLocalTUNInstallObservationPointer(&fallbackObservation)
		m.tun.LastInstallObservation = cloneProbeLocalTUNInstallObservationPointer(&fallbackObservation)
	}
	stats := probeVirtualRouterTUNDataPlaneStatsSnapshot()
	m.tun.DataPlane = stats.Running
	m.tun.DataPlaneRX = stats.RXPackets
	m.tun.DataPlaneBytes = stats.RXBytes
	m.tun.DataPlaneTX = stats.TXPackets
	m.tun.DataPlaneTXBytes = stats.TXBytes
	m.tun.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	persistProbeLocalTUNStateBestEffort(true, true)
	logProbeInfof("probe local tun install/check completed: installed=true elapsed=%s", time.Since(startedAt).String())
	return m.tun, nil
}

func (m *probeLocalControlManager) resetTUN() (probeLocalTunRuntimeState, error) {
	return m.resetTUNLocked(false)
}

func (m *probeLocalControlManager) uninstallTUN() (probeLocalTunRuntimeState, error) {
	return m.resetTUNLocked(true)
}

func (m *probeLocalControlManager) resetTUNLocked(uninstall bool) (probeLocalTunRuntimeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var allErr error
	settings := loadProbeVirtualRouterLocalSettings()
	if settings.VirtualRouterEnabled || settings.VirtualDNSEnabled {
		settings.VirtualRouterEnabled = false
		settings.VirtualDNSEnabled = false
		if _, err := saveProbeVirtualRouterLocalSettingsForTUNReset(settings); err != nil {
			allErr = errors.Join(allErr, fmt.Errorf("disable virtual router settings: %w", err))
		}
	}
	stopProbeVirtualRouterDNSService()
	if err := probeVirtualRouterRestoreSystemDNS(); err != nil {
		allErr = errors.Join(allErr, fmt.Errorf("restore system dns: %w", err))
	}
	if err := cleanupProbeVirtualRouterPlatformRoutes(); err != nil {
		allErr = errors.Join(allErr, fmt.Errorf("cleanup virtual router platform routes: %w", err))
	}
	if err := stopProbeVirtualRouterTUNDataPlane(); err != nil {
		allErr = errors.Join(allErr, err)
	}
	if uninstall {
		if err := probeLocalUninstallTUNDriver(); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	installed := m.tun.Installed
	if uninstall && allErr == nil {
		installed = false
	} else if !uninstall {
		if detected, detectErr := probeLocalDetectTUNInstalled(); detectErr == nil {
			installed = detected
		} else if !errors.Is(detectErr, errProbeLocalTUNUnsupported) {
			allErr = errors.Join(allErr, detectErr)
		}
	}
	m.tun.Installed = installed
	m.tun.Enabled = false
	m.tun.DataPlane = false
	m.tun.DataPlaneRX = 0
	m.tun.DataPlaneBytes = 0
	m.tun.DataPlaneTX = 0
	m.tun.DataPlaneTXBytes = 0
	m.tun.UpdatedAt = now
	if allErr != nil {
		m.tun.LastError = strings.TrimSpace(allErr.Error())
		persistProbeLocalTUNStateBestEffort(m.tun.Installed, false)
		return m.tun, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: m.tun.LastError}
	}
	m.tun.LastError = ""
	persistProbeLocalTUNStateBestEffort(m.tun.Installed, false)
	return m.tun, nil
}

var (
	probeLocalAuthInitMu   sync.Mutex
	probeLocalAuthInstance *probeLocalAuthManager
	probeLocalControl      = newProbeLocalControlManager()
)

var probeLocalTUNStartupRecoveryLoopState = struct {
	mu      sync.Mutex
	running bool
}{}

var probeLocalRuntimeState = struct {
	mu      sync.RWMutex
	context probeLocalRouteRuntimeContext
}{}

var probeLocalUpgradeState = struct {
	mu    sync.RWMutex
	state probeLocalUpgradeRuntimeState
}{
	state: probeLocalUpgradeRuntimeState{
		Status:    "idle",
		Progress:  0,
		Message:   "尚未触发升级",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	},
}

var probeLocalConsoleState = struct {
	mu         sync.Mutex
	server     *http.Server
	listenAddr string
}{}

func ensureProbeLocalAuthManager() (*probeLocalAuthManager, error) {
	probeLocalAuthInitMu.Lock()
	defer probeLocalAuthInitMu.Unlock()

	if probeLocalAuthInstance != nil {
		return probeLocalAuthInstance, nil
	}

	state, err := loadProbeLocalAuthState()
	if err != nil {
		return nil, err
	}

	probeLocalAuthInstance = &probeLocalAuthManager{
		state:       state,
		sessions:    make(map[string]probeLocalSessionState),
		loginFailed: make(map[string]int),
		loginFrozen: make(map[string]time.Time),
	}
	return probeLocalAuthInstance, nil
}

func resolveProbeLocalAuthStorePath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalAuthStoreFile), nil
}

func loadProbeLocalAuthState() (probeLocalAuthState, error) {
	path, err := resolveProbeLocalAuthStorePath()
	if err != nil {
		return probeLocalAuthState{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return probeLocalAuthState{}, nil
		}
		return probeLocalAuthState{}, err
	}
	state := probeLocalAuthState{}
	if err := json.Unmarshal(raw, &state); err != nil {
		return probeLocalAuthState{}, err
	}
	state.Username = strings.TrimSpace(state.Username)
	state.PasswordHash = strings.TrimSpace(state.PasswordHash)
	state.PasswordSalt = strings.TrimSpace(state.PasswordSalt)
	state.PasswordType = strings.TrimSpace(state.PasswordType)
	state.UpdatedAt = strings.TrimSpace(state.UpdatedAt)
	if !state.Registered {
		return probeLocalAuthState{}, nil
	}
	if state.Username == "" || state.PasswordHash == "" || (state.PasswordType == "" && state.PasswordSalt == "") {
		return probeLocalAuthState{}, errors.New("invalid probe local auth data")
	}
	return state, nil
}

func persistProbeLocalAuthState(state probeLocalAuthState) error {
	path, err := resolveProbeLocalAuthStorePath()
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// loadProbeLocalAuthStateRaw reads the full persisted state WITHOUT the registration
// gating applied by loadProbeLocalAuthState, so non-auth settings (the local console
// listen config) survive even before the user registers. existed reports whether the
// file was present.
func loadProbeLocalAuthStateRaw() (state probeLocalAuthState, existed bool, err error) {
	path, err := resolveProbeLocalAuthStorePath()
	if err != nil {
		return probeLocalAuthState{}, false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return probeLocalAuthState{}, false, nil
		}
		return probeLocalAuthState{}, false, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return probeLocalAuthState{}, true, err
	}
	return state, true, nil
}

// resolveProbeLocalConfiguredListenAddr returns the local console listen address
// configured in probe_local_auth.json, or "" when none is set. A missing IP or port
// falls back to the loopback host / default port for that part.
func resolveProbeLocalConfiguredListenAddr() string {
	state, _, err := loadProbeLocalAuthStateRaw()
	if err != nil {
		return ""
	}
	ip := strings.TrimSpace(state.ListenIP)
	port := state.ListenPort
	if ip == "" && (port <= 0 || port > 65535) {
		return ""
	}
	if ip == "" {
		ip = probeLocalListenDefaultHost
	}
	if port <= 0 || port > 65535 {
		port = probeLocalListenDefaultPort
	}
	return net.JoinHostPort(ip, strconv.Itoa(port))
}

// ensureProbeLocalListenConfigDefaults writes default listen_ip/listen_port into
// probe_local_auth.json when absent, preserving any existing auth fields, so the
// settings exist in the file for manual editing.
func ensureProbeLocalListenConfigDefaults() {
	state, existed, err := loadProbeLocalAuthStateRaw()
	if err != nil {
		logProbeWarnf("probe local listen config read failed, leaving file untouched: err=%v", err)
		return
	}
	changed := false
	if strings.TrimSpace(state.ListenIP) == "" {
		state.ListenIP = probeLocalListenDefaultHost
		changed = true
	}
	if state.ListenPort <= 0 || state.ListenPort > 65535 {
		state.ListenPort = probeLocalListenDefaultPort
		changed = true
	}
	if !changed {
		return
	}
	if err := persistProbeLocalAuthState(state); err != nil {
		logProbeWarnf("probe local listen config write failed: err=%v", err)
		return
	}
	if !existed {
		logProbeInfof("probe local listen config initialized: %s", net.JoinHostPort(state.ListenIP, strconv.Itoa(state.ListenPort)))
	}
}

// isProbeLocalLoopbackHost reports whether host is a loopback address. An empty host
// (bind-all) and any non-loopback IP are treated as exposed (returns false).
func isProbeLocalLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func normalizeProbeLocalUsername(raw string) string {
	return strings.TrimSpace(raw)
}

func hashProbeLocalPassword(password, salt string) string {
	material := strings.TrimSpace(salt) + "\n" + password
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

func (m *probeLocalAuthManager) bootstrap() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]any{
		"registered":           m.state.Registered,
		"setup_token_required": !m.state.Registered,
	}
}

func (m *probeLocalAuthManager) registered() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.Registered
}

func (m *probeLocalAuthManager) register(username, password, confirmPassword string) error {
	username = normalizeProbeLocalUsername(username)
	if username == "" {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "username is required"}
	}
	if len([]rune(username)) > probeLocalMaxUsernameLength {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "username is too long"}
	}
	if strings.TrimSpace(password) == "" {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "password is required"}
	}
	if len(password) < probeLocalMinPasswordLength {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "password is too short"}
	}
	if len(password) > probeLocalMaxPasswordLength {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "password is too long"}
	}
	if password != confirmPassword {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "password confirmation does not match"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state.Registered {
		return &probeLocalHTTPError{Status: http.StatusForbidden, Message: "registration is closed"}
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	next := probeLocalAuthState{
		Registered:   true,
		Username:     username,
		PasswordHash: string(passwordHash),
		PasswordType: "bcrypt",
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	// Preserve the local console listen config that lives in the same file.
	if raw, _, rawErr := loadProbeLocalAuthStateRaw(); rawErr == nil {
		next.ListenIP = strings.TrimSpace(raw.ListenIP)
		next.ListenPort = raw.ListenPort
	}
	if err := persistProbeLocalAuthState(next); err != nil {
		return err
	}
	m.state = next
	m.sessions = make(map[string]probeLocalSessionState)
	return nil
}

func (m *probeLocalAuthManager) login(clientIP, username, password string) (string, probeLocalSessionState, error) {
	username = normalizeProbeLocalUsername(username)
	if username == "" || strings.TrimSpace(password) == "" {
		return "", probeLocalSessionState{}, &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "username and password are required"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	loginKey := strings.TrimSpace(clientIP)
	if loginKey == "" {
		loginKey = "unknown"
	}
	if frozenUntil := m.loginFrozen[loginKey]; frozenUntil.After(now) {
		return "", probeLocalSessionState{}, &probeLocalHTTPError{Status: http.StatusTooManyRequests, Message: "too many failed attempts, try again later"}
	}
	delete(m.loginFrozen, loginKey)
	recordFailure := func() {
		m.loginFailed[loginKey]++
		if m.loginFailed[loginKey] >= probeLocalLoginFailThreshold {
			delete(m.loginFailed, loginKey)
			m.loginFrozen[loginKey] = now.Add(probeLocalLoginFreezeTTL)
		}
	}

	if !m.state.Registered {
		return "", probeLocalSessionState{}, &probeLocalHTTPError{Status: http.StatusForbidden, Message: "account is not registered"}
	}

	if !strings.EqualFold(username, m.state.Username) {
		recordFailure()
		return "", probeLocalSessionState{}, &probeLocalHTTPError{Status: http.StatusUnauthorized, Message: "invalid username or password"}
	}
	validPassword := false
	legacyPassword := m.state.PasswordType == ""
	if legacyPassword {
		givenHash := hashProbeLocalPassword(password, m.state.PasswordSalt)
		validPassword = hmac.Equal([]byte(strings.ToLower(givenHash)), []byte(strings.ToLower(m.state.PasswordHash)))
	} else if m.state.PasswordType == "bcrypt" {
		validPassword = bcrypt.CompareHashAndPassword([]byte(m.state.PasswordHash), []byte(password)) == nil
	}
	if !validPassword {
		recordFailure()
		return "", probeLocalSessionState{}, &probeLocalHTTPError{Status: http.StatusUnauthorized, Message: "invalid username or password"}
	}
	delete(m.loginFailed, loginKey)
	delete(m.loginFrozen, loginKey)
	if legacyPassword {
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return "", probeLocalSessionState{}, err
		}
		m.state.PasswordHash = string(passwordHash)
		m.state.PasswordSalt = ""
		m.state.PasswordType = "bcrypt"
		m.state.UpdatedAt = now.UTC().Format(time.RFC3339)
		if err := persistProbeLocalAuthState(m.state); err != nil {
			return "", probeLocalSessionState{}, err
		}
	}

	token := randomHexToken(32)
	session := probeLocalSessionState{
		Username:  m.state.Username,
		ExpiresAt: now.Add(probeLocalSessionTTL),
	}
	m.sessions[token] = session
	m.cleanupExpiredLocked(time.Now())
	return token, session, nil
}

func (m *probeLocalAuthManager) sessionByToken(token string) (probeLocalSessionState, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return probeLocalSessionState{}, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[token]
	if !ok {
		return probeLocalSessionState{}, false
	}
	if time.Now().After(session.ExpiresAt) {
		delete(m.sessions, token)
		return probeLocalSessionState{}, false
	}
	return session, true
}

func (m *probeLocalAuthManager) logoutToken(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

func (m *probeLocalAuthManager) cleanupExpiredLocked(now time.Time) {
	for token, session := range m.sessions {
		if now.After(session.ExpiresAt) {
			delete(m.sessions, token)
		}
	}
}

func extractProbeLocalSessionToken(r *http.Request) (string, error) {
	cookie, err := r.Cookie(probeLocalSessionCookieName)
	if err != nil {
		return "", errors.New("missing local session")
	}
	token := strings.TrimSpace(cookie.Value)
	if token == "" {
		return "", errors.New("missing local session")
	}
	return token, nil
}

func currentProbeLocalSessionFromRequest(r *http.Request) (probeLocalSessionState, string, error) {
	// In-process requests proxied from the (already authenticated) controller are
	// marked trusted via request context and bypass the local login. External HTTP
	// requests cannot set this context value, so this is not forgeable over the wire.
	if isProbeLocalConsoleTrusted(r.Context()) {
		return probeLocalSessionState{Username: "controller", ExpiresAt: time.Now().Add(probeLocalSessionTTL)}, "controller-trusted", nil
	}
	mgr, err := ensureProbeLocalAuthManager()
	if err != nil {
		return probeLocalSessionState{}, "", err
	}
	token, err := extractProbeLocalSessionToken(r)
	if err != nil {
		return probeLocalSessionState{}, "", err
	}
	session, ok := mgr.sessionByToken(token)
	if !ok {
		return probeLocalSessionState{}, "", errors.New("invalid or expired local session")
	}
	return session, token, nil
}

func requireProbeLocalSession(w http.ResponseWriter, r *http.Request) (probeLocalSessionState, bool) {
	session, _, err := currentProbeLocalSessionFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return probeLocalSessionState{}, false
	}
	return session, true
}

func setProbeLocalSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     probeLocalSessionCookieName,
		Value:    strings.TrimSpace(token),
		Path:     "/local",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r != nil && r.TLS != nil,
		Expires:  expiresAt,
	})
}

func clearProbeLocalSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     probeLocalSessionCookieName,
		Value:    "",
		Path:     "/local",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0).UTC(),
		MaxAge:   -1,
	})
}

func writeProbeLocalError(w http.ResponseWriter, err error) {
	if httpErr, ok := err.(*probeLocalHTTPError); ok {
		payload := map[string]any{"error": httpErr.Message}
		for key, value := range httpErr.Payload {
			if strings.TrimSpace(key) == "" || value == nil {
				continue
			}
			payload[key] = value
		}
		writeJSON(w, httpErr.Status, payload)
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": strings.TrimSpace(err.Error())})
}

func buildProbeLocalTUNErrorPayload(err error) map[string]any {
	if err == nil {
		return nil
	}
	payload := map[string]any{}
	var installErr *probeLocalTUNInstallError
	if errors.As(err, &installErr) && installErr != nil {
		payload["diagnostic"] = installErr.Diagnostic
		if strings.TrimSpace(installErr.Diagnostic.Code) != "" {
			payload["code"] = strings.TrimSpace(installErr.Diagnostic.Code)
		}
		if strings.TrimSpace(installErr.Diagnostic.Stage) != "" {
			payload["stage"] = strings.TrimSpace(installErr.Diagnostic.Stage)
		}
		if strings.TrimSpace(installErr.Diagnostic.Hint) != "" {
			payload["hint"] = strings.TrimSpace(installErr.Diagnostic.Hint)
		}
		if strings.TrimSpace(installErr.Diagnostic.Details) != "" {
			payload["details"] = strings.TrimSpace(installErr.Diagnostic.Details)
		}
		if len(installErr.Diagnostic.Steps) > 0 {
			payload["steps"] = append([]string(nil), installErr.Diagnostic.Steps...)
		}
		if observation, ok := installErr.InstallObservation(); ok {
			payload["install_observation"] = observation
		}
	}
	if _, exists := payload["install_observation"]; !exists {
		if observation, ok := currentProbeLocalTUNInstallObservation(); ok {
			payload["install_observation"] = observation
		}
	}
	if len(payload) == 0 {
		return nil
	}
	return payload
}

func defaultProbeLocalDNSHostContent() string {
	return "# dns,ip\n"
}

func resolveProbeLocalTUNStatePath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalTUNStateFileName), nil
}

func resolveProbeLocalTUNEgressStatePath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalTUNEgressStateFileName), nil
}

func resolveProbeLocalDNSHostPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalDNSHostFileName), nil
}

func decodeProbeLocalJSONStrict(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("unexpected extra data")
		}
		return err
	}
	return nil
}

func persistProbeLocalJSONFile(path string, payload any) error {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func defaultProbeLocalTUNStateFile() probeLocalTUNStateFile {
	now := time.Now().UTC().Format(time.RFC3339)
	return probeLocalTUNStateFile{
		Version:   1,
		UpdatedAt: now,
		TUN: probeLocalTUNPersistentState{
			Installed: false,
			Enabled:   false,
			UpdatedAt: now,
		},
	}
}

func loadProbeLocalTUNStateFile() (probeLocalTUNStateFile, error) {
	path, err := resolveProbeLocalTUNStatePath()
	if err != nil {
		return probeLocalTUNStateFile{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			def := defaultProbeLocalTUNStateFile()
			if writeErr := persistProbeLocalTUNStateFile(def); writeErr != nil {
				return probeLocalTUNStateFile{}, writeErr
			}
			return def, nil
		}
		return probeLocalTUNStateFile{}, err
	}
	payload := probeLocalTUNStateFile{}
	if err := decodeProbeLocalJSONStrict(raw, &payload); err != nil {
		return probeLocalTUNStateFile{}, err
	}
	if payload.Version <= 0 {
		payload.Version = 1
	}
	if strings.TrimSpace(payload.UpdatedAt) == "" {
		payload.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(payload.TUN.UpdatedAt) == "" {
		payload.TUN.UpdatedAt = payload.UpdatedAt
	}
	return payload, nil
}

func persistProbeLocalTUNStateFile(payload probeLocalTUNStateFile) error {
	if payload.Version <= 0 {
		payload.Version = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	payload.UpdatedAt = now
	if strings.TrimSpace(payload.TUN.UpdatedAt) == "" {
		payload.TUN.UpdatedAt = now
	}
	path, err := resolveProbeLocalTUNStatePath()
	if err != nil {
		return err
	}
	return persistProbeLocalJSONFile(path, payload)
}

func shouldRestoreProbeLocalTUNFromState(state probeLocalTUNPersistentState) bool {
	return probeLocalTUNRouteFeatureActive() && state.Enabled
}

func persistProbeLocalTUNPersistentState(installed, enabled bool) error {
	state, err := loadProbeLocalTUNStateFile()
	if err != nil {
		return err
	}
	state.TUN.Installed = installed
	state.TUN.Enabled = enabled
	state.TUN.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return persistProbeLocalTUNStateFile(state)
}

func parseProbeLocalHostMappings(content string) ([]probeLocalHostMapping, error) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	indexByDNS := map[string]int{}
	out := make([]probeLocalHostMapping, 0, len(lines))
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(trimmed, ",", 2)
		if len(parts) != 2 {
			return nil, &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("dns_host.txt line %d must be dns,ip", i+1)}
		}
		dns := strings.ToLower(strings.TrimSpace(parts[0]))
		ipText := strings.TrimSpace(parts[1])
		if dns == "" {
			return nil, &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("dns_host.txt line %d dns is empty", i+1)}
		}
		if net.ParseIP(ipText) == nil {
			return nil, &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("dns_host.txt line %d ip is invalid", i+1)}
		}
		entry := probeLocalHostMapping{DNS: dns, IP: ipText}
		if idx, exists := indexByDNS[dns]; exists {
			out[idx] = entry
			logProbeWarnf("probe local proxy host duplicate dns replaced: %s", dns)
			continue
		}
		indexByDNS[dns] = len(out)
		out = append(out, entry)
	}
	return out, nil
}

func encodeProbeLocalHostMappingsContent(hosts []probeLocalHostMapping) string {
	if len(hosts) == 0 {
		return defaultProbeLocalDNSHostContent()
	}
	lines := make([]string, 0, len(hosts))
	for _, host := range hosts {
		dns := strings.ToLower(strings.TrimSpace(host.DNS))
		ipText := strings.TrimSpace(host.IP)
		if dns == "" || ipText == "" {
			continue
		}
		lines = append(lines, dns+","+ipText)
	}
	if len(lines) == 0 {
		return defaultProbeLocalDNSHostContent()
	}
	return strings.Join(lines, "\n") + "\n"
}

func loadProbeLocalHostMappingsWithContent() (string, []probeLocalHostMapping, error) {
	path, err := resolveProbeLocalDNSHostPath()
	if err != nil {
		return "", nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			content := defaultProbeLocalDNSHostContent()
			hosts, parseErr := parseProbeLocalHostMappings(content)
			if parseErr != nil {
				return "", nil, parseErr
			}
			if writeErr := persistProbeLocalHostMappings(hosts); writeErr != nil {
				return "", nil, writeErr
			}
			return content, hosts, nil
		}
		return "", nil, err
	}
	content := string(raw)
	hosts, err := parseProbeLocalHostMappings(content)
	if err != nil {
		return "", nil, err
	}
	return content, hosts, nil
}

func persistProbeLocalHostMappings(hosts []probeLocalHostMapping) error {
	path, err := resolveProbeLocalDNSHostPath()
	if err != nil {
		return err
	}
	content := encodeProbeLocalHostMappingsContent(hosts)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func ensureprobeLocalRouteDefaultsInitialized() error {
	if _, _, err := loadProbeLocalHostMappingsWithContent(); err != nil {
		logProbeErrorf("probe local dns host config invalid, service will continue without static host mappings until fixed: %v", err)
	}
	return nil
}

func normalizeProbeLocalListenAddr(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return ""
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		host = "127.0.0.1"
	}
	portNum, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil || portNum <= 0 || portNum > 65535 {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(portNum))
}

func resolveProbeLocalListenAddr(explicit string) string {
	candidate := firstNonEmpty(
		strings.TrimSpace(explicit),
		strings.TrimSpace(os.Getenv("PROBE_LOCAL_LISTEN")),
		strings.TrimSpace(resolveProbeLocalConfiguredListenAddr()),
		probeLocalListenAddrDefault,
	)
	normalized := normalizeProbeLocalListenAddr(candidate)
	if normalized != "" {
		return normalized
	}
	return probeLocalListenAddrDefault
}

func probeLocalListenFallbackCandidates(addr string) []string {
	normalized := normalizeProbeLocalListenAddr(addr)
	if normalized == "" {
		normalized = probeLocalListenAddrDefault
	}
	candidates := []string{normalized}
	host, portText, err := net.SplitHostPort(normalized)
	if err != nil {
		return candidates
	}
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil || port <= 0 || port >= 65535 {
		return candidates
	}
	for offset := 1; offset <= 10 && port+offset <= 65535; offset++ {
		candidates = append(candidates, net.JoinHostPort(host, strconv.Itoa(port+offset)))
	}
	return candidates
}

func listenProbeLocalConsoleWithFallback(addr string) (net.Listener, string, error) {
	var lastErr error
	for i, candidate := range probeLocalListenFallbackCandidates(addr) {
		listener, err := net.Listen("tcp", candidate)
		if err == nil {
			if i > 0 {
				logProbeWarnf("probe local console fallback listen selected: requested=%s actual=%s previous_err=%v", addr, candidate, lastErr)
			}
			return listener, candidate, nil
		}
		lastErr = err
		logProbeWarnf("probe local console listen failed: candidate=%s err=%v", candidate, err)
	}
	if lastErr == nil {
		lastErr = errors.New("no local console listen candidates")
	}
	return nil, "", lastErr
}

func startProbeLocalConsoleServer(handler http.Handler, explicitListen string) error {
	if handler == nil {
		return errors.New("nil local console handler")
	}
	addr := resolveProbeLocalListenAddr(explicitListen)
	if host, _, splitErr := net.SplitHostPort(addr); splitErr == nil && !isProbeLocalLoopbackHost(host) {
		if !strings.EqualFold(strings.TrimSpace(os.Getenv("PROBE_LOCAL_ALLOW_INSECURE_HTTP")), "true") {
			return fmt.Errorf("refusing insecure local console HTTP on non-loopback address %s; use a TLS reverse proxy or explicitly set PROBE_LOCAL_ALLOW_INSECURE_HTTP=true", addr)
		}
		logProbeWarnf("probe local console insecure HTTP explicitly enabled on non-loopback address: %s", addr)
	}

	probeLocalConsoleState.mu.Lock()
	if probeLocalConsoleState.server != nil {
		probeLocalConsoleState.mu.Unlock()
		return nil
	}
	listener, listenAddr, err := listenProbeLocalConsoleWithFallback(addr)
	if err != nil {
		probeLocalConsoleState.mu.Unlock()
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	probeLocalConsoleState.server = server
	probeLocalConsoleState.listenAddr = listenAddr
	probeLocalConsoleState.mu.Unlock()

	logProbeInfof("probe local console listening on http://%s", listenAddr)
	go func(s *http.Server, ln net.Listener, listenAddr string) {
		err := s.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			logProbeErrorf("probe local console exited: listen=%s err=%v", listenAddr, err)
		}
		probeLocalConsoleState.mu.Lock()
		if probeLocalConsoleState.server == s {
			probeLocalConsoleState.server = nil
			probeLocalConsoleState.listenAddr = ""
		}
		probeLocalConsoleState.mu.Unlock()
	}(server, listener, listenAddr)

	return nil
}

func applyProbeLocalConsoleListenerEnabled(enabled bool, explicitListen string, reason string) error {
	if !enabled {
		stopProbeLocalConsoleServer(reason)
		return nil
	}
	return startProbeLocalConsoleServer(buildProbeLocalConsoleMux(), explicitListen)
}

func stopProbeLocalConsoleServer(reason string) {
	probeLocalConsoleState.mu.Lock()
	server := probeLocalConsoleState.server
	addr := probeLocalConsoleState.listenAddr
	probeLocalConsoleState.server = nil
	probeLocalConsoleState.listenAddr = ""
	probeLocalConsoleState.mu.Unlock()
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := server.Shutdown(ctx)
	cancel()
	if err != nil {
		_ = server.Close()
		logProbeWarnf("probe local console shutdown forced: listen=%s reason=%s err=%v", addr, strings.TrimSpace(reason), err)
		return
	}
	logProbeInfof("probe local console stopped: listen=%s reason=%s", addr, strings.TrimSpace(reason))
}

func buildProbeLocalConsoleMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", probeLocalRootHandler)
	registerProbeLocalConsoleRoutes(mux)
	return mux
}

func registerProbeLocalConsoleRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("/local/login", probeLocalLoginPageHandler)
	mux.HandleFunc("/local/panel", probeLocalPanelPageHandler)
	mux.HandleFunc("/local/logs", probeLocalLogsPageHandler)
	mux.HandleFunc("/local/system", probeLocalSystemPageHandler)
	mux.HandleFunc("/local/virtual-router", probeLocalVirtualRouterPageHandler)
	mux.HandleFunc("/local/sync", probeLocalSyncPageHandler)
	mux.HandleFunc("/local/shell", probeLocalShellPageHandler)
	mux.HandleFunc("/local/information", probeLocalInformationPageHandler)
	mux.HandleFunc("/local/api/auth/bootstrap", probeLocalAuthBootstrapHandler)
	mux.HandleFunc("/local/api/auth/register", probeLocalAuthRegisterHandler)
	mux.HandleFunc("/local/api/auth/login", probeLocalAuthLoginHandler)
	mux.HandleFunc("/local/api/auth/logout", probeLocalAuthLogoutHandler)
	mux.HandleFunc("/local/api/auth/session", probeLocalAuthSessionHandler)

	mux.HandleFunc("/local/api/tun/status", probeLocalTUNStatusHandler)
	mux.HandleFunc("/local/api/tun/egress", probeLocalTUNEgressHandler)
	mux.HandleFunc("/local/api/tun/install", probeLocalTUNInstallHandler)
	mux.HandleFunc("/local/api/tun/reset", probeLocalTUNResetHandler)
	mux.HandleFunc("/local/api/tun/uninstall", probeLocalTUNUninstallHandler)
	mux.HandleFunc("/local/api/logs", probeLocalLogsHandler)
	mux.HandleFunc("/local/api/virtual_router/settings", probeLocalVirtualRouterSettingsHandler)
	mux.HandleFunc("/local/api/virtual_router/status", probeLocalVirtualRouterStatusHandler)
	mux.HandleFunc("/local/api/virtual_router/packets", probeLocalVirtualRouterPacketsHandler)
	mux.HandleFunc("/local/api/virtual_router/connections", probeLocalVirtualRouterConnectionsHandler)
	mux.HandleFunc("/local/api/virtual_router/debug", probeLocalVirtualRouterDebugHandler)
	mux.HandleFunc("/local/api/virtual_router/debug/logs", probeLocalVirtualRouterDebugLogsHandler)
	mux.HandleFunc("/local/api/virtual_router/path_rtt", probeLocalVirtualRouterPathRTTHandler)
	mux.HandleFunc("/local/api/virtual_router/route_test", probeLocalVirtualRouterRouteTestHandler)
	mux.HandleFunc("/local/api/virtual_router/route_test/curl", probeLocalVirtualRouterRouteTestCurlHandler)
	mux.HandleFunc("/local/api/virtual_router/route_test/speed", probeLocalVirtualRouterRouteTestSpeedHandler)
	mux.HandleFunc("/local/api/virtual_router/diagnostic", probeLocalVirtualRouterDiagnosticHandler)
	mux.HandleFunc("/local/api/system/upgrade", probeLocalSystemUpgradeHandler)
	mux.HandleFunc("/local/api/system/upgrade/check", probeLocalSystemUpgradeCheckHandler)
	mux.HandleFunc("/local/api/system/upgrade/status", probeLocalSystemUpgradeStatusHandler)
	mux.HandleFunc("/local/api/system/restart", probeLocalSystemRestartHandler)
	mux.HandleFunc("/local/api/system/ip_report_settings", probeLocalSystemIPReportSettingsHandler)
	mux.HandleFunc("/local/api/system/ddns", probeLocalSystemDDNSHandler)
	mux.HandleFunc("/local/api/system/route_auth_blacklist", probeLocalSystemRouteAuthBlacklistHandler)
	mux.HandleFunc("/local/api/shell/exec", probeLocalShellExecHandler)
	mux.HandleFunc("/local/api/shell/stream", probeLocalShellStreamHandler)
	mux.HandleFunc("/local/api/sync/status", probeLocalSyncStatusHandler)
	mux.HandleFunc("/local/api/sync/settings", probeLocalSyncSettingsHandler)
	mux.HandleFunc("/local/api/sync/test", probeLocalSyncTestHandler)
	mux.HandleFunc("/local/api/sync/run", probeLocalSyncRunHandler)
	mux.HandleFunc("/local/api/sync/google/auth/start", probeLocalSyncGoogleAuthStartHandler)
	mux.HandleFunc("/local/api/sync/google/auth/poll", probeLocalSyncGoogleAuthPollHandler)
	mux.HandleFunc("/local/api/sync/google/disconnect", probeLocalSyncGoogleDisconnectHandler)
	mux.HandleFunc("/local/api/info_box", probeLocalInfoBoxHandler)
	mux.HandleFunc("/local/api/info_box/events", probeLocalInfoBoxEventsHandler)
}

type probeLocalRegisterRequest struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
	SetupToken      string `json:"setup_token"`
}

type probeLocalLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type probeLocalRouteEnableRequest struct {
	Group           string `json:"group"`
	SelectedRouteID string `json:"selected_route_id"`
	TunnelNodeID    string `json:"tunnel_node_id"`
}

type probeLocalRouteDirectRequest struct {
	Group string `json:"group"`
}

type probeLocalRouteRejectRequest struct {
	Group string `json:"group"`
}

type probeLocalSystemUpgradeRequest struct {
	Mode        string `json:"mode"`
	ReleaseRepo string `json:"release_repo"`
}

type probeLocalSyncSettingsRequest struct {
	Enabled            bool     `json:"enabled"`
	SourcePaths        []string `json:"source_paths"`
	LocalTempDir       string   `json:"local_temp_dir"`
	Schedule           string   `json:"schedule"`
	GoogleClientID     string   `json:"google_client_id"`
	GoogleClientSecret string   `json:"google_client_secret"`
	GoogleFolder       string   `json:"google_folder"`
}

type probeLocalSyncGoogleAuthPollRequest struct {
	SessionID string `json:"session_id"`
}

type probeLocalShellExecRequest struct {
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec"`
}

type probeLocalVirtualRouterDebugLogsRequest struct {
	TargetNodeID string `json:"target_node_id"`
	Lines        int    `json:"lines,omitempty"`
	SinceMinutes int    `json:"since_minutes,omitempty"`
	MinLevel     string `json:"min_level,omitempty"`
	Keyword      string `json:"keyword,omitempty"`
	TimeoutMS    int    `json:"timeout_ms,omitempty"`
}

func probeLocalRootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, _, err := currentProbeLocalSessionFromRequest(r); err == nil {
		http.Redirect(w, r, "/local/panel", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/local/login", http.StatusFound)
}

func probeLocalLoginPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/local/login" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(probeLocalLoginPageHTML))
}

func probeLocalPanelPageHandler(w http.ResponseWriter, r *http.Request) {
	serveProbeLocalHTMLPage(w, r, "/local/panel", probeLocalPanelPageHTML)
}

func probeLocalRoutePageHandler(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

func probeLocalLogsPageHandler(w http.ResponseWriter, r *http.Request) {
	serveProbeLocalHTMLPage(w, r, "/local/logs", probeLocalLogsPageHTML)
}

func probeLocalMonitorPageHandler(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

func probeLocalSystemPageHandler(w http.ResponseWriter, r *http.Request) {
	serveProbeLocalHTMLPage(w, r, "/local/system", probeLocalSystemPageHTML)
}

func probeLocalVirtualRouterPageHandler(w http.ResponseWriter, r *http.Request) {
	serveProbeLocalHTMLPage(w, r, "/local/virtual-router", probeLocalVirtualRouterPageHTML)
}

func probeLocalSyncPageHandler(w http.ResponseWriter, r *http.Request) {
	serveProbeLocalHTMLPage(w, r, "/local/sync", probeLocalSyncPageHTML)
}

func probeLocalShellPageHandler(w http.ResponseWriter, r *http.Request) {
	serveProbeLocalHTMLPage(w, r, "/local/shell", probeLocalShellPageHTML)
}

func probeLocalInformationPageHandler(w http.ResponseWriter, r *http.Request) {
	serveProbeLocalHTMLPage(w, r, "/local/information", probeLocalInformationPageHTML)
}

func probeLocalInfoBoxHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	message := ""
	if r.Method == http.MethodPost {
		var request struct {
			Message string `json:"message"`
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
		if err != nil || decodeProbeLocalJSONStrict(raw, &request) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
		message = request.Message
	}
	ctx, cancel := context.WithTimeout(r.Context(), probeInfoBoxRequestTimeout)
	defer cancel()
	payload, err := probeLocalRequestInfoBox(ctx, currentprobeLocalRouteRuntimeContext(), r.Method, message)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func serveProbeLocalHTMLPage(w http.ResponseWriter, r *http.Request, expectedPath string, pageHTML string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != expectedPath {
		http.NotFound(w, r)
		return
	}
	if _, _, err := currentProbeLocalSessionFromRequest(r); err != nil {
		http.Redirect(w, r, "/local/login", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(pageHTML))
}

func probeLocalAuthBootstrapHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mgr, err := ensureProbeLocalAuthManager()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	if !mgr.registered() {
		if _, err := ensureProbeLocalSetupToken(); err != nil {
			writeProbeLocalError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, mgr.bootstrap())
}

func probeLocalAuthRegisterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mgr, err := ensureProbeLocalAuthManager()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalAuthReadBodyMaxLen)
	defer body.Close()
	var req probeLocalRegisterRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if mgr.registered() {
		writeProbeLocalError(w, &probeLocalHTTPError{Status: http.StatusForbidden, Message: "registration is closed"})
		return
	}
	if err := verifyProbeLocalSetupToken(req.SetupToken); err != nil {
		writeProbeLocalError(w, err)
		return
	}
	if err := mgr.register(req.Username, req.Password, req.ConfirmPassword); err != nil {
		writeProbeLocalError(w, err)
		return
	}
	consumeProbeLocalSetupToken()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "registered": true})
}

func probeLocalAuthLoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mgr, err := ensureProbeLocalAuthManager()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalAuthReadBodyMaxLen)
	defer body.Close()
	var req probeLocalLoginRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	clientIP := resolveProbeRouteSourceIPFromAddrString(r.RemoteAddr)
	token, session, err := mgr.login(clientIP, req.Username, req.Password)
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	setProbeLocalSessionCookie(w, r, token, session.ExpiresAt)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"username":   session.Username,
		"expires_at": session.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func probeLocalAuthLogoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mgr, err := ensureProbeLocalAuthManager()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	if token, tokenErr := extractProbeLocalSessionToken(r); tokenErr == nil {
		mgr.logoutToken(token)
	}
	clearProbeLocalSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func probeLocalAuthSessionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, _, err := currentProbeLocalSessionFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"username":      session.Username,
		"expires_at":    session.ExpiresAt.UTC().Format(time.RFC3339),
		"version":       BuildVersion,
	})
}

func probeLocalTUNStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	status := probeLocalControl.tunStatus()
	status.InstallObservation = nil
	if status.LastInstallObservation == nil {
		if observation, ok := currentProbeLocalTUNInstallObservation(); ok {
			status.LastInstallObservation = cloneProbeLocalTUNInstallObservationPointer(&observation)
		}
	}
	writeJSON(w, http.StatusOK, status)
}

func probeLocalTUNInstallHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	state, err := probeLocalControl.installTUN()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tun": state, "install_observation": state.InstallObservation})
}

func probeLocalTUNResetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	state, err := probeLocalControl.resetTUN()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tun": state})
}

func probeLocalTUNUninstallHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	state, err := probeLocalControl.uninstallTUN()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tun": state})
}

func probeLocalLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}

	lines := defaultProbeLogLines
	if raw := strings.TrimSpace(r.URL.Query().Get("lines")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			lines = parsed
		}
	}
	lines = normalizeProbeLogLines(lines)

	sinceMinutes := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("since_minutes")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			sinceMinutes = parsed
		}
	}
	sinceMinutes = normalizeProbeLogSinceMinutes(sinceMinutes)

	minLevel := strings.TrimSpace(r.URL.Query().Get("min_level"))
	keyword := strings.TrimSpace(r.URL.Query().Get("keyword"))
	source, filePath, content, entries, err := collectProbeLocalLogsForView(lines, sinceMinutes, minLevel, keyword)
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"source":        source,
		"file_path":     filePath,
		"lines":         lines,
		"since_minutes": sinceMinutes,
		"min_level":     minLevel,
		"keyword":       keyword,
		"content":       content,
		"entries":       entries,
		"count":         len(entries),
	})
}

func filterProbeLocalLogEntriesByKeyword(entries []probeLogViewEntry, keyword string) []probeLogViewEntry {
	needle := strings.ToLower(strings.TrimSpace(keyword))
	if needle == "" {
		return entries
	}
	filtered := make([]probeLogViewEntry, 0, len(entries))
	for _, entry := range entries {
		line := strings.ToLower(strings.TrimSpace(entry.Line))
		message := strings.ToLower(strings.TrimSpace(entry.Message))
		if strings.Contains(line, needle) || strings.Contains(message, needle) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func buildProbeLocalLogContent(entries []probeLogViewEntry) string {
	if len(entries) == 0 {
		return ""
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		line := strings.TrimSpace(entry.Line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func probeLocalVirtualRouterSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, probeLocalVirtualRouterSettingsPayload(loadProbeVirtualRouterLocalSettings()))
	case http.MethodPost:
		var req struct {
			VirtualRouterEnabled *bool   `json:"virtual_router_enabled"`
			VirtualDNSEnabled    *bool   `json:"virtual_dns_enabled"`
			ProxyEnabled         *bool   `json:"proxy_enabled"`
			HTTPProxyListen      *string `json:"http_proxy_listen"`
			SOCKS5ProxyListen    *string `json:"socks5_proxy_listen"`
			ProxyUsername        *string `json:"proxy_username"`
			ProxyPassword        *string `json:"proxy_password"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		next := loadProbeVirtualRouterLocalSettings()
		if req.VirtualRouterEnabled != nil {
			next.VirtualRouterEnabled = *req.VirtualRouterEnabled
		}
		if req.VirtualDNSEnabled != nil {
			next.VirtualDNSEnabled = *req.VirtualDNSEnabled
		}
		if req.ProxyEnabled != nil {
			next.ProxyEnabled = *req.ProxyEnabled
		}
		if req.HTTPProxyListen != nil {
			next.HTTPProxyListen = strings.TrimSpace(*req.HTTPProxyListen)
		}
		if req.SOCKS5ProxyListen != nil {
			next.SOCKS5ProxyListen = strings.TrimSpace(*req.SOCKS5ProxyListen)
		}
		if req.ProxyUsername != nil {
			next.ProxyUsername = strings.TrimSpace(*req.ProxyUsername)
			if next.ProxyUsername == "" && req.ProxyPassword == nil {
				next.ProxyPassword = ""
			}
		}
		if req.ProxyPassword != nil {
			next.ProxyPassword = *req.ProxyPassword
		}
		settings, err := saveProbeVirtualRouterLocalSettings(next)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, probeLocalVirtualRouterSettingsPayload(settings))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func probeLocalVirtualRouterSettingsPayload(settings probeVirtualRouterLocalSettings) map[string]any {
	library := currentProbeVirtualRouterFakeIPLibrary()
	return map[string]any{
		"virtual_router_enabled":    settings.VirtualRouterEnabled,
		"virtual_dns_enabled":       settings.VirtualDNSEnabled,
		"proxy_enabled":             settings.ProxyEnabled,
		"http_proxy_listen":         settings.HTTPProxyListen,
		"socks5_proxy_listen":       settings.SOCKS5ProxyListen,
		"proxy_username":            settings.ProxyUsername,
		"proxy_password_configured": settings.ProxyPassword != "",
		"updated_at":                settings.UpdatedAt,
		"local_node_id":             currentProbeVirtualRouterLocalNodeID(),
		"local_ip":                  currentProbeVirtualRouterLocalIP(),
		"fake_ip_cidr":              currentProbeVirtualRouterFakeIPCIDR(),
		"fake_ip_library":           library,
		"fake_ip_count":             len(library.Items),
	}
}

func probeLocalVirtualRouterStatusHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, probeLocalVirtualRouterStatusPayload())
}

func probeLocalVirtualRouterPathRTTHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	refresh := probeVirtualRouterExploreAllPathRTTs()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"queried":           refresh.Queried,
		"explored":          refresh.Explored,
		"recovered_targets": refresh.RecoveredTargets,
		"updated_at":        time.Now().UTC().Format(time.RFC3339Nano),
		"status":            probeLocalVirtualRouterStatusPayload(),
	})
}

func probeLocalVirtualRouterDebugHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload := map[string]any{
		"updated_at": time.Now().UTC().Format(time.RFC3339Nano),
		"status":     probeLocalVirtualRouterStatusPayload(),
	}
	if r.URL.Query().Get("goroutines") == "1" {
		var buf bytes.Buffer
		if profile := runtimepprof.Lookup("goroutine"); profile != nil {
			_ = profile.WriteTo(&buf, 2)
		}
		payload["goroutines"] = buf.String()
	}
	writeJSON(w, http.StatusOK, payload)
}

func probeLocalVirtualRouterDebugLogsHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req probeLocalVirtualRouterDebugLogsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	targetNodeID := normalizeProbeRouteNodeID(req.TargetNodeID)
	if targetNodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target_node_id is required"})
		return
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = probeVirtualRouterDebugLogTimeout
	}
	result, err := runProbeVirtualRouterDebugLogFetch(targetNodeID, req.Lines, req.SinceMinutes, req.MinLevel, req.Keyword, timeout)
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func probeLocalVirtualRouterStatusPayload() map[string]any {
	settings := loadProbeVirtualRouterLocalSettings()
	library := currentProbeVirtualRouterFakeIPLibrary()
	tunStats := probeVirtualRouterTUNDataPlaneStatsSnapshot()
	exitNetstack := snapshotProbeVirtualRouterExitNetstack()
	runtimes := probeLocalVirtualRouterRuntimeStatusPayloads()
	frameLinks := probeLocalVirtualRouterFrameLinkStatusPayloads()
	rules := probeLocalVirtualRouterRuleRuntimeStatusPayloads()
	paths := probeLocalVirtualRouterPathStatusPayloads()
	recentPackets := snapshotProbeVirtualRouterRecentPackets()
	recentSummary := probeLocalVirtualRouterRecentPacketSummaryPayload(recentPackets)
	proxyStatus := snapshotProbeVRouteProxyRuntime()

	hasCarrier := false
	txQueued := 0
	rxQueued := 0
	for _, item := range frameLinks {
		if carrier, _ := item["carrier"].(bool); carrier {
			hasCarrier = true
		}
		if value, ok := item["tx_queue"].(int); ok {
			txQueued += value
		}
		if value, ok := item["rx_queue"].(int); ok {
			rxQueued += value
		}
	}
	reasons := make([]string, 0, 6)
	if !settings.VirtualRouterEnabled && !settings.VirtualDNSEnabled && !settings.ProxyEnabled {
		reasons = append(reasons, "虚拟路由未开启")
	}
	if strings.TrimSpace(currentProbeVirtualRouterLocalNodeID()) == "" {
		reasons = append(reasons, "本机节点 ID 为空")
	}
	if strings.TrimSpace(currentProbeVirtualRouterLocalIP()) == "" {
		reasons = append(reasons, "本机虚拟 IP 为空")
	}
	if settings.VirtualRouterEnabled && !tunStats.Running {
		reasons = append(reasons, "TUN 数据面未运行")
	}
	if len(runtimes) == 0 {
		reasons = append(reasons, "本机没有匹配的路由运行时")
	}
	if len(runtimes) > 0 && !hasCarrier {
		reasons = append(reasons, "没有已连接的物理承载")
	}
	status := "ok"
	if len(reasons) > 0 {
		status = "warning"
	}

	return map[string]any{
		"updated_at":             time.Now().UTC().Format(time.RFC3339Nano),
		"status":                 status,
		"reasons":                reasons,
		"virtual_router_enabled": settings.VirtualRouterEnabled,
		"virtual_dns_enabled":    settings.VirtualDNSEnabled,
		"proxy_enabled":          settings.ProxyEnabled,
		"local_node_id":          currentProbeVirtualRouterLocalNodeID(),
		"local_ip":               currentProbeVirtualRouterLocalIP(),
		"fake_ip_cidr":           currentProbeVirtualRouterFakeIPCIDR(),
		"fake_ip_count":          len(library.Items),
		"proxy":                  proxyStatus,
		"tun": map[string]any{
			"running":                         tunStats.Running,
			"rx_packets":                      tunStats.RXPackets,
			"rx_bytes":                        tunStats.RXBytes,
			"tx_packets":                      tunStats.TXPackets,
			"tx_bytes":                        tunStats.TXBytes,
			"inbound_queue_depth":             tunStats.InboundQueueDepth,
			"inbound_queue_capacity":          tunStats.InboundQueueCapacity,
			"inbound_entry_queue_depth":       tunStats.InboundEntryQueueDepth,
			"inbound_entry_queue_capacity":    tunStats.InboundEntryQueueCapacity,
			"inbound_dispatch_queue_depth":    tunStats.InboundDispatchQueueDepth,
			"inbound_dispatch_queue_capacity": tunStats.InboundDispatchQueueCapacity,
			"inbound_dispatch_workers":        tunStats.InboundDispatchWorkers,
			"outbound_queue_depth":            tunStats.OutboundQueueDepth,
			"outbound_queue_capacity":         tunStats.OutboundQueueCapacity,
			"outbound_workers":                tunStats.OutboundWorkers,
			"tx_dropped":                      tunStats.TXDropped,
			"tx_errors":                       tunStats.TXErrors,
			"tx_slow_writes":                  tunStats.TXSlowWrites,
			"tx_slow_queue_waits":             tunStats.TXSlowQueueWaits,
			"tx_last_write_ms":                tunStats.TXLastWriteMs,
			"tx_max_write_ms":                 tunStats.TXMaxWriteMs,
			"tx_last_queue_wait_ms":           tunStats.TXLastQueueWaitMs,
			"tx_max_queue_wait_ms":            tunStats.TXMaxQueueWaitMs,
			"tx_max_lock_wait_us":             tunStats.TXMaxLockWaitUS,
			"tx_max_allocate_us":              tunStats.TXMaxAllocateUS,
			"tx_max_copy_us":                  tunStats.TXMaxCopyUS,
			"tx_max_send_us":                  tunStats.TXMaxSendUS,
		},
		"exit_netstack": map[string]any{
			"running":               exitNetstack.Running,
			"mtu":                   exitNetstack.MTU,
			"output_shards":         exitNetstack.OutputShards,
			"output_queue_depth":    exitNetstack.OutputQueueDepth,
			"output_queue_capacity": exitNetstack.OutputQueueCapacity,
			"output_enqueued":       exitNetstack.OutputEnqueued,
			"output_forwarded":      exitNetstack.OutputForwarded,
			"output_dropped":        exitNetstack.OutputDropped,
			"output_queue_full":     exitNetstack.OutputQueueFull,
			"tcp_no_delay":          exitNetstack.TCPNoDelay,
			"tcp_buffer_default":    exitNetstack.TCPBufferDefault,
			"tcp_buffer_max":        exitNetstack.TCPBufferMax,
		},
		"summary": map[string]any{
			"runtime_count":      len(runtimes),
			"rule_count":         len(rules),
			"frame_link_count":   len(frameLinks),
			"carrier_count":      probeLocalVirtualRouterCarrierCount(frameLinks),
			"tx_queue_depth":     txQueued,
			"rx_queue_depth":     rxQueued,
			"recent_count":       recentSummary["count"],
			"recent_error_count": recentSummary["error_count"],
			"recent_drop_count":  recentSummary["drop_count"],
		},
		"rules":          rules,
		"paths":          paths,
		"runtimes":       runtimes,
		"frame_links":    frameLinks,
		"recent_summary": recentSummary,
	}
}

func probeLocalVirtualRouterPathStatusPayloads() []map[string]any {
	probeVirtualRouterState.mu.RLock()
	config := probeVirtualRouterState.config
	localNodeID := normalizeProbeRouteNodeID(probeVirtualRouterState.localNodeID)
	probeVirtualRouterState.mu.RUnlock()
	if localNodeID == "" {
		return []map[string]any{}
	}
	items := make([]map[string]any, 0, len(config.ProbeIPs))
	for _, probe := range config.ProbeIPs {
		targetNodeID := normalizeProbeRouteNodeID(probe.NodeID)
		if targetNodeID == "" || targetNodeID == localNodeID {
			continue
		}
		path := currentProbeVirtualRouterPathBetweenNodes(localNodeID, targetNodeID)
		pathNodes := make([]map[string]string, 0, len(path))
		for _, nodeID := range path {
			cleanNodeID := normalizeProbeRouteNodeID(nodeID)
			if cleanNodeID == "" {
				continue
			}
			pathNodes = append(pathNodes, map[string]string{
				"node_id":   cleanNodeID,
				"node_name": probeVirtualRouterDisplayNameForNode(config, cleanNodeID),
			})
		}
		pathKey := probeVirtualRouterPathKey(path)
		probeVirtualRouterPathRTTState.mu.RLock()
		rtt := probeVirtualRouterPathRTTState.items[pathKey]
		probeVirtualRouterPathRTTState.mu.RUnlock()
		status := "unavailable"
		hopCount := 0
		if len(path) >= 2 {
			hopCount = len(path) - 1
			status = "running"
			if probeVirtualRouterPathShouldAvoid(path) {
				status = "quarantined"
			} else if strings.TrimSpace(rtt.LastError) != "" {
				status = "degraded"
			}
		}
		items = append(items, map[string]any{
			"target_node_id":   targetNodeID,
			"target_node_name": probeVirtualRouterDisplayNameForNode(config, targetNodeID),
			"target_ip":        strings.TrimSpace(probe.IP),
			"path":             pathNodes,
			"hop_count":        hopCount,
			"rtt_ms":           rtt.RTTMS,
			"failure_count":    rtt.ConsecutiveFailureCount,
			"last_error":       strings.TrimSpace(rtt.LastError),
			"last_observed_at": probeLocalVirtualRouterTimeString(rtt.LastAt),
			"status":           status,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.TrimSpace(items[i]["target_node_id"].(string)) < strings.TrimSpace(items[j]["target_node_id"].(string))
	})
	return items
}

func probeLocalVirtualRouterRuntimeStatusPayloads() []map[string]any {
	probeVirtualRouterRuntimeState.mu.RLock()
	runtimes := make([]*probeVirtualRouterRuntime, 0, len(probeVirtualRouterRuntimeState.runtimes))
	for _, rt := range probeVirtualRouterRuntimeState.runtimes {
		if rt != nil {
			runtimes = append(runtimes, rt)
		}
	}
	probeVirtualRouterRuntimeState.mu.RUnlock()
	sort.Slice(runtimes, func(i, j int) bool {
		return strings.TrimSpace(runtimes[i].cfg.routeID) < strings.TrimSpace(runtimes[j].cfg.routeID)
	})

	out := make([]map[string]any, 0, len(runtimes))
	for _, rt := range runtimes {
		cfg := rt.cfg
		selectedProtocol, selectedProtocolEndpoint := probeLocalVirtualRouterRuntimeSelectedProtocol(cfg)
		routeLayer := normalizeProbeRouteRouteLayer(cfg.routeLayer)
		item := map[string]any{
			"route_id":                   strings.TrimSpace(cfg.routeID),
			"name":                       strings.TrimSpace(cfg.name),
			"local_node_id":              normalizeProbeRouteNodeID(cfg.localNodeID),
			"peer_node_id":               normalizeProbeRouteNodeID(cfg.peerNodeID),
			"peer_node_name":             strings.TrimSpace(cfg.peerName),
			"from_node_id":               normalizeProbeRouteNodeID(cfg.fromNodeID),
			"to_node_id":                 normalizeProbeRouteNodeID(cfg.toNodeID),
			"local_ip":                   strings.TrimSpace(cfg.localIP),
			"peer_ip":                    strings.TrimSpace(cfg.peerIP),
			"peer_host":                  strings.TrimSpace(cfg.peerHost),
			"peer_port":                  cfg.peerPort,
			"listen_host":                strings.TrimSpace(cfg.listenHost),
			"listen_port":                cfg.listenPort,
			"route_layer":                routeLayer,
			"selected_protocol":          selectedProtocol,
			"selected_protocol_endpoint": selectedProtocolEndpoint,
			"protocol_text":              probeLocalVirtualRouterRuntimeProtocolText(routeLayer, selectedProtocol),
			"dialer":                     cfg.dialer,
			"bridge_role":                probeLocalVirtualRouterRuntimeBridgeRole(cfg),
			"frame_link_key":             probeVirtualRouterFrameLinkKey(rt, "", "", nil),
		}
		if stats := snapshotProbeVirtualRouterRuntimeStats(cfg.routeID); stats != nil {
			item["stats"] = stats
			item["last_error"] = firstNonEmpty(stats.LastPingError, stats.LastOpenError, stats.LastDeliveryError, stats.LastRemoteRTTError, stats.LastSpeedTestError)
			item["last_packet_at"] = stats.LastPacketAt
			item["last_frame_at"] = stats.LastFrameAt
		}
		out = append(out, item)
	}
	return out
}

func probeLocalVirtualRouterRuntimeSelectedProtocol(cfg probeVirtualRouterRuntimeConfig) (string, string) {
	host := strings.TrimSpace(cfg.listenHost)
	port := cfg.listenPort
	if cfg.dialer {
		host = strings.TrimSpace(cfg.peerHost)
		port = cfg.peerPort
	}
	if host == "" || port <= 0 {
		return "", ""
	}
	snapshot := snapshotProbeRouteProtocolState(host, port)
	rawSelected := strings.TrimSpace(snapshot.SelectedProtocol)
	if rawSelected == "" {
		return "", strings.TrimSpace(snapshot.Endpoint)
	}
	selected := normalizeProbeRouteRouteLayer(rawSelected)
	return selected, strings.TrimSpace(snapshot.Endpoint)
}

func probeLocalVirtualRouterRuntimeProtocolText(routeLayer string, selectedProtocol string) string {
	configured := normalizeProbeRouteRouteLayer(routeLayer)
	selected := ""
	if rawSelected := strings.TrimSpace(selectedProtocol); rawSelected != "" {
		selected = normalizeProbeRouteRouteLayer(rawSelected)
	}
	if configured == "" {
		configured = "auto"
	}
	if selected == "" {
		return configured
	}
	if configured == selected {
		return selected
	}
	return configured + " -> " + selected
}

func probeLocalVirtualRouterRuntimeBridgeRole(cfg probeVirtualRouterRuntimeConfig) string {
	if cfg.dialer {
		return probeRouteBridgeRoleToNext
	}
	return probeRouteBridgeRoleToPrev
}

func probeLocalVirtualRouterFrameLinkStatusPayloads() []map[string]any {
	probeVirtualRouterFrameLinkState.mu.Lock()
	links := make([]*probeVirtualRouterFrameLink, 0, len(probeVirtualRouterFrameLinkState.links))
	for _, link := range probeVirtualRouterFrameLinkState.links {
		if link != nil {
			links = append(links, link)
		}
	}
	probeVirtualRouterFrameLinkState.mu.Unlock()
	sort.Slice(links, func(i, j int) bool {
		return strings.TrimSpace(links[i].key) < strings.TrimSpace(links[j].key)
	})
	out := make([]map[string]any, 0, len(links))
	for _, link := range links {
		out = append(out, probeLocalVirtualRouterFrameLinkStatusPayload(link))
	}
	return out
}

func probeLocalVirtualRouterFrameLinkStatusPayload(link *probeVirtualRouterFrameLink) map[string]any {
	if link == nil {
		return map[string]any{}
	}
	now := time.Now()
	txDepth, txCap, txControlDepth, txControlCap, txBusinessDepth, txBusinessCap, txBulkDepth, txBulkCap := link.txQueueSnapshot()
	txLastWrite, txWriteEMA, txLastBatchFrames, txLastBatchBytes := link.txWriteSnapshot()
	rxDepth, rxCap := 0, 0
	rxEntryDepth, rxEntryCap, rxDispatchDepth, rxDispatchCap, rxDispatchWorkers := link.rxQueueSnapshot()
	rxDepth = rxEntryDepth + rxDispatchDepth
	rxCap = rxEntryCap + rxDispatchCap
	link.mu.Lock()
	key := strings.TrimSpace(link.key)
	requestPath := append([]string(nil), link.requestPath...)
	openedAt := link.openedAt
	lastUsed := link.lastUsed
	token := link.carrier
	rt := link.runtime
	closed := isProbeVirtualRouterFrameLinkClosed(link)
	link.mu.Unlock()

	item := map[string]any{
		"key":                  key,
		"route_id":             probeVirtualRouterRuntimeLogRouteID(rt),
		"path":                 cleanProbeVirtualRouterPath(requestPath),
		"path_text":            strings.Join(cleanProbeVirtualRouterPath(requestPath), ">"),
		"opened_at":            probeLocalVirtualRouterTimeString(openedAt),
		"last_used_at":         probeLocalVirtualRouterTimeString(lastUsed),
		"last_used_ms":         probeDurationMilliseconds(now.Sub(lastUsed)),
		"closed":               closed,
		"carrier":              false,
		"tx_queue":             txDepth,
		"tx_capacity":          txCap,
		"tx_control_queue":     txControlDepth,
		"tx_control_capacity":  txControlCap,
		"tx_business_queue":    txBusinessDepth,
		"tx_business_capacity": txBusinessCap,
		"tx_bulk_queue":        txBulkDepth,
		"tx_bulk_capacity":     txBulkCap,
		"tx_last_write_ms":     probeDurationMilliseconds(txLastWrite),
		"tx_write_ema_ms":      probeDurationMilliseconds(txWriteEMA),
		"tx_last_batch_frames": txLastBatchFrames,
		"tx_last_batch_bytes":  txLastBatchBytes,
		"rx_queue":             rxDepth,
		"rx_capacity":          rxCap,
		"rx_entry_queue":       rxEntryDepth,
		"rx_entry_capacity":    rxEntryCap,
		"rx_dispatch_queue":    rxDispatchDepth,
		"rx_dispatch_capacity": rxDispatchCap,
		"rx_dispatch_workers":  rxDispatchWorkers,
	}
	if rt != nil {
		item["peer_node_id"] = normalizeProbeRouteNodeID(rt.cfg.peerNodeID)
		item["peer_node_name"] = strings.TrimSpace(rt.cfg.peerName)
		item["bridge_role"] = probeLocalVirtualRouterRuntimeBridgeRole(rt.cfg)
	}
	if token == nil {
		return item
	}
	token.mu.Lock()
	sessionID := strings.TrimSpace(token.sessionID)
	remoteAddr := strings.TrimSpace(token.remoteAddr)
	connectedAt := token.connectedAt
	lastReadAt := token.lastReadAt
	lastWriteAt := token.lastWriteAt
	carrierClosed := false
	if token.done != nil {
		select {
		case <-token.done:
			carrierClosed = true
		default:
		}
	}
	token.mu.Unlock()
	item["carrier"] = !carrierClosed
	item["carrier_closed"] = carrierClosed
	item["carrier_session_id"] = sessionID
	item["remote_addr"] = remoteAddr
	item["connected_at"] = probeLocalVirtualRouterTimeString(connectedAt)
	item["connected_ms"] = probeDurationMilliseconds(now.Sub(connectedAt))
	item["rx_idle_ms"] = probeDurationMilliseconds(now.Sub(lastReadAt))
	item["tx_idle_ms"] = probeDurationMilliseconds(now.Sub(lastWriteAt))
	return item
}

func probeLocalVirtualRouterRuleRuntimeStatusPayloads() []map[string]any {
	probeVirtualRouterRuleRuntimeState.mu.RLock()
	items := make([]*probeVirtualRouterRuleRuntime, 0, len(probeVirtualRouterRuleRuntimeState.items))
	for _, item := range probeVirtualRouterRuleRuntimeState.items {
		if item != nil {
			cp := *item
			items = append(items, &cp)
		}
	}
	probeVirtualRouterRuleRuntimeState.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		return strings.TrimSpace(items[i].RouteID) < strings.TrimSpace(items[j].RouteID)
	})
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"rule_id":             strings.TrimSpace(item.RuleID),
			"rule_name":           strings.TrimSpace(item.RuleName),
			"route_id":            strings.TrimSpace(item.RouteID),
			"from_node_id":        normalizeProbeRouteNodeID(item.FromNodeID),
			"to_node_id":          normalizeProbeRouteNodeID(item.ToNodeID),
			"local_node_id":       normalizeProbeRouteNodeID(item.LocalNodeID),
			"peer_node_id":        normalizeProbeRouteNodeID(item.PeerNodeID),
			"local_ip":            strings.TrimSpace(item.LocalIP),
			"peer_ip":             strings.TrimSpace(item.PeerIP),
			"local_service_port":  item.LocalServicePort,
			"peer_service_domain": strings.TrimSpace(item.PeerServiceDomain),
			"peer_service_port":   item.PeerServicePort,
			"dialer":              item.Dialer,
			"listen_host":         strings.TrimSpace(item.ListenHost),
			"listen_port":         item.ListenPort,
			"peer_host":           strings.TrimSpace(item.PeerHost),
			"peer_port":           item.PeerPort,
			"status":              strings.TrimSpace(item.Status),
			"updated_at":          probeLocalVirtualRouterTimeString(item.UpdatedAt),
		})
	}
	return out
}

func probeLocalVirtualRouterCarrierCount(items []map[string]any) int {
	count := 0
	for _, item := range items {
		if carrier, _ := item["carrier"].(bool); carrier {
			count++
		}
	}
	return count
}

func probeLocalVirtualRouterRecentPacketSummaryPayload(items []probeVirtualRouterRecentPacket) map[string]any {
	errorCount := 0
	dropCount := 0
	forwardCount := 0
	deliverCount := 0
	var latestError probeVirtualRouterRecentPacket
	for _, item := range items {
		action := strings.TrimSpace(item.Action)
		if strings.TrimSpace(item.Error) != "" || strings.Contains(action, "error") {
			errorCount++
			if latestError.ID == 0 {
				latestError = item
			}
		}
		if action == "drop" {
			dropCount++
		}
		if action == "forward" {
			forwardCount++
		}
		if action == "deliver" || action == "fake_exit" || action == "local_icmp" {
			deliverCount++
		}
	}
	payload := map[string]any{
		"count":                 len(items),
		"capacity":              probeVirtualRouterRecentPacketLimit,
		"error_count":           errorCount,
		"drop_count":            dropCount,
		"forward_count":         forwardCount,
		"deliver_count":         deliverCount,
		"monitor_dropped_total": probeVirtualRouterRecentPacketMonitorDroppedTotal(),
	}
	if latestError.ID != 0 {
		payload["latest_error"] = latestError
	}
	return payload
}

func probeLocalVirtualRouterTimeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func probeLocalVirtualRouterPacketsHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items := snapshotProbeVirtualRouterRecentPackets()
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    items,
		"count":    len(items),
		"capacity": probeVirtualRouterRecentPacketLimit,
	})
}

func probeLocalVirtualRouterConnectionsHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items := snapshotProbeVirtualRouterRecentConnections()
	writeJSON(w, http.StatusOK, map[string]any{
		"items":             items,
		"count":             len(items),
		"retention_seconds": int(probeVirtualRouterRecentConnectionTTL / time.Second),
	})
}

func probeLocalVirtualRouterRouteTestHandler(w http.ResponseWriter, r *http.Request) {
	probeLocalVirtualRouterRouteTestHandlerWithCurl(w, r, false)
}

func probeLocalVirtualRouterRouteTestCurlHandler(w http.ResponseWriter, r *http.Request) {
	probeLocalVirtualRouterRouteTestHandlerWithCurl(w, r, true)
}

func probeLocalVirtualRouterRouteTestSpeedHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if r.Method == http.MethodGet {
		requestID := strings.TrimSpace(r.URL.Query().Get("request_id"))
		if requestID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request_id is required"})
			return
		}
		result, ok := getProbeVirtualRouterRouteSpeedTestRun(requestID)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "route speed test not found"})
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TargetNodeID string `json:"target_node_id"`
		MaxBytes     int64  `json:"max_bytes,omitempty"`
		MaxSeconds   int    `json:"max_seconds,omitempty"`
		Async        bool   `json:"async,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	targetNodeID := normalizeProbeRouteNodeID(req.TargetNodeID)
	if targetNodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target_node_id is required"})
		return
	}
	if req.Async {
		result := startProbeVirtualRouterRouteSpeedTest(targetNodeID, req.MaxBytes, req.MaxSeconds)
		writeJSON(w, http.StatusOK, result)
		return
	}
	result := runProbeVirtualRouterRouteSpeedTest(targetNodeID, req.MaxBytes, req.MaxSeconds)
	writeJSON(w, http.StatusOK, result)
}

func probeLocalVirtualRouterDiagnosticHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if r.Method == http.MethodGet {
		requestID := strings.TrimSpace(r.URL.Query().Get("request_id"))
		if requestID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request_id is required"})
			return
		}
		result, ok := getProbeVirtualRouterRouteTestRun(requestID)
		if !ok || !result.Detailed {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "virtual router diagnostic not found"})
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Target    string `json:"target"`
		Port      int    `json:"port,omitempty"`
		TimeoutMS int    `json:"timeout_ms,omitempty"`
		Samples   int    `json:"samples,omitempty"`
		Async     bool   `json:"async,omitempty"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Target) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target is required"})
		return
	}
	samples := normalizeProbeVirtualRouterRouteDiagnosticSamples(req.Samples)
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	if req.Async {
		result := startProbeVirtualRouterRouteDiagnostic(req.Target, req.Port, timeout, samples)
		writeJSON(w, http.StatusOK, result)
		return
	}
	result := runProbeVirtualRouterRouteDiagnostic(req.Target, req.Port, timeout, samples)
	writeJSON(w, http.StatusOK, result)
}

func probeLocalVirtualRouterRouteTestHandlerWithCurl(w http.ResponseWriter, r *http.Request, withCurl bool) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if r.Method == http.MethodGet {
		requestID := strings.TrimSpace(r.URL.Query().Get("request_id"))
		if requestID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request_id is required"})
			return
		}
		result, ok := getProbeVirtualRouterRouteTestRun(requestID)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "route test not found"})
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Target    string `json:"target"`
		Port      int    `json:"port,omitempty"`
		TimeoutMS int    `json:"timeout_ms,omitempty"`
		Async     bool   `json:"async,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Target) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target is required"})
		return
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = probeVirtualRouterRouteTestTimeout
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	if req.Async {
		result := startProbeVirtualRouterRouteTestWithCurl(req.Target, req.Port, timeout, withCurl)
		writeJSON(w, http.StatusOK, result)
		return
	}
	result := runProbeVirtualRouterRouteTestWithCurl(req.Target, req.Port, timeout, withCurl)
	writeJSON(w, http.StatusOK, result)
}

func probeLocalSystemUpgradeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalRouteReadBodyMaxLen)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var req probeLocalSystemUpgradeRequest
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "direct"
	}
	if mode != "direct" && mode != "proxy" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be direct or proxy"})
		return
	}
	runtimeContext := currentprobeLocalRouteRuntimeContext()
	if mode == "proxy" && strings.TrimSpace(runtimeContext.ControllerBaseURL) == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "controller base url is empty"})
		return
	}
	repo := strings.TrimSpace(req.ReleaseRepo)
	reportProbeLocalUpgradeProgress(probeLocalUpgradeRuntimeState{
		Status:      "accepted",
		Step:        "accepted",
		Progress:    0,
		Message:     "升级任务已提交",
		Mode:        mode,
		ReleaseRepo: repo,
	})
	go probeLocalRunUpgrade(probeControlMessage{
		Type:              "upgrade",
		Mode:              mode,
		ReleaseRepo:       repo,
		ControllerBaseURL: strings.TrimSpace(runtimeContext.ControllerBaseURL),
	}, runtimeContext.Identity)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"accepted":     true,
		"mode":         mode,
		"release_repo": repo,
	})
}

func probeLocalSystemUpgradeCheckHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalRouteReadBodyMaxLen)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var req probeLocalSystemUpgradeRequest
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "direct"
	}
	if mode != "direct" && mode != "proxy" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be direct or proxy"})
		return
	}
	runtimeContext := currentprobeLocalRouteRuntimeContext()
	if mode == "proxy" && strings.TrimSpace(runtimeContext.ControllerBaseURL) == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "controller base url is empty"})
		return
	}
	repo := strings.TrimSpace(req.ReleaseRepo)
	if repo == "" {
		repo = "fengzhanhuaer/CloudHelper"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	release, err := probeLocalFetchRelease(ctx, mode, repo, strings.TrimSpace(runtimeContext.ControllerBaseURL), runtimeContext.Identity)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(err.Error())})
		return
	}
	result := probeLocalUpgradeCheckResult{
		OK:             true,
		CurrentVersion: BuildVersion,
		LatestVersion:  strings.TrimSpace(release.TagName),
		Upgradeable:    normalizeVersionTag(release.TagName) != normalizeVersionTag(BuildVersion),
		Mode:           mode,
		ReleaseRepo:    repo,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	if asset, assetErr := pickProbeNodeAsset(release.Assets, detectRuntimePlatformInfo()); assetErr != nil {
		result.AssetError = strings.TrimSpace(assetErr.Error())
	} else {
		result.AssetName = strings.TrimSpace(asset.Name)
	}
	writeJSON(w, http.StatusOK, result)
}

func probeLocalSystemUpgradeStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, currentProbeLocalUpgradeState())
}

func probeLocalSystemRouteAuthBlacklistHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"items":   listProbeRouteAuthBlacklistEntries(),
			"content": probeRouteAuthBlacklistContent(),
		})
	case http.MethodPost:
		body := http.MaxBytesReader(w, r.Body, probeLocalRouteReadBodyMaxLen)
		defer body.Close()
		decoder := json.NewDecoder(body)
		decoder.DisallowUnknownFields()
		var req probeLocalRouteAuthBlacklistSaveRequest
		if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		ips := req.IPs
		if len(ips) == 0 {
			parsed, err := parseProbeRouteAuthBlacklistContent(req.Content)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			ips = parsed
		} else {
			ips = normalizeProbeRouteAuthBlacklistIPs(ips)
		}
		if err := setProbeRouteAuthBlacklistManualIPs(ips, true); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"items":   listProbeRouteAuthBlacklistEntries(),
			"content": probeRouteAuthBlacklistContent(),
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func probeLocalSystemRestartHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"accepted": true,
	})
	go func() {
		time.Sleep(200 * time.Millisecond)
		prepareProbeLocalProcessRestart()
		if err := probeLocalRestartProcess(""); err != nil {
			logProbeErrorf("probe local restart failed: %v", err)
		}
	}()
}

func probeLocalShellExecHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, 64*1024)
	defer body.Close()
	var req probeLocalShellExecRequest
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	commandText := strings.TrimSpace(req.Command)
	if commandText == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "command is required"})
		return
	}
	timeoutSec := normalizeProbeShellTimeoutSec(req.TimeoutSec)
	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	command := buildProbeShellCommand(ctx, commandText)
	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	command.Stdout = &stdoutBuf
	command.Stderr = &stderrBuf
	runErr := command.Run()

	finishedAt := time.Now()
	exitCode := 0
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	} else if runErr != nil {
		exitCode = -1
	}
	result := map[string]any{
		"ok":          runErr == nil,
		"command":     commandText,
		"exit_code":   exitCode,
		"stdout":      truncateProbeShellOutput(stdoutBuf.String(), maxProbeShellOutputBytes),
		"stderr":      truncateProbeShellOutput(stderrBuf.String(), maxProbeShellOutputBytes),
		"started_at":  startedAt.UTC().Format(time.RFC3339),
		"finished_at": finishedAt.UTC().Format(time.RFC3339),
		"duration_ms": finishedAt.Sub(startedAt).Milliseconds(),
	}
	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result["error"] = fmt.Sprintf("command timeout after %ds", timeoutSec)
		} else {
			result["error"] = runErr.Error()
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func probeLocalShellStreamHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, 64*1024)
	defer body.Close()
	var req probeLocalShellExecRequest
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	commandText := strings.TrimSpace(req.Command)
	if commandText == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "command is required"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming is unavailable"})
		return
	}

	timeoutSec := normalizeProbeShellTimeoutSec(req.TimeoutSec)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	command := buildProbeShellCommand(ctx, commandText)
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "open stdout failed: " + err.Error()})
		return
	}
	stderrPipe, err := command.StderrPipe()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "open stderr failed: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	encoder := json.NewEncoder(w)
	var writeMu sync.Mutex
	sendProbeLocalShellStreamEvent := func(event map[string]any) {
		writeMu.Lock()
		_ = encoder.Encode(event)
		flusher.Flush()
		writeMu.Unlock()
	}

	startedAt := time.Now()
	sendProbeLocalShellStreamEvent(map[string]any{
		"type":       "start",
		"ok":         true,
		"command":    commandText,
		"started_at": startedAt.UTC().Format(time.RFC3339),
		"timeout":    timeoutSec,
	})
	if err := command.Start(); err != nil {
		sendProbeLocalShellStreamEvent(map[string]any{"type": "done", "ok": false, "error": "start command failed: " + err.Error(), "exit_code": -1})
		return
	}

	var readers sync.WaitGroup
	readStream := func(name string, reader io.Reader) {
		defer readers.Done()
		buf := make([]byte, 4096)
		for {
			n, readErr := reader.Read(buf)
			if n > 0 {
				sendProbeLocalShellStreamEvent(map[string]any{
					"type":   "chunk",
					"stream": name,
					"data":   string(buf[:n]),
				})
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					sendProbeLocalShellStreamEvent(map[string]any{
						"type":   "chunk",
						"stream": "stderr",
						"data":   name + " read failed: " + readErr.Error() + "\n",
					})
				}
				return
			}
		}
	}
	readers.Add(2)
	go readStream("stdout", stdoutPipe)
	go readStream("stderr", stderrPipe)

	waitErr := command.Wait()
	readers.Wait()
	finishedAt := time.Now()
	exitCode := 0
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	} else if waitErr != nil {
		exitCode = -1
	}
	done := map[string]any{
		"type":        "done",
		"ok":          waitErr == nil,
		"exit_code":   exitCode,
		"started_at":  startedAt.UTC().Format(time.RFC3339),
		"finished_at": finishedAt.UTC().Format(time.RFC3339),
		"duration_ms": finishedAt.Sub(startedAt).Milliseconds(),
	}
	if waitErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			done["error"] = fmt.Sprintf("command timeout after %ds", timeoutSec)
		} else if errors.Is(ctx.Err(), context.Canceled) {
			done["error"] = "command canceled"
		} else {
			done["error"] = waitErr.Error()
		}
	}
	sendProbeLocalShellStreamEvent(done)
}

func prepareProbeLocalProcessRestart() {
	logProbeInfof("probe local restart preparing: closing listeners")
	stopProbeVRouteProxyRuntime()
	_ = stopProbeVirtualRouterTUNDataPlane()
	stopProbeLocalConsoleServer("process restart")
	time.Sleep(300 * time.Millisecond)
}

func probeLocalSyncStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	settings, err := loadProbeSyncSettings()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, probeLocalSyncStatusPayload(settings))
}

func probeLocalSyncSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	defer body.Close()
	var req probeLocalSyncSettingsRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	settings, err := updateProbeSyncSettings(req.Enabled, req.SourcePaths, req.LocalTempDir, req.Schedule, req.GoogleClientID, req.GoogleClientSecret, req.GoogleFolder)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, probeLocalSyncStatusPayload(settings))
}

func probeLocalSyncTestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	settings, err := loadProbeSyncSettings()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	token, err := ensureProbeSyncGoogleAccessToken(settings)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if _, err := ensureProbeSyncGoogleDriveFolderPath(token.AccessToken, probeSyncRemoteFolderPath(settings.GoogleFolder, currentProbeSyncNodeID())); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func probeLocalSyncRunHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	identity := nodeIdentity{NodeID: currentProbeSyncNodeID()}
	accepted := triggerProbeSyncAsync(identity, "local_manual")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": accepted,
		"runtime":  getProbeSyncRuntimeStatus(),
	})
}

func probeLocalSyncGoogleAuthStartHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	defer body.Close()
	var req probeLocalSyncSettingsRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	current, _ := loadProbeSyncSettings()
	normalizeProbeSyncSubmittedGoogleOAuthCredentials(&req.GoogleClientID, &req.GoogleClientSecret, current.GoogleClientSecret)
	session, err := startProbeSyncGoogleDeviceAuth(req.GoogleClientID, req.GoogleClientSecret)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(req.GoogleClientID) != "" || strings.TrimSpace(req.GoogleClientSecret) != "" {
		current.GoogleClientID = strings.TrimSpace(req.GoogleClientID)
		current.GoogleClientSecret = strings.TrimSpace(req.GoogleClientSecret)
		_ = persistProbeSyncSettings(current)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":                session.ID,
		"user_code":                 session.UserCode,
		"verification_url":          session.VerifyURL,
		"verification_url_complete": session.CompleteURL,
		"expires_at":                session.ExpiresAt.UTC().Format(time.RFC3339),
		"interval_sec":              session.IntervalSec,
	})
}

func probeLocalSyncGoogleAuthPollHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, 64*1024)
	defer body.Close()
	var req probeLocalSyncGoogleAuthPollRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	authorized, status, err := pollProbeSyncGoogleDeviceAuth(req.SessionID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"authorized": false, "status": firstNonEmpty(status, "error"), "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authorized": authorized, "status": status})
}

func probeLocalSyncGoogleDisconnectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if err := clearProbeSyncGoogleToken(); err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func probeLocalSyncStatusPayload(settings probeSyncSettings) map[string]any {
	nodeID := currentProbeSyncNodeID()
	return map[string]any{
		"settings": map[string]any{
			"enabled":                 settings.Enabled,
			"source_paths":            settings.SourcePaths,
			"local_temp_dir":          settings.LocalTempDir,
			"schedule":                normalizeProbeSyncSchedule(settings.Schedule),
			"google_client_id":        settings.GoogleClientID,
			"google_client_secret":    probeSyncSecretConfiguredLabel(settings.GoogleClientSecret),
			"google_folder":           settings.GoogleFolder,
			"google_authorized":       settings.GoogleToken.hasRefreshToken(),
			"google_token_expires_at": probeSyncGoogleTokenExpiryString(settings.GoogleToken),
			"last_attempt_at":         settings.LastAttemptAt,
			"last_success_at":         settings.LastSuccessAt,
			"last_error":              settings.LastError,
			"last_archive_name":       settings.LastArchiveName,
		},
		"runtime": getProbeSyncRuntimeStatus(),
		"paths": map[string]any{
			"node_id":       nodeID,
			"remote_folder": probeSyncRemoteFolderPath(settings.GoogleFolder, nodeID),
		},
	}
}

func probeLocalAuthDataFilePath() (string, error) {
	path, err := resolveProbeLocalAuthStorePath()
	if err != nil {
		return "", err
	}
	return path, nil
}

func resetProbeLocalAuthManagerForTest() {
	probeLocalAuthInitMu.Lock()
	probeLocalAuthInstance = nil
	probeLocalAuthInitMu.Unlock()
	resetProbeLocalSetupTokenForTest()
}

func resetProbeLocalControlStateForTest() {
	clearProbeLocalTUNInstallObservation()
	probeLocalConsoleRefreshState.mu.Lock()
	probeLocalConsoleRefreshState.running = false
	probeLocalConsoleRefreshState.lastStartedAt = ""
	probeLocalConsoleRefreshState.lastFinishedAt = ""
	probeLocalConsoleRefreshState.lastError = ""
	probeLocalConsoleRefreshState.mu.Unlock()
	probeLocalControl = newProbeLocalControlManager()
}

func resetprobeLocalRouteHooksForTest() {
	probeLocalTUNRouteFeatureEnabled = func() bool { return false }
	probeLocalLookupIPv4ForBypass = lookupProbeLocalIPv4ForBypass
	probeLocalRouteRelaySpeedDebugFetch = probeRouteRelayFetchSpeedDebugDefault
	probeLocalStartCFIPOptimizeTask = func(fn func()) { go fn() }
}

func resetProbeLocalTUNHooksForTest() {
	probeLocalInstallTUNDriver = installProbeLocalTUNDriver
	probeLocalCheckTUNReadyAfterInstall = probeLocalNoopPostInstallTUNReadyCheck
	probeLocalResetTUNDetectInstalledHook()
	probeLocalUninstallTUNDriver = uninstallProbeLocalTUNDriver
	probeVirtualRouterApplySystemDNS = applyProbeVirtualRouterSystemDNS
	probeVirtualRouterRestoreSystemDNS = restoreProbeVirtualRouterSystemDNS
	resetProbeVirtualRouterTUNDataPlaneHooksForTest()
}

func resetProbeLocalUpgradeHooksForTest() {
	probeLocalRunUpgrade = runProbeUpgrade
	probeLocalFetchRelease = fetchProbeRelease
	probeLocalRestartProcess = restartCurrentProcess
	resetProbeLocalUpgradeRuntimeStateForTest()
}

func setprobeLocalRouteRuntimeContext(identity nodeIdentity, controllerBaseURL string) {
	probeLocalRuntimeState.mu.Lock()
	probeLocalRuntimeState.context = probeLocalRouteRuntimeContext{
		Identity:          identity,
		ControllerBaseURL: strings.TrimSpace(controllerBaseURL),
	}
	probeLocalRuntimeState.mu.Unlock()
}

func currentprobeLocalRouteRuntimeContext() probeLocalRouteRuntimeContext {
	probeLocalRuntimeState.mu.RLock()
	defer probeLocalRuntimeState.mu.RUnlock()
	return probeLocalRuntimeState.context
}

func reportProbeLocalUpgradeProgress(state probeLocalUpgradeRuntimeState) {
	now := time.Now().UTC().Format(time.RFC3339)
	state.Status = strings.TrimSpace(strings.ToLower(state.Status))
	if state.Status == "" {
		state.Status = "running"
	}
	if state.Progress < 0 {
		state.Progress = 0
	}
	if state.Progress > 100 {
		state.Progress = 100
	}
	state.Step = strings.TrimSpace(state.Step)
	state.Message = strings.TrimSpace(state.Message)
	state.Error = strings.TrimSpace(state.Error)
	state.Mode = strings.TrimSpace(strings.ToLower(state.Mode))
	state.ReleaseRepo = strings.TrimSpace(state.ReleaseRepo)
	state.UpdatedAt = now

	probeLocalUpgradeState.mu.Lock()
	probeLocalUpgradeState.state = state
	probeLocalUpgradeState.mu.Unlock()
}

func reportProbeLocalUpgradeSuccess(message, mode, repo string) {
	reportProbeLocalUpgradeProgress(probeLocalUpgradeRuntimeState{
		Status:      "succeeded",
		Step:        "done",
		Progress:    100,
		Message:     strings.TrimSpace(message),
		Mode:        strings.TrimSpace(strings.ToLower(mode)),
		ReleaseRepo: strings.TrimSpace(repo),
	})
}

func reportProbeLocalUpgradeFailed(step string, err error, mode, repo string, progress int) {
	errText := ""
	if err != nil {
		errText = strings.TrimSpace(err.Error())
	}
	reportProbeLocalUpgradeProgress(probeLocalUpgradeRuntimeState{
		Status:      "failed",
		Step:        strings.TrimSpace(step),
		Progress:    progress,
		Message:     "升级失败",
		Error:       errText,
		Mode:        strings.TrimSpace(strings.ToLower(mode)),
		ReleaseRepo: strings.TrimSpace(repo),
	})
}

func currentProbeLocalUpgradeState() probeLocalUpgradeRuntimeState {
	probeLocalUpgradeState.mu.RLock()
	defer probeLocalUpgradeState.mu.RUnlock()
	return probeLocalUpgradeState.state
}

func resetProbeLocalUpgradeRuntimeStateForTest() {
	reportProbeLocalUpgradeProgress(probeLocalUpgradeRuntimeState{
		Status:   "idle",
		Progress: 0,
		Message:  "尚未触发升级",
	})
}

func currentProbeLocalConsoleListen() string {
	probeLocalConsoleState.mu.Lock()
	defer probeLocalConsoleState.mu.Unlock()
	return strings.TrimSpace(probeLocalConsoleState.listenAddr)
}

func resolveProbeLocalConsoleURL() string {
	addr := strings.TrimSpace(currentProbeLocalConsoleListen())
	if addr == "" {
		addr = probeLocalListenAddrDefault
	}
	return fmt.Sprintf("http://%s", addr)
}
