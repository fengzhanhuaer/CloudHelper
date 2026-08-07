//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const probeVRouteInternetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func probeVRouteSystemProxyRequired() bool {
	return true
}

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
	userSID  string
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
	if !probeVRouteWindowsSystemProxyState.captured {
		userSID, targetErr := probeVRouteWindowsSystemProxyUserSID()
		if targetErr != nil {
			return targetErr
		}
		probeVRouteWindowsSystemProxyState.userSID = userSID
	}
	key, err := openProbeVRouteWindowsInternetSettings(probeVRouteWindowsSystemProxyState.userSID)
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
	userSID := probeVRouteWindowsSystemProxyState.userSID
	if strings.TrimSpace(userSID) == "" {
		var err error
		userSID, err = probeVRouteWindowsSystemProxyUserSID()
		if err != nil {
			return err
		}
	}
	key, err := openProbeVRouteWindowsInternetSettings(userSID)
	if err != nil {
		return err
	}
	defer key.Close()
	current, err := readProbeVRouteWindowsProxySnapshot(key)
	if err != nil {
		return err
	}
	managed := probeVRouteWindowsSystemProxyState.captured || probeVRouteWindowsProxySnapshotUsesManagedServer(current)
	if !managed {
		resetProbeVRouteWindowsSystemProxyStateLocked()
		return nil
	}
	if err := writeProbeVRouteWindowsProxySnapshot(key, cleanProbeVRouteWindowsProxySnapshot()); err != nil {
		return err
	}
	if err := notifyProbeVRouteWinINetProxyChanged(); err != nil {
		return err
	}
	resetProbeVRouteWindowsSystemProxyStateLocked()
	logProbeInfof("probe vroute windows system proxy cleaned")
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

func probeVRouteWindowsSystemProxyUserSID() (string, error) {
	processToken, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer processToken.Close()
	processUser, err := processToken.GetTokenUser()
	if err != nil {
		return "", err
	}
	processSID := processUser.User.Sid.String()
	if !strings.EqualFold(processSID, "S-1-5-18") {
		return "", nil
	}
	sessionIDs, enumerateErr := probeVRouteWindowsLoggedOnSessionIDs()
	userSID, err := probeVRouteWindowsSelectUserSID(sessionIDs, probeVRouteWindowsSessionUserSID)
	if err == nil {
		return userSID, nil
	}
	if enumerateErr != nil {
		return "", fmt.Errorf("enumerate windows user sessions: %v; %w", enumerateErr, err)
	}
	return "", err
}

func probeVRouteWindowsLoggedOnSessionIDs() ([]uint32, error) {
	consoleSessionID := windows.WTSGetActiveConsoleSessionId()
	var sessionInfo *windows.WTS_SESSION_INFO
	var count uint32
	err := windows.WTSEnumerateSessions(0, 0, 1, &sessionInfo, &count)
	if err != nil {
		return probeVRouteWindowsOrderSessionIDs(consoleSessionID, nil), err
	}
	if sessionInfo == nil || count == 0 {
		return probeVRouteWindowsOrderSessionIDs(consoleSessionID, nil), nil
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(sessionInfo)))
	sessions := unsafe.Slice(sessionInfo, int(count))
	return probeVRouteWindowsOrderSessionIDs(consoleSessionID, sessions), nil
}

func probeVRouteWindowsOrderSessionIDs(consoleSessionID uint32, sessions []windows.WTS_SESSION_INFO) []uint32 {
	ids := make([]uint32, 0, len(sessions)+1)
	seen := make(map[uint32]struct{}, len(sessions)+1)
	appendSession := func(sessionID uint32) {
		if sessionID == ^uint32(0) {
			return
		}
		if _, ok := seen[sessionID]; ok {
			return
		}
		seen[sessionID] = struct{}{}
		ids = append(ids, sessionID)
	}
	appendSession(consoleSessionID)
	for _, state := range []uint32{windows.WTSActive, windows.WTSConnected} {
		for _, session := range sessions {
			if session.State == state {
				appendSession(session.SessionID)
			}
		}
	}
	return ids
}

func probeVRouteWindowsSelectUserSID(sessionIDs []uint32, query func(uint32) (string, error)) (string, error) {
	if len(sessionIDs) == 0 {
		return "", errors.New("windows system proxy requires an active logged-on user")
	}
	attempts := make([]string, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		userSID, err := query(sessionID)
		if err != nil {
			attempts = append(attempts, fmt.Sprintf("session=%d: %v", sessionID, err))
			continue
		}
		userSID = strings.TrimSpace(userSID)
		if userSID == "" || strings.EqualFold(userSID, "S-1-5-18") {
			attempts = append(attempts, fmt.Sprintf("session=%d: user sid unavailable", sessionID))
			continue
		}
		return userSID, nil
	}
	return "", fmt.Errorf("no active windows user token is available (%s)", strings.Join(attempts, "; "))
}

func probeVRouteWindowsSessionUserSID(sessionID uint32) (string, error) {
	var userToken windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &userToken); err != nil {
		return "", fmt.Errorf("query user token: %w", err)
	}
	defer userToken.Close()
	user, err := userToken.GetTokenUser()
	if err != nil {
		return "", err
	}
	userSID := user.User.Sid.String()
	if userSID == "" || strings.EqualFold(userSID, "S-1-5-18") {
		return "", errors.New("windows session user sid is unavailable")
	}
	return userSID, nil
}

func openProbeVRouteWindowsInternetSettings(userSID string) (registry.Key, error) {
	root := registry.CURRENT_USER
	path := probeVRouteInternetSettingsKey
	if strings.TrimSpace(userSID) != "" {
		root = registry.USERS
		path = strings.TrimSpace(userSID) + `\` + probeVRouteInternetSettingsKey
	}
	return registry.OpenKey(root, path, registry.QUERY_VALUE|registry.SET_VALUE)
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

func cleanProbeVRouteWindowsProxySnapshot() probeVRouteWindowsProxySnapshot {
	return probeVRouteWindowsProxySnapshot{
		proxyEnable: probeVRouteRegistryInteger{value: 0, exists: true},
	}
}

func probeVRouteWindowsProxySnapshotUsesServer(snapshot probeVRouteWindowsProxySnapshot, proxyServer string) bool {
	return snapshot.proxyEnable.exists && snapshot.proxyEnable.value != 0 && snapshot.proxyServer.exists &&
		strings.EqualFold(strings.TrimSpace(snapshot.proxyServer.value), strings.TrimSpace(proxyServer))
}

func probeVRouteWindowsProxySnapshotUsesManagedServer(snapshot probeVRouteWindowsProxySnapshot) bool {
	if !snapshot.proxyEnable.exists || snapshot.proxyEnable.value == 0 || !snapshot.proxyServer.exists {
		return false
	}
	servers := []string{
		probeVRouteWindowsProxyServerValue("127.0.0.1:18080", "127.0.0.1:18081"),
	}
	settings := loadProbeVirtualRouterLocalSettings()
	httpAddress, httpErr := probeVRouteSystemProxyAddress(settings.HTTPProxyListen)
	socks5Address, socksErr := probeVRouteSystemProxyAddress(settings.SOCKS5ProxyListen)
	if httpErr == nil && socksErr == nil {
		servers = append(servers, probeVRouteWindowsProxyServerValue(httpAddress, socks5Address))
	}
	for _, server := range servers {
		if probeVRouteWindowsProxySnapshotUsesServer(snapshot, server) {
			return true
		}
	}
	return false
}

func resetProbeVRouteWindowsSystemProxyStateLocked() {
	probeVRouteWindowsSystemProxyState.captured = false
	probeVRouteWindowsSystemProxyState.applied = false
	probeVRouteWindowsSystemProxyState.http = ""
	probeVRouteWindowsSystemProxyState.socks5 = ""
	probeVRouteWindowsSystemProxyState.userSID = ""
	probeVRouteWindowsSystemProxyState.original = probeVRouteWindowsProxySnapshot{}
}

func rollbackProbeVRouteWindowsProxyLocked(key registry.Key, cause error) error {
	restoreErr := writeProbeVRouteWindowsProxySnapshot(key, probeVRouteWindowsSystemProxyState.original)
	if restoreErr == nil {
		restoreErr = notifyProbeVRouteWinINetProxyChanged()
	}
	if restoreErr != nil {
		return fmt.Errorf("%w (rollback failed: %v)", cause, restoreErr)
	}
	resetProbeVRouteWindowsSystemProxyStateLocked()
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
