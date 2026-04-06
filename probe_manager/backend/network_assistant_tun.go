package backend

import (
	"bytes"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	tunEmbeddedLibraryPath  = "embedded://wintun/amd64/wintun.dll"
	tunTempRelativePath     = "temp/Lib/wintun/amd64/wintun.dll"
	tunAdapterName          = "Maple"
	tunAdapterDescription   = "Maple Virtual Network Adapter"
	tunAdapterTunnelType    = "Maple"
	tunAdapterRequestedGUID = "{6BA2B7A3-1C2D-4E63-9E3C-6F7A8B9C0D21}"
	tunStatusUnsupported    = "仅支持 Windows amd64"
	tunStatusNotInstalled   = "未安装"
	tunStatusInstalled      = "已准备(临时)"
	tunStatusDetected       = "已安装(检测到网卡)"
	tunStatusEnabled        = "已启用"
)

//go:embed lib/wintun/amd64/wintun.dll
var embeddedWintunAMD64 []byte

type windowsNetAdapter struct {
	Name                 string `json:"Name"`
	InterfaceDescription string `json:"InterfaceDescription"`
}

func (a *App) InstallNetworkAssistantTUN() (NetworkAssistantStatus, error) {
	if a.networkAssistant == nil {
		return NetworkAssistantStatus{}, errors.New("network assistant service is not initialized")
	}
	if err := a.networkAssistant.InstallTUN(); err != nil {
		return a.networkAssistant.Status(), err
	}
	return a.networkAssistant.Status(), nil
}

func (a *App) EnableNetworkAssistantTUN() (NetworkAssistantStatus, error) {
	if a.networkAssistant == nil {
		return NetworkAssistantStatus{}, errors.New("network assistant service is not initialized")
	}
	if err := a.networkAssistant.EnableTUN(); err != nil {
		return a.networkAssistant.Status(), err
	}
	return a.networkAssistant.Status(), nil
}

func (s *networkAssistantService) syncTUNInstallState() {
	// Throttle: only re-detect TUN state at most once every 5 seconds to avoid
	// spawning a PowerShell process on every GetNetworkAssistantStatus() call.
	now := time.Now()
	throttleWaitStartedAt := time.Now()
	s.logf("sync tun install state throttle lock wait begin")
	s.mu.Lock()
	s.logf("sync tun install state throttle lock acquired: elapsed=%s", time.Since(throttleWaitStartedAt))
	if now.Sub(s.tunLastSyncAt) < 5*time.Second {
		s.mu.Unlock()
		return
	}
	s.tunLastSyncAt = now
	s.mu.Unlock()

	installedByLibrary := false
	installedByAdapter := false
	path := ""
	if validateEmbeddedWintunDLL() == nil {
		if p, err := resolveTUNLibraryTempPath(); err == nil {
			path = p
			if info, statErr := os.Stat(p); statErr == nil && !info.IsDir() && info.Size() > 0 {
				installedByLibrary = true
			}
		}
	}
	if isTUNSupported() {
		if exists, err := detectConfiguredTUNAdapter(); err == nil {
			installedByAdapter = exists
		}
	}
	installed := installedByLibrary || installedByAdapter

	stateCommitWaitStartedAt := time.Now()
	s.logf("sync tun install state commit lock wait begin")
	s.mu.Lock()
	s.logf("sync tun install state commit lock acquired: elapsed=%s", time.Since(stateCommitWaitStartedAt))
	defer s.mu.Unlock()

	s.tunSupported = isTUNSupported()
	if !s.tunSupported {
		s.tunInstalled = false
		s.tunEnabled = false
		s.tunLibraryPath = ""
		s.tunStatus = tunStatusUnsupported
		if s.mode == networkModeTUN {
			s.mode = networkModeDirect
			s.tunnelStatusMessage = "直连模式"
			s.systemProxyMessage = "已恢复"
		}
		return
	}

	s.tunLibraryPath = path
	s.tunInstalled = installed
	if s.mode == networkModeTUN && installed {
		s.tunEnabled = true
		s.tunStatus = tunStatusEnabled
		return
	}
	s.tunEnabled = false
	if installedByAdapter {
		s.tunStatus = tunStatusDetected
		return
	}
	s.tunStatus = tunStatusAfterDisable(true, installed)
}

func tunStatusAfterDisable(supported bool, installed bool) string {
	if !supported {
		return tunStatusUnsupported
	}
	if installed {
		return tunStatusInstalled
	}
	return tunStatusNotInstalled
}

func isTUNSupported() bool {
	return runtime.GOOS == "windows" && runtime.GOARCH == "amd64"
}

func validateEmbeddedWintunDLL() error {
	if len(embeddedWintunAMD64) == 0 {
		return errors.New("embedded wintun.dll is empty")
	}
	if len(embeddedWintunAMD64) < 2 || !bytes.Equal(embeddedWintunAMD64[:2], []byte{'M', 'Z'}) {
		return errors.New("embedded wintun.dll has invalid pe header")
	}
	return nil
}

func listWindowsNetAdapters() ([]windowsNetAdapter, error) {
	if runtime.GOOS != "windows" {
		return []windowsNetAdapter{}, nil
	}

	items, err := windowsListAdaptersIPv4()
	if err != nil {
		return nil, err
	}
	adapters := make([]windowsNetAdapter, 0, len(items))
	for _, item := range items {
		adapters = append(adapters, windowsNetAdapter{
			Name:                 strings.TrimSpace(item.Name),
			InterfaceDescription: strings.TrimSpace(item.InterfaceDescription),
		})
	}
	return adapters, nil
}

func detectConfiguredTUNAdapter() (bool, error) {
	if runtime.GOOS != "windows" {
		return false, nil
	}

	adapters, err := listWindowsNetAdapters()
	if err != nil {
		return false, err
	}

	for _, adapter := range adapters {
		name := strings.TrimSpace(adapter.Name)
		desc := strings.TrimSpace(adapter.InterfaceDescription)
		if strings.EqualFold(name, tunAdapterName) || strings.HasPrefix(strings.ToLower(name), strings.ToLower(tunAdapterName)+" ") {
			return true, nil
		}
		if strings.EqualFold(desc, tunAdapterDescription) {
			return true, nil
		}
	}
	return false, nil
}

func resolveTUNLibraryTempPath() (string, error) {
	candidates := make([]string, 0, 2)
	if exePath, err := os.Executable(); err == nil && exePath != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(exePath), filepath.FromSlash(tunTempRelativePath)))
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		candidates = append(candidates, filepath.Join(wd, filepath.FromSlash(tunTempRelativePath)))
	}
	for _, candidate := range candidates {
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		return abs, nil
	}
	return "", errors.New("failed to resolve runtime temp directory")
}

func (s *networkAssistantService) InstallTUN() error {
	if !isTUNSupported() {
		err := errors.New(tunStatusUnsupported)
		s.setLastError(err)
		s.syncTUNInstallState()
		return err
	}

	if exists, err := detectConfiguredTUNAdapter(); err == nil && exists {
		path := ""
		if p, pathErr := resolveTUNLibraryTempPath(); pathErr == nil {
			path = p
		}

		s.mu.Lock()
		s.lastError = ""
		s.tunSupported = true
		s.tunInstalled = true
		s.tunLibraryPath = path
		if s.mode == networkModeTUN {
			s.tunEnabled = true
			s.tunStatus = tunStatusEnabled
		} else {
			s.tunEnabled = false
			s.tunStatus = tunStatusDetected
		}
		s.mu.Unlock()

		s.logf("tun adapter already exists, skip install: name=%s description=%s", tunAdapterName, tunAdapterDescription)
		return nil
	}

	if err := validateEmbeddedWintunDLL(); err != nil {
		s.setLastError(err)
		return err
	}
	path, err := resolveTUNLibraryTempPath()
	if err != nil {
		s.setLastError(err)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.setLastError(err)
		return err
	}
	if err := os.WriteFile(path, embeddedWintunAMD64, 0o644); err != nil {
		s.setLastError(err)
		return err
	}
	adapterHandle, err := createConfiguredTUNAdapter(path, tunAdapterName, tunAdapterTunnelType, tunAdapterRequestedGUID)
	if err != nil {
		s.setLastError(err)
		return err
	}

	s.mu.Lock()
	s.lastError = ""
	s.tunSupported = true
	s.tunInstalled = true
	s.tunLibraryPath = path
	if adapterHandle != 0 {
		s.tunAdapterHandle = adapterHandle
	}
	if s.mode == networkModeTUN {
		s.tunEnabled = true
		s.tunStatus = tunStatusEnabled
	} else {
		s.tunEnabled = false
		s.tunStatus = tunStatusDetected
	}
	s.mu.Unlock()

	s.logf("tun library prepared from embed: source=%s target=%s size=%d", tunEmbeddedLibraryPath, path, len(embeddedWintunAMD64))
	s.logf("tun adapter installed: name=%s description=%s", tunAdapterName, tunAdapterDescription)
	return nil
}

func (s *networkAssistantService) EnableTUN() error {
	s.logf("enable tun begin")
	if !isTUNSupported() {
		err := errors.New(tunStatusUnsupported)
		s.logf("enable tun failed: unsupported")
		s.setLastError(err)
		s.syncTUNInstallState()
		return err
	}
	if !isWindowsAdmin() {
		s.logf("enable tun stage: ensure-admin begin")
		s.logf("tun enable requires admin privileges, requesting elevation")
		if err := relaunchAsAdmin(); err != nil {
			if errors.Is(err, ErrRelaunchAsAdmin) {
				// UAC 提权已触发，新进程将以管理员身份启动，当前进程应尽快退出
				go func() {
					time.Sleep(300 * time.Millisecond)
					os.Exit(0)
				}()
				return ErrRelaunchAsAdmin
			}
			s.logf("enable tun failed at ensure-admin: %v", err)
			s.setLastError(err)
			return err
		}
	}
	
	s.logf("enable tun stage: install begin")
	if err := s.InstallTUN(); err != nil {
		s.logf("enable tun failed at install: %v", err)
		return err
	}
	
	s.logf("enable tun stage: load-rule-routing begin")
	routing, err := loadOrCreateTunnelRuleRouting()
	if err != nil {
		s.logf("enable tun failed at load-rule-routing: %v", err)
		s.setLastError(err)
		return err
	}

	s.mu.RLock()
	effectiveBase := strings.TrimSpace(s.controllerBaseURL)
	effectiveToken := strings.TrimSpace(s.sessionToken)
	s.mu.RUnlock()
	if effectiveBase != "" && effectiveToken != "" {
		s.logf("enable tun stage: refresh-available-nodes begin: base=%s has_token=%t", effectiveBase, effectiveToken != "")
		if err := s.refreshAvailableNodes(); err != nil {
			s.logf("enable tun failed at refresh-available-nodes: %v", err)
			s.setLastError(err)
			return err
		}
	}
	
	s.logf("enable tun stage: stop-mux begin")
	if err := s.stopTunnelMuxClients(); err != nil {
		s.logf("enable tun failed at stop-mux: %v", err)
		s.setLastError(err)
		return err
	}
	
	s.logf("enable tun stage: apply-direct-proxy begin")
	if err := applyDirectSystemProxy(); err != nil {
		s.logf("enable tun failed at apply-direct-proxy: %v", err)
		s.setLastError(err)
		return err
	}
	
	s.logf("enable tun stage: start-dataplane begin")
	if err := s.startLocalTUNDataPlane(); err != nil {
		s.logf("enable tun failed at start-dataplane: %v", err)
		s.setLastError(err)
		return err
	}
	
	s.logf("enable tun stage: apply-routing begin: base=%s", effectiveBase)
	if err := s.applyTUNSystemRouting(effectiveBase); err != nil {
		s.logf("enable tun failed at apply-routing: %v", err)
		return s.fallbackToDirectModeOnTUNRoutingFailure("enable tun: apply direct routes failed", err)
	}

	s.logf("enable tun stage: ensure-internal-dns begin")
	if err := s.ensureInternalDNSServerHealthy(); err != nil {
		s.logf("enable tun failed at ensure-internal-dns: %v", err)
		s.setLastError(err)
		return err
	}

	s.logf("enable tun stage: commit-runtime-state begin")
	s.mu.Lock()
	s.mode = networkModeTUN
	s.tunSupported = true
	s.tunInstalled = true
	s.tunEnabled = true
	s.tunEverEnabled = true
	s.tunManualClosed = false
	s.tunStatus = tunStatusEnabled
	s.tunnelStatusMessage = "TUN 模式已启用（按规则分流）"
	s.systemProxyMessage = "TUN 模式（系统代理已清除，SOCKS/HTTP 代理已停用）"
	s.tunnelOpenFailures = 0
	s.lastError = ""
	s.ruleRouting = routing
	s.ruleDNSCache = make(map[string]dnsCacheEntry)
	libraryPath := s.tunLibraryPath
	pref := tunPreferenceState{EverEnabled: s.tunEverEnabled, ManualClosed: s.tunManualClosed}
	s.mu.Unlock()
	if err := saveTUNPreferenceState(pref); err != nil {
		s.logf("failed to persist tun preference state: %v", err)
	}

	s.logf("enable tun success: library=%s", libraryPath)
	s.logf("switched mode to tun, library=%s", libraryPath)
	s.triggerMuxAutoMaintainNow()
	return nil
}

func (s *networkAssistantService) fallbackToDirectModeOnTUNRoutingFailure(context string, cause error) error {
	s.logf("%s, tun enable failed and fallback to direct is starting: cause=%v", context, cause)

	errStopTUN := s.stopLocalTUNDataPlane()
	errTunRouting := s.clearTUNSystemRouting()
	errStopMux := s.stopTunnelMuxClients()
	errDirectProxy := applyDirectSystemProxy()
	cleanupErr := errors.Join(errStopTUN, errTunRouting, errStopMux, errDirectProxy)
	if cleanupErr != nil {
		s.logf("tun fallback direct cleanup summary: stop_tun=%v, clear_tun_routing=%v, stop_mux=%v, apply_direct_proxy=%v", errStopTUN, errTunRouting, errStopMux, errDirectProxy)
	}

	pref := tunPreferenceState{}
	s.mu.Lock()
	pref = tunPreferenceState{EverEnabled: s.tunEverEnabled, ManualClosed: s.tunManualClosed}
	s.mode = networkModeDirect
	s.tunnelStatusMessage = "直连模式"
	s.systemProxyMessage = "已清除系统代理并恢复系统 DNS（直连）"
	s.tunnelOpenFailures = 0
	s.tunEnabled = false
	s.tunStatus = tunStatusAfterDisable(s.tunSupported, s.tunInstalled)
	s.mu.Unlock()
	s.logf("tun fallback finished: runtime mode switched to direct")

	if err := saveTUNPreferenceState(pref); err != nil {
		s.logf("failed to persist tun preference state during fallback: %v", err)
		cleanupErr = errors.Join(cleanupErr, err)
	}

	finalErr := cause
	if cleanupErr != nil {
		if finalErr != nil {
			finalErr = errors.Join(finalErr, cleanupErr)
		} else {
			finalErr = cleanupErr
		}
	}
	if finalErr != nil {
		s.setLastError(finalErr)
		s.logf("%s, switched to direct mode: %v", context, finalErr)
		return finalErr
	}
	s.logf("%s, switched to direct mode", context)
	return nil
}
