//go:build linux

package main

import (
	"errors"
	"net"
	"os"
	"reflect"
	"strings"
	"syscall"
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
		if call == "resolvectl dns cloudhelper0" {
			return "Link 3 (cloudhelper0):", nil
		}
		return "", nil
	}

	if err := applyProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("apply resolved dns: %v", err)
	}
	for _, want := range []string{"resolvectl dns cloudhelper0 198.18.0.2", "resolvectl domain cloudhelper0 ~."} {
		if !containsProbeLocalLinuxCall(commands, want) {
			t.Fatalf("missing command %q calls=%v", want, commands)
		}
	}
	if got := currentProbeLocalSystemDNSServers(); !reflect.DeepEqual(got, []string{"9.9.9.9"}) {
		t.Fatalf("upstream servers=%v want [9.9.9.9]", got)
	}
	backup, ok := loadProbeVirtualRouterLinuxDNSBackupBestEffort()
	if !ok || backup.Mode != probeVirtualRouterLinuxDNSModeResolved || backup.Interface != "cloudhelper0" {
		t.Fatalf("backup=%+v ok=%v", backup, ok)
	}

	if err := restoreProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("restore resolved dns: %v", err)
	}
	if !containsProbeLocalLinuxCall(commands, "resolvectl revert cloudhelper0") {
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
	if !strings.Contains(managed, "nameserver 198.18.0.2") || strings.Contains(managed, "nameserver 192.168.50.1") {
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
	got := filterProbeVirtualRouterLinuxDNSUpstreams([]string{"127.0.0.1", "127.0.0.53", "198.18.0.2", "1.1.1.1", "1.1.1.1", "bad"})
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

func TestProbeVirtualRouterLinuxDockerHostDNSUsesDBusAndHostResolvConf(t *testing.T) {
	t.Setenv(probeVirtualRouterLinuxDockerHostDNSEnv, "true")
	t.Setenv(probeVirtualRouterLinuxHostDBusSocketEnv, "/host/run/dbus/system_bus_socket")
	t.Setenv(probeVirtualRouterLinuxHostResolvConfEnv, "/host/etc/resolv.conf")
	oldAvailable := probeVirtualRouterLinuxResolvedDBusAvailable
	oldCommand := probeVirtualRouterLinuxResolvedDBusCommand
	t.Cleanup(func() {
		probeVirtualRouterLinuxResolvedDBusAvailable = oldAvailable
		probeVirtualRouterLinuxResolvedDBusCommand = oldCommand
	})
	availableCalls := 0
	probeVirtualRouterLinuxResolvedDBusAvailable = func() error {
		availableCalls++
		return nil
	}
	var commandArgs []string
	probeVirtualRouterLinuxResolvedDBusCommand = func(_ time.Duration, args ...string) (string, error) {
		commandArgs = append([]string(nil), args...)
		return "100.100.2.136 223.5.5.5", nil
	}

	path, err := probeVirtualRouterLinuxDNSCommandLookPath("resolvectl")
	if err != nil || path != "dbus:/host/run/dbus/system_bus_socket" {
		t.Fatalf("docker resolvectl path=%q err=%v", path, err)
	}
	if !probeVirtualRouterLinuxSystemResolverUsesResolved() {
		t.Fatal("docker host dbus should select systemd-resolved mode")
	}
	output, err := runProbeVirtualRouterLinuxDNSCommand(time.Second, "resolvectl", "dns", "eth0")
	if err != nil || output != "100.100.2.136 223.5.5.5" {
		t.Fatalf("docker dbus output=%q err=%v", output, err)
	}
	if !reflect.DeepEqual(commandArgs, []string{"dns", "eth0"}) {
		t.Fatalf("docker dbus args=%v", commandArgs)
	}
	if availableCalls < 2 {
		t.Fatalf("docker dbus availability calls=%d want at least 2", availableCalls)
	}
	if got := currentProbeVirtualRouterLinuxResolvConfPath(); got != "/host/etc/resolv.conf" {
		t.Fatalf("docker host resolv.conf=%q", got)
	}
}

func TestApplyProbeVirtualRouterSystemDNSMigratesLegacyDockerBackup(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	t.Setenv(probeVirtualRouterLinuxDockerHostDNSEnv, "true")
	t.Setenv(probeVirtualRouterLinuxHostResolvConfEnv, "/host/etc/resolv.conf")
	stubProbeVirtualRouterLinuxDNSRoute(t, "default via 192.168.50.1 dev eth0 metric 100")
	resetProbeVirtualRouterLinuxDNSHooksForTest(t)

	containerOriginal := []byte("nameserver 127.0.0.11\n")
	hostOriginal := []byte("nameserver 192.168.50.1\nsearch example.internal\n")
	files := map[string][]byte{
		probeVirtualRouterLinuxResolvConfPath: containerOriginal,
		"/host/etc/resolv.conf":               hostOriginal,
	}
	probeVirtualRouterLinuxResolvedDBusAvailable = func() error { return errors.New("systemd-resolved unavailable") }
	probeVirtualRouterLinuxDNSReadFile = func(path string) ([]byte, error) {
		raw, ok := files[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return append([]byte(nil), raw...), nil
	}
	probeVirtualRouterLinuxDNSWriteFile = func(path string, data []byte, _ os.FileMode) error {
		files[path] = append([]byte(nil), data...)
		return nil
	}
	probeVirtualRouterLinuxDNSStat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }

	legacy := probeVirtualRouterLinuxDNSBackup{
		Mode:             probeVirtualRouterLinuxDNSModeResolvConf,
		UpstreamServers:  []string{"127.0.0.11"},
		ResolvConfPath:   probeVirtualRouterLinuxResolvConfPath,
		ResolvConf:       containerOriginal,
		ResolvConfMode:   0o644,
		AppliedDNSServer: "127.0.0.1",
	}
	if err := persistProbeVirtualRouterLinuxDNSBackup(legacy); err != nil {
		t.Fatalf("persist legacy docker backup: %v", err)
	}
	if err := applyProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("apply migrated docker dns: %v", err)
	}
	if got := string(files[probeVirtualRouterLinuxResolvConfPath]); got != string(containerOriginal) {
		t.Fatalf("container resolv.conf changed=%q", got)
	}
	if got := string(files["/host/etc/resolv.conf"]); !strings.Contains(got, "nameserver 198.18.0.2") {
		t.Fatalf("host resolv.conf not managed=%q", got)
	}
	backup, ok := loadProbeVirtualRouterLinuxDNSBackupBestEffort()
	if !ok || backup.ResolvConfPath != "/host/etc/resolv.conf" || backup.AppliedDNSServer != "198.18.0.2" {
		t.Fatalf("migrated backup=%+v ok=%v", backup, ok)
	}
	if !reflect.DeepEqual(backup.ResolvConf, hostOriginal) || !reflect.DeepEqual(backup.UpstreamServers, []string{"192.168.50.1"}) {
		t.Fatalf("migrated host snapshot=%q upstreams=%v", backup.ResolvConf, backup.UpstreamServers)
	}
	if err := restoreProbeVirtualRouterSystemDNS(); err != nil {
		t.Fatalf("restore migrated docker dns: %v", err)
	}
	if got := files["/host/etc/resolv.conf"]; !reflect.DeepEqual(got, hostOriginal) {
		t.Fatalf("restored host resolv.conf=%q want=%q", got, hostOriginal)
	}
}

func TestProbeVirtualRouterLinuxResolvedDNSRecordsIncludeGlobalAndSelectedLink(t *testing.T) {
	records := []probeVirtualRouterLinuxResolvedDNSRecord{
		{InterfaceIndex: 0, Family: syscall.AF_INET, Address: net.ParseIP("1.1.1.1").To4()},
		{InterfaceIndex: 2, Family: syscall.AF_INET, Address: net.ParseIP("8.8.8.8").To4()},
		{InterfaceIndex: 3, Family: syscall.AF_INET, Address: net.ParseIP("9.9.9.9").To4()},
		{InterfaceIndex: 2, Family: syscall.AF_INET6, Address: net.ParseIP("2001:4860:4860::8888").To16()},
		{InterfaceIndex: 2, Family: syscall.AF_INET, Address: net.ParseIP("198.18.0.2").To4()},
	}
	got := probeVirtualRouterLinuxResolvedDNSRecordsToServers(records, 2)
	if !reflect.DeepEqual(got, []string{"1.1.1.1", "8.8.8.8"}) {
		t.Fatalf("resolved dns servers=%v", got)
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
	oldDBusAvailable := probeVirtualRouterLinuxResolvedDBusAvailable
	oldDBusCommand := probeVirtualRouterLinuxResolvedDBusCommand
	t.Cleanup(func() {
		probeVirtualRouterLinuxDNSLookPath = oldLookPath
		probeVirtualRouterLinuxDNSRun = oldRun
		probeVirtualRouterLinuxDNSReadFile = oldReadFile
		probeVirtualRouterLinuxDNSWriteFile = oldWriteFile
		probeVirtualRouterLinuxDNSStat = oldStat
		probeVirtualRouterLinuxDNSReadlink = oldReadlink
		probeVirtualRouterLinuxResolvedDBusAvailable = oldDBusAvailable
		probeVirtualRouterLinuxResolvedDBusCommand = oldDBusCommand
	})
}
