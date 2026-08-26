//go:build linux && linux_router

package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRewriteProbeLinuxRouterInterfaceAutoPreservesOtherConfiguration(t *testing.T) {
	original := []byte("auto lo\niface lo inet loopback\n\nauto eth0\niface eth0 inet static\n    address 192.168.51.105/24\n    gateway 192.168.51.1\n    metric 202\n    post-up logger eth0-ready\niface eth0 inet6 manual\n    up ip link set $IFACE up\n    down ip link set $IFACE down\n")
	updated, changed, err := rewriteProbeLinuxRouterInterfaceAuto(original, "eth0")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("static interface configuration was not changed")
	}
	text := string(updated)
	for _, marker := range []string{"iface eth0 inet dhcp", "metric 202", "post-up logger eth0-ready", "iface eth0 inet6 manual", "ip link set $IFACE up"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("automatic interface configuration missing %q:\n%s", marker, text)
		}
	}
	for _, removed := range []string{"iface eth0 inet static", "address 192.168.51.105/24", "gateway 192.168.51.1"} {
		if strings.Contains(text, removed) {
			t.Fatalf("automatic interface configuration retained %q:\n%s", removed, text)
		}
	}
}

func TestRewriteProbeLinuxRouterInterfaceAutoIsIdempotent(t *testing.T) {
	original := []byte("auto eth0\r\niface eth0 inet dhcp\r\n    metric 202\r\n")
	updated, changed, err := rewriteProbeLinuxRouterInterfaceAuto(original, "eth0")
	if err != nil {
		t.Fatal(err)
	}
	if changed || !reflect.DeepEqual(updated, original) {
		t.Fatalf("DHCP configuration changed: changed=%t content=%q", changed, updated)
	}
}

func TestRewriteProbeLinuxRouterInterfaceAutoRejectsAmbiguousConfiguration(t *testing.T) {
	original := []byte("iface eth0 inet static\n    address 192.168.1.2/24\niface eth0 inet dhcp\n")
	if _, _, err := rewriteProbeLinuxRouterInterfaceAuto(original, "eth0"); err == nil || !strings.Contains(err.Error(), "multiple IPv4 stanzas") {
		t.Fatalf("ambiguous interface error=%v", err)
	}
	if _, _, err := rewriteProbeLinuxRouterInterfaceAuto([]byte("iface eth1 inet dhcp\n"), "eth0"); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing interface error=%v", err)
	}
}

func TestRestoreProbeLinuxRouterInterfaceAutoWritesBackupAndReconnects(t *testing.T) {
	resetProbeLinuxRouterNetworkAutoHooksForTest(t)
	directory := t.TempDir()
	probeLinuxRouterInterfacesPath = filepath.Join(directory, "interfaces")
	original := []byte("auto eth0\niface eth0 inet static\n    address 192.168.51.105/24\n    gateway 192.168.51.1\n")
	if err := os.WriteFile(probeLinuxRouterInterfacesPath, original, 0o640); err != nil {
		t.Fatal(err)
	}
	probeLinuxRouterNetworkLookPath = func(command string) (string, error) { return "/sbin/" + command, nil }
	var commands []string
	probeLinuxRouterRunCommand = func(_ time.Duration, name string, args ...string) (string, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return "", nil
	}
	probeLinuxRouterScheduleNetworkReconnect = func(_ time.Duration, reconnect func()) { reconnect() }

	interfaceName, changed, err := restoreProbeLinuxRouterInterfaceAuto("eth0")
	if err != nil {
		t.Fatal(err)
	}
	if interfaceName != "eth0" || !changed {
		t.Fatalf("restore result interface=%q changed=%t", interfaceName, changed)
	}
	if !reflect.DeepEqual(commands, []string{"ifquery eth0", "ifdown eth0", "ifup eth0"}) {
		t.Fatalf("network commands=%v", commands)
	}
	backup, err := os.ReadFile(probeLinuxRouterInterfacesPath + ".cloudhelper-before-auto")
	if err != nil || !reflect.DeepEqual(backup, original) {
		t.Fatalf("backup err=%v content=%q", err, backup)
	}
	updated, err := os.ReadFile(probeLinuxRouterInterfacesPath)
	if err != nil || !strings.Contains(string(updated), "iface eth0 inet dhcp") || strings.Contains(string(updated), "192.168.51.105") {
		t.Fatalf("updated err=%v content=%q", err, updated)
	}
	info, err := os.Stat(probeLinuxRouterInterfacesPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("updated mode=%v", info.Mode().Perm())
	}
}

func TestRestoreProbeLinuxRouterInterfaceAutoRollsBackValidationFailure(t *testing.T) {
	resetProbeLinuxRouterNetworkAutoHooksForTest(t)
	directory := t.TempDir()
	probeLinuxRouterInterfacesPath = filepath.Join(directory, "interfaces")
	original := []byte("auto eth0\niface eth0 inet static\n    address 192.168.51.105/24\n")
	if err := os.WriteFile(probeLinuxRouterInterfacesPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	probeLinuxRouterNetworkLookPath = func(command string) (string, error) { return "/sbin/" + command, nil }
	probeLinuxRouterRunCommand = func(_ time.Duration, name string, _ ...string) (string, error) {
		if name == "ifquery" {
			return "invalid interfaces", errors.New("exit status 1")
		}
		t.Fatalf("unexpected network command %s", name)
		return "", nil
	}
	probeLinuxRouterScheduleNetworkReconnect = func(time.Duration, func()) { t.Fatal("reconnect was scheduled after validation failure") }

	if _, changed, err := restoreProbeLinuxRouterInterfaceAuto("eth0"); err == nil || changed {
		t.Fatalf("validation result changed=%t err=%v", changed, err)
	}
	restored, err := os.ReadFile(probeLinuxRouterInterfacesPath)
	if err != nil || !reflect.DeepEqual(restored, original) {
		t.Fatalf("rollback err=%v content=%q", err, restored)
	}
}

func resetProbeLinuxRouterNetworkAutoHooksForTest(t *testing.T) {
	t.Helper()
	oldInterfacesPath := probeLinuxRouterInterfacesPath
	oldLookPath := probeLinuxRouterNetworkLookPath
	oldRunCommand := probeLinuxRouterRunCommand
	oldSchedule := probeLinuxRouterScheduleNetworkReconnect
	t.Cleanup(func() {
		probeLinuxRouterInterfacesPath = oldInterfacesPath
		probeLinuxRouterNetworkLookPath = oldLookPath
		probeLinuxRouterRunCommand = oldRunCommand
		probeLinuxRouterScheduleNetworkReconnect = oldSchedule
	})
}
