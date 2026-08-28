//go:build linux

package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestInstallProbeLocalTUNDriverLinuxCreatesDefaultDevice(t *testing.T) {
	oldStat := probeLocalLinuxStat
	oldLookPath := probeLocalLinuxLookPath
	oldRun := probeLocalLinuxRunCommand
	t.Cleanup(func() {
		probeLocalLinuxStat = oldStat
		probeLocalLinuxLookPath = oldLookPath
		probeLocalLinuxRunCommand = oldRun
	})

	probeLocalLinuxStat = func(name string) (os.FileInfo, error) {
		if name != "/dev/net/tun" {
			return nil, errors.New("unexpected stat path")
		}
		return fakeProbeLocalLinuxFileInfo{}, nil
	}
	probeLocalLinuxLookPath = func(file string) (string, error) {
		if file != "ip" {
			return "", errors.New("unexpected lookpath")
		}
		return "/sbin/ip", nil
	}
	calls := make([]string, 0, 4)
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, name+" "+joined)
		if joined == "link show dev "+probeLocalLinuxDefaultTUNDeviceName {
			return "", errors.New("Device does not exist")
		}
		return "", nil
	}

	if err := installProbeLocalTUNDriver(); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	for _, want := range []string{
		"ip link show dev cloudhelper0",
		"ip tuntap add dev cloudhelper0 mode tun",
		"ip link set dev cloudhelper0 up",
		"ip -4 addr replace 198.18.0.2/15 dev cloudhelper0",
		"ip -4 route replace 198.18.0.0/15 dev cloudhelper0 src 198.18.0.2",
	} {
		if !hasProbeVirtualRouterLinuxCommand(calls, want) {
			t.Fatalf("missing command %q calls=%v", want, calls)
		}
	}
}

func TestDetectProbeLocalTUNInstalledLinuxUsesDefaultDevice(t *testing.T) {
	oldStat := probeLocalLinuxStat
	oldLookPath := probeLocalLinuxLookPath
	oldRun := probeLocalLinuxRunCommand
	t.Cleanup(func() {
		probeLocalLinuxStat = oldStat
		probeLocalLinuxLookPath = oldLookPath
		probeLocalLinuxRunCommand = oldRun
	})

	probeLocalLinuxStat = func(name string) (os.FileInfo, error) { return fakeProbeLocalLinuxFileInfo{}, nil }
	probeLocalLinuxLookPath = func(file string) (string, error) { return "/sbin/ip", nil }
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		if strings.Join(args, " ") == "link show dev "+probeLocalLinuxDefaultTUNDeviceName {
			return "1: cloudhelper0: <POINTOPOINT,UP> mtu 1500", nil
		}
		return "", errors.New("unexpected command")
	}

	installed, err := detectProbeLocalTUNInstalledLinux()
	if err != nil {
		t.Fatalf("detect failed: %v", err)
	}
	if !installed {
		t.Fatalf("expected default linux tun device to be detected")
	}
}

func TestUninstallProbeLocalTUNDriverLinuxDeletesDefaultDeviceOnly(t *testing.T) {
	oldLookPath := probeLocalLinuxLookPath
	oldRun := probeLocalLinuxRunCommand
	t.Cleanup(func() {
		probeLocalLinuxLookPath = oldLookPath
		probeLocalLinuxRunCommand = oldRun
	})

	probeLocalLinuxLookPath = func(file string) (string, error) { return "/sbin/ip", nil }
	calls := make([]string, 0, 1)
	probeLocalLinuxRunCommand = func(timeout time.Duration, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}

	if err := uninstallProbeLocalTUNDriver(); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}
	if !hasProbeVirtualRouterLinuxCommand(calls, "ip link del dev cloudhelper0") {
		t.Fatalf("missing delete command calls=%v", calls)
	}

	t.Setenv("PROBE_LOCAL_TUN_DEV", "external0")
	calls = calls[:0]
	if err := uninstallProbeLocalTUNDriver(); err != nil {
		t.Fatalf("custom uninstall failed: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("custom device should not be deleted by default, calls=%v", calls)
	}
}
