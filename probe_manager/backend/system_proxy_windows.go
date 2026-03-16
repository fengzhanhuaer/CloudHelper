//go:build windows

package backend

import (
	"fmt"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const internetSettingsRegistryPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

var (
	wininetDLL                = syscall.NewLazyDLL("wininet.dll")
	internetSetOptionProc     = wininetDLL.NewProc("InternetSetOptionW")
	internetOptionRefresh     = uintptr(37)
	internetOptionSettingsNew = uintptr(39)
)

type systemProxySnapshot struct {
	ProxyEnable uint32
	ProxyServer string
	AutoDetect  uint32
	AutoConfig  string
	ProxyBypass string
}

func captureSystemProxySnapshot() (systemProxySnapshot, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsRegistryPath, registry.QUERY_VALUE)
	if err != nil {
		return systemProxySnapshot{}, err
	}
	defer key.Close()

	proxyEnable, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil {
		proxyEnable = 0
	}
	proxyServer, _, err := key.GetStringValue("ProxyServer")
	if err != nil {
		proxyServer = ""
	}
	autoDetect, _, err := key.GetIntegerValue("AutoDetect")
	if err != nil {
		autoDetect = 0
	}
	autoConfig, _, err := key.GetStringValue("AutoConfigURL")
	if err != nil {
		autoConfig = ""
	}
	proxyBypass, _, err := key.GetStringValue("ProxyOverride")
	if err != nil {
		proxyBypass = ""
	}

	return systemProxySnapshot{
		ProxyEnable: uint32(proxyEnable),
		ProxyServer: strings.TrimSpace(proxyServer),
		AutoDetect:  uint32(autoDetect),
		AutoConfig:  strings.TrimSpace(autoConfig),
		ProxyBypass: strings.TrimSpace(proxyBypass),
	}, nil
}

func (s systemProxySnapshot) Summary() string {
	return fmt.Sprintf("enable=%d server=%q autodetect=%d autoconfig=%q bypass=%q", s.ProxyEnable, s.ProxyServer, s.AutoDetect, s.AutoConfig, s.ProxyBypass)
}

func applySocks5SystemProxy(socksAddr string) error {
	addr := strings.TrimSpace(socksAddr)
	if addr == "" {
		return fmt.Errorf("empty socks5 listen address")
	}

	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsRegistryPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()

	if err := key.SetDWordValue("ProxyEnable", 1); err != nil {
		return err
	}
	if err := key.SetStringValue("ProxyServer", "socks="+addr); err != nil {
		return err
	}
	if err := key.SetStringValue("ProxyOverride", "<local>"); err != nil {
		return err
	}
	if err := key.SetDWordValue("AutoDetect", 0); err != nil {
		return err
	}
	if err := key.SetStringValue("AutoConfigURL", ""); err != nil {
		return err
	}

	refreshWindowsSystemProxy()
	return nil
}

func restoreSystemProxy(snapshot systemProxySnapshot) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsRegistryPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()

	if err := key.SetDWordValue("ProxyEnable", snapshot.ProxyEnable); err != nil {
		return err
	}
	if err := key.SetStringValue("ProxyServer", snapshot.ProxyServer); err != nil {
		return err
	}
	if err := key.SetStringValue("ProxyOverride", snapshot.ProxyBypass); err != nil {
		return err
	}
	if err := key.SetDWordValue("AutoDetect", snapshot.AutoDetect); err != nil {
		return err
	}
	if err := key.SetStringValue("AutoConfigURL", snapshot.AutoConfig); err != nil {
		return err
	}

	refreshWindowsSystemProxy()
	return nil
}

func applyDirectSystemProxy() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsRegistryPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()

	if err := key.SetDWordValue("ProxyEnable", 0); err != nil {
		return err
	}
	if err := key.SetStringValue("ProxyServer", ""); err != nil {
		return err
	}
	if err := key.SetStringValue("ProxyOverride", ""); err != nil {
		return err
	}
	if err := key.SetDWordValue("AutoDetect", 0); err != nil {
		return err
	}
	if err := key.SetStringValue("AutoConfigURL", ""); err != nil {
		return err
	}

	refreshWindowsSystemProxy()
	return nil
}

func refreshWindowsSystemProxy() {
	if err := wininetDLL.Load(); err != nil {
		return
	}
	_, _, _ = internetSetOptionProc.Call(0, internetOptionSettingsNew, 0, 0)
	_, _, _ = internetSetOptionProc.Call(0, internetOptionRefresh, 0, 0)
}
