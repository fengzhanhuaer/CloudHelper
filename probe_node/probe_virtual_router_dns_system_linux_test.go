//go:build linux

package main

import (
	"errors"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestApplyAndRestoreProbeVirtualRouterSystemDNSLinuxResolved(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	stubProbeVirtualRouterLinuxDNSRoute(t, "default via 192.168.50.1 dev eth0 metric 100")
	resetProbeVirtualRouterLinuxDNSHooksForTest(t)

	commands := make([]string, 0)
	probeVirtualRouterLinuxDNSLookPath = func(file string) (string, error) { return "/usr/bin/resolvectl", nil }
	probeVirtualRouterLinuxDNSReadlink = func(path string) (string, error) {
		return "/run/systemd/resolve/stub-resolv.conf", nil
	}
	probeVirtualRouterLinuxDNSReadFile = func(path string) ([]byte, error) {
		if path == "/run/systemd/resolve/resolv.conf" {
			return []byte("nameserver 9.9.9.9\n"), nil
		}
		return nil, os.ErrNotExist
	}
	probeVirtualRouterLinuxDNSRun = func(timeout time.Duration, name string, args ...string) (string, error) {
		call := name + " " + strings.Join(args, " ")
		commands = append(commands, call)
		if call == "resolvectl dns lo" {
			return "Link 1 (lo):", nil
		}
		return "", nil
	}

	if err := applyProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("apply resolved dns: %v", err)
	}
	for _, want := range []string{"resolvectl dns lo 127.0.0.1", "resolvectl domain lo ~."} {
		if !containsProbeLocalLinuxCall(commands, want) {
			t.Fatalf("missing command %q calls=%v", want, commands)
		}
	}
	if got := currentProbeLocalSystemDNSServers(); !reflect.DeepEqual(got, []string{"9.9.9.9"}) {
		t.Fatalf("upstream servers=%v want [9.9.9.9]", got)
	}
	backup, ok := loadProbeVirtualRouterLinuxDNSBackupBestEffort()
	if !ok || backup.Mode != probeVirtualRouterLinuxDNSModeResolved || backup.Interface != "lo" {
		t.Fatalf("backup=%+v ok=%v", backup, ok)
	}

	if err := restoreProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("restore resolved dns: %v", err)
	}
	if !containsProbeLocalLinuxCall(commands, "resolvectl revert lo") {
		t.Fatalf("missing resolved revert calls=%v", commands)
	}
	if _, ok := loadProbeVirtualRouterLinuxDNSBackupBestEffort(); ok {
		t.Fatal("resolved backup should be removed after restore")
	}
}

func TestApplyAndRestoreProbeVirtualRouterSystemDNSLinuxResolvConf(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	stubProbeVirtualRouterLinuxDNSRoute(t, "default via 192.168.50.1 dev eth0 metric 100")
	resetProbeVirtualRouterLinuxDNSHooksForTest(t)

	original := []byte("# original\nsearch example.internal\nnameserver 192.168.50.1\noptions timeout:1\n")
	current := append([]byte(nil), original...)
	writes := 0
	probeVirtualRouterLinuxDNSLookPath = func(string) (string, error) { return "", errors.New("not found") }
	probeVirtualRouterLinuxDNSReadFile = func(path string) ([]byte, error) {
		if path != probeVirtualRouterLinuxResolvConfPath {
			return nil, os.ErrNotExist
		}
		return append([]byte(nil), current...), nil
	}
	probeVirtualRouterLinuxDNSWriteFile = func(path string, data []byte, mode os.FileMode) error {
		if path != probeVirtualRouterLinuxResolvConfPath {
			t.Fatalf("write path=%q", path)
		}
		writes++
		current = append([]byte(nil), data...)
		return nil
	}
	probeVirtualRouterLinuxDNSStat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }

	if err := applyProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("apply resolv.conf dns: %v", err)
	}
	managed := string(current)
	if !strings.Contains(managed, "nameserver 127.0.0.1") || strings.Contains(managed, "nameserver 192.168.50.1") {
		t.Fatalf("managed resolv.conf=%q", managed)
	}
	if !strings.Contains(managed, "search example.internal") || !strings.Contains(managed, "options timeout:1") {
		t.Fatalf("managed resolv.conf lost non-server settings: %q", managed)
	}
	if got := currentProbeLocalSystemDNSServers(); !reflect.DeepEqual(got, []string{"192.168.50.1"}) {
		t.Fatalf("upstream servers=%v want original dns", got)
	}

	if err := applyProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("reapply resolv.conf dns: %v", err)
	}
	if writes != 1 {
		t.Fatalf("managed resolv.conf writes=%d want 1 before restore", writes)
	}
	if err := restoreProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("restore resolv.conf dns: %v", err)
	}
	if !reflect.DeepEqual(current, original) {
		t.Fatalf("restored resolv.conf=%q want=%q", current, original)
	}
	if writes != 2 {
		t.Fatalf("total resolv.conf writes=%d want 2", writes)
	}
}

func TestFilterProbeVirtualRouterLinuxDNSUpstreamsRemovesLoopback(t *testing.T) {
	got := filterProbeVirtualRouterLinuxDNSUpstreams([]string{"127.0.0.1", "127.0.0.53", "1.1.1.1", "1.1.1.1", "bad"})
	if !reflect.DeepEqual(got, []string{"1.1.1.1"}) {
		t.Fatalf("filtered upstreams=%v", got)
	}
}

func TestProbeVirtualRouterLinuxResolvedAvailableRejectsUnconnectedResolvConf(t *testing.T) {
	resetProbeVirtualRouterLinuxDNSHooksForTest(t)
	probeVirtualRouterLinuxDNSLookPath = func(string) (string, error) { return "/usr/bin/resolvectl", nil }
	probeVirtualRouterLinuxDNSReadlink = func(string) (string, error) { return "", errors.New("not a symlink") }
	probeVirtualRouterLinuxDNSReadFile = func(path string) ([]byte, error) {
		if path == probeVirtualRouterLinuxResolvConfPath {
			return []byte("nameserver 192.168.50.1\n"), nil
		}
		return nil, os.ErrNotExist
	}
	probeVirtualRouterLinuxDNSRun = func(time.Duration, string, ...string) (string, error) {
		t.Fatal("resolvectl must not be used when resolv.conf bypasses systemd-resolved")
		return "", nil
	}
	if probeVirtualRouterLinuxResolvedAvailable("lo") {
		t.Fatal("plain resolv.conf must use the backup/write fallback")
	}
}

func stubProbeVirtualRouterLinuxDNSRoute(t *testing.T, route string) {
	t.Helper()
	oldRun := probeLocalLinuxRunCommand
	oldInterfaceByName := probeLocalLinuxInterfaceByName
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		if name+" "+strings.Join(args, " ") == "ip -4 route show default" {
			return route + "\n", nil
		}
		return "", nil
	}
	probeLocalLinuxInterfaceByName = func(name string) (*net.Interface, error) {
		return &net.Interface{Index: 2, Name: name}, nil
	}
	resetProbeRouteLinuxEgressStateForTest()
	t.Cleanup(func() {
		probeLocalLinuxRunCommand = oldRun
		probeLocalLinuxInterfaceByName = oldInterfaceByName
		resetProbeRouteLinuxEgressStateForTest()
	})
}

func resetProbeVirtualRouterLinuxDNSHooksForTest(t *testing.T) {
	t.Helper()
	oldLookPath := probeVirtualRouterLinuxDNSLookPath
	oldRun := probeVirtualRouterLinuxDNSRun
	oldReadFile := probeVirtualRouterLinuxDNSReadFile
	oldWriteFile := probeVirtualRouterLinuxDNSWriteFile
	oldStat := probeVirtualRouterLinuxDNSStat
	oldReadlink := probeVirtualRouterLinuxDNSReadlink
	t.Cleanup(func() {
		probeVirtualRouterLinuxDNSLookPath = oldLookPath
		probeVirtualRouterLinuxDNSRun = oldRun
		probeVirtualRouterLinuxDNSReadFile = oldReadFile
		probeVirtualRouterLinuxDNSWriteFile = oldWriteFile
		probeVirtualRouterLinuxDNSStat = oldStat
		probeVirtualRouterLinuxDNSReadlink = oldReadlink
	})
}
