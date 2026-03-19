package backend

import (
	"bytes"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const (
	tunEmbeddedLibraryPath = "embedded://wintun/amd64/wintun.dll"
	tunTempRelativePath    = "temp/Lib/wintun/amd64/wintun.dll"
	tunStatusUnsupported   = "仅支持 Windows amd64"
	tunStatusNotInstalled  = "未安装"
	tunStatusInstalled     = "已准备(临时)"
	tunStatusEnabled       = "已启用"
)

//go:embed lib/wintun/amd64/wintun.dll
var embeddedWintunAMD64 []byte

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
	installed := false
	path := ""
	if validateEmbeddedWintunDLL() == nil {
		if p, err := resolveTUNLibraryTempPath(); err == nil {
			path = p
			if info, statErr := os.Stat(p); statErr == nil && !info.IsDir() && info.Size() > 0 {
				installed = true
			}
		}
	}

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
