//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	probeLocalWindowsInternetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
)

type probeLocalExplicitProxySystemBackup struct {
	Version       int                                         `json:"version"`
	UpdatedAt     string                                      `json:"updated_at,omitempty"`
	Env           map[string]probeLocalExplicitRegistryString `json:"env,omitempty"`
	ProxyEnable   probeLocalExplicitRegistryDWORD             `json:"proxy_enable"`
	ProxyServer   probeLocalExplicitRegistryString            `json:"proxy_server"`
	ProxyOverride probeLocalExplicitRegistryString            `json:"proxy_override"`
	Extra         map[string]probeLocalExplicitRegistryString `json:"extra,omitempty"`
}

type probeLocalExplicitRegistryString struct {
	Exists bool   `json:"exists"`
	Value  string `json:"value,omitempty"`
}

type probeLocalExplicitRegistryDWORD struct {
	Exists bool   `json:"exists"`
	Value  uint32 `json:"value,omitempty"`
}

func applyProbeLocalExplicitProxySystemSettingsPlatform(httpAddr string, socksAddr string) error {
	if httpAddr == "" && socksAddr == "" {
		return errors.New("explicit proxy listener is unavailable")
	}
	setProbeLocalExplicitProxyProcessEnv(httpAddr, socksAddr)
	if err := setProbeLocalWindowsUserEnvironment(httpAddr, socksAddr); err != nil {
		return err
	}
	if err := setProbeLocalWindowsInternetProxy(httpAddr, socksAddr); err != nil {
		return err
	}
	broadcastProbeLocalWindowsSettingsChanged("Environment")
	broadcastProbeLocalWindowsSettingsChanged("Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings")
	return nil
}

func restoreProbeLocalExplicitProxySystemSettingsPlatform() error {
	clearProbeLocalExplicitProxyProcessEnv()
	if err := clearProbeLocalWindowsUserEnvironment(); err != nil {
		return err
	}
	if err := clearProbeLocalWindowsInternetProxy(); err != nil {
		return err
	}
	path, pathErr := resolveProbeLocalExplicitProxySystemBackupPath()
	if pathErr == nil {
		_ = os.Remove(path)
	}
	broadcastProbeLocalWindowsSettingsChanged("Environment")
	broadcastProbeLocalWindowsSettingsChanged("Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings")
	return nil
}

func loadOrCreateProbeLocalExplicitProxySystemBackup() (probeLocalExplicitProxySystemBackup, error) {
	backup, err := loadProbeLocalExplicitProxySystemBackup()
	if err == nil {
		return backup, nil
	}
	if os.IsNotExist(err) {
		return probeLocalExplicitProxySystemBackup{}, nil
	}
	return probeLocalExplicitProxySystemBackup{}, err
}

func loadProbeLocalExplicitProxySystemBackup() (probeLocalExplicitProxySystemBackup, error) {
	path, err := resolveProbeLocalExplicitProxySystemBackupPath()
	if err != nil {
		return probeLocalExplicitProxySystemBackup{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return probeLocalExplicitProxySystemBackup{}, err
	}
	var backup probeLocalExplicitProxySystemBackup
	if err := json.Unmarshal(raw, &backup); err != nil {
		return probeLocalExplicitProxySystemBackup{}, err
	}
	return backup, nil
}

func saveProbeLocalExplicitProxySystemBackup(backup probeLocalExplicitProxySystemBackup) error {
	path, err := resolveProbeLocalExplicitProxySystemBackupPath()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0600)
}

func captureProbeLocalExplicitProxySystemBackup() probeLocalExplicitProxySystemBackup {
	return probeLocalExplicitProxySystemBackup{
		Version:       1,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		Env:           captureProbeLocalWindowsUserEnvironment(),
		ProxyEnable:   readProbeLocalWindowsRegistryDWORD(registry.CURRENT_USER, probeLocalWindowsInternetSettingsKey, "ProxyEnable"),
		ProxyServer:   readProbeLocalWindowsRegistryString(registry.CURRENT_USER, probeLocalWindowsInternetSettingsKey, "ProxyServer"),
		ProxyOverride: readProbeLocalWindowsRegistryString(registry.CURRENT_USER, probeLocalWindowsInternetSettingsKey, "ProxyOverride"),
	}
}

func setProbeLocalWindowsUserEnvironment(httpAddr string, socksAddr string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	values := resolveProbeLocalExplicitProxyEnvValues(httpAddr, socksAddr)
	for name, value := range values {
		if value == "" {
			if err := key.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
				return err
			}
			continue
		}
		if err := key.SetStringValue(name, value); err != nil {
			return err
		}
	}
	return nil
}

func restoreProbeLocalWindowsUserEnvironment(values map[string]probeLocalExplicitRegistryString) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	for _, name := range probeLocalExplicitProxyEnvKeys() {
		value := values[name]
		if value.Exists {
			if err := key.SetStringValue(name, value.Value); err != nil {
				return err
			}
			continue
		}
		if err := key.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
	}
	return nil
}

func clearProbeLocalWindowsUserEnvironment() error {
	return restoreProbeLocalWindowsUserEnvironment(map[string]probeLocalExplicitRegistryString{})
}

func setProbeLocalWindowsInternetProxy(httpAddr string, socksAddr string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, probeLocalWindowsInternetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	servers := make([]string, 0, 3)
	if httpAddr != "" {
		servers = append(servers, "http="+httpAddr, "https="+httpAddr)
	}
	if socksAddr != "" {
		servers = append(servers, "socks="+socksAddr)
	}
	if len(servers) == 0 {
		return errors.New("empty proxy server settings")
	}
	if err := key.SetDWordValue("ProxyEnable", 1); err != nil {
		return err
	}
	if err := key.SetStringValue("ProxyServer", strings.Join(servers, ";")); err != nil {
		return err
	}
	return key.SetStringValue("ProxyOverride", "localhost;127.*;::1;<local>")
}

func restoreProbeLocalWindowsInternetProxy(backup probeLocalExplicitProxySystemBackup) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, probeLocalWindowsInternetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if backup.ProxyEnable.Exists {
		if err := key.SetDWordValue("ProxyEnable", backup.ProxyEnable.Value); err != nil {
			return err
		}
	} else if err := key.DeleteValue("ProxyEnable"); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	if backup.ProxyServer.Exists {
		if err := key.SetStringValue("ProxyServer", backup.ProxyServer.Value); err != nil {
			return err
		}
	} else if err := key.DeleteValue("ProxyServer"); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	if backup.ProxyOverride.Exists {
		if err := key.SetStringValue("ProxyOverride", backup.ProxyOverride.Value); err != nil {
			return err
		}
	} else if err := key.DeleteValue("ProxyOverride"); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func clearProbeLocalWindowsInternetProxy() error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, probeLocalWindowsInternetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.SetDWordValue("ProxyEnable", 0); err != nil {
		return err
	}
	if err := key.DeleteValue("ProxyServer"); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	if err := key.DeleteValue("ProxyOverride"); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func captureProbeLocalWindowsUserEnvironment() map[string]probeLocalExplicitRegistryString {
	out := map[string]probeLocalExplicitRegistryString{}
	for _, name := range probeLocalExplicitProxyEnvKeys() {
		out[name] = readProbeLocalWindowsRegistryString(registry.CURRENT_USER, `Environment`, name)
	}
	return out
}

func readProbeLocalWindowsRegistryString(root registry.Key, path string, name string) probeLocalExplicitRegistryString {
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return probeLocalExplicitRegistryString{}
	}
	defer key.Close()
	value, _, err := key.GetStringValue(name)
	if err != nil {
		return probeLocalExplicitRegistryString{}
	}
	return probeLocalExplicitRegistryString{Exists: true, Value: value}
}

func readProbeLocalWindowsRegistryDWORD(root registry.Key, path string, name string) probeLocalExplicitRegistryDWORD {
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return probeLocalExplicitRegistryDWORD{}
	}
	defer key.Close()
	value, _, err := key.GetIntegerValue(name)
	if err != nil {
		return probeLocalExplicitRegistryDWORD{}
	}
	return probeLocalExplicitRegistryDWORD{Exists: true, Value: uint32(value)}
}

func resolveProbeLocalExplicitProxyEnvValues(httpAddr string, socksAddr string) map[string]string {
	values := map[string]string{}
	if httpAddr != "" {
		values["HTTP_PROXY"] = "http://" + httpAddr
		values["HTTPS_PROXY"] = "http://" + httpAddr
		values["http_proxy"] = "http://" + httpAddr
		values["https_proxy"] = "http://" + httpAddr
	}
	if socksAddr != "" {
		values["ALL_PROXY"] = "socks5://" + socksAddr
		values["all_proxy"] = "socks5://" + socksAddr
	}
	values["NO_PROXY"] = "localhost,127.0.0.1,::1"
	values["no_proxy"] = "localhost,127.0.0.1,::1"
	return values
}

func setProbeLocalExplicitProxyProcessEnv(httpAddr string, socksAddr string) {
	for name, value := range resolveProbeLocalExplicitProxyEnvValues(httpAddr, socksAddr) {
		if value != "" {
			_ = os.Setenv(name, value)
		}
	}
}

func clearProbeLocalExplicitProxyProcessEnv() {
	restoreProbeLocalExplicitProxyProcessEnv(map[string]probeLocalExplicitRegistryString{})
}

func restoreProbeLocalExplicitProxyProcessEnv(values map[string]probeLocalExplicitRegistryString) {
	for _, name := range probeLocalExplicitProxyEnvKeys() {
		if values != nil {
			if value := values[name]; value.Exists {
				_ = os.Setenv(name, value.Value)
				continue
			}
		}
		_ = os.Unsetenv(name)
	}
}

func probeLocalExplicitProxyEnvKeys() []string {
	return []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"}
}

func broadcastProbeLocalWindowsSettingsChanged(area string) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	proc := user32.NewProc("SendMessageTimeoutW")
	text, err := windows.UTF16PtrFromString(area)
	if err != nil {
		return
	}
	proc.Call(
		uintptr(0xffff),
		uintptr(0x001a),
		0,
		uintptr(unsafe.Pointer(text)),
		uintptr(0x0002),
		uintptr(3000),
		0,
	)
}
