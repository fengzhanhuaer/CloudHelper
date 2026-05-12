package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeLocalListenAddrDefault = "127.0.0.1:16032"

	probeLocalAuthStoreFile      = "probe_local_auth.json"
	probeLocalSessionCookieName  = "probe_local_session"
	probeLocalSessionTTL         = 30 * 24 * time.Hour
	probeLocalMinPasswordLength  = 8
	probeLocalMaxPasswordLength  = 128
	probeLocalMaxUsernameLength  = 64
	probeLocalAuthReadBodyMaxLen = 64 * 1024

	probeLocalProxyModeDirect = "direct"
	probeLocalProxyModeTUN    = "tunnel"

	probeLocalProxyGroupFileName  = "proxy_group.json"
	probeLocalProxyStateFileName  = "proxy_state.json"
	probeLocalProxyHostFileName   = "proxy_host.txt"
	probeLocalProxyChainFileName  = "proxy_chain.json"
	probeLocalProxyBackupAPIPath  = "/api/probe/proxy_group/backup"
	probeLocalProxyReadBodyMaxLen = 512 * 1024
)

type probeLocalAuthState struct {
	Registered   bool   `json:"registered"`
	Username     string `json:"username,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
	PasswordSalt string `json:"password_salt,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

type probeLocalSessionState struct {
	Username  string
	ExpiresAt time.Time
}

type probeLocalAuthManager struct {
	mu sync.RWMutex

	state    probeLocalAuthState
	sessions map[string]probeLocalSessionState
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
	LastError              string                           `json:"last_error,omitempty"`
	InstallObservation     *probeLocalTUNInstallObservation `json:"install_observation,omitempty"`
	LastInstallObservation *probeLocalTUNInstallObservation `json:"last_install_observation,omitempty"`
	UpdatedAt              string                           `json:"updated_at,omitempty"`
}

type probeLocalProxyRuntimeState struct {
	Enabled   bool   `json:"enabled"`
	Mode      string `json:"mode"`
	LastError string `json:"last_error,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type probeLocalControlManager struct {
	mu    sync.RWMutex
	tun   probeLocalTunRuntimeState
	proxy probeLocalProxyRuntimeState
}

type probeLocalProxyGroupEntry struct {
	Group     string   `json:"group"`
	Rules     []string `json:"rules,omitempty"`
	RulesText string   `json:"rules_text,omitempty"`
}

type probeLocalProxyGroupFile struct {
	Version         int                         `json:"version"`
	DNSServers      []string                    `json:"dns_servers,omitempty"`
	DoTServers      []string                    `json:"dot_servers,omitempty"`
	DoHServers      []string                    `json:"doh_servers,omitempty"`
	DoHProxyServers []string                    `json:"doh_proxy_servers,omitempty"`
	FakeIPCIDR      string                      `json:"fake_ip_cidr,omitempty"`
	LegacyTUN       json.RawMessage             `json:"tun,omitempty"`
	Groups          []probeLocalProxyGroupEntry `json:"groups"`
	Note            string                      `json:"note,omitempty"`
}

type probeLocalProxyStateGroupEntry struct {
	Group           string `json:"group"`
	Action          string `json:"action,omitempty"`
	SelectedChainID string `json:"selected_chain_id,omitempty"`
	TunnelNodeID    string `json:"tunnel_node_id,omitempty"`
	RuntimeStatus   string `json:"runtime_status,omitempty"`
}

type probeLocalProxyBackupState struct {
	LastUploadedAt   string `json:"last_uploaded_at,omitempty"`
	LastUploadStatus string `json:"last_upload_status,omitempty"`
	LastUploadError  string `json:"last_upload_error,omitempty"`
}

type probeLocalTUNPersistentState struct {
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type probeLocalProxyStateFile struct {
	Version   int                              `json:"version"`
	UpdatedAt string                           `json:"updated_at"`
	Groups    []probeLocalProxyStateGroupEntry `json:"groups"`
	Backup    probeLocalProxyBackupState       `json:"backup"`
	TUN       probeLocalTUNPersistentState     `json:"tun"`
}

type probeLocalHostMapping struct {
	DNS string `json:"dns"`
	IP  string `json:"ip"`
}

type probeLocalProxyRuntimeContext struct {
	Identity          nodeIdentity
	ControllerBaseURL string
}

type probeLocalUpgradeRuntimeState struct {
	Status      string `json:"status"`
	Step        string `json:"step,omitempty"`
	Progress    int    `json:"progress"`
	Message     string `json:"message,omitempty"`
	Error       string `json:"error,omitempty"`
	Mode        string `json:"mode,omitempty"`
	ReleaseRepo string `json:"release_repo,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
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
	errProbeLocalProxyUnsupported         = errors.New("probe local proxy takeover is not supported on this platform")
	errProbeLocalTUNUnsupported           = errors.New("probe local tun install is not supported on this platform")
	probeLocalInstallTUNDriver            = installProbeLocalTUNDriver
	probeLocalCheckTUNReadyAfterInstall   = probeLocalNoopPostInstallTUNReadyCheck
	probeLocalDetectTUNInstalled          = defaultProbeLocalDetectTUNInstalled
	probeLocalResetTUNDetectInstalledHook = func() { probeLocalDetectTUNInstalled = defaultProbeLocalDetectTUNInstalled }
	probeLocalApplyProxyTakeover          = applyProbeLocalProxyTakeover
	probeLocalRestoreProxyDirect          = restoreProbeLocalProxyDirect
	probeLocalEnsureExplicitDirectBypass  = ensureProbeLocalExplicitDirectBypassForTarget
	probeLocalRunUpgrade                  = runProbeUpgrade
	probeLocalRestartProcess              = restartCurrentProcess
	probeLocalRefreshProxyChainCache      = refreshProbeProxyChainCacheFromController
)

func newProbeLocalControlManager() *probeLocalControlManager {
	now := time.Now().UTC().Format(time.RFC3339)
	return &probeLocalControlManager{
		tun: probeLocalTunRuntimeState{
			Platform:  runtime.GOOS,
			Installed: false,
			Enabled:   false,
			UpdatedAt: now,
		},
		proxy: probeLocalProxyRuntimeState{
			Enabled:   false,
			Mode:      probeLocalProxyModeDirect,
			UpdatedAt: now,
		},
	}
}

func resolveProbeLocalExplicitBypassTarget(host string, port int) (string, error) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return "", errors.New("bypass host is empty")
	}
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("invalid bypass port: %d", port)
	}
	if parsed := net.ParseIP(cleanHost); parsed != nil {
		return net.JoinHostPort(parsed.String(), strconv.Itoa(port)), nil
	}
	return net.JoinHostPort(cleanHost, strconv.Itoa(port)), nil
}

func appendProbeLocalExplicitBypassTarget(targets []string, seen map[string]struct{}, host string, port int) ([]string, error) {
	targetAddr, err := resolveProbeLocalExplicitBypassTarget(host, port)
	if err != nil {
		return targets, err
	}
	if _, ok := seen[targetAddr]; ok {
		return targets, nil
	}
	seen[targetAddr] = struct{}{}
	return append(targets, targetAddr), nil
}

func resolveProbeLocalExplicitBypassTargetsForChain(item probeLinkChainServerItem) ([]string, error) {
	route := buildChainRoute(item)
	seen := make(map[string]struct{}, len(route))
	targets := make([]string, 0, len(route))
	for _, nodeID := range route {
		hop := findHopConfigForNode(item, nodeID)
		host := strings.TrimSpace(hop.RelayHost)
		port := hop.ExternalPort
		if port <= 0 {
			port = hop.ListenPort
		}
		if host == "" || port <= 0 {
			continue
		}
		var err error
		targets, err = appendProbeLocalExplicitBypassTarget(targets, seen, host, port)
		if err != nil {
			return nil, fmt.Errorf("resolve chain bypass target failed: chain=%s node=%s err=%w", strings.TrimSpace(item.ChainID), strings.TrimSpace(nodeID), err)
		}
	}
	return targets, nil
}

func collectProbeLocalSelectedChainIDs(extraSelectedChainIDs ...string) ([]string, error) {
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(state.Groups)+len(extraSelectedChainIDs))
	chainIDs := make([]string, 0, len(state.Groups)+len(extraSelectedChainIDs))
	appendChainID := func(raw string) error {
		chainID, err := normalizeProbeLocalSelectedChainID(raw)
		if err != nil {
			return err
		}
		if chainID == "" {
			return nil
		}
		if _, ok := seen[chainID]; ok {
			return nil
		}
		seen[chainID] = struct{}{}
		chainIDs = append(chainIDs, chainID)
		return nil
	}
	for _, entry := range state.Groups {
		if !strings.EqualFold(strings.TrimSpace(entry.Action), "tunnel") {
			continue
		}
		if err := appendChainID(firstNonEmpty(strings.TrimSpace(entry.SelectedChainID), strings.TrimSpace(entry.TunnelNodeID))); err != nil {
			return nil, err
		}
	}
	for _, raw := range extraSelectedChainIDs {
		if err := appendChainID(raw); err != nil {
			return nil, err
		}
	}
	return chainIDs, nil
}

func resolveProbeLocalExplicitBypassTargetsForProxyEnable(extraSelectedChainIDs ...string) ([]string, error) {
	seen := make(map[string]struct{}, 8)
	targets := make([]string, 0, 8)
	runtimeContext := currentProbeLocalProxyRuntimeContext()
	if controllerBaseURL := strings.TrimSpace(runtimeContext.ControllerBaseURL); controllerBaseURL != "" {
		parsed, err := url.Parse(controllerBaseURL)
		if err != nil || parsed == nil || strings.TrimSpace(parsed.Host) == "" {
			return nil, fmt.Errorf("parse controller base url failed: %s", controllerBaseURL)
		}
		port := 0
		if rawPort := strings.TrimSpace(parsed.Port()); rawPort != "" {
			port, err = strconv.Atoi(rawPort)
			if err != nil {
				return nil, fmt.Errorf("invalid controller port: %s", rawPort)
			}
		} else if strings.EqualFold(strings.TrimSpace(parsed.Scheme), "http") {
			port = 80
		} else {
			port = 443
		}
		targets, err = appendProbeLocalExplicitBypassTarget(targets, seen, parsed.Hostname(), port)
		if err != nil {
			return nil, fmt.Errorf("resolve controller bypass target failed: %w", err)
		}
	}
	chainIDs, err := collectProbeLocalSelectedChainIDs(extraSelectedChainIDs...)
	if err != nil {
		return nil, err
	}
	if len(chainIDs) == 0 {
		return targets, nil
	}
	items, err := loadProbeLocalProxyChainItems()
	if err != nil {
		return nil, err
	}
	itemByChainID := make(map[string]probeLinkChainServerItem, len(items))
	for _, item := range items {
		itemByChainID[strings.TrimSpace(item.ChainID)] = item
	}
	for _, chainID := range chainIDs {
		item, ok := itemByChainID[strings.TrimSpace(chainID)]
		if !ok {
			return nil, fmt.Errorf("selected chain not found for bypass prewarm: %s", chainID)
		}
		chainTargets, err := resolveProbeLocalExplicitBypassTargetsForChain(item)
		if err != nil {
			return nil, err
		}
		for _, target := range chainTargets {
			if _, ok := seen[target]; ok {
				continue
			}
			seen[target] = struct{}{}
			targets = append(targets, target)
		}
	}
	return targets, nil
}

func ensureProbeLocalProxyBootstrapDirectBypass(extraSelectedChainIDs ...string) error {
	targets, err := resolveProbeLocalExplicitBypassTargetsForProxyEnable(extraSelectedChainIDs...)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := probeLocalEnsureExplicitDirectBypass(target); err != nil {
			return fmt.Errorf("ensure explicit direct bypass failed: target=%s err=%w", target, err)
		}
	}
	if len(targets) > 0 {
		logProbeInfof("probe local proxy explicit direct bypass prepared: targets=%s", strings.Join(targets, ", "))
	}
	return nil
}

func (m *probeLocalControlManager) tunStatus() probeLocalTunRuntimeState {
	m.mu.RLock()
	status := m.tun
	m.mu.RUnlock()
	stats := probeLocalTUNDataPlaneStatsSnapshot()
	status.DataPlane = stats.Running
	status.DataPlaneRX = stats.RXPackets
	status.DataPlaneBytes = stats.RXBytes
	return status
}

func persistProbeLocalTUNStateBestEffort(installed, enabled bool) {
	if err := persistProbeLocalTUNPersistentState(installed, enabled); err != nil {
		logProbeWarnf("probe local tun persist state failed: installed=%v enabled=%v err=%v", installed, enabled, err)
	}
}

func recoverProbeLocalTUNRuntimeOnStartup() error {
	return probeLocalControl.recoverTUNOnStartup()
}

func (m *probeLocalControlManager) recoverTUNOnStartup() error {
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return err
	}

	detectedInstalled, detectErr := probeLocalDetectTUNInstalled()
	if detectErr != nil && !errors.Is(detectErr, errProbeLocalTUNUnsupported) {
		logProbeWarnf("probe local tun startup detect failed: %v", detectErr)
	}
	installed := state.TUN.Installed || detectedInstalled
	persistedEnabled := state.TUN.Enabled
	now := time.Now().UTC().Format(time.RFC3339)

	m.mu.Lock()
	m.tun.Platform = runtime.GOOS
	m.tun.Installed = installed
	m.tun.Enabled = false
	m.tun.DataPlane = false
	m.tun.DataPlaneRX = 0
	m.tun.DataPlaneBytes = 0
	if installed {
		m.tun.LastError = ""
	} else if strings.TrimSpace(m.tun.LastError) == "" && detectErr != nil && !errors.Is(detectErr, errProbeLocalTUNUnsupported) {
		m.tun.LastError = strings.TrimSpace(detectErr.Error())
	}
	m.tun.UpdatedAt = now
	m.proxy.Enabled = false
	m.proxy.Mode = probeLocalProxyModeDirect
	m.proxy.UpdatedAt = now
	m.mu.Unlock()

	if installed != state.TUN.Installed || (!installed && state.TUN.Enabled) {
		persistProbeLocalTUNStateBestEffort(installed, installed && persistedEnabled)
	}
	if !installed || !persistedEnabled {
		if installed {
			logProbeInfof("probe local tun startup recovered installed state: enabled_restore=false")
		}
		return nil
	}

	if _, _, err := m.enableProxy(); err != nil {
		return fmt.Errorf("recover probe local tun enabled state failed: %w", err)
	}
	logProbeInfof("probe local tun startup recovered enabled state")
	return nil
}

func (m *probeLocalControlManager) proxyStatus() probeLocalProxyRuntimeState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.proxy
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
		logProbeWarnf("probe local tun post-install ready check failed elapsed=%s err=%v", time.Since(startedAt).String(), err)
		return m.tun, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: m.tun.LastError, Payload: buildProbeLocalTUNErrorPayload(wrappedErr)}
	}

	m.tun.Installed = true
	m.tun.LastError = ""
	persistProbeLocalTUNStateBestEffort(true, m.tun.Enabled)
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
	if strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_INDEX")) != "" {
		if err := startProbeLocalTUNDataPlane(); err != nil {
			m.tun.Enabled = false
			m.tun.DataPlane = false
			m.tun.DataPlaneRX = 0
			m.tun.DataPlaneBytes = 0
			m.tun.LastError = strings.TrimSpace(err.Error())
			m.tun.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			persistProbeLocalTUNStateBestEffort(true, false)
			return m.tun, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: m.tun.LastError}
		}
		stats := probeLocalTUNDataPlaneStatsSnapshot()
		m.tun.Enabled = stats.Running
		m.tun.DataPlane = stats.Running
		m.tun.DataPlaneRX = stats.RXPackets
		m.tun.DataPlaneBytes = stats.RXBytes
	}
	m.tun.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	persistProbeLocalTUNStateBestEffort(true, m.tun.Enabled)
	logProbeInfof("probe local tun install/check completed: installed=true elapsed=%s", time.Since(startedAt).String())
	return m.tun, nil
}

func (m *probeLocalControlManager) enableProxy() (probeLocalTunRuntimeState, probeLocalProxyRuntimeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.tun.Installed {
		m.proxy.LastError = "tun driver is not installed"
		m.proxy.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return m.tun, m.proxy, &probeLocalHTTPError{Status: http.StatusConflict, Message: m.proxy.LastError}
	}

	if err := probeLocalApplyProxyTakeover(); err != nil {
		m.proxy.LastError = strings.TrimSpace(err.Error())
		m.proxy.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		status := http.StatusInternalServerError
		if errors.Is(err, errProbeLocalProxyUnsupported) {
			status = http.StatusNotImplemented
		}
		return m.tun, m.proxy, &probeLocalHTTPError{Status: status, Message: m.proxy.LastError}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	m.tun.Enabled = true
	m.tun.LastError = ""
	m.tun.UpdatedAt = now
	m.proxy.Enabled = true
	m.proxy.Mode = probeLocalProxyModeTUN
	m.proxy.LastError = ""
	m.proxy.UpdatedAt = now

	if strings.TrimSpace(currentProbeLocalTUNDNSListenHost()) != "" {
		if err := startProbeLocalTUNDataPlane(); err != nil {
			_ = probeLocalRestoreProxyDirect()
			m.tun.Enabled = false
			m.tun.DataPlane = false
			m.tun.DataPlaneRX = 0
			m.tun.DataPlaneBytes = 0
			m.tun.LastError = strings.TrimSpace(err.Error())
			m.tun.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			m.proxy.Enabled = false
			m.proxy.Mode = probeLocalProxyModeDirect
			m.proxy.LastError = m.tun.LastError
			m.proxy.UpdatedAt = m.tun.UpdatedAt
			persistProbeLocalTUNStateBestEffort(m.tun.Installed, false)
			return m.tun, m.proxy, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: m.tun.LastError}
		}
	}
	stats := probeLocalTUNDataPlaneStatsSnapshot()
	m.tun.DataPlane = stats.Running
	m.tun.DataPlaneRX = stats.RXPackets
	m.tun.DataPlaneBytes = stats.RXBytes

	persistProbeLocalTUNStateBestEffort(m.tun.Installed, true)
	reconcileProbeLocalDNSRuntime()
	return m.tun, m.proxy, nil
}

func (m *probeLocalControlManager) directProxy() (probeLocalTunRuntimeState, probeLocalProxyRuntimeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := probeLocalRestoreProxyDirect(); err != nil {
		m.proxy.LastError = strings.TrimSpace(err.Error())
		m.proxy.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		status := http.StatusInternalServerError
		if errors.Is(err, errProbeLocalProxyUnsupported) {
			status = http.StatusNotImplemented
		}
		return m.tun, m.proxy, &probeLocalHTTPError{Status: status, Message: m.proxy.LastError}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	errStopDataPlane := stopProbeLocalTUNDataPlane()
	m.tun.Enabled = false
	m.tun.DataPlane = false
	m.tun.DataPlaneRX = 0
	m.tun.DataPlaneBytes = 0
	m.tun.UpdatedAt = now
	m.proxy.Enabled = false
	m.proxy.Mode = probeLocalProxyModeDirect
	m.proxy.LastError = ""
	m.proxy.UpdatedAt = now
	persistProbeLocalTUNStateBestEffort(m.tun.Installed, false)
	reconcileProbeLocalDNSRuntime()
	if errStopDataPlane != nil {
		m.tun.LastError = strings.TrimSpace(errStopDataPlane.Error())
		m.proxy.LastError = m.tun.LastError
		return m.tun, m.proxy, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: m.tun.LastError}
	}
	return m.tun, m.proxy, nil
}

var (
	probeLocalAuthInitMu   sync.Mutex
	probeLocalAuthInstance *probeLocalAuthManager
	probeLocalControl      = newProbeLocalControlManager()
)

var probeLocalRuntimeState = struct {
	mu      sync.RWMutex
	context probeLocalProxyRuntimeContext
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
		state:    state,
		sessions: make(map[string]probeLocalSessionState),
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
	state.UpdatedAt = strings.TrimSpace(state.UpdatedAt)
	if !state.Registered {
		return probeLocalAuthState{}, nil
	}
	if state.Username == "" || state.PasswordHash == "" || state.PasswordSalt == "" {
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
	return os.WriteFile(path, payload, 0o600)
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
		"registered": m.state.Registered,
	}
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

	salt := randomHexToken(16)
	next := probeLocalAuthState{
		Registered:   true,
		Username:     username,
		PasswordSalt: salt,
		PasswordHash: hashProbeLocalPassword(password, salt),
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if err := persistProbeLocalAuthState(next); err != nil {
		return err
	}
	m.state = next
	m.sessions = make(map[string]probeLocalSessionState)
	return nil
}

func (m *probeLocalAuthManager) login(username, password string) (string, probeLocalSessionState, error) {
	username = normalizeProbeLocalUsername(username)
	if username == "" || strings.TrimSpace(password) == "" {
		return "", probeLocalSessionState{}, &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "username and password are required"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.state.Registered {
		return "", probeLocalSessionState{}, &probeLocalHTTPError{Status: http.StatusForbidden, Message: "account is not registered"}
	}

	if !strings.EqualFold(username, m.state.Username) {
		return "", probeLocalSessionState{}, &probeLocalHTTPError{Status: http.StatusUnauthorized, Message: "invalid username or password"}
	}
	givenHash := hashProbeLocalPassword(password, m.state.PasswordSalt)
	if !hmac.Equal([]byte(strings.ToLower(givenHash)), []byte(strings.ToLower(m.state.PasswordHash))) {
		return "", probeLocalSessionState{}, &probeLocalHTTPError{Status: http.StatusUnauthorized, Message: "invalid username or password"}
	}

	token := randomHexToken(32)
	session := probeLocalSessionState{
		Username:  m.state.Username,
		ExpiresAt: time.Now().Add(probeLocalSessionTTL),
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

func setProbeLocalSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     probeLocalSessionCookieName,
		Value:    strings.TrimSpace(token),
		Path:     "/local",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
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

func defaultProbeLocalProxyGroupFile() probeLocalProxyGroupFile {
	return probeLocalProxyGroupFile{
		Version:         1,
		DNSServers:      append([]string(nil), defaultProbeLocalDNSServers()...),
		DoTServers:      append([]string(nil), defaultProbeLocalDoTServers()...),
		DoHServers:      append([]string(nil), defaultProbeLocalDoHServers()...),
		DoHProxyServers: append([]string(nil), defaultProbeLocalDoHProxyServers()...),
		FakeIPCIDR:      "198.18.0.0/15",
		Groups: []probeLocalProxyGroupEntry{
			{Group: "default", Rules: []string{"domain_suffix:example.com", "domain_prefix:api."}},
			{Group: "media", Rules: []string{"domain_keyword:stream"}},
		},
		Note: "fallback is built in; rules are examples",
	}
}

func defaultProbeLocalProxyStateFile() probeLocalProxyStateFile {
	now := time.Now().UTC().Format(time.RFC3339)
	return probeLocalProxyStateFile{
		Version:   1,
		UpdatedAt: now,
		Groups:    []probeLocalProxyStateGroupEntry{},
		Backup: probeLocalProxyBackupState{
			LastUploadedAt:   "",
			LastUploadStatus: "idle",
			LastUploadError:  "",
		},
		TUN: probeLocalTUNPersistentState{
			Installed: false,
			Enabled:   false,
			UpdatedAt: now,
		},
	}
}

func defaultProbeLocalProxyHostContent() string {
	return "# dns,ip\n"
}

func resolveProbeLocalProxyGroupPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalProxyGroupFileName), nil
}

func resolveProbeLocalProxyStatePath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalProxyStateFileName), nil
}

func resolveProbeLocalProxyHostPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalProxyHostFileName), nil
}

func resolveProbeLocalProxyChainPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalProxyChainFileName), nil
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

func normalizeProbeLocalProxyGroupDNSConfig(payload *probeLocalProxyGroupFile) {
	if payload == nil {
		return
	}
	payload.DNSServers = normalizeProbeLocalDNSHostPortList(payload.DNSServers, "53", defaultProbeLocalDNSServers())
	payload.DoTServers = normalizeProbeLocalDNSHostPortList(payload.DoTServers, "853", defaultProbeLocalDoTServers())
	payload.DoHServers = normalizeProbeLocalDoHURLList(payload.DoHServers, defaultProbeLocalDoHServers())
	payload.DoHProxyServers = normalizeProbeLocalDoHURLList(payload.DoHProxyServers, defaultProbeLocalDoHProxyServers())
	payload.FakeIPCIDR = strings.TrimSpace(payload.FakeIPCIDR)
	if payload.FakeIPCIDR == "" {
		payload.FakeIPCIDR = "198.18.0.0/15"
	}
	payload.LegacyTUN = nil
}

func normalizeProbeLocalProxyGroupRules(payload *probeLocalProxyGroupFile) {
	if payload == nil {
		return
	}
	for i := range payload.Groups {
		rules := payload.Groups[i].Rules
		if len(rules) == 0 {
			legacy := strings.TrimSpace(payload.Groups[i].RulesText)
			if legacy != "" {
				lines := strings.Split(strings.ReplaceAll(legacy, "\r\n", "\n"), "\n")
				rules = make([]string, 0, len(lines))
				for _, line := range lines {
					trimmed := strings.TrimSpace(line)
					if trimmed == "" || strings.HasPrefix(trimmed, "#") {
						continue
					}
					rules = append(rules, trimmed)
				}
			}
		}
		normalized := make([]string, 0, len(rules))
		for _, rule := range rules {
			trimmed := strings.TrimSpace(rule)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			normalized = append(normalized, trimmed)
		}
		payload.Groups[i].Rules = normalized
		payload.Groups[i].RulesText = ""
	}
}

func validateProbeLocalProxyGroupFile(payload probeLocalProxyGroupFile) error {
	payload.FakeIPCIDR = strings.TrimSpace(payload.FakeIPCIDR)
	if payload.FakeIPCIDR != "" && payload.FakeIPCIDR != "0.0.0.0/0" {
		ipValue, ipnet, err := net.ParseCIDR(payload.FakeIPCIDR)
		if err != nil || ipValue == nil || ipnet == nil || ipValue.To4() == nil {
			return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "fake_ip_cidr is invalid"}
		}
	}
	for i, item := range payload.DNSServers {
		if strings.TrimSpace(item) == "" {
			continue
		}
		if _, ok := normalizeProbeLocalDNSHostPort(item, "53"); !ok {
			return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("dns_servers[%d] is invalid", i)}
		}
	}
	for i, item := range payload.DoTServers {
		if strings.TrimSpace(item) == "" {
			continue
		}
		if _, ok := normalizeProbeLocalDNSHostPort(item, "853"); !ok {
			return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("dot_servers[%d] is invalid", i)}
		}
	}
	for i, item := range payload.DoHServers {
		if strings.TrimSpace(item) == "" {
			continue
		}
		if _, ok := normalizeProbeLocalDoHURL(item); !ok {
			return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("doh_servers[%d] is invalid", i)}
		}
	}
	for i, item := range payload.DoHProxyServers {
		if strings.TrimSpace(item) == "" {
			continue
		}
		if _, ok := normalizeProbeLocalDoHURL(item); !ok {
			return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("doh_proxy_servers[%d] is invalid", i)}
		}
	}
	if len(payload.Groups) == 0 {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "groups is required"}
	}
	seen := make(map[string]struct{}, len(payload.Groups))
	for i, group := range payload.Groups {
		name := strings.TrimSpace(group.Group)
		if name == "" {
			return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("groups[%d].group is required", i)}
		}
		if strings.EqualFold(name, "fallback") {
			return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "fallback is built in and must not be configured explicitly"}
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("duplicate group: %s", name)}
		}
		seen[key] = struct{}{}
		for ruleIndex, rule := range group.Rules {
			trimmed := strings.TrimSpace(rule)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if !strings.Contains(trimmed, ":") {
				return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("groups[%d].rules[%d] must contain ':'", i, ruleIndex)}
			}
		}
	}
	return nil
}

func persistProbeLocalJSONFile(path string, payload any) error {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

func loadProbeLocalProxyGroupFile() (probeLocalProxyGroupFile, error) {
	path, err := resolveProbeLocalProxyGroupPath()
	if err != nil {
		return probeLocalProxyGroupFile{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			def := defaultProbeLocalProxyGroupFile()
			if writeErr := persistProbeLocalProxyGroupFile(def); writeErr != nil {
				return probeLocalProxyGroupFile{}, writeErr
			}
			return def, nil
		}
		return probeLocalProxyGroupFile{}, err
	}
	payload := probeLocalProxyGroupFile{}
	if err := decodeProbeLocalJSONStrict(raw, &payload); err != nil {
		return probeLocalProxyGroupFile{}, err
	}
	if payload.Version <= 0 {
		payload.Version = 1
	}
	for i := range payload.Groups {
		payload.Groups[i].Group = strings.TrimSpace(payload.Groups[i].Group)
	}
	normalizeProbeLocalProxyGroupDNSConfig(&payload)
	normalizeProbeLocalProxyGroupRules(&payload)
	payload.Note = firstNonEmpty(strings.TrimSpace(payload.Note), "fallback is built in")
	if err := validateProbeLocalProxyGroupFile(payload); err != nil {
		return probeLocalProxyGroupFile{}, err
	}
	return payload, nil
}

func persistProbeLocalProxyGroupFile(payload probeLocalProxyGroupFile) error {
	if payload.Version <= 0 {
		payload.Version = 1
	}
	normalizeProbeLocalProxyGroupDNSConfig(&payload)
	normalizeProbeLocalProxyGroupRules(&payload)
	payload.Note = firstNonEmpty(strings.TrimSpace(payload.Note), "fallback is built in")
	if err := validateProbeLocalProxyGroupFile(payload); err != nil {
		return err
	}
	path, err := resolveProbeLocalProxyGroupPath()
	if err != nil {
		return err
	}
	return persistProbeLocalJSONFile(path, payload)
}

func loadProbeLocalProxyStateFile() (probeLocalProxyStateFile, error) {
	path, err := resolveProbeLocalProxyStatePath()
	if err != nil {
		return probeLocalProxyStateFile{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			def := defaultProbeLocalProxyStateFile()
			if writeErr := persistProbeLocalProxyStateFile(def); writeErr != nil {
				return probeLocalProxyStateFile{}, writeErr
			}
			return def, nil
		}
		return probeLocalProxyStateFile{}, err
	}
	payload := probeLocalProxyStateFile{}
	if err := decodeProbeLocalJSONStrict(raw, &payload); err != nil {
		return probeLocalProxyStateFile{}, err
	}
	if payload.Version <= 0 {
		payload.Version = 1
	}
	if strings.TrimSpace(payload.UpdatedAt) == "" {
		payload.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if payload.Groups == nil {
		payload.Groups = []probeLocalProxyStateGroupEntry{}
	}
	if strings.TrimSpace(payload.Backup.LastUploadStatus) == "" {
		payload.Backup.LastUploadStatus = "idle"
	}
	if strings.TrimSpace(payload.TUN.UpdatedAt) == "" {
		payload.TUN.UpdatedAt = payload.UpdatedAt
	}
	return payload, nil
}

func persistProbeLocalProxyStateFile(payload probeLocalProxyStateFile) error {
	if payload.Version <= 0 {
		payload.Version = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	payload.UpdatedAt = now
	if payload.Groups == nil {
		payload.Groups = []probeLocalProxyStateGroupEntry{}
	}
	if strings.TrimSpace(payload.Backup.LastUploadStatus) == "" {
		payload.Backup.LastUploadStatus = "idle"
	}
	if strings.TrimSpace(payload.TUN.UpdatedAt) == "" {
		payload.TUN.UpdatedAt = now
	}
	path, err := resolveProbeLocalProxyStatePath()
	if err != nil {
		return err
	}
	return persistProbeLocalJSONFile(path, payload)
}

func persistProbeLocalTUNPersistentState(installed, enabled bool) error {
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return err
	}
	state.TUN.Installed = installed
	state.TUN.Enabled = enabled
	state.TUN.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return persistProbeLocalProxyStateFile(state)
}

func validateProbeLocalRuntimeAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "direct", "reject", "tunnel":
		return true
	default:
		return false
	}
}

func validateProbeLocalRuntimeGroup(group string) error {
	group = strings.TrimSpace(group)
	if group == "" {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "group is required"}
	}
	if strings.EqualFold(group, "fallback") {
		return nil
	}
	payload, err := loadProbeLocalProxyGroupFile()
	if err != nil {
		return err
	}
	for _, item := range payload.Groups {
		if strings.EqualFold(strings.TrimSpace(item.Group), group) {
			return nil
		}
	}
	return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("group %q not found", group)}
}

func normalizeProbeLocalTunnelNodeID(raw string) (normalized string, chainID string, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", nil
	}
	if len(trimmed) >= len("chain:") && strings.EqualFold(trimmed[:len("chain:")], "chain:") {
		trimmed = strings.TrimSpace(trimmed[len("chain:"):])
	}
	if trimmed == "" {
		return "", "", &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "tunnel_node_id is invalid"}
	}
	return "chain:" + trimmed, trimmed, nil
}

func validateProbeLocalRuntimeTunnelSelection(tunnelNodeID string) (string, error) {
	normalized, chainID, err := normalizeProbeLocalTunnelNodeID(tunnelNodeID)
	if err != nil {
		return "", err
	}
	if chainID == "" {
		return "", nil
	}
	items, err := loadProbeLocalProxyChainItems()
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.ChainID), chainID) {
			return normalized, nil
		}
	}
	return "", &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("tunnel_node_id %q not found in proxy chains", strings.TrimSpace(tunnelNodeID))}
}

func resolveProbeLocalProxyEnableSelection(req probeLocalProxyEnableRequest) (group string, tunnelNodeID string, err error) {
	group = firstNonEmpty(strings.TrimSpace(req.Group), "fallback")
	if err := validateProbeLocalRuntimeGroup(group); err != nil {
		return "", "", err
	}
	selectedChainRaw := firstNonEmpty(strings.TrimSpace(req.SelectedChainID), strings.TrimSpace(req.TunnelNodeID))
	tunnelNodeID, err = validateProbeLocalRuntimeTunnelSelection(selectedChainRaw)
	if err != nil {
		return "", "", err
	}
	return group, tunnelNodeID, nil
}

func resolveProbeLocalProxyDirectGroup(req probeLocalProxyDirectRequest) (string, error) {
	group := firstNonEmpty(strings.TrimSpace(req.Group), "fallback")
	if err := validateProbeLocalRuntimeGroup(group); err != nil {
		return "", err
	}
	return group, nil
}

func upsertProbeLocalRuntimeStateGroup(group, action, tunnelNodeID, runtimeStatus string) error {
	group = strings.TrimSpace(group)
	action = strings.ToLower(strings.TrimSpace(action))
	tunnelNodeID = strings.TrimSpace(tunnelNodeID)
	runtimeStatus = strings.TrimSpace(runtimeStatus)
	selectedChainID := mustProbeLocalSelectedChainIDFromLegacy(tunnelNodeID)
	if group == "" {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "group is required"}
	}
	if !validateProbeLocalRuntimeAction(action) {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "invalid runtime action"}
	}
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return err
	}
	matched := false
	for i := range state.Groups {
		if strings.EqualFold(strings.TrimSpace(state.Groups[i].Group), group) {
			state.Groups[i].Group = group
			state.Groups[i].Action = action
			if selectedChainID != "" {
				state.Groups[i].SelectedChainID = selectedChainID
				state.Groups[i].TunnelNodeID = tunnelNodeID
			}
			state.Groups[i].RuntimeStatus = runtimeStatus
			matched = true
			break
		}
	}
	if !matched {
		state.Groups = append(state.Groups, probeLocalProxyStateGroupEntry{
			Group:           group,
			Action:          action,
			SelectedChainID: selectedChainID,
			TunnelNodeID:    tunnelNodeID,
			RuntimeStatus:   runtimeStatus,
		})
	}
	return persistProbeLocalProxyStateFile(state)
}

func setProbeLocalBackupStatus(status, lastError, uploadedAt string) error {
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return err
	}
	state.Backup.LastUploadStatus = firstNonEmpty(strings.TrimSpace(status), "idle")
	state.Backup.LastUploadError = strings.TrimSpace(lastError)
	state.Backup.LastUploadedAt = strings.TrimSpace(uploadedAt)
	return persistProbeLocalProxyStateFile(state)
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
			return nil, &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("proxy_host.txt line %d must be dns,ip", i+1)}
		}
		dns := strings.ToLower(strings.TrimSpace(parts[0]))
		ipText := strings.TrimSpace(parts[1])
		if dns == "" {
			return nil, &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("proxy_host.txt line %d dns is empty", i+1)}
		}
		if net.ParseIP(ipText) == nil {
			return nil, &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("proxy_host.txt line %d ip is invalid", i+1)}
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
		return defaultProbeLocalProxyHostContent()
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
		return defaultProbeLocalProxyHostContent()
	}
	return strings.Join(lines, "\n") + "\n"
}

func loadProbeLocalHostMappingsWithContent() (string, []probeLocalHostMapping, error) {
	path, err := resolveProbeLocalProxyHostPath()
	if err != nil {
		return "", nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			content := defaultProbeLocalProxyHostContent()
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
	path, err := resolveProbeLocalProxyHostPath()
	if err != nil {
		return err
	}
	content := encodeProbeLocalHostMappingsContent(hosts)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func ensureProbeLocalProxyDefaultsInitialized() error {
	if _, err := loadProbeLocalProxyGroupFile(); err != nil {
		return err
	}
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return err
	}
	if strings.TrimSpace(state.TUN.UpdatedAt) == "" {
		state.TUN.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := persistProbeLocalProxyStateFile(state); err != nil {
			return err
		}
	}
	if _, _, err := loadProbeLocalHostMappingsWithContent(); err != nil {
		return err
	}
	return nil
}

type probeLocalProxyChainsFile struct {
	UpdatedAt string                     `json:"updated_at"`
	Items     []probeLinkChainServerItem `json:"items"`
	Chains    []probeLinkChainServerItem `json:"chains"`
}

func loadProbeLocalProxyChainItems() ([]probeLinkChainServerItem, error) {
	path, err := resolveProbeLocalProxyChainPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []probeLinkChainServerItem{}, nil
		}
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return []probeLinkChainServerItem{}, nil
	}
	payload := probeLocalProxyChainsFile{}
	if err := decodeProbeLocalJSONStrict([]byte(trimmed), &payload); err != nil {
		var items []probeLinkChainServerItem
		if err2 := decodeProbeLocalJSONStrict([]byte(trimmed), &items); err2 != nil {
			return nil, err
		}
		payload.Items = items
	}
	items := payload.Items
	if len(items) == 0 && len(payload.Chains) > 0 {
		items = payload.Chains
	}
	items = sanitizeProbeChainServerItemsForCache(items)
	out := make([]probeLinkChainServerItem, 0, len(items))
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.ChainType), "proxy_chain") {
			item.PortForwards = []probeChainPortForwardServerItem{}
			out = append(out, item)
		}
	}
	return out, nil
}

func backupProbeLocalProxyGroupToController(ctx context.Context) error {
	runtimeContext := currentProbeLocalProxyRuntimeContext()
	baseURL := strings.TrimSpace(runtimeContext.ControllerBaseURL)
	if baseURL == "" {
		return &probeLocalHTTPError{Status: http.StatusConflict, Message: "controller base url is empty"}
	}
	path, err := resolveProbeLocalProxyGroupPath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"file_name":      probeLocalProxyGroupFileName,
		"node_id":        strings.TrimSpace(runtimeContext.Identity.NodeID),
		"content_base64": base64.StdEncoding.EncodeToString(raw),
	})
	if err != nil {
		return err
	}
	requestURL := strings.TrimRight(baseURL, "/") + probeLocalProxyBackupAPIPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range buildProbeAuthHeaders(runtimeContext.Identity) {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		message := strings.TrimSpace(string(responseBody))
		if message == "" {
			message = "controller backup upload failed"
		}
		return &probeLocalHTTPError{Status: http.StatusBadGateway, Message: fmt.Sprintf("controller backup upload failed: %d %s", resp.StatusCode, message)}
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
	candidate := firstNonEmpty(strings.TrimSpace(explicit), strings.TrimSpace(os.Getenv("PROBE_LOCAL_LISTEN")), probeLocalListenAddrDefault)
	normalized := normalizeProbeLocalListenAddr(candidate)
	if normalized != "" {
		return normalized
	}
	return probeLocalListenAddrDefault
}

func startProbeLocalConsoleServer(handler http.Handler, explicitListen string) error {
	if handler == nil {
		return errors.New("nil local console handler")
	}
	addr := resolveProbeLocalListenAddr(explicitListen)

	probeLocalConsoleState.mu.Lock()
	if probeLocalConsoleState.server != nil {
		probeLocalConsoleState.mu.Unlock()
		return nil
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		probeLocalConsoleState.mu.Unlock()
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	probeLocalConsoleState.server = server
	probeLocalConsoleState.listenAddr = addr
	probeLocalConsoleState.mu.Unlock()

	logProbeInfof("probe local console listening on http://%s", addr)
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
	}(server, listener, addr)

	return nil
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
	mux.HandleFunc("/local/api/auth/bootstrap", probeLocalAuthBootstrapHandler)
	mux.HandleFunc("/local/api/auth/register", probeLocalAuthRegisterHandler)
	mux.HandleFunc("/local/api/auth/login", probeLocalAuthLoginHandler)
	mux.HandleFunc("/local/api/auth/logout", probeLocalAuthLogoutHandler)
	mux.HandleFunc("/local/api/auth/session", probeLocalAuthSessionHandler)

	mux.HandleFunc("/local/api/tun/status", probeLocalTUNStatusHandler)
	mux.HandleFunc("/local/api/tun/install", probeLocalTUNInstallHandler)
	mux.HandleFunc("/local/api/logs", probeLocalLogsHandler)
	mux.HandleFunc("/local/api/proxy/enable", probeLocalProxyEnableHandler)
	mux.HandleFunc("/local/api/proxy/select", probeLocalProxySelectHandler)
	mux.HandleFunc("/local/api/proxy/direct", probeLocalProxyDirectHandler)
	mux.HandleFunc("/local/api/proxy/reject", probeLocalProxyRejectHandler)
	mux.HandleFunc("/local/api/proxy/status", probeLocalProxyStatusHandler)
	mux.HandleFunc("/local/api/proxy/chains", probeLocalProxyChainsHandler)
	mux.HandleFunc("/local/api/proxy/chains/refresh", probeLocalProxyChainsRefreshHandler)
	mux.HandleFunc("/local/api/proxy/groups", probeLocalProxyGroupsHandler)
	mux.HandleFunc("/local/api/proxy/groups/refresh", probeLocalProxyGroupsRefreshHandler)
	mux.HandleFunc("/local/api/proxy/groups/save", probeLocalProxyGroupsSaveHandler)
	mux.HandleFunc("/local/api/proxy/state", probeLocalProxyStateHandler)
	mux.HandleFunc("/local/api/proxy/hosts", probeLocalProxyHostsHandler)
	mux.HandleFunc("/local/api/proxy/hosts/save", probeLocalProxyHostsSaveHandler)
	mux.HandleFunc("/local/api/dns/status", probeLocalDNSStatusHandler)
	mux.HandleFunc("/local/api/dns/real_ip/list", probeLocalDNSRealIPListHandler)
	mux.HandleFunc("/local/api/dns/real_ip/lookup", probeLocalDNSRealIPLookupHandler)
	mux.HandleFunc("/local/api/dns/fake_ip/list", probeLocalDNSFakeIPListHandler)
	mux.HandleFunc("/local/api/dns/fake_ip/lookup", probeLocalDNSFakeIPLookupHandler)
	mux.HandleFunc("/local/api/system/upgrade", probeLocalSystemUpgradeHandler)
	mux.HandleFunc("/local/api/system/upgrade/status", probeLocalSystemUpgradeStatusHandler)
	mux.HandleFunc("/local/api/system/restart", probeLocalSystemRestartHandler)
	mux.HandleFunc("/local/api/proxy/groups/backup", probeLocalProxyGroupsBackupHandler)
}

type probeLocalRegisterRequest struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
}

type probeLocalLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type probeLocalProxyEnableRequest struct {
	Group           string `json:"group"`
	SelectedChainID string `json:"selected_chain_id"`
	TunnelNodeID    string `json:"tunnel_node_id"`
}

type probeLocalProxyDirectRequest struct {
	Group string `json:"group"`
}

type probeLocalProxyRejectRequest struct {
	Group string `json:"group"`
}

type probeLocalSystemUpgradeRequest struct {
	Mode        string `json:"mode"`
	ReleaseRepo string `json:"release_repo"`
}

type probeLocalProxyHostsSaveRequest struct {
	Content string `json:"content"`
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
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/local/panel" {
		http.NotFound(w, r)
		return
	}
	if _, _, err := currentProbeLocalSessionFromRequest(r); err != nil {
		http.Redirect(w, r, "/local/login", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(probeLocalPanelPageHTML))
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
	if err := mgr.register(req.Username, req.Password, req.ConfirmPassword); err != nil {
		writeProbeLocalError(w, err)
		return
	}
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
	token, session, err := mgr.login(req.Username, req.Password)
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	setProbeLocalSessionCookie(w, token, session.ExpiresAt)
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
	content, entries := probeLogStore.Tail(lines, sinceMinutes, minLevel)
	if keyword != "" {
		entries = filterProbeLocalLogEntriesByKeyword(entries, keyword)
		content = buildProbeLocalLogContent(entries)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"source":        probeLogSourceName,
		"file_path":     probeLogSourcePath,
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

func probeLocalDNSStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	status := currentProbeLocalDNSStatus()
	tunStatus := currentProbeLocalDNSTUNStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":       status.Enabled,
		"listen_addr":   status.ListenAddr,
		"port":          status.Port,
		"fallback_used": status.FallbackUsed,
		"last_error":    status.LastError,
		"updated_at":    status.UpdatedAt,
		"tun_listener": map[string]any{
			"enabled":     tunStatus.Enabled,
			"listen_addr": tunStatus.ListenAddr,
			"port":        tunStatus.Port,
			"last_error":  tunStatus.LastError,
			"updated_at":  tunStatus.UpdatedAt,
		},
		"fake_ip_cidr":      currentProbeLocalDNSFakeIPCIDR(),
		"fake_ip_entries":   queryProbeLocalDNSFakeIPEntries(),
		"route_hint_count":  probeLocalDNSRouteHintCount(),
		"cache_ttl_seconds": int64(probeLocalDNSCacheTTL / time.Second),
		"cache_records":     queryProbeLocalDNSCacheRecords(),
	})
}

func probeLocalDNSRealIPListHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": queryProbeLocalDNSCacheRecords(),
	})
}

func probeLocalDNSRealIPLookupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	domainText := strings.TrimSpace(r.URL.Query().Get("domain"))
	if domainText == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "domain is required"})
		return
	}
	items := lookupProbeLocalDNSCacheRecordsByDomain(domainText)
	if len(items) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "real ip not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func probeLocalDNSFakeIPListHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": queryProbeLocalDNSFakeIPEntries(),
	})
}

func probeLocalDNSFakeIPLookupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	ipText := strings.TrimSpace(r.URL.Query().Get("ip"))
	if ipText == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "ip is required"})
		return
	}
	item, ok := lookupProbeLocalDNSFakeIPEntry(ipText)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "fake ip not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func probeLocalProxyEnableHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalProxyReadBodyMaxLen)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var req probeLocalProxyEnableRequest
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	group, tunnelNodeID, err := resolveProbeLocalProxyEnableSelection(req)
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	selectedChainID := mustProbeLocalSelectedChainIDFromLegacy(tunnelNodeID)
	if err := ensureProbeLocalProxyBootstrapDirectBypass(selectedChainID); err != nil {
		writeProbeLocalError(w, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: strings.TrimSpace(err.Error())})
		return
	}
	tunState, proxyState, err := probeLocalControl.enableProxy()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	if selectedChainID != "" {
		syncProbeLocalTUNGroupRuntimeSelection(group, selectedChainID)
	}
	if updateErr := upsertProbeLocalRuntimeStateGroup(group, "tunnel", tunnelNodeID, "online"); updateErr != nil {
		logProbeWarnf("probe local runtime state update failed: %v", updateErr)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"tun":   tunState,
		"proxy": proxyState,
		"selection": map[string]any{
			"group":             group,
			"selected_chain_id": selectedChainID,
			"tunnel_node_id":    tunnelNodeID,
		},
	})
}

func probeLocalProxySelectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalProxyReadBodyMaxLen)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var req probeLocalProxyEnableRequest
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	group, tunnelNodeID, err := resolveProbeLocalProxyEnableSelection(req)
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	selectedChainID := mustProbeLocalSelectedChainIDFromLegacy(tunnelNodeID)
	if selectedChainID != "" {
		syncProbeLocalTUNGroupRuntimeSelection(group, selectedChainID)
	}
	if updateErr := upsertProbeLocalRuntimeStateGroup(group, "tunnel", tunnelNodeID, "online"); updateErr != nil {
		logProbeWarnf("probe local runtime state update failed: %v", updateErr)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"tun":   probeLocalControl.tunStatus(),
		"proxy": probeLocalControl.proxyStatus(),
		"selection": map[string]any{
			"group":             group,
			"selected_chain_id": selectedChainID,
			"tunnel_node_id":    tunnelNodeID,
		},
	})
}

func probeLocalProxyDirectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalProxyReadBodyMaxLen)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var req probeLocalProxyDirectRequest
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	group, err := resolveProbeLocalProxyDirectGroup(req)
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	tunState, proxyState, err := probeLocalControl.directProxy()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	if updateErr := upsertProbeLocalRuntimeStateGroup(group, "direct", "", "online"); updateErr != nil {
		logProbeWarnf("probe local runtime state update failed: %v", updateErr)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"tun":   tunState,
		"proxy": proxyState,
		"selection": map[string]any{
			"group": group,
		},
	})
}

func probeLocalProxyRejectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalProxyReadBodyMaxLen)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var req probeLocalProxyRejectRequest
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	group, err := resolveProbeLocalProxyDirectGroup(probeLocalProxyDirectRequest{Group: req.Group})
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	if updateErr := upsertProbeLocalRuntimeStateGroup(group, "reject", "", "blocked"); updateErr != nil {
		logProbeWarnf("probe local runtime state update failed: %v", updateErr)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"tun":   probeLocalControl.tunStatus(),
		"proxy": probeLocalControl.proxyStatus(),
		"selection": map[string]any{
			"group":  group,
			"action": "reject",
		},
	})
}

func resolveProbeLocalSelectedTunnelNodeID(state probeLocalProxyStateFile) string {
	for _, entry := range state.Groups {
		selectedChainID := firstNonEmpty(strings.TrimSpace(entry.SelectedChainID), mustProbeLocalSelectedChainIDFromLegacy(entry.TunnelNodeID))
		if strings.TrimSpace(selectedChainID) == "" {
			continue
		}
		return formatProbeLocalLegacyTunnelNodeID(selectedChainID)
	}
	return ""
}

func resolveProbeLocalChainNameByID(chainID string) string {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return ""
	}
	items, err := loadProbeLocalProxyChainItems()
	if err != nil {
		return cleanID
	}
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.ChainID), cleanID) {
			name := strings.TrimSpace(item.Name)
			if name != "" {
				return name
			}
			break
		}
	}
	return cleanID
}

func resolveProbeLocalChainKeepaliveAndLatency(chainID string) (string, *int64, string, string) {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return "none", nil, "", ""
	}
	rt := getProbeChainRuntime(cleanID)
	if rt == nil {
		return "not_running", nil, "", ""
	}
	downstream := rt.getDownstreamSession() != nil
	upstream := rt.getUpstreamSession() != nil
	if !downstream && !upstream {
		return "disconnected", nil, "", ""
	}

	keepalive := "connected"
	targetHost := strings.TrimSpace(rt.cfg.nextHost)
	targetPort := rt.cfg.nextPort
	if targetHost == "" || targetPort <= 0 {
		targetHost = strings.TrimSpace(rt.cfg.prevHost)
		targetPort = rt.cfg.prevPort
	}
	if targetHost == "" || targetPort <= 0 {
		return keepalive, nil, "", "latency target is unavailable"
	}

	addr := net.JoinHostPort(targetHost, strconv.Itoa(targetPort))
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 1800*time.Millisecond)
	if err != nil {
		return keepalive, nil, "", strings.TrimSpace(err.Error())
	}
	_ = conn.Close()
	latencyMS := int64(time.Since(start) / time.Millisecond)
	if latencyMS < 0 {
		latencyMS = 0
	}
	updatedAt := time.Now().UTC().Format(time.RFC3339)
	return keepalive, &latencyMS, updatedAt, ""
}

func probeLocalProxyStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}

	status := probeLocalControl.proxyStatus()
	payload := map[string]any{
		"enabled":    status.Enabled,
		"mode":       status.Mode,
		"last_error": status.LastError,
		"updated_at": status.UpdatedAt,
	}
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		writeJSON(w, http.StatusOK, payload)
		return
	}
	selectedGroup := ""
	selectedChainID := ""
	for _, entry := range state.Groups {
		candidateGroup := strings.TrimSpace(entry.Group)
		candidateChainID := firstNonEmpty(
			strings.TrimSpace(entry.SelectedChainID),
			mustProbeLocalSelectedChainIDFromLegacy(entry.TunnelNodeID),
		)
		if candidateGroup == "" || candidateChainID == "" {
			continue
		}
		selectedGroup = candidateGroup
		selectedChainID = candidateChainID
		break
	}
	selectedTunnelNodeID := formatProbeLocalLegacyTunnelNodeID(selectedChainID)
	payload["selected_tunnel_node_id"] = selectedTunnelNodeID
	payload["selected_chain_id"] = selectedChainID
	payload["selected_chain_name"] = resolveProbeLocalChainNameByID(selectedChainID)

	var selectedGroupRuntime *probeLocalTUNGroupRuntime
	if selectedGroup != "" && selectedChainID != "" {
		syncProbeLocalTUNGroupRuntimeSelection(selectedGroup, selectedChainID)
		selectedGroupRuntime = currentProbeLocalTUNGroupRuntime(selectedGroup)
	}
	keepalive, latencyMS, latencyUpdatedAt, latencyError := resolveProbeLocalTUNGroupRuntimeKeepaliveAndLatency(selectedGroupRuntime)
	payload["selected_chain_keepalive"] = keepalive
	latencyStatus := "unreachable"
	if latencyMS != nil {
		payload["selected_chain_latency_ms"] = *latencyMS
		latencyStatus = "reachable"
	}
	payload["selected_chain_latency_status"] = latencyStatus
	payload["selected_chain_latency_updated_at"] = latencyUpdatedAt
	payload["selected_chain_latency_error"] = latencyError

	writeJSON(w, http.StatusOK, payload)
}

func probeLocalProxyChainsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	items, err := loadProbeLocalProxyChainItems()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func probeLocalProxyChainsRefreshHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	runtimeContext := currentProbeLocalProxyRuntimeContext()
	ctx, cancel := context.WithTimeout(r.Context(), probeLinkChainsSyncFetchTimeout)
	defer cancel()
	items, err := probeLocalRefreshProxyChainCache(ctx, runtimeContext.Identity, runtimeContext.ControllerBaseURL)
	if err != nil {
		writeProbeLocalError(w, &probeLocalHTTPError{Status: http.StatusBadGateway, Message: strings.TrimSpace(err.Error())})
		return
	}
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	groups := make([]map[string]any, 0, len(state.Groups))
	for _, entry := range state.Groups {
		groups = append(groups, buildProbeLocalProxyStateGroupPayload(entry))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"items":      items,
		"state":      map[string]any{"version": state.Version, "updated_at": state.UpdatedAt, "groups": groups, "backup": state.Backup},
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func probeLocalProxyGroupsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	groups, err := loadProbeLocalProxyGroupFile()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func probeLocalProxyGroupsRefreshHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	groupsFile, err := loadProbeLocalProxyGroupFile()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	resetProbeLocalDNSRuntimeCachesForProxyGroupRefresh()
	stateGroups := make([]map[string]any, 0, len(state.Groups))
	for _, entry := range state.Groups {
		stateGroups = append(stateGroups, buildProbeLocalProxyStateGroupPayload(entry))
	}
	status := currentProbeLocalDNSStatus()
	tunStatus := currentProbeLocalDNSTUNStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"groups": groupsFile,
		"state":  map[string]any{"version": state.Version, "updated_at": state.UpdatedAt, "groups": stateGroups, "backup": state.Backup},
		"dns": map[string]any{
			"enabled":           status.Enabled,
			"listen_addr":       status.ListenAddr,
			"port":              status.Port,
			"fallback_used":     status.FallbackUsed,
			"last_error":        status.LastError,
			"updated_at":        status.UpdatedAt,
			"tun_listener":      tunStatus,
			"fake_ip_cidr":      currentProbeLocalDNSFakeIPCIDR(),
			"fake_ip_entries":   queryProbeLocalDNSFakeIPEntries(),
			"route_hint_count":  probeLocalDNSRouteHintCount(),
			"cache_ttl_seconds": int64(probeLocalDNSCacheTTL / time.Second),
			"cache_records":     queryProbeLocalDNSCacheRecords(),
		},
	})
}

func probeLocalProxyGroupsSaveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalProxyReadBodyMaxLen)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var payload probeLocalProxyGroupFile
	if err := decoder.Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := validateProbeLocalProxyGroupFile(payload); err != nil {
		writeProbeLocalError(w, err)
		return
	}
	normalizeProbeLocalProxyGroupDNSConfig(&payload)
	payload.Note = firstNonEmpty(strings.TrimSpace(payload.Note), "fallback is built in")
	if payload.Version <= 0 {
		payload.Version = 1
	}
	if err := persistProbeLocalProxyGroupFile(payload); err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "groups": payload})
}

func buildProbeLocalProxyStateGroupPayload(entry probeLocalProxyStateGroupEntry) map[string]any {
	group := strings.TrimSpace(entry.Group)
	action := strings.TrimSpace(entry.Action)
	selectedChainID := firstNonEmpty(
		strings.TrimSpace(entry.SelectedChainID),
		mustProbeLocalSelectedChainIDFromLegacy(entry.TunnelNodeID),
	)
	tunnelNodeID := firstNonEmpty(strings.TrimSpace(entry.TunnelNodeID), formatProbeLocalLegacyTunnelNodeID(selectedChainID))
	runtimeStatus := strings.TrimSpace(entry.RuntimeStatus)

	payload := map[string]any{
		"group": group,
	}
	if action != "" {
		payload["action"] = action
	}
	if selectedChainID != "" {
		payload["selected_chain_id"] = selectedChainID
	}
	if tunnelNodeID != "" {
		payload["tunnel_node_id"] = tunnelNodeID
	}

	if strings.EqualFold(action, "tunnel") && group != "" && selectedChainID != "" {
		syncProbeLocalTUNGroupRuntimeSelection(group, selectedChainID)
		if rt := currentProbeLocalTUNGroupRuntime(group); rt != nil {
			snapshot := rt.snapshot()
			if groupRuntimeStatus := strings.TrimSpace(snapshot.RuntimeStatus); groupRuntimeStatus != "" {
				payload["group_runtime_status"] = groupRuntimeStatus
			}
			keepalive, latencyMS, latencyUpdatedAt, latencyError := resolveProbeLocalTUNGroupRuntimeKeepaliveAndLatency(rt)
			payload["selected_chain_keepalive"] = keepalive
			latencyStatus := "unreachable"
			if latencyMS != nil {
				payload["selected_chain_latency_ms"] = *latencyMS
				latencyStatus = "reachable"
			}
			payload["selected_chain_latency_status"] = latencyStatus
			payload["selected_chain_latency_updated_at"] = latencyUpdatedAt
			payload["selected_chain_latency_error"] = latencyError
		}
	}

	if runtimeStatus != "" {
		payload["runtime_status"] = runtimeStatus
	}
	return payload
}

func probeLocalProxyStateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	groups := make([]map[string]any, 0, len(state.Groups))
	for _, entry := range state.Groups {
		groups = append(groups, buildProbeLocalProxyStateGroupPayload(entry))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    state.Version,
		"updated_at": state.UpdatedAt,
		"groups":     groups,
		"backup":     state.Backup,
	})
}

func probeLocalProxyHostsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	content, hosts, err := loadProbeLocalHostMappingsWithContent()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": content, "hosts": hosts})
}

func probeLocalProxyHostsSaveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalProxyReadBodyMaxLen)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var req probeLocalProxyHostsSaveRequest
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	hosts, err := parseProbeLocalHostMappings(req.Content)
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	if err := persistProbeLocalHostMappings(hosts); err != nil {
		writeProbeLocalError(w, err)
		return
	}
	content, normalizedHosts, err := loadProbeLocalHostMappingsWithContent()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "content": content, "hosts": normalizedHosts})
}

func probeLocalSystemUpgradeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalProxyReadBodyMaxLen)
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
	runtimeContext := currentProbeLocalProxyRuntimeContext()
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
		if err := probeLocalRestartProcess(""); err != nil {
			logProbeErrorf("probe local restart failed: %v", err)
		}
	}()
}

func probeLocalProxyGroupsBackupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if err := backupProbeLocalProxyGroupToController(r.Context()); err != nil {
		_ = setProbeLocalBackupStatus("failed", strings.TrimSpace(err.Error()), "")
		writeProbeLocalError(w, err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_ = setProbeLocalBackupStatus("ok", "", now)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "uploaded_at": now})
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
}

func resetProbeLocalControlStateForTest() {
	clearProbeLocalTUNInstallObservation()
	resetProbeLocalTUNGroupRuntimeRegistryForTest()
	probeLocalControl = newProbeLocalControlManager()
}

func resetProbeLocalProxyHooksForTest() {
	probeLocalApplyProxyTakeover = applyProbeLocalProxyTakeover
	probeLocalRestoreProxyDirect = restoreProbeLocalProxyDirect
	probeLocalEnsureExplicitDirectBypass = ensureProbeLocalExplicitDirectBypassForTarget
	probeLocalRefreshProxyChainCache = refreshProbeProxyChainCacheFromController
}

func resetProbeLocalTUNHooksForTest() {
	probeLocalInstallTUNDriver = installProbeLocalTUNDriver
	probeLocalCheckTUNReadyAfterInstall = probeLocalNoopPostInstallTUNReadyCheck
	probeLocalResetTUNDetectInstalledHook()
	resetProbeLocalTUNDataPlaneHooksForTest()
}

func resetProbeLocalUpgradeHooksForTest() {
	probeLocalRunUpgrade = runProbeUpgrade
	probeLocalRestartProcess = restartCurrentProcess
	resetProbeLocalUpgradeRuntimeStateForTest()
}

func setProbeLocalProxyRuntimeContext(identity nodeIdentity, controllerBaseURL string) {
	probeLocalRuntimeState.mu.Lock()
	probeLocalRuntimeState.context = probeLocalProxyRuntimeContext{
		Identity:          identity,
		ControllerBaseURL: strings.TrimSpace(controllerBaseURL),
	}
	probeLocalRuntimeState.mu.Unlock()
}

func currentProbeLocalProxyRuntimeContext() probeLocalProxyRuntimeContext {
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
