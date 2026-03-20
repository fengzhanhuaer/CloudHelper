package backend

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	tunEmbeddedLibraryPath = "embedded://wintun/amd64/wintun.dll"
	tunTempRelativePath    = "temp/Lib/wintun/amd64/wintun.dll"
	tunAdapterName         = "Maple"
	tunAdapterDescription  = "Maple Virtual Network Adapter"
	tunStatusUnsupported   = "仅支持 Windows amd64"
	tunStatusNotInstalled  = "未安装"
	tunStatusInstalled     = "已准备(临时)"
	tunStatusDetected      = "已安装(检测到网卡)"
	tunStatusEnabled       = "已启用"
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

	s.mu.Lock()
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
	if installedByAdapter && !installedByLibrary {
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

	script := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; $ErrorActionPreference='Stop'; $adapters = Get-NetAdapter -IncludeHidden | Select-Object -Property Name,InterfaceDescription; if ($null -eq $adapters) { '[]' } else { $adapters | ConvertTo-Json -Compress }"
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	payload := strings.TrimSpace(string(output))
	if payload == "" || strings.EqualFold(payload, "null") {
		return []windowsNetAdapter{}, nil
	}

	adapters := make([]windowsNetAdapter, 0)
	if err := json.Unmarshal([]byte(payload), &adapters); err == nil {
		return adapters, nil
	}

	var single windowsNetAdapter
	if err := json.Unmarshal([]byte(payload), &single); err == nil {
		return []windowsNetAdapter{single}, nil
	}

	return nil, errors.New("failed to parse Get-NetAdapter output")
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
		if strings.EqualFold(name, tunAdapterName) {
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
		s.tunStatus = tunStatusInstalled
	}
	s.mu.Unlock()

	s.logf("tun library prepared from embed: source=%s target=%s size=%d", tunEmbeddedLibraryPath, path, len(embeddedWintunAMD64))
	return nil
}

func (s *networkAssistantService) EnableTUN() error {
	if !isTUNSupported() {
		err := errors.New(tunStatusUnsupported)
		s.setLastError(err)
		s.syncTUNInstallState()
		return err
	}
	if err := s.InstallTUN(); err != nil {
		return err
	}
	if err := s.stopProxyAndServer(); err != nil {
		s.setLastError(err)
		return err
	}

	s.mu.Lock()
	s.mode = networkModeTUN
	s.tunSupported = true
	s.tunInstalled = true
	s.tunEnabled = true
	s.tunStatus = tunStatusEnabled
	s.tunnelStatusMessage = "TUN 模式已启用"
	s.systemProxyMessage = "TUN 模式"
	s.tunnelOpenFailures = 0
	s.lastError = ""
	libraryPath := s.tunLibraryPath
	s.mu.Unlock()

	s.logf("switched mode to tun, library=%s", libraryPath)
	return nil
}
