//go:build windows

package main

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

	return systemProxySnapshot{
		ProxyEnable: uint32(proxyEnable),
		ProxyServer: strings.TrimSpace(proxyServer),
	}, nil
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
