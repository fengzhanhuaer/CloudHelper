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
	"hash/fnv"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	probeLocalListenAddrDefault = "127.0.0.1:16032"
	probeLocalListenDefaultHost = "127.0.0.1"
	probeLocalListenDefaultPort = 16032

	probeLocalAuthStoreFile      = "probe_local_auth.json"
	probeLocalSessionCookieName  = "probe_local_session"
	probeLocalSessionTTL         = 30 * 24 * time.Hour
	probeLocalMinPasswordLength  = 8
	probeLocalMaxPasswordLength  = 128
	probeLocalMaxUsernameLength  = 64
	probeLocalAuthReadBodyMaxLen = 64 * 1024

	probeLocalProxyModeDirect = "direct"
	probeLocalProxyModeTUN    = "tunnel"

	probeLocalProxyGroupFileName          = "proxy_group.json"
	probeLocalProxyStateFileName          = "proxy_state.json"
	probeLocalProxyHostFileName           = "proxy_host.txt"
	probeLocalProxyChainFileName          = "proxy_chain.json"
	probeLocalTUNPrimaryDNSBackupFileName = "tun_primary_dns_backup.json"
	probeLocalExplicitProxyPreconnectWait = 8 * time.Second
	probeLocalProxyBackupAPIPath          = "/api/probe/proxy_group/backup"
	probeLocalProxyReadBodyMaxLen         = 512 * 1024
	probeLocalProxyLinkCFOptimizeMaxIPs   = 512
	probeLocalProxyLinkCFOptimizeTopN     = 10
	probeLocalProxyLinkCFOptimizeParallel = 64
	probeLocalProxyLinkCFOptimizeTimeout  = 6 * time.Second
)

type probeLocalAuthState struct {
	Registered   bool   `json:"registered"`
	Username     string `json:"username,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
	PasswordSalt string `json:"password_salt,omitempty"`
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
	RecoveryStatus         string                           `json:"recovery_status,omitempty"`
	RecoveryAttempts       int                              `json:"recovery_attempts,omitempty"`
	RecoveryLastError      string                           `json:"recovery_last_error,omitempty"`
	RecoveryNextAt         string                           `json:"recovery_next_at,omitempty"`
	RecoveryUpdatedAt      string                           `json:"recovery_updated_at,omitempty"`
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
	LastUploadedAt    string `json:"last_uploaded_at,omitempty"`
	LastUploadStatus  string `json:"last_upload_status,omitempty"`
	LastUploadError   string `json:"last_upload_error,omitempty"`
	LastRestoredAt    string `json:"last_restored_at,omitempty"`
	LastRestoreStatus string `json:"last_restore_status,omitempty"`
	LastRestoreError  string `json:"last_restore_error,omitempty"`
}

type probeLocalTUNPersistentState struct {
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type probeLocalProxyPersistentState struct {
	Enabled   bool   `json:"enabled"`
	Mode      string `json:"mode"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type probeLocalExplicitProxyPersistentState struct {
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type probeLocalProxyStateFile struct {
	Version   int                                    `json:"version"`
	UpdatedAt string                                 `json:"updated_at"`
	Groups    []probeLocalProxyStateGroupEntry       `json:"groups"`
	Backup    probeLocalProxyBackupState             `json:"backup"`
	TUN       probeLocalTUNPersistentState           `json:"tun"`
	Proxy     probeLocalProxyPersistentState         `json:"proxy"`
	Explicit  probeLocalExplicitProxyPersistentState `json:"explicit_proxy,omitempty"`
}

type probeLocalProxyGroupRuntimeSnapshot struct {
	Group                         string                               `json:"group,omitempty"`
	SelectedChainID               string                               `json:"selected_chain_id,omitempty"`
	GroupRuntimeStatus            string                               `json:"group_runtime_status,omitempty"`
	SelectedChainKeepalive        string                               `json:"selected_chain_keepalive,omitempty"`
	SelectedChainLatencyMS        *int64                               `json:"selected_chain_latency_ms,omitempty"`
	SelectedChainLatencyStatus    string                               `json:"selected_chain_latency_status,omitempty"`
	SelectedChainLatencyUpdatedAt string                               `json:"selected_chain_latency_updated_at,omitempty"`
	SelectedChainLatencyError     string                               `json:"selected_chain_latency_error,omitempty"`
	ProtocolState                 probeChainRelayProtocolStateSnapshot `json:"protocol_state,omitempty"`
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
	errProbeLocalProxyUnsupported            = errors.New("probe local proxy takeover is not supported on this platform")
	errProbeLocalTUNUnsupported              = errors.New("probe local tun install is not supported on this platform")
	probeLocalInstallTUNDriver               = installProbeLocalTUNDriver
	probeLocalCheckTUNReadyAfterInstall      = probeLocalNoopPostInstallTUNReadyCheck
	probeLocalDetectTUNInstalled             = defaultProbeLocalDetectTUNInstalled
	probeLocalResetTUNDetectInstalledHook    = func() { probeLocalDetectTUNInstalled = defaultProbeLocalDetectTUNInstalled }
	probeLocalApplyProxyTakeover             = applyProbeLocalProxyTakeover
	probeLocalRestoreProxyDirect             = restoreProbeLocalProxyDirect
	probeLocalApplyTUNPrimaryDNS             = applyProbeLocalTUNPrimaryDNS
	probeLocalRestoreTUNPrimaryDNS           = restoreProbeLocalTUNPrimaryDNS
	probeLocalUninstallTUNDriver             = uninstallProbeLocalTUNDriver
	probeLocalResolveGroupRuntimeLatency     = resolveProbeLocalTUNGroupRuntimeKeepaliveAndLatency
	probeLocalProxyLinkHandshakeProbe        = runProbeLocalProxyLinkHandshakeProbe
	probeLocalProxyLinkProtocolProbe         = runProbeLocalProxyLinkProtocolProbe
	probeLocalProxyLinkSpeedProbe            = runProbeLocalProxyLinkSpeedProbe
	probeLocalProxyRelaySpeedDebugFetch      = probeChainRelayFetchSpeedDebugDefault
	probeLocalProxyLinkRemoteSpeedDebugFetch = runProbeLocalProxyLinkRemoteSpeedDebugFetch
	probeLocalProxyLinkOpenRelayConn         = openProbeChainRelayNetConnWithLayerConn
	probeLocalFetchCloudflareIPv4CIDRs       = defaultProbeLocalFetchCloudflareIPv4CIDRs
	probeLocalProxyLinkCFIPLookup            = defaultProbeLocalProxyLinkCFIPLookup
	probeLocalProxyLinkCFIPProbe             = runProbeLocalProxyLinkCFIPProbe
	probeLocalStartCFIPOptimizeTask          = func(fn func()) { go fn() }
	probeLocalRunUpgrade                     = runProbeUpgrade
	probeLocalFetchRelease                   = fetchProbeRelease
	probeLocalRestartProcess                 = restartCurrentProcess
	probeLocalRefreshProxyChainCache         = refreshProbeProxyChainCacheFromController
	probeLocalLookupIPv4ForBypass            = lookupProbeLocalIPv4ForBypass
)

var probeLocalProxyStatusRefreshState = struct {
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
		if id := strings.TrimSpace(item.ChainID); id != "" {
			itemByChainID[id] = item
		}
		if id := strings.TrimSpace(item.ClientEntryID); id != "" {
			itemByChainID[id] = item
		}
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

func preconnectProbeLocalTUNGroupRuntimesFromState(reason string) {
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		logProbeWarnf("probe local proxy group runtime preconnect state load failed: reason=%s err=%v", strings.TrimSpace(reason), err)
		return
	}
	if !shouldRestoreProbeLocalProxyFromState(state) {
		if shouldRestoreProbeLocalExplicitProxyFromState(state) {
			if err := startProbeLocalExplicitProxyServer(); err != nil {
				logProbeWarnf("probe local explicit proxy startup recovery failed: %v", err)
			}
		}
		return
	}
	go preconnectProbeLocalTUNGroupRuntimes(state, reason)
}

func preconnectProbeLocalTUNGroupRuntimes(state probeLocalProxyStateFile, reason string) {
	result := preconnectProbeLocalTUNGroupRuntimesWithResult(state, reason, true)
	if result.Attempted > 0 {
		logProbeInfof("probe local proxy group runtime preconnect completed: reason=%s attempted=%d connected=%d", strings.TrimSpace(reason), result.Attempted, result.Connected)
	}
}

type probeLocalProxyPreconnectResult struct {
	Attempted int                 `json:"attempted"`
	Connected int                 `json:"connected"`
	Skipped   int                 `json:"skipped,omitempty"`
	Failed    int                 `json:"failed,omitempty"`
	Ready     bool                `json:"ready"`
	Groups    []map[string]string `json:"groups"`
}

func preconnectProbeLocalTUNGroupRuntimesWithResult(state probeLocalProxyStateFile, reason string, resolveLatency bool) probeLocalProxyPreconnectResult {
	seen := map[string]struct{}{}
	result := probeLocalProxyPreconnectResult{Groups: []map[string]string{}}
	for _, entry := range state.Groups {
		group := strings.TrimSpace(entry.Group)
		if group == "" || !strings.EqualFold(strings.TrimSpace(entry.Action), "tunnel") {
			continue
		}
		selectedChainID := firstNonEmpty(
			strings.TrimSpace(entry.SelectedChainID),
			mustProbeLocalSelectedChainIDFromLegacy(entry.TunnelNodeID),
		)
		if strings.TrimSpace(selectedChainID) == "" {
			continue
		}
		key := strings.ToLower(group) + "|" + strings.TrimSpace(selectedChainID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		rt, err := ensureProbeLocalTUNGroupRuntime(group, selectedChainID)
		if err != nil {
			result.Skipped++
			result.Groups = append(result.Groups, map[string]string{
				"group":             group,
				"selected_chain_id": selectedChainID,
				"status":            "skipped",
				"error":             strings.TrimSpace(err.Error()),
			})
			logProbeWarnf("probe local proxy group runtime preconnect skipped: reason=%s group=%s chain=%s err=%v", strings.TrimSpace(reason), group, selectedChainID, err)
			continue
		}
		result.Attempted++
		rt.mu.Lock()
		err = rt.ensureConnectedLocked()
		snapshot := rt.snapshotLocked()
		rt.mu.Unlock()
		if err != nil {
			result.Failed++
			result.Groups = append(result.Groups, map[string]string{
				"group":             group,
				"selected_chain_id": selectedChainID,
				"status":            firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "unavailable"),
				"error":             strings.TrimSpace(err.Error()),
			})
			logProbeWarnf("probe local proxy group runtime preconnect failed: reason=%s group=%s chain=%s status=%s err=%v", strings.TrimSpace(reason), group, selectedChainID, strings.TrimSpace(snapshot.RuntimeStatus), err)
			continue
		}
		result.Connected++
		keepalive := "connected"
		var latencyMSPtr *int64
		latencyUpdatedAt := strings.TrimSpace(snapshot.UpdatedAt)
		latencyError := ""
		if resolveLatency {
			keepalive, latencyMSPtr, latencyUpdatedAt, latencyError = probeLocalResolveGroupRuntimeLatency(rt)
		}
		latencyStatus := "unreachable"
		if latencyMSPtr != nil {
			latencyStatus = "reachable"
		}
		setProbeLocalProxyViewGroupRuntimeSnapshot(group, probeLocalProxyGroupRuntimeSnapshot{
			Group:                         group,
			SelectedChainID:               selectedChainID,
			GroupRuntimeStatus:            firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "connected"),
			SelectedChainKeepalive:        firstNonEmpty(strings.TrimSpace(keepalive), "connected"),
			SelectedChainLatencyMS:        latencyMSPtr,
			SelectedChainLatencyStatus:    latencyStatus,
			SelectedChainLatencyUpdatedAt: firstNonEmpty(strings.TrimSpace(latencyUpdatedAt), strings.TrimSpace(snapshot.UpdatedAt), time.Now().UTC().Format(time.RFC3339)),
			SelectedChainLatencyError:     strings.TrimSpace(latencyError),
		})
		result.Groups = append(result.Groups, map[string]string{
			"group":             group,
			"selected_chain_id": selectedChainID,
			"status":            firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "connected"),
		})
		logProbeInfof("probe local proxy group runtime preconnected: reason=%s group=%s chain=%s entry=%s:%d layer=%s", strings.TrimSpace(reason), group, selectedChainID, strings.TrimSpace(snapshot.EntryHost), snapshot.EntryPort, strings.TrimSpace(snapshot.LinkLayer))
	}
	result.Ready = result.Attempted > 0 && result.Connected == result.Attempted
	return result
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
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return false
	}
	if !shouldRestoreProbeLocalTUNFromState(state) && !shouldRestoreProbeLocalProxyFromState(state) {
		return false
	}
	proxyStatus := m.proxyStatus()
	return !(proxyStatus.Enabled && strings.EqualFold(strings.TrimSpace(proxyStatus.Mode), probeLocalProxyModeTUN))
}

func recoverProbeLocalTUNRuntimeOnStartup() error {
	return probeLocalControl.recoverTUNOnStartup(1)
}

func startProbeLocalExplicitProxyStartupRecovery() {
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		logProbeWarnf("probe local explicit proxy startup recovery skipped: %v", err)
		return
	}
	if !shouldRestoreProbeLocalExplicitProxyFromState(state) {
		return
	}
	if err := startProbeLocalExplicitProxyServer(); err != nil {
		logProbeWarnf("probe local explicit proxy startup recovery failed: %v", err)
	}
	go preconnectProbeLocalTUNGroupRuntimes(state, "explicit_proxy_startup_recovery")
}

func startProbeLocalTUNStartupRecoveryAsync() {
	startProbeLocalExplicitProxyStartupRecovery()
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

func recoverProbeLocalTUNRuntimeAfterChainConfigSync() {
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		logProbeWarnf("probe local tun chain-sync recovery state load failed: %v", err)
		return
	}
	if !shouldRestoreProbeLocalTUNFromState(state) && !shouldRestoreProbeLocalProxyFromState(state) {
		return
	}
	proxyStatus := probeLocalControl.proxyStatus()
	if proxyStatus.Enabled && strings.EqualFold(strings.TrimSpace(proxyStatus.Mode), probeLocalProxyModeTUN) {
		return
	}
	if err := recoverProbeLocalTUNRuntimeOnStartup(); err != nil {
		logProbeWarnf("probe local tun chain-sync recovery skipped: %v", err)
		startProbeLocalTUNStartupRecoveryLoop()
		return
	}
	proxyStatus = probeLocalControl.proxyStatus()
	if proxyStatus.Enabled && strings.EqualFold(strings.TrimSpace(proxyStatus.Mode), probeLocalProxyModeTUN) {
		logProbeInfof("probe local tun recovered after chain config sync")
		preconnectProbeLocalTUNGroupRuntimesFromState("chain_config_sync")
	}
	if shouldRestoreProbeLocalExplicitProxyFromState(state) {
		if err := startProbeLocalExplicitProxyServer(); err != nil {
			logProbeWarnf("probe local explicit proxy startup recovery failed: %v", err)
		}
	}
}

func (m *probeLocalControlManager) recoverTUNOnStartup(attempt int) error {
	if attempt <= 0 {
		attempt = 1
	}
	m.setTUNRecoveryStatus("running", attempt, time.Time{}, "")
	logProbeInfof("probe local tun startup recovery attempt started: attempt=%d", attempt)
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		m.setTUNRecoveryStatus("failed", attempt, time.Time{}, strings.TrimSpace(err.Error()))
		return err
	}

	detectedInstalled, detectErr := probeLocalDetectTUNInstalled()
	if detectErr != nil && !errors.Is(detectErr, errProbeLocalTUNUnsupported) {
		logProbeWarnf("probe local tun startup detect failed: %v", detectErr)
	}
	installed := detectedInstalled && detectErr == nil
	restoreTUN := shouldRestoreProbeLocalTUNFromState(state)
	restoreProxy := shouldRestoreProbeLocalProxyFromState(state)
	now := time.Now().UTC().Format(time.RFC3339)
	installErrText := ""

	if (restoreTUN || restoreProxy) && !installed && !errors.Is(detectErr, errProbeLocalTUNUnsupported) {
		logProbeWarnf("probe local tun startup recovery will run install/check: persisted_installed=%v detected_installed=%v detect_err=%v", state.TUN.Installed, detectedInstalled, detectErr)
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
	m.tun.Enabled = false
	m.tun.DataPlane = false
	m.tun.DataPlaneRX = 0
	m.tun.DataPlaneBytes = 0
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
	m.proxy.Enabled = false
	m.proxy.Mode = probeLocalProxyModeDirect
	m.proxy.UpdatedAt = now
	m.mu.Unlock()

	if installed != state.TUN.Installed {
		persistProbeLocalTUNStateBestEffort(installed, false)
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
	if !restoreTUN && !restoreProxy {
		if installed {
			logProbeInfof("probe local tun startup recovered installed state: proxy_restore=false")
		}
		if shouldRestoreProbeLocalExplicitProxyFromState(state) {
			if err := startProbeLocalExplicitProxyServer(); err != nil {
				logProbeWarnf("probe local explicit proxy startup recovery failed: %v", err)
			}
		}
		m.setTUNRecoveryStatus("idle", attempt, time.Time{}, "")
		return nil
	}

	now = time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	m.tun.Installed = installed
	m.tun.Enabled = false
	m.tun.DataPlane = false
	m.tun.DataPlaneRX = 0
	m.tun.DataPlaneBytes = 0
	m.tun.LastError = ""
	m.tun.UpdatedAt = now
	m.proxy.Enabled = false
	m.proxy.Mode = probeLocalProxyModeDirect
	m.proxy.LastError = ""
	m.proxy.UpdatedAt = now
	m.mu.Unlock()
	persistProbeLocalTUNStateBestEffort(installed, false)
	if err := persistProbeLocalProxyPersistentState(false, probeLocalProxyModeDirect); err != nil {
		logProbeWarnf("probe local proxy persist direct state failed: %v", err)
	}
	reconcileProbeLocalDNSRuntimeForTUNProxyEnabled(false)
	stopProbeLocalProxyMonitor()
	m.setTUNRecoveryStatus("recovered", attempt, time.Time{}, "")
	logProbeInfof("probe local tun startup recovered adapter only: persisted_tun_enabled=%v persisted_proxy_enabled=%v", restoreTUN, restoreProxy)
	if shouldRestoreProbeLocalExplicitProxyFromState(state) {
		if err := startProbeLocalExplicitProxyServer(); err != nil {
			logProbeWarnf("probe local explicit proxy startup recovery failed: %v", err)
		}
	}
	return nil
}

func (m *probeLocalControlManager) proxyStatus() probeLocalProxyRuntimeState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.proxy
}

func probeLocalTUNProxyEnabled() bool {
	status := probeLocalControl.proxyStatus()
	return status.Enabled && strings.EqualFold(strings.TrimSpace(status.Mode), probeLocalProxyModeTUN)
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
	stats := probeLocalTUNDataPlaneStatsSnapshot()
	m.tun.Enabled = stats.Running
	m.tun.DataPlane = stats.Running
	m.tun.DataPlaneRX = stats.RXPackets
	m.tun.DataPlaneBytes = stats.RXBytes
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

	reconcileProbeLocalDNSRuntimeForTUNProxyEnabled(false)
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

	now := time.Now().UTC().Format(time.RFC3339)
	m.tun.LastError = ""
	m.tun.UpdatedAt = now
	m.proxy.Enabled = true
	m.proxy.Mode = probeLocalProxyModeTUN
	m.proxy.LastError = ""
	m.proxy.UpdatedAt = now

	stats := probeLocalTUNDataPlaneStatsSnapshot()
	m.tun.Enabled = stats.Running
	m.tun.DataPlane = stats.Running
	m.tun.DataPlaneRX = stats.RXPackets
	m.tun.DataPlaneBytes = stats.RXBytes

	persistProbeLocalTUNStateBestEffort(m.tun.Installed, m.tun.DataPlane)
	if err := persistProbeLocalProxyPersistentState(true, probeLocalProxyModeTUN); err != nil {
		logProbeWarnf("probe local proxy persist enabled state failed: %v", err)
	}
	reconcileProbeLocalDNSRuntimeForTUNProxyEnabled(true)
	startProbeLocalProxyMonitor()
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
	if err := persistProbeLocalProxyPersistentState(false, probeLocalProxyModeDirect); err != nil {
		logProbeWarnf("probe local proxy persist direct state failed: %v", err)
	}
	reconcileProbeLocalDNSRuntimeForTUNProxyEnabled(false)
	stopProbeLocalProxyMonitor()
	if errStopDataPlane != nil {
		m.tun.LastError = strings.TrimSpace(errStopDataPlane.Error())
		m.proxy.LastError = m.tun.LastError
		return m.tun, m.proxy, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: m.tun.LastError}
	}
	return m.tun, m.proxy, nil
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
	if err := probeLocalRestoreProxyDirect(); err != nil {
		allErr = errors.Join(allErr, err)
	}
	if err := stopProbeLocalTUNDataPlane(); err != nil {
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
	m.tun.UpdatedAt = now
	m.proxy.Enabled = false
	m.proxy.Mode = probeLocalProxyModeDirect
	m.proxy.UpdatedAt = now
	if allErr != nil {
		m.tun.LastError = strings.TrimSpace(allErr.Error())
		m.proxy.LastError = m.tun.LastError
		persistProbeLocalTUNStateBestEffort(m.tun.Installed, false)
		if err := persistProbeLocalProxyPersistentState(false, probeLocalProxyModeDirect); err != nil {
			logProbeWarnf("probe local proxy persist reset state failed: %v", err)
		}
		reconcileProbeLocalDNSRuntimeForTUNProxyEnabled(false)
		stopProbeLocalProxyMonitor()
		return m.tun, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: m.tun.LastError}
	}
	m.tun.LastError = ""
	m.proxy.LastError = ""
	persistProbeLocalTUNStateBestEffort(m.tun.Installed, false)
	if err := persistProbeLocalProxyPersistentState(false, probeLocalProxyModeDirect); err != nil {
		logProbeWarnf("probe local proxy persist reset state failed: %v", err)
	}
	reconcileProbeLocalDNSRuntimeForTUNProxyEnabled(false)
	stopProbeLocalProxyMonitor()
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
	context probeLocalProxyRuntimeContext
}{}

var probeLocalProxyViewState = struct {
	mu       sync.RWMutex
	groups   probeLocalProxyGroupFile
	state    probeLocalProxyStateFile
	chains   []probeLinkChainServerItem
	runtimes map[string]probeLocalProxyGroupRuntimeSnapshot
}{
	groups:   defaultProbeLocalProxyGroupFile(),
	state:    defaultProbeLocalProxyStateFile(),
	chains:   []probeLinkChainServerItem{},
	runtimes: map[string]probeLocalProxyGroupRuntimeSnapshot{},
}

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
			LastUploadedAt:    "",
			LastUploadStatus:  "idle",
			LastUploadError:   "",
			LastRestoredAt:    "",
			LastRestoreStatus: "idle",
			LastRestoreError:  "",
		},
		TUN: probeLocalTUNPersistentState{
			Installed: false,
			Enabled:   false,
			UpdatedAt: now,
		},
		Proxy: probeLocalProxyPersistentState{
			Enabled:   false,
			Mode:      probeLocalProxyModeDirect,
			UpdatedAt: now,
		},
		Explicit: probeLocalExplicitProxyPersistentState{
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
	setProbeLocalProxyViewGroups(payload)
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
	if err := persistProbeLocalJSONFile(path, payload); err != nil {
		return err
	}
	setProbeLocalProxyViewGroups(payload)
	return nil
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
	if strings.TrimSpace(payload.Backup.LastRestoreStatus) == "" {
		payload.Backup.LastRestoreStatus = "idle"
	}
	if strings.TrimSpace(payload.TUN.UpdatedAt) == "" {
		payload.TUN.UpdatedAt = payload.UpdatedAt
	}
	normalizeProbeLocalProxyPersistentState(&payload)
	normalizeProbeLocalExplicitProxyPersistentState(&payload)
	setProbeLocalProxyViewState(payload)
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
	if strings.TrimSpace(payload.Backup.LastRestoreStatus) == "" {
		payload.Backup.LastRestoreStatus = "idle"
	}
	if strings.TrimSpace(payload.TUN.UpdatedAt) == "" {
		payload.TUN.UpdatedAt = now
	}
	normalizeProbeLocalProxyPersistentState(&payload)
	normalizeProbeLocalExplicitProxyPersistentState(&payload)
	path, err := resolveProbeLocalProxyStatePath()
	if err != nil {
		return err
	}
	if err := persistProbeLocalJSONFile(path, payload); err != nil {
		return err
	}
	setProbeLocalProxyViewState(payload)
	return nil
}

func normalizeProbeLocalProxyPersistentState(payload *probeLocalProxyStateFile) {
	if payload == nil {
		return
	}
	mode := strings.ToLower(strings.TrimSpace(payload.Proxy.Mode))
	if mode == "" {
		mode = probeLocalProxyModeDirect
		payload.Proxy.Enabled = false
	}
	if mode != probeLocalProxyModeTUN {
		mode = probeLocalProxyModeDirect
		payload.Proxy.Enabled = false
	}
	payload.Proxy.Mode = mode
	if strings.TrimSpace(payload.Proxy.UpdatedAt) == "" {
		payload.Proxy.UpdatedAt = payload.UpdatedAt
	}
}

func normalizeProbeLocalExplicitProxyPersistentState(payload *probeLocalProxyStateFile) {
	if payload == nil {
		return
	}
	if strings.TrimSpace(payload.Explicit.UpdatedAt) == "" {
		payload.Explicit.UpdatedAt = payload.UpdatedAt
	}
}

func shouldRestoreProbeLocalProxyFromState(state probeLocalProxyStateFile) bool {
	return state.Proxy.Enabled && strings.EqualFold(strings.TrimSpace(state.Proxy.Mode), probeLocalProxyModeTUN)
}

func shouldRestoreProbeLocalTUNFromState(state probeLocalProxyStateFile) bool {
	return state.TUN.Enabled
}

func shouldRestoreProbeLocalExplicitProxyFromState(state probeLocalProxyStateFile) bool {
	return state.Explicit.Enabled
}

func cleanupProbeLocalExplicitProxySystemSettingsOnStartup(state probeLocalProxyStateFile) {
	if shouldRestoreProbeLocalExplicitProxyFromState(state) {
		return
	}
	if err := restoreProbeLocalExplicitProxySystemSettings(); err != nil {
		logProbeWarnf("probe local explicit proxy startup cleanup failed: %v", err)
		return
	}
	logProbeInfof("probe local explicit proxy startup cleanup completed: enabled=false")
}

func persistProbeLocalTUNPersistentState(installed, enabled bool) error {
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return err
	}
	state.TUN.Installed = installed
	state.TUN.Enabled = enabled
	state.TUN.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		return err
	}
	resetProbeLocalDNSRuntimeCachesForProxyGroupRefresh()
	return nil
}

func persistProbeLocalProxyPersistentState(enabled bool, mode string) error {
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return err
	}
	cleanMode := strings.ToLower(strings.TrimSpace(mode))
	if cleanMode == "" {
		if enabled {
			cleanMode = probeLocalProxyModeTUN
		} else {
			cleanMode = probeLocalProxyModeDirect
		}
	}
	if cleanMode != probeLocalProxyModeTUN {
		cleanMode = probeLocalProxyModeDirect
		enabled = false
	}
	state.Proxy.Enabled = enabled
	state.Proxy.Mode = cleanMode
	state.Proxy.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := persistProbeLocalProxyStateFile(state); err != nil {
		return err
	}
	resetProbeLocalDNSRuntimeCachesForProxyGroupRefresh()
	return nil
}

func persistProbeLocalExplicitProxyPersistentState(enabled bool) error {
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return err
	}
	state.Explicit.Enabled = enabled
	state.Explicit.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return persistProbeLocalProxyStateFile(state)
}

func resolveProbeLocalTUNPrimaryDNSBackupPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalTUNPrimaryDNSBackupFileName), nil
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
		if matchesProbeLocalProxyChainSelection(item, chainID) {
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

func setProbeLocalBackupRestoreStatus(status, lastError, restoredAt string) error {
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return err
	}
	state.Backup.LastRestoreStatus = firstNonEmpty(strings.TrimSpace(status), "idle")
	state.Backup.LastRestoreError = strings.TrimSpace(lastError)
	state.Backup.LastRestoredAt = strings.TrimSpace(restoredAt)
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

func cloneProbeLocalProxyGroupFile(payload probeLocalProxyGroupFile) probeLocalProxyGroupFile {
	raw, err := json.Marshal(payload)
	if err != nil {
		return payload
	}
	var cloned probeLocalProxyGroupFile
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return payload
	}
	return cloned
}

func cloneProbeLocalProxyStateFile(payload probeLocalProxyStateFile) probeLocalProxyStateFile {
	raw, err := json.Marshal(payload)
	if err != nil {
		return payload
	}
	var cloned probeLocalProxyStateFile
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return payload
	}
	return cloned
}

func cloneProbeLocalProxyChainItems(items []probeLinkChainServerItem) []probeLinkChainServerItem {
	if len(items) == 0 {
		return []probeLinkChainServerItem{}
	}
	return sanitizeProbeChainServerItemsForCache(items)
}

func setProbeLocalProxyViewGroups(payload probeLocalProxyGroupFile) {
	probeLocalProxyViewState.mu.Lock()
	probeLocalProxyViewState.groups = cloneProbeLocalProxyGroupFile(payload)
	probeLocalProxyViewState.mu.Unlock()
}

func currentProbeLocalProxyViewGroups() probeLocalProxyGroupFile {
	probeLocalProxyViewState.mu.RLock()
	defer probeLocalProxyViewState.mu.RUnlock()
	return cloneProbeLocalProxyGroupFile(probeLocalProxyViewState.groups)
}

func setProbeLocalProxyViewState(payload probeLocalProxyStateFile) {
	probeLocalProxyViewState.mu.Lock()
	probeLocalProxyViewState.state = cloneProbeLocalProxyStateFile(payload)
	probeLocalProxyViewState.mu.Unlock()
}

func currentProbeLocalProxyViewState() probeLocalProxyStateFile {
	probeLocalProxyViewState.mu.RLock()
	defer probeLocalProxyViewState.mu.RUnlock()
	return cloneProbeLocalProxyStateFile(probeLocalProxyViewState.state)
}

func setProbeLocalProxyViewChains(items []probeLinkChainServerItem) {
	probeLocalProxyViewState.mu.Lock()
	probeLocalProxyViewState.chains = cloneProbeLocalProxyChainItems(items)
	probeLocalProxyViewState.mu.Unlock()
}

func currentProbeLocalProxyViewChains() []probeLinkChainServerItem {
	probeLocalProxyViewState.mu.RLock()
	defer probeLocalProxyViewState.mu.RUnlock()
	return cloneProbeLocalProxyChainItems(probeLocalProxyViewState.chains)
}

func setProbeLocalProxyViewGroupRuntimeSnapshot(group string, snapshot probeLocalProxyGroupRuntimeSnapshot) {
	cleanGroup := strings.TrimSpace(group)
	if cleanGroup == "" {
		return
	}
	snapshot.Group = cleanGroup
	probeLocalProxyViewState.mu.Lock()
	if probeLocalProxyViewState.runtimes == nil {
		probeLocalProxyViewState.runtimes = make(map[string]probeLocalProxyGroupRuntimeSnapshot)
	}
	key := strings.ToLower(cleanGroup)
	if snapshot.SelectedChainLatencyMS != nil {
		value := *snapshot.SelectedChainLatencyMS
		snapshot.SelectedChainLatencyMS = &value
	}
	probeLocalProxyViewState.runtimes[key] = snapshot
	probeLocalProxyViewState.mu.Unlock()
}

func currentProbeLocalProxyViewGroupRuntimeSnapshot(group string) (probeLocalProxyGroupRuntimeSnapshot, bool) {
	cleanGroup := strings.TrimSpace(group)
	if cleanGroup == "" {
		return probeLocalProxyGroupRuntimeSnapshot{}, false
	}
	probeLocalProxyViewState.mu.RLock()
	defer probeLocalProxyViewState.mu.RUnlock()
	snapshot, ok := probeLocalProxyViewState.runtimes[strings.ToLower(cleanGroup)]
	if !ok {
		return probeLocalProxyGroupRuntimeSnapshot{}, false
	}
	if snapshot.SelectedChainLatencyMS != nil {
		value := *snapshot.SelectedChainLatencyMS
		snapshot.SelectedChainLatencyMS = &value
	}
	return snapshot, true
}

func resetProbeLocalProxyViewGroupRuntimeSnapshots() {
	probeLocalProxyViewState.mu.Lock()
	probeLocalProxyViewState.runtimes = make(map[string]probeLocalProxyGroupRuntimeSnapshot)
	probeLocalProxyViewState.mu.Unlock()
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
	if groups, err := loadProbeLocalProxyGroupFile(); err != nil {
		logProbeErrorf("probe local proxy group config invalid, service will continue with defaults until fixed: %v", err)
		setProbeLocalProxyViewGroups(defaultProbeLocalProxyGroupFile())
	} else {
		setProbeLocalProxyViewGroups(groups)
	}
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		logProbeErrorf("probe local proxy state config invalid, service will continue with defaults until fixed: %v", err)
		setProbeLocalProxyViewState(defaultProbeLocalProxyStateFile())
	} else if strings.TrimSpace(state.TUN.UpdatedAt) == "" {
		state.TUN.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := persistProbeLocalProxyStateFile(state); err != nil {
			logProbeErrorf("probe local proxy state update failed, service will continue: %v", err)
		}
		cleanupProbeLocalExplicitProxySystemSettingsOnStartup(state)
	} else {
		cleanupProbeLocalExplicitProxySystemSettingsOnStartup(state)
	}
	if _, _, err := loadProbeLocalHostMappingsWithContent(); err != nil {
		logProbeErrorf("probe local proxy host config invalid, service will continue without static host mappings until fixed: %v", err)
	}
	if chains, err := loadProbeLocalProxyChainItems(); err != nil {
		logProbeErrorf("probe local proxy chain config invalid, service will continue without cached chains until fixed: %v", err)
		setProbeLocalProxyViewChains(nil)
	} else {
		setProbeLocalProxyViewChains(chains)
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
	setProbeLocalProxyViewChains(out)
	rememberProbeChainAuthTicketsForItems(out)
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
	client, closeClient, err := newProbeResolvedHTTPClientForURL(requestURL, 15*time.Second)
	if err != nil {
		return err
	}
	defer closeClient()
	resp, err := client.Do(req)
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

func restoreProbeLocalProxyGroupFromController(ctx context.Context) (string, error) {
	runtimeContext := currentProbeLocalProxyRuntimeContext()
	baseURL := strings.TrimSpace(runtimeContext.ControllerBaseURL)
	if baseURL == "" {
		return "", &probeLocalHTTPError{Status: http.StatusConflict, Message: "controller base url is empty"}
	}

	requestURL := strings.TrimRight(baseURL, "/") + probeLocalProxyBackupAPIPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", err
	}
	for key, value := range buildProbeAuthHeaders(runtimeContext.Identity) {
		req.Header.Set(key, value)
	}
	client, closeClient, err := newProbeResolvedHTTPClientForURL(requestURL, 15*time.Second)
	if err != nil {
		return "", err
	}
	defer closeClient()
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		message := strings.TrimSpace(string(responseBody))
		if message == "" {
			message = "controller backup restore failed"
		}
		return "", &probeLocalHTTPError{Status: http.StatusBadGateway, Message: fmt.Sprintf("controller backup restore failed: %d %s", resp.StatusCode, message)}
	}

	var payload struct {
		FileName      string `json:"file_name"`
		ContentBase64 string `json:"content_base64"`
		UpdatedAt     string `json:"updated_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, probeLocalProxyReadBodyMaxLen)).Decode(&payload); err != nil {
		return "", &probeLocalHTTPError{Status: http.StatusBadGateway, Message: "controller backup restore response is invalid"}
	}
	if fileName := firstNonEmpty(strings.TrimSpace(payload.FileName), probeLocalProxyGroupFileName); fileName != probeLocalProxyGroupFileName {
		return "", &probeLocalHTTPError{Status: http.StatusBadGateway, Message: "controller backup file_name is invalid"}
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload.ContentBase64))
	if err != nil || len(raw) == 0 {
		return "", &probeLocalHTTPError{Status: http.StatusBadGateway, Message: "controller backup content_base64 is invalid"}
	}
	var groups probeLocalProxyGroupFile
	if err := decodeProbeLocalJSONStrict(raw, &groups); err != nil {
		return "", &probeLocalHTTPError{Status: http.StatusBadGateway, Message: "controller backup content is invalid"}
	}
	if err := persistProbeLocalProxyGroupFile(groups); err != nil {
		return "", err
	}
	resetProbeLocalDNSRuntimeCachesForProxyGroupRefresh()
	return strings.TrimSpace(payload.UpdatedAt), nil
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
		logProbeWarnf("probe local console binding to a non-loopback address (%s): the local UI will be reachable from the network — make sure a strong local password is set", addr)
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
	mux.HandleFunc("/local/proxy", probeLocalProxyPageHandler)
	mux.HandleFunc("/local/dns", probeLocalDNSPageHandler)
	mux.HandleFunc("/local/logs", probeLocalLogsPageHandler)
	mux.HandleFunc("/local/monitor", probeLocalMonitorPageHandler)
	mux.HandleFunc("/local/system", probeLocalSystemPageHandler)
	mux.HandleFunc("/local/api/auth/bootstrap", probeLocalAuthBootstrapHandler)
	mux.HandleFunc("/local/api/auth/register", probeLocalAuthRegisterHandler)
	mux.HandleFunc("/local/api/auth/login", probeLocalAuthLoginHandler)
	mux.HandleFunc("/local/api/auth/logout", probeLocalAuthLogoutHandler)
	mux.HandleFunc("/local/api/auth/session", probeLocalAuthSessionHandler)

	mux.HandleFunc("/local/api/tun/status", probeLocalTUNStatusHandler)
	mux.HandleFunc("/local/api/tun/install", probeLocalTUNInstallHandler)
	mux.HandleFunc("/local/api/tun/reset", probeLocalTUNResetHandler)
	mux.HandleFunc("/local/api/tun/uninstall", probeLocalTUNUninstallHandler)
	mux.HandleFunc("/local/api/logs", probeLocalLogsHandler)
	mux.HandleFunc("/local/api/proxy/enable", probeLocalProxyEnableHandler)
	mux.HandleFunc("/local/api/proxy/select", probeLocalProxySelectHandler)
	mux.HandleFunc("/local/api/proxy/direct", probeLocalProxyDirectHandler)
	mux.HandleFunc("/local/api/proxy/explicit/enable", probeLocalProxyExplicitEnableHandler)
	mux.HandleFunc("/local/api/proxy/explicit/direct", probeLocalProxyExplicitDirectHandler)
	mux.HandleFunc("/local/api/proxy/reject", probeLocalProxyRejectHandler)
	mux.HandleFunc("/local/api/proxy/status", probeLocalProxyStatusHandler)
	mux.HandleFunc("/local/api/proxy/status/refresh", probeLocalProxyStatusRefreshHandler)
	mux.HandleFunc("/local/api/proxy/monitor", probeLocalProxyMonitorHandler)
	mux.HandleFunc("/local/api/proxy/remote/tcp_debug", probeLocalProxyRemoteTCPDebugHandler)
	mux.HandleFunc("/local/api/proxy/remote/speed_debug", probeLocalProxyRemoteSpeedDebugHandler)
	mux.HandleFunc("/local/api/proxy/chains", probeLocalProxyChainsHandler)
	mux.HandleFunc("/local/api/proxy/chains/refresh", probeLocalProxyChainsRefreshHandler)
	mux.HandleFunc("/local/api/proxy/link/status", probeLocalProxyLinkStatusHandler)
	mux.HandleFunc("/local/api/proxy/link/latency", probeLocalProxyLinkLatencyHandler)
	mux.HandleFunc("/local/api/proxy/link/speed", probeLocalProxyLinkSpeedHandler)
	mux.HandleFunc("/local/api/proxy/link/cf_ip_optimize", probeLocalProxyLinkCFIPOptimizeHandler)
	mux.HandleFunc("/local/api/proxy/groups", probeLocalProxyGroupsHandler)
	mux.HandleFunc("/local/api/proxy/groups/refresh", probeLocalProxyGroupsRefreshHandler)
	mux.HandleFunc("/local/api/proxy/groups/save", probeLocalProxyGroupsSaveHandler)
	mux.HandleFunc("/local/api/proxy/state", probeLocalProxyStateHandler)
	mux.HandleFunc("/local/api/proxy/hosts", probeLocalProxyHostsHandler)
	mux.HandleFunc("/local/api/proxy/hosts/save", probeLocalProxyHostsSaveHandler)
	mux.HandleFunc("/local/api/dns/status", probeLocalDNSStatusHandler)
	mux.HandleFunc("/local/api/dns/records", probeLocalDNSRecordsHandler)
	mux.HandleFunc("/local/api/dns/clear", probeLocalDNSClearHandler)
	mux.HandleFunc("/local/api/dns/real_ip/list", probeLocalDNSRealIPListHandler)
	mux.HandleFunc("/local/api/dns/real_ip/lookup", probeLocalDNSRealIPLookupHandler)
	mux.HandleFunc("/local/api/dns/fake_ip/list", probeLocalDNSFakeIPListHandler)
	mux.HandleFunc("/local/api/dns/fake_ip/lookup", probeLocalDNSFakeIPLookupHandler)
	mux.HandleFunc("/local/api/system/upgrade", probeLocalSystemUpgradeHandler)
	mux.HandleFunc("/local/api/system/upgrade/check", probeLocalSystemUpgradeCheckHandler)
	mux.HandleFunc("/local/api/system/upgrade/status", probeLocalSystemUpgradeStatusHandler)
	mux.HandleFunc("/local/api/system/restart", probeLocalSystemRestartHandler)
	mux.HandleFunc("/local/api/proxy/groups/backup", probeLocalProxyGroupsBackupHandler)
	mux.HandleFunc("/local/api/proxy/groups/restore", probeLocalProxyGroupsRestoreHandler)
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

type probeLocalProxyLinkProbeRequest struct {
	ChainID  string `json:"chain_id"`
	Protocol string `json:"protocol,omitempty"`
}

type probeLocalProxyLinkReachabilityResult struct {
	Protocol  string `json:"protocol"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

type probeLocalProxyLinkReachabilityStatus struct {
	ChainID        string                                  `json:"chain_id"`
	EntryHost      string                                  `json:"entry_host,omitempty"`
	EntryPort      int                                     `json:"entry_port,omitempty"`
	BestProtocol   string                                  `json:"best_protocol,omitempty"`
	ReachableCount int                                     `json:"reachable_count,omitempty"`
	TestedCount    int                                     `json:"tested_count,omitempty"`
	UpdatedAt      string                                  `json:"updated_at,omitempty"`
	Results        []probeLocalProxyLinkReachabilityResult `json:"results,omitempty"`
}

type probeLocalProxyLinkStatusItem struct {
	ChainID          string                                 `json:"chain_id"`
	ChainName        string                                 `json:"chain_name,omitempty"`
	ChainType        string                                 `json:"chain_type,omitempty"`
	RelayChainID     string                                 `json:"relay_chain_id,omitempty"`
	ClientEntryID    string                                 `json:"client_entry_id,omitempty"`
	ClientEntryType  string                                 `json:"client_entry_type,omitempty"`
	Route            []string                               `json:"route,omitempty"`
	EntryNodeID      string                                 `json:"entry_node_id,omitempty"`
	EntryHost        string                                 `json:"entry_host,omitempty"`
	EntryPort        int                                    `json:"entry_port,omitempty"`
	LinkLayer        string                                 `json:"link_layer,omitempty"`
	Endpoint         string                                 `json:"endpoint,omitempty"`
	SelectedGroups   []string                               `json:"selected_groups,omitempty"`
	Status           string                                 `json:"status,omitempty"`
	ObservedRateBPS  int64                                  `json:"observed_rate_bps,omitempty"`
	Reachability     *probeLocalProxyLinkReachabilityStatus `json:"reachability,omitempty"`
	ProtocolState    probeChainRelayProtocolStateSnapshot   `json:"protocol_state,omitempty"`
	SpeedTest        *probeLocalProxyLinkSpeedStatus        `json:"speed_test,omitempty"`
	CFOptimize       *probeLocalProxyLinkCFOptimizeStatus   `json:"cf_optimize,omitempty"`
	UnavailableError string                                 `json:"unavailable_error,omitempty"`
	UpdatedAt        string                                 `json:"updated_at,omitempty"`
}

type probeLocalProxyLinkCFOptimizeResult struct {
	IP        string `json:"ip"`
	Protocol  string `json:"protocol"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
	TestedAt  string `json:"tested_at,omitempty"`
}

type probeLocalProxyLinkCFOptimizeStatus struct {
	ChainID        string                                `json:"chain_id"`
	EntryHost      string                                `json:"entry_host,omitempty"`
	EntryPort      int                                   `json:"entry_port,omitempty"`
	Status         string                                `json:"status"`
	Running        bool                                  `json:"running"`
	CandidateCount int                                   `json:"candidate_count,omitempty"`
	PlannedCount   int                                   `json:"planned_count,omitempty"`
	TestedCount    int                                   `json:"tested_count,omitempty"`
	FailedCount    int                                   `json:"failed_count,omitempty"`
	BestIP         string                                `json:"best_ip,omitempty"`
	BestProtocol   string                                `json:"best_protocol,omitempty"`
	BestLatencyMS  int64                                 `json:"best_latency_ms,omitempty"`
	Error          string                                `json:"error,omitempty"`
	StartedAt      string                                `json:"started_at,omitempty"`
	FinishedAt     string                                `json:"finished_at,omitempty"`
	UpdatedAt      string                                `json:"updated_at,omitempty"`
	TopResults     []probeLocalProxyLinkCFOptimizeResult `json:"top_results,omitempty"`
	Results        []probeLocalProxyLinkCFOptimizeResult `json:"results,omitempty"`
}

var probeLocalProxyLinkCFOptimizeState = struct {
	mu    sync.Mutex
	items map[string]probeLocalProxyLinkCFOptimizeStatus
}{items: make(map[string]probeLocalProxyLinkCFOptimizeStatus)}

var probeLocalProxyLinkReachabilityState = struct {
	mu    sync.Mutex
	items map[string]probeLocalProxyLinkReachabilityStatus
}{items: make(map[string]probeLocalProxyLinkReachabilityStatus)}

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
	serveProbeLocalHTMLPage(w, r, "/local/panel", probeLocalPanelPageHTML)
}

func probeLocalProxyPageHandler(w http.ResponseWriter, r *http.Request) {
	serveProbeLocalHTMLPage(w, r, "/local/proxy", probeLocalProxyPageHTML)
}

func probeLocalDNSPageHandler(w http.ResponseWriter, r *http.Request) {
	serveProbeLocalHTMLPage(w, r, "/local/dns", probeLocalDNSPageHTML)
}

func probeLocalLogsPageHandler(w http.ResponseWriter, r *http.Request) {
	serveProbeLocalHTMLPage(w, r, "/local/logs", probeLocalLogsPageHTML)
}

func probeLocalMonitorPageHandler(w http.ResponseWriter, r *http.Request) {
	serveProbeLocalHTMLPage(w, r, "/local/monitor", probeLocalMonitorPageHTML)
}

func probeLocalSystemPageHandler(w http.ResponseWriter, r *http.Request) {
	serveProbeLocalHTMLPage(w, r, "/local/system", probeLocalSystemPageHTML)
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

func probeLocalDNSRecordsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": queryProbeLocalDNSUnifiedRecords(),
	})
}

func probeLocalDNSClearHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	clearProbeLocalDNSUnifiedCache()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
	tunState, proxyState, err := probeLocalControl.enableProxy()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	if selectedChainID != "" {
		syncProbeLocalTUNGroupRuntimeSelectionAsync(group, selectedChainID)
	}
	if updateErr := upsertProbeLocalRuntimeStateGroup(group, "tunnel", tunnelNodeID, "online"); updateErr != nil {
		logProbeWarnf("probe local runtime state update failed: %v", updateErr)
	}
	selectionEntry := probeLocalProxyStateGroupEntry{
		Group:           group,
		Action:          "tunnel",
		SelectedChainID: selectedChainID,
		TunnelNodeID:    tunnelNodeID,
		RuntimeStatus:   "online",
	}
	setProbeLocalProxyGroupRuntimeSelectionSnapshot(selectionEntry)
	refreshProbeLocalProxyGroupRuntimeSnapshotForEntryAsync(selectionEntry)
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
		syncProbeLocalTUNGroupRuntimeSelectionAsync(group, selectedChainID)
	}
	if updateErr := upsertProbeLocalRuntimeStateGroup(group, "tunnel", tunnelNodeID, "online"); updateErr != nil {
		logProbeWarnf("probe local runtime state update failed: %v", updateErr)
	}
	selectionEntry := probeLocalProxyStateGroupEntry{
		Group:           group,
		Action:          "tunnel",
		SelectedChainID: selectedChainID,
		TunnelNodeID:    tunnelNodeID,
		RuntimeStatus:   "online",
	}
	setProbeLocalProxyGroupRuntimeSelectionSnapshot(selectionEntry)
	refreshProbeLocalProxyGroupRuntimeSnapshotForEntryAsync(selectionEntry)
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
	setProbeLocalProxyViewGroupRuntimeSnapshot(group, probeLocalProxyGroupRuntimeSnapshot{Group: group})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"tun":   tunState,
		"proxy": proxyState,
		"selection": map[string]any{
			"group": group,
		},
	})
}

func probeLocalProxyExplicitEnableHandler(w http.ResponseWriter, r *http.Request) {
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
	if err := decoder.Decode(&struct{}{}); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		writeProbeLocalError(w, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: strings.TrimSpace(err.Error())})
		return
	}
	if err := startProbeLocalExplicitProxyServer(); err != nil {
		writeProbeLocalError(w, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: strings.TrimSpace(err.Error())})
		return
	}
	if err := persistProbeLocalExplicitProxyPersistentState(true); err != nil {
		writeProbeLocalError(w, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: strings.TrimSpace(err.Error())})
		return
	}
	preconnectDone := make(chan probeLocalProxyPreconnectResult, 1)
	go func() {
		result := preconnectProbeLocalTUNGroupRuntimesWithResult(state, "explicit_proxy_enable", false)
		if result.Attempted > 0 {
			logProbeInfof("probe local proxy group runtime preconnect completed: reason=%s attempted=%d connected=%d", "explicit_proxy_enable", result.Attempted, result.Connected)
		}
		preconnectDone <- result
	}()
	preconnectResult := probeLocalProxyPreconnectResult{Ready: true, Groups: []map[string]string{}}
	select {
	case preconnectResult = <-preconnectDone:
	case <-time.After(probeLocalExplicitProxyPreconnectWait):
		preconnectResult = probeLocalProxyPreconnectResult{Ready: false, Groups: []map[string]string{}}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"explicit_proxy": snapshotProbeLocalExplicitProxyStatus(),
		"preconnect":     preconnectResult,
	})
}

func probeLocalProxyExplicitDirectHandler(w http.ResponseWriter, r *http.Request) {
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
	if err := decoder.Decode(&struct{}{}); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	stopProbeLocalExplicitProxyServer()
	if err := persistProbeLocalExplicitProxyPersistentState(false); err != nil {
		writeProbeLocalError(w, &probeLocalHTTPError{Status: http.StatusInternalServerError, Message: strings.TrimSpace(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"explicit_proxy": snapshotProbeLocalExplicitProxyStatus(),
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
	setProbeLocalProxyViewGroupRuntimeSnapshot(group, probeLocalProxyGroupRuntimeSnapshot{Group: group})
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

func resolveProbeLocalChainNameByIDFromItems(chainID string, items []probeLinkChainServerItem) string {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return ""
	}
	for _, item := range items {
		if matchesProbeLocalProxyChainSelection(item, cleanID) {
			name := strings.TrimSpace(item.Name)
			if name != "" {
				return name
			}
			break
		}
	}
	return cleanID
}

func buildProbeLocalProxyGroupRuntimeSnapshot(entry probeLocalProxyStateGroupEntry) probeLocalProxyGroupRuntimeSnapshot {
	group := strings.TrimSpace(entry.Group)
	selectedChainID := firstNonEmpty(
		strings.TrimSpace(entry.SelectedChainID),
		mustProbeLocalSelectedChainIDFromLegacy(entry.TunnelNodeID),
	)
	snapshot := probeLocalProxyGroupRuntimeSnapshot{
		Group:           group,
		SelectedChainID: selectedChainID,
	}
	if group == "" || !strings.EqualFold(strings.TrimSpace(entry.Action), "tunnel") || selectedChainID == "" {
		return snapshot
	}

	syncProbeLocalTUNGroupRuntimeSelection(group, selectedChainID)
	rt := currentProbeLocalTUNGroupRuntime(group)
	if rt == nil {
		return snapshot
	}
	rtSnapshot := rt.snapshot()
	snapshot.GroupRuntimeStatus = strings.TrimSpace(rtSnapshot.RuntimeStatus)
	snapshot.ProtocolState = rtSnapshot.ProtocolState
	keepalive, latencyMS, latencyUpdatedAt, latencyError := probeLocalResolveGroupRuntimeLatency(rt)
	snapshot.SelectedChainKeepalive = strings.TrimSpace(keepalive)
	if latencyMS != nil {
		value := *latencyMS
		snapshot.SelectedChainLatencyMS = &value
		snapshot.SelectedChainLatencyStatus = "reachable"
	} else {
		snapshot.SelectedChainLatencyStatus = "unreachable"
	}
	snapshot.SelectedChainLatencyUpdatedAt = strings.TrimSpace(latencyUpdatedAt)
	snapshot.SelectedChainLatencyError = strings.TrimSpace(latencyError)
	return snapshot
}

func refreshProbeLocalProxyGroupRuntimeSnapshotForEntry(entry probeLocalProxyStateGroupEntry) probeLocalProxyGroupRuntimeSnapshot {
	snapshot := buildProbeLocalProxyGroupRuntimeSnapshot(entry)
	if strings.TrimSpace(snapshot.Group) != "" {
		setProbeLocalProxyViewGroupRuntimeSnapshot(snapshot.Group, snapshot)
	}
	return snapshot
}

func refreshProbeLocalProxyGroupRuntimeSnapshotForEntryAsync(entry probeLocalProxyStateGroupEntry) {
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				logProbeWarnf("probe local proxy group runtime snapshot refresh panic: group=%s err=%v", strings.TrimSpace(entry.Group), recovered)
			}
		}()
		refreshProbeLocalProxyGroupRuntimeSnapshotForEntry(entry)
	}()
}

func setProbeLocalProxyGroupRuntimeSelectionSnapshot(entry probeLocalProxyStateGroupEntry) {
	group := strings.TrimSpace(entry.Group)
	if group == "" {
		return
	}
	selectedChainID := firstNonEmpty(
		strings.TrimSpace(entry.SelectedChainID),
		mustProbeLocalSelectedChainIDFromLegacy(entry.TunnelNodeID),
	)
	setProbeLocalProxyViewGroupRuntimeSnapshot(group, probeLocalProxyGroupRuntimeSnapshot{
		Group:              group,
		SelectedChainID:    selectedChainID,
		GroupRuntimeStatus: strings.TrimSpace(entry.RuntimeStatus),
	})
}

func refreshProbeLocalProxyGroupRuntimeSnapshotsForState(state probeLocalProxyStateFile) map[string]probeLocalProxyGroupRuntimeSnapshot {
	snapshots := make(map[string]probeLocalProxyGroupRuntimeSnapshot, len(state.Groups))
	for _, entry := range state.Groups {
		snapshot := refreshProbeLocalProxyGroupRuntimeSnapshotForEntry(entry)
		if strings.TrimSpace(snapshot.Group) == "" {
			continue
		}
		snapshots[strings.ToLower(snapshot.Group)] = snapshot
	}
	return snapshots
}

func startProbeLocalProxyStatusRefreshAsync(state probeLocalProxyStateFile) map[string]any {
	probeLocalProxyStatusRefreshState.mu.Lock()
	if probeLocalProxyStatusRefreshState.running {
		payload := probeLocalProxyStatusRefreshStatePayloadLocked(false, true)
		probeLocalProxyStatusRefreshState.mu.Unlock()
		return payload
	}
	probeLocalProxyStatusRefreshState.running = true
	probeLocalProxyStatusRefreshState.lastStartedAt = time.Now().UTC().Format(time.RFC3339)
	probeLocalProxyStatusRefreshState.lastError = ""
	payload := probeLocalProxyStatusRefreshStatePayloadLocked(true, false)
	probeLocalProxyStatusRefreshState.mu.Unlock()

	go func(snapshotState probeLocalProxyStateFile) {
		errText := ""
		defer func() {
			if recovered := recover(); recovered != nil {
				errText = fmt.Sprintf("panic: %v", recovered)
			}
			probeLocalProxyStatusRefreshState.mu.Lock()
			probeLocalProxyStatusRefreshState.running = false
			probeLocalProxyStatusRefreshState.lastFinishedAt = time.Now().UTC().Format(time.RFC3339)
			probeLocalProxyStatusRefreshState.lastError = strings.TrimSpace(errText)
			probeLocalProxyStatusRefreshState.mu.Unlock()
		}()
		refreshProbeLocalProxyGroupRuntimeSnapshotsForState(snapshotState)
	}(cloneProbeLocalProxyStateFile(state))
	return payload
}

func probeLocalProxyStatusRefreshStatePayloadLocked(accepted bool, alreadyRunning bool) map[string]any {
	return map[string]any{
		"accepted":         accepted,
		"already_running":  alreadyRunning,
		"running":          probeLocalProxyStatusRefreshState.running,
		"last_started_at":  strings.TrimSpace(probeLocalProxyStatusRefreshState.lastStartedAt),
		"last_finished_at": strings.TrimSpace(probeLocalProxyStatusRefreshState.lastFinishedAt),
		"last_error":       strings.TrimSpace(probeLocalProxyStatusRefreshState.lastError),
	}
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
		"enabled":        status.Enabled,
		"mode":           status.Mode,
		"last_error":     status.LastError,
		"updated_at":     status.UpdatedAt,
		"explicit_proxy": snapshotProbeLocalExplicitProxyStatus(),
	}
	state := currentProbeLocalProxyViewState()
	chains := currentProbeLocalProxyViewChains()
	selectedChainID := ""
	for _, entry := range state.Groups {
		candidateChainID := firstNonEmpty(
			strings.TrimSpace(entry.SelectedChainID),
			mustProbeLocalSelectedChainIDFromLegacy(entry.TunnelNodeID),
		)
		if strings.TrimSpace(entry.Group) == "" || candidateChainID == "" {
			continue
		}
		selectedChainID = candidateChainID
		break
	}
	selectedTunnelNodeID := formatProbeLocalLegacyTunnelNodeID(selectedChainID)
	payload["selected_tunnel_node_id"] = selectedTunnelNodeID
	payload["selected_chain_id"] = selectedChainID
	payload["selected_chain_name"] = resolveProbeLocalChainNameByIDFromItems(selectedChainID, chains)
	payload["selected_chain_keepalive"] = ""
	payload["selected_chain_latency_status"] = ""
	payload["selected_chain_latency_updated_at"] = ""
	payload["selected_chain_latency_error"] = ""

	writeJSON(w, http.StatusOK, payload)
}

func probeLocalProxyStatusRefreshHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}

	status := probeLocalControl.proxyStatus()
	state := currentProbeLocalProxyViewState()
	refreshState := startProbeLocalProxyStatusRefreshAsync(state)

	payload := map[string]any{
		"ok":         true,
		"async":      true,
		"enabled":    status.Enabled,
		"mode":       status.Mode,
		"last_error": status.LastError,
		"updated_at": status.UpdatedAt,
		"refresh":    refreshState,
	}
	writeJSON(w, http.StatusOK, payload)
}

func probeLocalProxyMonitorHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, currentProbeLocalProxyMonitorSnapshot())
}

func probeLocalProxyRemoteTCPDebugHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	group := strings.TrimSpace(r.URL.Query().Get("group"))
	var rt *probeLocalTUNGroupRuntime
	if group != "" {
		rt = currentProbeLocalTUNGroupRuntime(group)
	} else {
		group, rt = resolveProbeLocalSelectedGroupRuntime(currentProbeLocalProxyViewState())
	}
	if rt == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "selected group runtime is unavailable",
			"group": group,
		})
		return
	}
	payload, err := rt.fetchRemoteTCPDebug()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"group": group,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"group":   group,
		"remote":  payload,
		"fetched": time.Now().UTC().Format(time.RFC3339),
	})
}

func probeLocalProxyRemoteSpeedDebugHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	group := strings.TrimSpace(r.URL.Query().Get("group"))
	var rt *probeLocalTUNGroupRuntime
	if group != "" {
		rt = currentProbeLocalTUNGroupRuntime(group)
	} else {
		group, rt = resolveProbeLocalSelectedGroupRuntime(currentProbeLocalProxyViewState())
	}
	if rt == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "selected group runtime is unavailable",
			"group": group,
		})
		return
	}
	payload, err := rt.fetchRemoteSpeedDebug()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"group": group,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"group":   group,
		"remote":  payload,
		"fetched": time.Now().UTC().Format(time.RFC3339),
	})
}

func resolveProbeLocalProxyLinkEndpoint(item probeLinkChainServerItem) (probeLocalTUNChainEndpoint, error) {
	chainID := strings.TrimSpace(item.ChainID)
	if chainID == "" {
		return probeLocalTUNChainEndpoint{}, errors.New("chain_id is required")
	}
	route := buildChainRoute(item)
	if len(route) == 0 {
		return probeLocalTUNChainEndpoint{}, fmt.Errorf("chain route is empty: %s", chainID)
	}
	entryNodeID := strings.TrimSpace(route[0])
	entryHost := ""
	entryPort := 0
	linkLayer := normalizeProbeChainLinkLayer(strings.TrimSpace(item.LinkLayer))
	for _, hop := range item.HopConfigs {
		hopNodeID := normalizeProbeChainNodeID(strconv.Itoa(hop.NodeNo))
		if hopNodeID == "" || hopNodeID != normalizeProbeChainNodeID(entryNodeID) {
			continue
		}
		entryHost = strings.TrimSpace(hop.RelayHost)
		if hop.ExternalPort > 0 {
			entryPort = hop.ExternalPort
		} else if hop.ListenPort > 0 {
			entryPort = hop.ListenPort
		}
		linkLayer = normalizeProbeChainLinkLayer(firstNonEmpty(strings.TrimSpace(hop.LinkLayer), strings.TrimSpace(item.LinkLayer)))
		break
	}
	if entryHost == "" {
		return probeLocalTUNChainEndpoint{}, fmt.Errorf("selected chain entry host is unavailable: %s", chainID)
	}
	if entryPort <= 0 {
		return probeLocalTUNChainEndpoint{}, fmt.Errorf("selected chain entry port is unavailable: %s", chainID)
	}
	return probeLocalTUNChainEndpoint{
		ChainID:             effectiveProbeLocalRelayChainID(item),
		ChainName:           strings.TrimSpace(item.Name),
		EntryNodeID:         entryNodeID,
		EntryHost:           entryHost,
		EntryPort:           entryPort,
		LinkLayer:           linkLayer,
		ChainSecret:         strings.TrimSpace(item.Secret),
		AuthTicket:          strings.TrimSpace(item.AuthTicket),
		PreserveRelayDomain: isProbeLocalProxyLinkCFEntry(item),
	}, nil
}

func findProbeLocalProxyLinkItemByID(chainID string, items []probeLinkChainServerItem) (probeLinkChainServerItem, bool) {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return probeLinkChainServerItem{}, false
	}
	for _, item := range items {
		if matchesProbeLocalProxyChainSelection(item, cleanID) {
			return item, true
		}
	}
	return probeLinkChainServerItem{}, false
}

func selectedProbeLocalProxyGroupsByChainID(state probeLocalProxyStateFile) map[string][]string {
	out := make(map[string][]string, len(state.Groups))
	for _, entry := range state.Groups {
		if !strings.EqualFold(strings.TrimSpace(entry.Action), "tunnel") {
			continue
		}
		group := strings.TrimSpace(entry.Group)
		chainID := firstNonEmpty(strings.TrimSpace(entry.SelectedChainID), mustProbeLocalSelectedChainIDFromLegacy(entry.TunnelNodeID))
		if group == "" || chainID == "" {
			continue
		}
		out[strings.ToLower(chainID)] = append(out[strings.ToLower(chainID)], group)
	}
	return out
}

func probeLocalProxyChainIDMatchVariants(values ...string) []string {
	seen := make(map[string]struct{}, len(values)*2)
	out := make([]string, 0, len(values)*2)
	add := func(raw string) {
		clean := strings.TrimSpace(raw)
		if clean == "" {
			return
		}
		if strings.HasPrefix(strings.ToLower(clean), "chain:") {
			clean = strings.TrimSpace(clean[len("chain:"):])
		}
		if clean == "" {
			return
		}
		variants := []string{clean, strings.ToLower(clean)}
		lower := strings.ToLower(clean)
		if strings.HasSuffix(lower, "_pub") || strings.HasSuffix(lower, "-pub") {
			variants = append(variants, clean[:len(clean)-4], lower[:len(lower)-4])
		}
		for _, variant := range variants {
			key := strings.ToLower(strings.TrimSpace(variant))
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, key)
		}
	}
	for _, value := range values {
		add(value)
	}
	return out
}

func probeLocalProxySpeedDebugPayloadHasChain(payload probeSpeedDebugResultPayload, item probeLinkChainServerItem) bool {
	match := make(map[string]struct{})
	for _, id := range probeLocalProxyChainIDMatchVariants(item.ChainID, item.RelayChainID, item.ClientEntryID) {
		match[id] = struct{}{}
	}
	if len(match) == 0 {
		return false
	}
	for _, sample := range append(append([]probeSpeedDebugItemPayload{}, payload.Active...), payload.Recent...) {
		for _, id := range probeLocalProxyChainIDMatchVariants(sample.ChainID) {
			if _, ok := match[id]; ok {
				return true
			}
		}
	}
	return false
}

func probeLocalProxyGroupRuntimeForLink(item probeLinkChainServerItem) (string, *probeLocalTUNGroupRuntime) {
	state := currentProbeLocalProxyViewState()
	selectedGroups := selectedProbeLocalProxyGroupsByChainID(state)
	for _, id := range probeLocalProxyChainIDMatchVariants(item.ChainID, item.RelayChainID, item.ClientEntryID) {
		for _, group := range selectedGroups[id] {
			if rt := currentProbeLocalTUNGroupRuntime(group); rt != nil {
				return group, rt
			}
		}
	}
	return resolveProbeLocalSelectedGroupRuntime(state)
}

func runProbeLocalProxyLinkRemoteSpeedDebugFetch(item probeLinkChainServerItem, endpoint probeLocalTUNChainEndpoint, protocol string) map[string]any {
	if strings.TrimSpace(endpoint.EntryHost) != "" && endpoint.EntryPort > 0 {
		var lastPayload probeSpeedDebugResultPayload
		var lastErr error
		for idx, wait := range []time.Duration{0, 450 * time.Millisecond, 1200 * time.Millisecond} {
			if wait > 0 {
				time.Sleep(wait)
			}
			payload, err := probeLocalProxyRelaySpeedDebugFetch(endpoint.ChainID, endpoint.ChainSecret, endpoint.EntryHost, endpoint.EntryPort, endpoint.LinkLayer, protocol, 1200*time.Millisecond)
			if err != nil {
				lastErr = err
				continue
			}
			lastPayload = payload
			if probeLocalProxySpeedDebugPayloadHasChain(payload, item) || idx == 2 {
				return map[string]any{
					"ok":      true,
					"group":   "",
					"source":  "relay_entry",
					"remote":  payload,
					"fetched": time.Now().UTC().Format(time.RFC3339),
				}
			}
		}
		if strings.TrimSpace(lastPayload.Type) != "" || lastPayload.OK || len(lastPayload.Active) > 0 || len(lastPayload.Recent) > 0 {
			return map[string]any{
				"ok":      true,
				"group":   "",
				"source":  "relay_entry",
				"remote":  lastPayload,
				"fetched": time.Now().UTC().Format(time.RFC3339),
			}
		}
		if lastErr != nil {
			logProbeWarnf("probe local proxy direct relay speed debug fetch failed: chain=%s relay=%s:%d err=%v", strings.TrimSpace(endpoint.ChainID), strings.TrimSpace(endpoint.EntryHost), endpoint.EntryPort, lastErr)
		}
	}
	group, rt := probeLocalProxyGroupRuntimeForLink(item)
	if rt == nil {
		return map[string]any{
			"ok":      false,
			"group":   group,
			"error":   "selected group runtime is unavailable",
			"fetched": time.Now().UTC().Format(time.RFC3339),
		}
	}
	var lastPayload probeSpeedDebugResultPayload
	var lastErr error
	for idx, wait := range []time.Duration{0, 450 * time.Millisecond, 1200 * time.Millisecond} {
		if wait > 0 {
			time.Sleep(wait)
		}
		payload, err := rt.fetchRemoteSpeedDebug()
		if err != nil {
			lastErr = err
			continue
		}
		lastPayload = payload
		if probeLocalProxySpeedDebugPayloadHasChain(payload, item) || idx == 2 {
			return map[string]any{
				"ok":      true,
				"group":   group,
				"source":  "management",
				"remote":  payload,
				"fetched": time.Now().UTC().Format(time.RFC3339),
			}
		}
	}
	if strings.TrimSpace(lastPayload.Type) != "" || lastPayload.OK || len(lastPayload.Active) > 0 || len(lastPayload.Recent) > 0 {
		return map[string]any{
			"ok":      true,
			"group":   group,
			"source":  "management",
			"remote":  lastPayload,
			"fetched": time.Now().UTC().Format(time.RFC3339),
		}
	}
	errText := "remote speed debug fetch failed"
	if lastErr != nil {
		errText = lastErr.Error()
	}
	return map[string]any{
		"ok":      false,
		"group":   group,
		"error":   errText,
		"fetched": time.Now().UTC().Format(time.RFC3339),
	}
}

func observedProbeLocalProxyLinkRateBPS(snapshot probeChainRelayProtocolStateSnapshot) int64 {
	var best int64
	for _, quality := range snapshot.ProtocolQualities {
		if quality.RateBPS > best {
			best = quality.RateBPS
		}
	}
	return best
}

func buildProbeLocalProxyLinkStatusItems() []probeLocalProxyLinkStatusItem {
	items := currentProbeLocalProxyViewChains()
	state := currentProbeLocalProxyViewState()
	selectedGroups := selectedProbeLocalProxyGroupsByChainID(state)
	out := make([]probeLocalProxyLinkStatusItem, 0, len(items))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, item := range items {
		chainID := strings.TrimSpace(item.ChainID)
		if chainID == "" {
			continue
		}
		status := probeLocalProxyLinkStatusItem{
			ChainID:         chainID,
			ChainName:       strings.TrimSpace(item.Name),
			ChainType:       strings.TrimSpace(item.ChainType),
			RelayChainID:    strings.TrimSpace(item.RelayChainID),
			ClientEntryID:   strings.TrimSpace(item.ClientEntryID),
			ClientEntryType: strings.TrimSpace(item.ClientEntryType),
			Route:           buildChainRoute(item),
			SelectedGroups:  append([]string{}, selectedGroups[strings.ToLower(chainID)]...),
			Status:          "unknown",
			CFOptimize:      snapshotProbeLocalProxyLinkCFOptimizeStatus(chainID),
			UpdatedAt:       now,
		}
		endpoint, err := resolveProbeLocalProxyLinkEndpoint(item)
		if err != nil {
			status.Status = "unconfigured"
			status.UnavailableError = strings.TrimSpace(err.Error())
			out = append(out, status)
			continue
		}
		status.EntryNodeID = endpoint.EntryNodeID
		status.EntryHost = endpoint.EntryHost
		status.EntryPort = endpoint.EntryPort
		status.LinkLayer = endpoint.LinkLayer
		status.Endpoint = net.JoinHostPort(endpoint.EntryHost, strconv.Itoa(endpoint.EntryPort))
		status.Reachability = snapshotProbeLocalProxyLinkReachabilityStatus(chainID)
		if status.Reachability != nil && status.Reachability.TestedCount > 0 {
			if status.Reachability.ReachableCount > 0 {
				status.Status = "reachable"
			} else {
				status.Status = "unreachable"
			}
		}
		status.ProtocolState = snapshotProbeLocalTUNChainRelayProtocolState(endpoint.EntryHost, endpoint.EntryPort)
		status.ObservedRateBPS = observedProbeLocalProxyLinkRateBPS(status.ProtocolState)
		status.SpeedTest = snapshotProbeLocalProxyLinkSpeedStatus(chainID)
		if status.Status == "unknown" && (strings.TrimSpace(status.ProtocolState.SelectedProtocol) != "" || len(status.ProtocolState.ProtocolQualities) > 0) {
			status.Status = "observed"
		}
		out = append(out, status)
	}
	return out
}

func runProbeLocalProxyLinkHandshakeProbe(endpoint probeLocalTUNChainEndpoint) (net.Conn, error) {
	return openProbeLocalTUNChainRelayNetConnForEndpoint(endpoint, probeChainBridgeRoleToNext)
}

func runProbeLocalProxyLinkProtocolProbe(endpoint probeLocalTUNChainEndpoint, protocol string) (time.Duration, error) {
	cleanProtocol := normalizeProbeChainLinkLayer(protocol)
	switch cleanProtocol {
	case "websocket-h3", "websocket":
	default:
		return 0, fmt.Errorf("unsupported relay protocol: %s", protocol)
	}
	return probeLocalProxyLinkPingPongProbe(endpoint, cleanProtocol)
}

func probeLocalProxyLinkPingPongProbe(endpoint probeLocalTUNChainEndpoint, protocol string) (time.Duration, error) {
	const payloadBytes = 64
	var conn net.Conn
	var err error
	if endpoint.PreserveRelayDomain {
		conn, err = openProbeChainRelayNetConnWithLayerConnAndDomainPolicy(endpoint.ChainID, endpoint.ChainSecret, endpoint.EntryHost, endpoint.EntryPort, protocol, probeChainBridgeRoleToNext, probeChainRelayProtocolProbeTimeout, true)
	} else {
		conn, err = probeLocalProxyLinkOpenRelayConn(endpoint.ChainID, endpoint.ChainSecret, endpoint.EntryHost, endpoint.EntryPort, protocol, probeChainBridgeRoleToNext, probeChainRelayProtocolProbeTimeout)
	}
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	stream, err := openProbeLocalProxyLinkPingPongStream(conn, payloadBytes)
	if err != nil {
		return 0, err
	}
	defer stream.Close()
	payload := make([]byte, payloadBytes)
	for i := range payload {
		payload[i] = byte((i * 31) % 251)
	}
	echo := make([]byte, payloadBytes)
	startedAt := time.Now()
	_ = stream.SetDeadline(time.Now().Add(probeChainRelayProtocolProbeTimeout))
	if _, err := stream.Write(payload); err != nil {
		_ = stream.SetDeadline(time.Time{})
		return 0, err
	}
	if _, err := io.ReadFull(stream, echo); err != nil {
		_ = stream.SetDeadline(time.Time{})
		return 0, err
	}
	_ = stream.SetDeadline(time.Time{})
	if !bytes.Equal(payload, echo) {
		return 0, errors.New("ping-pong echo mismatch")
	}
	elapsed := time.Since(startedAt)
	if elapsed <= 0 {
		return time.Millisecond, nil
	}
	return elapsed, nil
}

func openProbeLocalProxyLinkPingPongStream(conn net.Conn, payloadBytes int64) (net.Conn, error) {
	if conn == nil {
		return nil, errors.New("relay connection is nil")
	}
	session, err := yamux.Client(conn, newProbeChainYamuxConfig())
	if err != nil {
		return nil, err
	}
	stream, err := session.Open()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	if err := writeProbeLocalProxyLinkPingPongRequest(stream, payloadBytes); err != nil {
		_ = stream.Close()
		_ = session.Close()
		return nil, err
	}
	return &probeLocalProxyLinkPingPongStreamConn{Conn: stream, session: session}, nil
}

type probeLocalProxyLinkPingPongStreamConn struct {
	net.Conn
	session *yamux.Session
}

func (c *probeLocalProxyLinkPingPongStreamConn) Close() error {
	var firstErr error
	if c != nil && c.Conn != nil {
		firstErr = c.Conn.Close()
	}
	if c != nil && c.session != nil {
		if err := c.session.Close(); firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func writeProbeLocalProxyLinkPingPongRequest(stream net.Conn, payloadBytes int64) error {
	_ = stream.SetWriteDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	if err := json.NewEncoder(stream).Encode(probeChainTunnelOpenRequest{Type: probeChainRelayModePingPong, PingBytes: payloadBytes}); err != nil {
		_ = stream.SetWriteDeadline(time.Time{})
		return err
	}
	_ = stream.SetWriteDeadline(time.Time{})
	_ = stream.SetReadDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
	var response probeChainTunnelOpenResponse
	if err := json.NewDecoder(stream).Decode(&response); err != nil {
		_ = stream.SetReadDeadline(time.Time{})
		return err
	}
	_ = stream.SetReadDeadline(time.Time{})
	if !response.OK {
		return errors.New(firstNonEmpty(strings.TrimSpace(response.Error), "ping-pong open failed"))
	}
	return nil
}

func probeLocalProxyLinkReachabilityProtocols() []string {
	return []string{"websocket-h3", "websocket"}
}

func probeLocalProxyLinkReachabilityProtocolsForEndpoint(item probeLinkChainServerItem, endpoint probeLocalTUNChainEndpoint) []string {
	if isProbeLocalProxyLinkCFEntry(item) {
		return []string{"websocket"}
	}
	candidates := probeChainRelayProtocolCandidates(endpoint.LinkLayer)
	if len(candidates) == 0 {
		return probeLocalProxyLinkReachabilityProtocols()
	}
	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		protocol := normalizeProbeChainLinkLayer(candidate)
		if protocol == "" {
			continue
		}
		if _, ok := seen[protocol]; ok {
			continue
		}
		switch protocol {
		case "websocket-h3", "websocket":
			seen[protocol] = struct{}{}
			out = append(out, protocol)
		}
	}
	if len(out) == 0 {
		return probeLocalProxyLinkReachabilityProtocols()
	}
	return out
}

func runProbeLocalProxyLinkSpeedProbe(endpoint probeLocalTUNChainEndpoint, protocol string) []probeChainRelaySpeedTestResult {
	return probeLocalTUNChainRelaySpeedTest(endpoint, protocol)
}

func snapshotProbeLocalProxyLinkReachabilityStatus(chainID string) *probeLocalProxyLinkReachabilityStatus {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return nil
	}
	probeLocalProxyLinkReachabilityState.mu.Lock()
	status, ok := probeLocalProxyLinkReachabilityState.items[cleanID]
	probeLocalProxyLinkReachabilityState.mu.Unlock()
	if !ok {
		return nil
	}
	status.Results = append([]probeLocalProxyLinkReachabilityResult{}, status.Results...)
	return &status
}

func storeProbeLocalProxyLinkReachabilityStatus(chainID string, endpoint probeLocalTUNChainEndpoint, bestProtocol string, reachableCount int, results []probeLocalProxyLinkReachabilityResult) {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return
	}
	status := probeLocalProxyLinkReachabilityStatus{
		ChainID:        cleanID,
		EntryHost:      strings.TrimSpace(endpoint.EntryHost),
		EntryPort:      endpoint.EntryPort,
		BestProtocol:   normalizeProbeChainLinkLayer(bestProtocol),
		ReachableCount: reachableCount,
		TestedCount:    len(results),
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
		Results:        append([]probeLocalProxyLinkReachabilityResult{}, results...),
	}
	probeLocalProxyLinkReachabilityState.mu.Lock()
	probeLocalProxyLinkReachabilityState.items[cleanID] = status
	probeLocalProxyLinkReachabilityState.mu.Unlock()
}

func defaultProbeLocalProxyLinkCFIPLookup(ctx context.Context, host string) ([]net.IP, error) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return nil, errors.New("cf entry host is empty")
	}
	cidrs, err := probeLocalFetchCloudflareIPv4CIDRs(ctx)
	if err != nil {
		return nil, err
	}
	ips, err := sampleProbeLocalCloudflareIPs(cleanHost, cidrs, probeLocalProxyLinkCFOptimizeMaxIPs)
	if err != nil {
		return nil, err
	}
	if len(ips) < probeLocalProxyLinkCFOptimizeMaxIPs {
		return nil, fmt.Errorf("cloudflare candidate sampling returned only %d ip(s)", len(ips))
	}
	return ips, nil
}

func defaultProbeLocalFetchCloudflareIPv4CIDRs(ctx context.Context) ([]string, error) {
	type probeLocalCloudflareIPResponse struct {
		Success bool `json:"success"`
		Result  struct {
			IPv4CIDRs []string `json:"ipv4_cidrs"`
		} `json:"result"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	client := &http.Client{Timeout: 8 * time.Second}
	apiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.cloudflare.com/client/v4/ips", nil)
	if err == nil {
		apiReq.Header.Set("Accept", "application/json")
		apiReq.Header.Set("User-Agent", "probe-node-cf-optimize/1.0")
		resp, doErr := client.Do(apiReq)
		if doErr == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var payload probeLocalCloudflareIPResponse
				if decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); decodeErr == nil {
					cidrs := cleanProbeLocalCloudflareCIDRs(payload.Result.IPv4CIDRs)
					if len(cidrs) > 0 {
						return cidrs, nil
					}
				}
			}
		} else {
			err = doErr
		}
	}

	textReq, textReqErr := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.cloudflare.com/ips-v4", nil)
	if textReqErr != nil {
		if err != nil {
			return nil, fmt.Errorf("fetch cloudflare ip ranges failed: api=%v, text=%v", err, textReqErr)
		}
		return nil, textReqErr
	}
	textReq.Header.Set("Accept", "text/plain")
	textReq.Header.Set("User-Agent", "probe-node-cf-optimize/1.0")
	resp, textErr := client.Do(textReq)
	if textErr != nil {
		if err != nil {
			return nil, fmt.Errorf("fetch cloudflare ip ranges failed: api=%v, text=%v", err, textErr)
		}
		return nil, textErr
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		trimmed := strings.TrimSpace(string(body))
		if err != nil {
			return nil, fmt.Errorf("fetch cloudflare ip ranges failed: api=%v, text status=%d body=%s", err, resp.StatusCode, trimmed)
		}
		return nil, fmt.Errorf("fetch cloudflare ip ranges failed: text status=%d body=%s", resp.StatusCode, trimmed)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		if err != nil {
			return nil, fmt.Errorf("fetch cloudflare ip ranges failed: api=%v, text=%v", err, readErr)
		}
		return nil, readErr
	}
	cidrs := cleanProbeLocalCloudflareCIDRs(strings.Fields(string(body)))
	if len(cidrs) == 0 {
		if err != nil {
			return nil, fmt.Errorf("fetch cloudflare ip ranges failed: api=%v, text returned no ipv4 cidr", err)
		}
		return nil, errors.New("cloudflare ipv4 cidr list is empty")
	}
	return cidrs, nil
}

func cleanProbeLocalCloudflareCIDRs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		candidate := strings.TrimSpace(value)
		if candidate == "" {
			continue
		}
		if _, ipNet, err := net.ParseCIDR(candidate); err != nil || ipNet == nil || ipNet.IP.To4() == nil {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	sort.Strings(out)
	return out
}

func sampleProbeLocalCloudflareIPs(host string, cidrs []string, count int) ([]net.IP, error) {
	if count <= 0 {
		return nil, errors.New("cloudflare sample count must be positive")
	}
	cleanCIDRs := cleanProbeLocalCloudflareCIDRs(cidrs)
	if len(cleanCIDRs) == 0 {
		return nil, errors.New("cloudflare ipv4 cidr list is empty")
	}

	type sampleRange struct {
		base       uint32
		firstHost  uint64
		hostCount  uint64
		start      uint64
		step       uint64
		iterations uint64
	}

	seedHasher := fnv.New32a()
	_, _ = seedHasher.Write([]byte(strings.ToLower(strings.TrimSpace(host))))
	hostSeed := uint64(seedHasher.Sum32())
	ranges := make([]sampleRange, 0, len(cleanCIDRs))
	for index, cidr := range cleanCIDRs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil || ipNet == nil {
			continue
		}
		ipv4 := ipNet.IP.To4()
		if ipv4 == nil {
			continue
		}
		ones, bits := ipNet.Mask.Size()
		if bits != 32 || ones < 0 || ones > 32 {
			continue
		}
		size := uint64(1) << uint(32-ones)
		firstHost := uint64(0)
		hostCount := size
		if size > 2 {
			firstHost = 1
			hostCount = size - 2
		}
		if hostCount == 0 {
			continue
		}
		base := probeLocalIPv4ToUint32(ipv4)
		start := (hostSeed + uint64(index+1)*1315423911) % hostCount
		step := probeLocalCoPrimeStep(hostCount, hostSeed+uint64(index+1)*2654435761)
		ranges = append(ranges, sampleRange{
			base:      base,
			firstHost: firstHost,
			hostCount: hostCount,
			start:     start,
			step:      step,
		})
	}
	if len(ranges) == 0 {
		return nil, errors.New("cloudflare ipv4 cidr list produced no usable range")
	}

	totalCapacity := uint64(0)
	for _, item := range ranges {
		totalCapacity += item.hostCount
	}
	limit := count
	if totalCapacity < uint64(limit) {
		limit = int(totalCapacity)
	}
	out := make([]net.IP, 0, limit)
	seen := make(map[uint32]struct{}, limit)
	for len(out) < limit {
		progressed := false
		for index := range ranges {
			if len(out) >= limit {
				break
			}
			item := &ranges[index]
			if item.iterations >= item.hostCount {
				continue
			}
			offset := (item.start + item.iterations*item.step) % item.hostCount
			item.iterations++
			candidate := item.base + uint32(item.firstHost+offset)
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			out = append(out, probeLocalUint32ToIPv4(candidate))
			progressed = true
		}
		if !progressed {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("cloudflare sample generated no ip")
	}
	return out, nil
}

func probeLocalIPv4ToUint32(ip net.IP) uint32 {
	ipv4 := ip.To4()
	if ipv4 == nil {
		return 0
	}
	return uint32(ipv4[0])<<24 | uint32(ipv4[1])<<16 | uint32(ipv4[2])<<8 | uint32(ipv4[3])
}

func probeLocalUint32ToIPv4(value uint32) net.IP {
	return net.IPv4(
		byte(value>>24),
		byte(value>>16),
		byte(value>>8),
		byte(value),
	)
}

func probeLocalCoPrimeStep(limit uint64, seed uint64) uint64 {
	if limit <= 1 {
		return 1
	}
	step := (seed % limit) + 1
	for probeLocalGCD64(step, limit) != 1 {
		step++
		if step > limit {
			step = 1
		}
	}
	return step
}

func probeLocalGCD64(a uint64, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

func buildProbeLocalProxyLinkCFOptimizeTopResults(results []probeLocalProxyLinkCFOptimizeResult) []probeLocalProxyLinkCFOptimizeResult {
	bestByIP := make(map[string]probeLocalProxyLinkCFOptimizeResult, len(results))
	for _, result := range results {
		if !result.OK {
			continue
		}
		ip := strings.TrimSpace(result.IP)
		if ip == "" {
			continue
		}
		current, ok := bestByIP[ip]
		if !ok || result.LatencyMS < current.LatencyMS || (result.LatencyMS == current.LatencyMS && strings.Compare(result.Protocol, current.Protocol) < 0) {
			bestByIP[ip] = result
		}
	}
	if len(bestByIP) == 0 {
		return nil
	}
	out := make([]probeLocalProxyLinkCFOptimizeResult, 0, len(bestByIP))
	for _, result := range bestByIP {
		out = append(out, result)
	}
	sort.Slice(out, func(i int, j int) bool {
		if out[i].LatencyMS != out[j].LatencyMS {
			return out[i].LatencyMS < out[j].LatencyMS
		}
		if out[i].IP != out[j].IP {
			return out[i].IP < out[j].IP
		}
		return out[i].Protocol < out[j].Protocol
	})
	if len(out) > probeLocalProxyLinkCFOptimizeTopN {
		out = out[:probeLocalProxyLinkCFOptimizeTopN]
	}
	return append([]probeLocalProxyLinkCFOptimizeResult{}, out...)
}

func probeLocalPreviewTexts(values []string, limit int) string {
	if len(values) == 0 {
		return ""
	}
	if limit <= 0 || len(values) <= limit {
		return strings.Join(values, ",")
	}
	return strings.Join(values[:limit], ",") + fmt.Sprintf(" ... total=%d", len(values))
}

func runProbeLocalProxyLinkCFIPProbe(endpoint probeLocalTUNChainEndpoint, ip string, protocol string) (time.Duration, error) {
	cleanIP := strings.TrimSpace(strings.Trim(ip, "[]"))
	if cleanIP == "" || net.ParseIP(cleanIP) == nil {
		return 0, fmt.Errorf("invalid candidate ip: %s", ip)
	}
	cleanProtocol := normalizeProbeChainLinkLayer(protocol)
	switch cleanProtocol {
	case "websocket":
	default:
		return 0, fmt.Errorf("invalid cf probe protocol: %s", protocol)
	}
	log.Printf("probe local cf optimize probe start: chain=%s entry=%s:%d candidate_ip=%s protocol=%s timeout=%s", endpoint.ChainID, endpoint.EntryHost, endpoint.EntryPort, cleanIP, cleanProtocol, probeLocalProxyLinkCFOptimizeTimeout)
	startedAt := time.Now()
	conn, err := openProbeLocalTUNChainRelayNetConnWithResolvedHost(
		endpoint.ChainID,
		endpoint.ChainSecret,
		endpoint.EntryHost,
		endpoint.EntryPort,
		cleanProtocol,
		probeChainBridgeRoleToNext,
		cleanIP,
		endpoint.EntryHost,
		probeLocalProxyLinkCFOptimizeTimeout,
		false,
	)
	latency := time.Since(startedAt)
	if err != nil {
		log.Printf("probe local cf optimize probe failed: chain=%s entry=%s:%d candidate_ip=%s protocol=%s latency_ms=%d err=%v", endpoint.ChainID, endpoint.EntryHost, endpoint.EntryPort, cleanIP, cleanProtocol, probeDurationMilliseconds(latency), err)
		return latency, err
	}
	_ = conn.Close()
	log.Printf("probe local cf optimize probe connected: chain=%s entry=%s:%d candidate_ip=%s protocol=%s latency_ms=%d", endpoint.ChainID, endpoint.EntryHost, endpoint.EntryPort, cleanIP, cleanProtocol, probeDurationMilliseconds(latency))
	return latency, nil
}

func probeLocalProxyLinkCFOptimizeProtocols(layer string) []string {
	return []string{"websocket"}
}

func isProbeLocalProxyLinkCFEntry(item probeLinkChainServerItem) bool {
	for _, value := range []string{
		item.ClientEntryType,
		item.ClientEntryID,
		item.ChainID,
		item.Name,
	} {
		clean := strings.ToLower(strings.TrimSpace(value))
		if clean == "cf" || strings.HasSuffix(clean, "_cf") {
			return true
		}
	}
	return false
}

func snapshotProbeLocalProxyLinkCFOptimizeStatus(chainID string) *probeLocalProxyLinkCFOptimizeStatus {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return nil
	}
	probeLocalProxyLinkCFOptimizeState.mu.Lock()
	status, ok := probeLocalProxyLinkCFOptimizeState.items[cleanID]
	probeLocalProxyLinkCFOptimizeState.mu.Unlock()
	if !ok {
		return nil
	}
	status.TopResults = append([]probeLocalProxyLinkCFOptimizeResult{}, status.TopResults...)
	status.Results = append([]probeLocalProxyLinkCFOptimizeResult{}, status.Results...)
	return &status
}

func appendProbeLocalProxyLinkCFOptimizeResult(chainID string, result probeLocalProxyLinkCFOptimizeResult) {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	probeLocalProxyLinkCFOptimizeState.mu.Lock()
	status := probeLocalProxyLinkCFOptimizeState.items[cleanID]
	status.Results = append(status.Results, result)
	status.TestedCount++
	if !result.OK {
		status.FailedCount++
	}
	status.TopResults = buildProbeLocalProxyLinkCFOptimizeTopResults(status.Results)
	if len(status.TopResults) > 0 {
		status.BestIP = status.TopResults[0].IP
		status.BestProtocol = status.TopResults[0].Protocol
		status.BestLatencyMS = status.TopResults[0].LatencyMS
	}
	status.UpdatedAt = now
	probeLocalProxyLinkCFOptimizeState.items[cleanID] = status
	probeLocalProxyLinkCFOptimizeState.mu.Unlock()
}

func finishProbeLocalProxyLinkCFOptimizeStatus(chainID string, statusText string, errText string) {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	probeLocalProxyLinkCFOptimizeState.mu.Lock()
	status := probeLocalProxyLinkCFOptimizeState.items[cleanID]
	status.Status = strings.TrimSpace(statusText)
	status.Running = false
	status.Error = strings.TrimSpace(errText)
	status.FinishedAt = now
	status.UpdatedAt = now
	probeLocalProxyLinkCFOptimizeState.items[cleanID] = status
	probeLocalProxyLinkCFOptimizeState.mu.Unlock()
}

func runProbeLocalProxyLinkCFIPOptimizeTask(chainID string, endpoint probeLocalTUNChainEndpoint, candidateIPs []net.IP) {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return
	}
	ipTexts := make([]string, 0, len(candidateIPs))
	seen := make(map[string]struct{}, len(candidateIPs))
	for _, ip := range candidateIPs {
		if ip == nil {
			continue
		}
		candidate := strings.TrimSpace(ip.String())
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		ipTexts = append(ipTexts, candidate)
		if len(ipTexts) >= probeLocalProxyLinkCFOptimizeMaxIPs {
			break
		}
	}
	if len(ipTexts) == 0 {
		finishProbeLocalProxyLinkCFOptimizeStatus(cleanID, "failed", "cf entry host resolved no ip")
		return
	}
	probeLocalProxyLinkCFOptimizeState.mu.Lock()
	status := probeLocalProxyLinkCFOptimizeState.items[cleanID]
	status.CandidateCount = len(ipTexts)
	status.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	probeLocalProxyLinkCFOptimizeState.items[cleanID] = status
	probeLocalProxyLinkCFOptimizeState.mu.Unlock()

	type probeJob struct {
		ip       string
		protocol string
	}
	protocols := probeLocalProxyLinkCFOptimizeProtocols(endpoint.LinkLayer)
	if len(protocols) == 0 {
		finishProbeLocalProxyLinkCFOptimizeStatus(cleanID, "failed", "cf optimize relay protocols are unavailable")
		return
	}
	log.Printf("probe local cf optimize start: chain=%s entry=%s:%d link_layer=%s candidate_count=%d candidate_preview=%s protocols=%s", cleanID, endpoint.EntryHost, endpoint.EntryPort, normalizeProbeChainLinkLayer(endpoint.LinkLayer), len(ipTexts), probeLocalPreviewTexts(ipTexts, 10), strings.Join(protocols, ","))
	probeLocalProxyLinkCFOptimizeState.mu.Lock()
	status = probeLocalProxyLinkCFOptimizeState.items[cleanID]
	status.PlannedCount = len(ipTexts) * len(protocols)
	status.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	probeLocalProxyLinkCFOptimizeState.items[cleanID] = status
	probeLocalProxyLinkCFOptimizeState.mu.Unlock()

	jobs := make([]probeJob, 0, len(ipTexts)*len(protocols))
	for _, ip := range ipTexts {
		for _, protocol := range protocols {
			jobs = append(jobs, probeJob{ip: ip, protocol: protocol})
		}
	}
	sem := make(chan struct{}, probeLocalProxyLinkCFOptimizeParallel)
	var wg sync.WaitGroup
	for _, job := range jobs {
		job := job
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			latency, err := probeLocalProxyLinkCFIPProbe(endpoint, job.ip, job.protocol)
			result := probeLocalProxyLinkCFOptimizeResult{
				IP:        job.ip,
				Protocol:  job.protocol,
				LatencyMS: probeDurationMilliseconds(latency),
				TestedAt:  time.Now().UTC().Format(time.RFC3339),
			}
			if err != nil {
				result.Error = strings.TrimSpace(err.Error())
			} else {
				result.OK = true
			}
			appendProbeLocalProxyLinkCFOptimizeResult(cleanID, result)
		}()
	}
	wg.Wait()

	statusSnapshot := snapshotProbeLocalProxyLinkCFOptimizeStatus(cleanID)
	if statusSnapshot == nil || statusSnapshot.BestIP == "" {
		log.Printf("probe local cf optimize failed: chain=%s entry=%s:%d tested=%d failed=%d planned=%d err=all cf candidate ips are unreachable", cleanID, endpoint.EntryHost, endpoint.EntryPort, func() int {
			if statusSnapshot == nil {
				return 0
			}
			return statusSnapshot.TestedCount
		}(), func() int {
			if statusSnapshot == nil {
				return 0
			}
			return statusSnapshot.FailedCount
		}(), func() int {
			if statusSnapshot == nil {
				return 0
			}
			return statusSnapshot.PlannedCount
		}())
		finishProbeLocalProxyLinkCFOptimizeStatus(cleanID, "failed", "all cf candidate ips are unreachable")
		return
	}
	topPreview := make([]string, 0, len(statusSnapshot.TopResults))
	for _, result := range statusSnapshot.TopResults {
		topPreview = append(topPreview, fmt.Sprintf("%s/%s/%dms", result.IP, result.Protocol, result.LatencyMS))
	}
	log.Printf("probe local cf optimize completed: chain=%s entry=%s:%d best_ip=%s best_protocol=%s best_latency_ms=%d tested=%d failed=%d planned=%d top_results=%s", cleanID, endpoint.EntryHost, endpoint.EntryPort, statusSnapshot.BestIP, statusSnapshot.BestProtocol, statusSnapshot.BestLatencyMS, statusSnapshot.TestedCount, statusSnapshot.FailedCount, statusSnapshot.PlannedCount, strings.Join(topPreview, ","))
	finishProbeLocalProxyLinkCFOptimizeStatus(cleanID, "optimized", "")
}

func probeLocalProxyLinkStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"items":      buildProbeLocalProxyLinkStatusItems(),
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func probeLocalProxyLinkLatencyHandler(w http.ResponseWriter, r *http.Request) {
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
	var req probeLocalProxyLinkProbeRequest
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	chainID := strings.TrimSpace(req.ChainID)
	if chainID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chain_id is required"})
		return
	}
	items := currentProbeLocalProxyViewChains()
	item, ok := findProbeLocalProxyLinkItemByID(chainID, items)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "chain not found"})
		return
	}
	endpoint, err := resolveProbeLocalProxyLinkEndpoint(item)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         false,
			"chain_id":   chainID,
			"status":     "unconfigured",
			"error":      strings.TrimSpace(err.Error()),
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		})
		return
	}
	type latencyProbeResult struct {
		probeLocalProxyLinkReachabilityResult
	}
	protocols := probeLocalProxyLinkReachabilityProtocolsForEndpoint(item, endpoint)
	resultsCh := make(chan latencyProbeResult, len(protocols))
	for _, protocol := range protocols {
		protocol := protocol
		go func() {
			latency, err := probeLocalProxyLinkProtocolProbe(endpoint, protocol)
			latencyMS := probeDurationMilliseconds(latency)
			result := latencyProbeResult{
				probeLocalProxyLinkReachabilityResult: probeLocalProxyLinkReachabilityResult{
					Protocol:  protocol,
					OK:        err == nil,
					LatencyMS: latencyMS,
				},
			}
			if err != nil {
				result.Error = strings.TrimSpace(err.Error())
			}
			resultsCh <- result
		}()
	}

	results := make([]probeLocalProxyLinkReachabilityResult, 0, len(protocols))
	reachableCount := 0
	bestProtocol := ""
	bestLatencyMS := int64(0)
	endpointKey := probeChainRelayProtocolEndpointKey(endpoint.EntryHost, endpoint.EntryPort)
	protocolOrder := map[string]int{"websocket-h3": 0, "websocket": 1}
	for range protocols {
		result := (<-resultsCh).probeLocalProxyLinkReachabilityResult
		results = append(results, result)
		probeResult := probeChainRelayProtocolDialResult{
			Protocol: normalizeProbeChainLinkLayer(result.Protocol),
			Latency:  time.Duration(result.LatencyMS) * time.Millisecond,
		}
		if result.OK {
			recordProbeChainRelayProtocolSuccess(endpointKey, probeResult, "latency_test")
		} else {
			probeErr := errors.New(firstNonEmpty(strings.TrimSpace(result.Error), "latency test failed"))
			probeResult.Err = probeErr
			recordProbeChainRelayProtocolFailure(endpointKey, probeResult, probeErr)
		}
		if !result.OK {
			continue
		}
		reachableCount++
		currentProtocol := normalizeProbeChainLinkLayer(result.Protocol)
		if bestProtocol == "" ||
			bestLatencyMS <= 0 ||
			(result.LatencyMS > 0 && result.LatencyMS < bestLatencyMS) ||
			(result.LatencyMS == bestLatencyMS && protocolOrder[currentProtocol] < protocolOrder[bestProtocol]) {
			bestProtocol = currentProtocol
			bestLatencyMS = result.LatencyMS
		}
	}
	sort.Slice(results, func(i, j int) bool {
		left := protocolOrder[normalizeProbeChainLinkLayer(results[i].Protocol)]
		right := protocolOrder[normalizeProbeChainLinkLayer(results[j].Protocol)]
		return left < right
	})
	storeProbeLocalProxyLinkReachabilityStatus(chainID, endpoint, bestProtocol, reachableCount, results)

	if bestProtocol == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":              false,
			"chain_id":        chainID,
			"chain_name":      strings.TrimSpace(item.Name),
			"status":          "unreachable",
			"reachable_count": reachableCount,
			"tested_count":    len(results),
			"entry_host":      endpoint.EntryHost,
			"entry_port":      endpoint.EntryPort,
			"link_layer":      endpoint.LinkLayer,
			"error":           "all relay protocols are unreachable",
			"results":         results,
			"reachability":    snapshotProbeLocalProxyLinkReachabilityStatus(chainID),
			"protocol_state":  snapshotProbeLocalTUNChainRelayProtocolState(endpoint.EntryHost, endpoint.EntryPort),
			"updated_at":      time.Now().UTC().Format(time.RFC3339),
		})
		return
	}
	recordProbeChainRelayProtocolSelected(endpointKey, bestProtocol, "latency_test")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"chain_id":        chainID,
		"chain_name":      strings.TrimSpace(item.Name),
		"status":          "reachable",
		"latency_ms":      bestLatencyMS,
		"reachable_count": reachableCount,
		"tested_count":    len(results),
		"best_protocol":   bestProtocol,
		"entry_host":      endpoint.EntryHost,
		"entry_port":      endpoint.EntryPort,
		"link_layer":      endpoint.LinkLayer,
		"results":         results,
		"reachability":    snapshotProbeLocalProxyLinkReachabilityStatus(chainID),
		"protocol_state":  snapshotProbeLocalTUNChainRelayProtocolState(endpoint.EntryHost, endpoint.EntryPort),
		"updated_at":      time.Now().UTC().Format(time.RFC3339),
	})
}

func probeLocalProxyLinkSpeedHandler(w http.ResponseWriter, r *http.Request) {
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
	var req probeLocalProxyLinkProbeRequest
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	chainID := strings.TrimSpace(req.ChainID)
	if chainID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chain_id is required"})
		return
	}
	protocol := ""
	if strings.TrimSpace(req.Protocol) != "" {
		protocol = normalizeProbeChainLinkLayer(req.Protocol)
	}
	if protocol != "" && !isProbeChainRelaySupportedProtocol(protocol) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "protocol must be websocket-h3 or websocket"})
		return
	}
	items := currentProbeLocalProxyViewChains()
	item, ok := findProbeLocalProxyLinkItemByID(chainID, items)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "chain not found"})
		return
	}
	endpoint, err := resolveProbeLocalProxyLinkEndpoint(item)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         false,
			"chain_id":   chainID,
			"status":     "unconfigured",
			"error":      strings.TrimSpace(err.Error()),
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		})
		return
	}
	if isProbeLocalProxyLinkCFEntry(item) {
		if protocol != "" && protocol != "websocket" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cf entry only supports websocket speed test"})
			return
		}
		protocol = "websocket"
	}
	results := probeLocalProxyLinkSpeedProbe(endpoint, protocol)
	recordProbeLocalProxyLinkSpeedStatus(strings.TrimSpace(item.ChainID), results)
	snapshot := snapshotProbeLocalTUNChainRelayProtocolState(endpoint.EntryHost, endpoint.EntryPort)
	rateBPS := int64(0)
	status := "unreachable"
	okResult := false
	for _, result := range results {
		if result.OK {
			okResult = true
			if result.RateBPS > rateBPS {
				rateBPS = result.RateBPS
			}
		}
	}
	if okResult {
		status = "tested"
		if rateBPS <= 0 {
			rateBPS = observedProbeLocalProxyLinkRateBPS(snapshot)
		}
	} else if len(results) == 0 {
		status = "no_result"
	}
	remoteSpeedDebug := probeLocalProxyLinkRemoteSpeedDebugFetch(item, endpoint, protocol)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 okResult,
		"chain_id":           chainID,
		"chain_name":         strings.TrimSpace(item.Name),
		"status":             status,
		"protocol":           protocol,
		"rate_bps":           rateBPS,
		"source":             "active_speed_test",
		"results":            results,
		"protocol_state":     snapshot,
		"remote_speed_debug": remoteSpeedDebug,
		"updated_at":         time.Now().UTC().Format(time.RFC3339),
	})
}

func probeLocalProxyLinkCFIPOptimizeHandler(w http.ResponseWriter, r *http.Request) {
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
	var req probeLocalProxyLinkProbeRequest
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	chainID := strings.TrimSpace(req.ChainID)
	if chainID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chain_id is required"})
		return
	}
	items := currentProbeLocalProxyViewChains()
	item, ok := findProbeLocalProxyLinkItemByID(chainID, items)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "chain not found"})
		return
	}
	canonicalChainID := strings.TrimSpace(item.ChainID)
	if canonicalChainID == "" {
		canonicalChainID = chainID
	}
	if !isProbeLocalProxyLinkCFEntry(item) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "selected chain is not a cf entry"})
		return
	}
	endpoint, err := resolveProbeLocalProxyLinkEndpoint(item)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         false,
			"chain_id":   canonicalChainID,
			"status":     "unconfigured",
			"error":      strings.TrimSpace(err.Error()),
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		})
		return
	}
	if net.ParseIP(strings.TrimSpace(strings.Trim(endpoint.EntryHost, "[]"))) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cf entry host must be a domain name"})
		return
	}

	probeLocalProxyLinkCFOptimizeState.mu.Lock()
	existing := probeLocalProxyLinkCFOptimizeState.items[canonicalChainID]
	if existing.Running {
		status := existing
		status.TopResults = append([]probeLocalProxyLinkCFOptimizeResult{}, existing.TopResults...)
		status.Results = append([]probeLocalProxyLinkCFOptimizeResult{}, existing.Results...)
		probeLocalProxyLinkCFOptimizeState.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accepted": false, "running": true, "status": status})
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	status := probeLocalProxyLinkCFOptimizeStatus{
		ChainID:   canonicalChainID,
		EntryHost: endpoint.EntryHost,
		EntryPort: endpoint.EntryPort,
		Status:    "resolving",
		Running:   true,
		StartedAt: now,
		UpdatedAt: now,
	}
	probeLocalProxyLinkCFOptimizeState.items[canonicalChainID] = status
	probeLocalProxyLinkCFOptimizeState.mu.Unlock()

	probeLocalStartCFIPOptimizeTask(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		log.Printf("probe local cf optimize lookup start: chain=%s entry_host=%s", canonicalChainID, endpoint.EntryHost)
		candidateIPs, lookupErr := probeLocalProxyLinkCFIPLookup(ctx, endpoint.EntryHost)
		cancel()
		if lookupErr != nil {
			log.Printf("probe local cf optimize lookup failed: chain=%s entry_host=%s err=%v", canonicalChainID, endpoint.EntryHost, lookupErr)
			finishProbeLocalProxyLinkCFOptimizeStatus(canonicalChainID, "failed", lookupErr.Error())
			return
		}
		log.Printf("probe local cf optimize lookup resolved: chain=%s entry_host=%s candidate_count=%d", canonicalChainID, endpoint.EntryHost, len(candidateIPs))
		probeLocalProxyLinkCFOptimizeState.mu.Lock()
		status := probeLocalProxyLinkCFOptimizeState.items[canonicalChainID]
		status.Status = "testing"
		status.CandidateCount = len(candidateIPs)
		status.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		probeLocalProxyLinkCFOptimizeState.items[canonicalChainID] = status
		probeLocalProxyLinkCFOptimizeState.mu.Unlock()
		runProbeLocalProxyLinkCFIPOptimizeTask(canonicalChainID, endpoint, candidateIPs)
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"accepted": true,
		"running":  true,
		"chain_id": canonicalChainID,
		"status":   snapshotProbeLocalProxyLinkCFOptimizeStatus(canonicalChainID),
	})
}

func probeLocalProxyChainsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	items := currentProbeLocalProxyViewChains()
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
	groups := currentProbeLocalProxyViewGroups()
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
	resetProbeLocalDNSRuntimeCachesForProxyGroupRefresh()
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
		if snapshot, ok := currentProbeLocalProxyViewGroupRuntimeSnapshot(group); ok {
			if groupRuntimeStatus := strings.TrimSpace(snapshot.GroupRuntimeStatus); groupRuntimeStatus != "" {
				payload["group_runtime_status"] = groupRuntimeStatus
			}
			if keepalive := strings.TrimSpace(snapshot.SelectedChainKeepalive); keepalive != "" {
				payload["selected_chain_keepalive"] = keepalive
			}
			latencyStatus := firstNonEmpty(strings.TrimSpace(snapshot.SelectedChainLatencyStatus), "unreachable")
			if snapshot.SelectedChainLatencyMS != nil {
				payload["selected_chain_latency_ms"] = *snapshot.SelectedChainLatencyMS
			}
			payload["selected_chain_latency_status"] = latencyStatus
			payload["selected_chain_latency_updated_at"] = strings.TrimSpace(snapshot.SelectedChainLatencyUpdatedAt)
			payload["selected_chain_latency_error"] = strings.TrimSpace(snapshot.SelectedChainLatencyError)
			if strings.TrimSpace(snapshot.ProtocolState.Endpoint) != "" {
				payload["protocol_state"] = snapshot.ProtocolState
			}
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
	state := currentProbeLocalProxyViewState()
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
	resetProbeLocalDNSRuntimeCachesForProxyGroupRefresh()
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

func probeLocalSystemUpgradeCheckHandler(w http.ResponseWriter, r *http.Request) {
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

func prepareProbeLocalProcessRestart() {
	logProbeInfof("probe local restart preparing: closing listeners")
	stopProbeLocalProxyMonitor()
	_ = stopProbeLocalTUNDataPlane()
	stopProbeHTTPSService("process restart")
	stoppedChains := stopAllProbeChainRuntimes("process restart")
	if stoppedChains > 0 {
		logProbeInfof("probe local restart stopped chain runtimes: count=%d", stoppedChains)
	}
	stopProbeLocalConsoleServer("process restart")
	time.Sleep(300 * time.Millisecond)
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

func probeLocalProxyGroupsRestoreHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	backupUpdatedAt, err := restoreProbeLocalProxyGroupFromController(r.Context())
	if err != nil {
		_ = setProbeLocalBackupRestoreStatus("failed", strings.TrimSpace(err.Error()), "")
		writeProbeLocalError(w, err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_ = setProbeLocalBackupRestoreStatus("ok", "", now)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restored_at": now, "backup_updated_at": backupUpdatedAt})
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
	resetProbeLocalProxyMonitorForTest()
	clearProbeLocalTUNInstallObservation()
	resetProbeLocalTUNGroupRuntimeRegistryForTest()
	resetProbeLocalProxyViewGroupRuntimeSnapshots()
	probeLocalProxyStatusRefreshState.mu.Lock()
	probeLocalProxyStatusRefreshState.running = false
	probeLocalProxyStatusRefreshState.lastStartedAt = ""
	probeLocalProxyStatusRefreshState.lastFinishedAt = ""
	probeLocalProxyStatusRefreshState.lastError = ""
	probeLocalProxyStatusRefreshState.mu.Unlock()
	probeLocalProxyLinkCFOptimizeState.mu.Lock()
	probeLocalProxyLinkCFOptimizeState.items = make(map[string]probeLocalProxyLinkCFOptimizeStatus)
	probeLocalProxyLinkCFOptimizeState.mu.Unlock()
	probeLocalProxyLinkReachabilityState.mu.Lock()
	probeLocalProxyLinkReachabilityState.items = make(map[string]probeLocalProxyLinkReachabilityStatus)
	probeLocalProxyLinkReachabilityState.mu.Unlock()
	probeLocalControl = newProbeLocalControlManager()
}

func resetProbeLocalProxyHooksForTest() {
	probeLocalApplyProxyTakeover = applyProbeLocalProxyTakeover
	probeLocalRestoreProxyDirect = restoreProbeLocalProxyDirect
	probeLocalLookupIPv4ForBypass = lookupProbeLocalIPv4ForBypass
	probeLocalResolveGroupRuntimeLatency = resolveProbeLocalTUNGroupRuntimeKeepaliveAndLatency
	probeLocalProxyLinkHandshakeProbe = runProbeLocalProxyLinkHandshakeProbe
	probeLocalProxyLinkProtocolProbe = runProbeLocalProxyLinkProtocolProbe
	probeLocalProxyLinkSpeedProbe = runProbeLocalProxyLinkSpeedProbe
	probeLocalProxyRelaySpeedDebugFetch = probeChainRelayFetchSpeedDebugDefault
	probeLocalProxyLinkRemoteSpeedDebugFetch = runProbeLocalProxyLinkRemoteSpeedDebugFetch
	probeLocalProxyLinkOpenRelayConn = openProbeChainRelayNetConnWithLayerConn
	probeLocalFetchCloudflareIPv4CIDRs = defaultProbeLocalFetchCloudflareIPv4CIDRs
	probeLocalProxyLinkCFIPLookup = defaultProbeLocalProxyLinkCFIPLookup
	probeLocalProxyLinkCFIPProbe = runProbeLocalProxyLinkCFIPProbe
	probeLocalStartCFIPOptimizeTask = func(fn func()) { go fn() }
	probeLocalRefreshProxyChainCache = refreshProbeProxyChainCacheFromController
	probeLocalTUNOpenChainRelayNetConn = openProbeLocalTUNChainRelayNetConn
	probeLocalTUNOpenChainRelayDataStreamNetConn = openProbeLocalTUNChainRelayDataStreamNetConn
}

func resetProbeLocalTUNHooksForTest() {
	probeLocalInstallTUNDriver = installProbeLocalTUNDriver
	probeLocalCheckTUNReadyAfterInstall = probeLocalNoopPostInstallTUNReadyCheck
	probeLocalResetTUNDetectInstalledHook()
	probeLocalApplyTUNPrimaryDNS = applyProbeLocalTUNPrimaryDNS
	probeLocalRestoreTUNPrimaryDNS = restoreProbeLocalTUNPrimaryDNS
	probeLocalUninstallTUNDriver = uninstallProbeLocalTUNDriver
	resetProbeLocalTUNDataPlaneHooksForTest()
}

func resetProbeLocalUpgradeHooksForTest() {
	probeLocalRunUpgrade = runProbeUpgrade
	probeLocalFetchRelease = fetchProbeRelease
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
