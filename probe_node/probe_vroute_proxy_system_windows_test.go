//go:build windows

package main

import (
	"errors"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

func TestProbeVRouteWindowsProxyServerValueUsesSOCKS5(t *testing.T) {
	got := probeVRouteWindowsProxyServerValue("127.0.0.1:18080", "127.0.0.1:18081")
	want := "http=127.0.0.1:18080;https=127.0.0.1:18080;socks=socks5://127.0.0.1:18081"
	if got != want {
		t.Fatalf("windows proxy server value=%q want=%q", got, want)
	}
}

func TestProbeVRouteSystemProxyAddressMapsWildcardToLoopback(t *testing.T) {
	got, err := probeVRouteSystemProxyAddress("0.0.0.0:18080")
	if err != nil || got != "127.0.0.1:18080" {
		t.Fatalf("system proxy address=%q err=%v", got, err)
	}
}

func TestProbeVRouteWindowsSystemProxyFallsBackToActiveRDPSession(t *testing.T) {
	sessions := []windows.WTS_SESSION_INFO{
		{SessionID: 1, State: windows.WTSConnected},
		{SessionID: 2, State: windows.WTSActive},
		{SessionID: 3, State: windows.WTSDisconnected},
	}
	ids := probeVRouteWindowsOrderSessionIDs(1, sessions)
	if !reflect.DeepEqual(ids, []uint32{1, 2}) {
		t.Fatalf("ordered session ids=%v want=[1 2]", ids)
	}

	queried := make([]uint32, 0, 2)
	userSID, err := probeVRouteWindowsSelectUserSID(ids, func(sessionID uint32) (string, error) {
		queried = append(queried, sessionID)
		switch sessionID {
		case 1:
			return "", windows.ERROR_NO_TOKEN
		case 2:
			return "S-1-5-21-1000", nil
		default:
			return "", errors.New("unexpected session")
		}
	})
	if err != nil || userSID != "S-1-5-21-1000" {
		t.Fatalf("selected user sid=%q err=%v", userSID, err)
	}
	if !reflect.DeepEqual(queried, []uint32{1, 2}) {
		t.Fatalf("queried sessions=%v want=[1 2]", queried)
	}
}

func TestProbeVRouteWindowsSystemProxySessionOrderDeduplicatesConsole(t *testing.T) {
	sessions := []windows.WTS_SESSION_INFO{
		{SessionID: 2, State: windows.WTSActive},
		{SessionID: 4, State: windows.WTSConnected},
		{SessionID: 5, State: windows.WTSListen},
	}
	ids := probeVRouteWindowsOrderSessionIDs(2, sessions)
	if !reflect.DeepEqual(ids, []uint32{2, 4}) {
		t.Fatalf("ordered session ids=%v want=[2 4]", ids)
	}
}

func TestCleanProbeVRouteWindowsProxySnapshotDisablesAndRemovesServer(t *testing.T) {
	snapshot := cleanProbeVRouteWindowsProxySnapshot()
	if !snapshot.proxyEnable.exists || snapshot.proxyEnable.value != 0 {
		t.Fatalf("clean proxy enable=%+v", snapshot.proxyEnable)
	}
	if snapshot.proxyServer.exists {
		t.Fatalf("clean proxy server=%+v", snapshot.proxyServer)
	}
}

func TestProbeVRouteWindowsProxySnapshotUsesServer(t *testing.T) {
	server := probeVRouteWindowsProxyServerValue("127.0.0.1:18080", "127.0.0.1:18081")
	snapshot := probeVRouteWindowsProxySnapshot{
		proxyEnable: probeVRouteRegistryInteger{value: 1, exists: true},
		proxyServer: probeVRouteRegistryString{value: server, exists: true},
	}
	if !probeVRouteWindowsProxySnapshotUsesServer(snapshot, server) {
		t.Fatal("enabled managed proxy was not recognized")
	}
	snapshot.proxyEnable.value = 0
	if probeVRouteWindowsProxySnapshotUsesServer(snapshot, server) {
		t.Fatal("disabled proxy was recognized as active")
	}
}

func TestProbeVRouteWindowsProxySnapshotRecognizesStaleConfiguredProxyAfterRestart(t *testing.T) {
	resetProbeVirtualRouterLocalSettingsForTest()
	t.Cleanup(resetProbeVirtualRouterLocalSettingsForTest)
	probeVirtualRouterLocalSettingsState.mu.Lock()
	probeVirtualRouterLocalSettingsState.loaded = true
	probeVirtualRouterLocalSettingsState.settings = probeVirtualRouterLocalSettings{
		HTTPProxyListen:   "127.0.0.1:28080",
		SOCKS5ProxyListen: "127.0.0.1:28081",
	}
	probeVirtualRouterLocalSettingsState.mu.Unlock()

	server := probeVRouteWindowsProxyServerValue("127.0.0.1:28080", "127.0.0.1:28081")
	snapshot := probeVRouteWindowsProxySnapshot{
		proxyEnable: probeVRouteRegistryInteger{value: 1, exists: true},
		proxyServer: probeVRouteRegistryString{value: server, exists: true},
	}
	if !probeVRouteWindowsProxySnapshotUsesManagedServer(snapshot) {
		t.Fatal("stale configured proxy was not recognized after runtime state loss")
	}
}
