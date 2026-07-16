//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const probeVRouteInternetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

type probeVRouteRegistryInteger struct {
	value  uint64
	exists bool
}

type probeVRouteRegistryString struct {
	value  string
	exists bool
}

type probeVRouteWindowsProxySnapshot struct {
	proxyEnable probeVRouteRegistryInteger
	proxyServer probeVRouteRegistryString
}

var probeVRouteWindowsSystemProxyState = struct {
	mu       sync.Mutex
	captured bool
	applied  bool
	http     string
	socks5   string
	original probeVRouteWindowsProxySnapshot
}{}

var probeVRouteWinINet = windows.NewLazySystemDLL("wininet.dll")
var probeVRouteInternetSetOption = probeVRouteWinINet.NewProc("InternetSetOptionW")

func setProbeVRouteSystemProxy(httpListenAddress string, socks5ListenAddress string) error {
	httpAddress, err := probeVRouteSystemProxyAddress(httpListenAddress)
	if err != nil {
		return err
	}
	socks5Address, err := probeVRouteSystemProxyAddress(socks5ListenAddress)
	if err != nil {
		return err
	}
	probeVRouteWindowsSystemProxyState.mu.Lock()
	defer probeVRouteWindowsSystemProxyState.mu.Unlock()
	if probeVRouteWindowsSystemProxyState.applied &&
		strings.EqualFold(probeVRouteWindowsSystemProxyState.http, httpAddress) &&
		strings.EqualFold(probeVRouteWindowsSystemProxyState.socks5, socks5Address) {
		return nil
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, probeVRouteInternetSettingsKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if !probeVRouteWindowsSystemProxyState.captured {
		snapshot, snapshotErr := readProbeVRouteWindowsProxySnapshot(key)
		if snapshotErr != nil {
			return snapshotErr
		}
		probeVRouteWindowsSystemProxyState.original = snapshot
		probeVRouteWindowsSystemProxyState.captured = true
	}
	proxyServer := probeVRouteWindowsProxyServerValue(httpAddress, socks5Address)
	if err := key.SetStringValue("ProxyServer", proxyServer); err != nil {
		return rollbackProbeVRouteWindowsProxyLocked(key, err)
	}
	if err := key.SetDWordValue("ProxyEnable", 1); err != nil {
		return rollbackProbeVRouteWindowsProxyLocked(key, err)
	}
	if err := notifyProbeVRouteWinINetProxyChanged(); err != nil {
		return rollbackProbeVRouteWindowsProxyLocked(key, err)
	}
	probeVRouteWindowsSystemProxyState.applied = true
	probeVRouteWindowsSystemProxyState.http = httpAddress
	probeVRouteWindowsSystemProxyState.socks5 = socks5Address
	logProbeInfof("probe vroute windows system proxy applied: http=%s https=%s socks=%s", httpAddress, httpAddress, socks5Address)
	return nil
}

func restoreProbeVRouteSystemProxy() error {
	probeVRouteWindowsSystemProxyState.mu.Lock()
	defer probeVRouteWindowsSystemProxyState.mu.Unlock()
	if !probeVRouteWindowsSystemProxyState.captured {
		return nil
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, probeVRouteInternetSettingsKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := writeProbeVRouteWindowsProxySnapshot(key, probeVRouteWindowsSystemProxyState.original); err != nil {
		return err
	}
	if err := notifyProbeVRouteWinINetProxyChanged(); err != nil {
		return err
	}
	probeVRouteWindowsSystemProxyState.captured = false
	probeVRouteWindowsSystemProxyState.applied = false
	probeVRouteWindowsSystemProxyState.http = ""
	probeVRouteWindowsSystemProxyState.socks5 = ""
	probeVRouteWindowsSystemProxyState.original = probeVRouteWindowsProxySnapshot{}
	logProbeInfof("probe vroute windows system proxy restored")
	return nil
}

func snapshotProbeVRouteSystemProxy() (bool, string, string) {
	probeVRouteWindowsSystemProxyState.mu.Lock()
	defer probeVRouteWindowsSystemProxyState.mu.Unlock()
	return probeVRouteWindowsSystemProxyState.applied, probeVRouteWindowsSystemProxyState.http, probeVRouteWindowsSystemProxyState.socks5
}

func probeVRouteSystemProxyAddress(listenAddress string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listenAddress))
	if err != nil {
		return "", err
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

func probeVRouteWindowsProxyServerValue(httpAddress string, socks5Address string) string {
	return "http=" + httpAddress + ";https=" + httpAddress + ";socks=socks5://" + socks5Address
}

func readProbeVRouteWindowsProxySnapshot(key registry.Key) (probeVRouteWindowsProxySnapshot, error) {
	var snapshot probeVRouteWindowsProxySnapshot
	value, _, err := key.GetIntegerValue("ProxyEnable")
	if err == nil {
		snapshot.proxyEnable = probeVRouteRegistryInteger{value: value, exists: true}
	} else if err != registry.ErrNotExist {
		return snapshot, err
	}
	text, _, err := key.GetStringValue("ProxyServer")
	if err == nil {
		snapshot.proxyServer = probeVRouteRegistryString{value: text, exists: true}
	} else if err != registry.ErrNotExist {
		return snapshot, err
	}
	return snapshot, nil
}

func writeProbeVRouteWindowsProxySnapshot(key registry.Key, snapshot probeVRouteWindowsProxySnapshot) error {
	if snapshot.proxyServer.exists {
		if err := key.SetStringValue("ProxyServer", snapshot.proxyServer.value); err != nil {
			return err
		}
	} else if err := key.DeleteValue("ProxyServer"); err != nil && err != registry.ErrNotExist {
		return err
	}
	if snapshot.proxyEnable.exists {
		if err := key.SetDWordValue("ProxyEnable", uint32(snapshot.proxyEnable.value)); err != nil {
			return err
		}
	} else if err := key.DeleteValue("ProxyEnable"); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

func rollbackProbeVRouteWindowsProxyLocked(key registry.Key, cause error) error {
	restoreErr := writeProbeVRouteWindowsProxySnapshot(key, probeVRouteWindowsSystemProxyState.original)
	if restoreErr == nil {
		restoreErr = notifyProbeVRouteWinINetProxyChanged()
	}
	if restoreErr != nil {
		return fmt.Errorf("%w (rollback failed: %v)", cause, restoreErr)
	}
	probeVRouteWindowsSystemProxyState.captured = false
	probeVRouteWindowsSystemProxyState.applied = false
	probeVRouteWindowsSystemProxyState.http = ""
	probeVRouteWindowsSystemProxyState.socks5 = ""
	probeVRouteWindowsSystemProxyState.original = probeVRouteWindowsProxySnapshot{}
	return cause
}

func notifyProbeVRouteWinINetProxyChanged() error {
	const (
		internetOptionRefresh         = 37
		internetOptionSettingsChanged = 39
	)
	for _, option := range []uintptr{internetOptionSettingsChanged, internetOptionRefresh} {
		result, _, callErr := probeVRouteInternetSetOption.Call(0, option, 0, 0)
		if result == 0 {
			if callErr == nil || errors.Is(callErr, windows.ERROR_SUCCESS) {
				callErr = windows.ERROR_INVALID_FUNCTION
			}
			return callErr
		}
	}
	return nil
}
