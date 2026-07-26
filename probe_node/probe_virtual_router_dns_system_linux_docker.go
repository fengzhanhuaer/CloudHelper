//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	probeVirtualRouterLinuxDockerHostDNSEnv   = "PROBE_NODE_DOCKER_HOST_DNS"
	probeVirtualRouterLinuxHostDBusSocketEnv  = "PROBE_NODE_HOST_DBUS_SOCKET"
	probeVirtualRouterLinuxHostResolvConfEnv  = "PROBE_NODE_HOST_RESOLV_CONF"
	probeVirtualRouterLinuxDefaultDBusSocket  = "/host/run/dbus/system_bus_socket"
	probeVirtualRouterLinuxResolvedService    = "org.freedesktop.resolve1"
	probeVirtualRouterLinuxResolvedObjectPath = dbus.ObjectPath("/org/freedesktop/resolve1")
	probeVirtualRouterLinuxResolvedManager    = "org.freedesktop.resolve1.Manager"
	probeVirtualRouterLinuxDBusProperties     = "org.freedesktop.DBus.Properties"
)

type probeVirtualRouterLinuxResolvedDNS struct {
	Family  int32
	Address []byte
}

type probeVirtualRouterLinuxResolvedDNSRecord struct {
	InterfaceIndex int32
	Family         int32
	Address        []byte
}

type probeVirtualRouterLinuxResolvedDomain struct {
	Name      string
	RouteOnly bool
}

var (
	probeVirtualRouterLinuxResolvedDBusAvailable = defaultProbeVirtualRouterLinuxResolvedDBusAvailable
	probeVirtualRouterLinuxResolvedDBusCommand   = defaultProbeVirtualRouterLinuxResolvedDBusCommand
)

func probeVirtualRouterLinuxDockerHostDNSEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(probeVirtualRouterLinuxDockerHostDNSEnv))) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}

func currentProbeVirtualRouterLinuxResolvConfPath() string {
	if probeVirtualRouterLinuxDockerHostDNSEnabled() {
		if path := strings.TrimSpace(os.Getenv(probeVirtualRouterLinuxHostResolvConfEnv)); filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
	}
	return probeVirtualRouterLinuxResolvConfPath
}

func probeVirtualRouterLinuxDNSCommandLookPath(file string) (string, error) {
	if probeVirtualRouterLinuxDockerHostDNSEnabled() && strings.EqualFold(strings.TrimSpace(file), "resolvectl") {
		if err := probeVirtualRouterLinuxResolvedDBusAvailable(); err != nil {
			return "", err
		}
		return "dbus:" + currentProbeVirtualRouterLinuxHostDBusSocket(), nil
	}
	return exec.LookPath(file)
}

func runProbeVirtualRouterLinuxDNSCommand(timeout time.Duration, name string, args ...string) (string, error) {
	if probeVirtualRouterLinuxDockerHostDNSEnabled() && strings.EqualFold(strings.TrimSpace(name), "resolvectl") {
		return probeVirtualRouterLinuxResolvedDBusCommand(timeout, args...)
	}
	return runProbeLocalCommand(timeout, name, args...)
}

func currentProbeVirtualRouterLinuxHostDBusSocket() string {
	path := strings.TrimSpace(os.Getenv(probeVirtualRouterLinuxHostDBusSocketEnv))
	if !filepath.IsAbs(path) {
		path = probeVirtualRouterLinuxDefaultDBusSocket
	}
	return filepath.Clean(path)
}

func defaultProbeVirtualRouterLinuxResolvedDBusAvailable() error {
	conn, err := openProbeVirtualRouterLinuxResolvedDBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var mode dbus.Variant
	call := conn.Object(probeVirtualRouterLinuxResolvedService, probeVirtualRouterLinuxResolvedObjectPath).CallWithContext(
		ctx,
		probeVirtualRouterLinuxDBusProperties+".Get",
		0,
		probeVirtualRouterLinuxResolvedManager,
		"ResolvConfMode",
	)
	if err := call.Store(&mode); err != nil {
		return fmt.Errorf("query host systemd-resolved: %w", err)
	}
	return nil
}

func defaultProbeVirtualRouterLinuxResolvedDBusCommand(timeout time.Duration, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	conn, err := openProbeVirtualRouterLinuxResolvedDBus()
	if err != nil {
		return "", err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	manager := conn.Object(probeVirtualRouterLinuxResolvedService, probeVirtualRouterLinuxResolvedObjectPath)
	if len(args) == 0 {
		return "", errors.New("empty resolvectl command")
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "dns":
		if len(args) < 2 {
			return "", errors.New("resolvectl dns interface is required")
		}
		ifIndex, err := probeVirtualRouterLinuxResolvedInterfaceIndex(args[1])
		if err != nil {
			return "", err
		}
		if len(args) == 2 {
			servers, err := probeVirtualRouterLinuxResolvedLinkDNSServers(ctx, manager, ifIndex)
			return strings.Join(servers, " "), err
		}
		ip4 := net.ParseIP(strings.TrimSpace(args[2])).To4()
		if ip4 == nil {
			return "", fmt.Errorf("invalid resolved dns server: %s", strings.TrimSpace(args[2]))
		}
		items := []probeVirtualRouterLinuxResolvedDNS{{Family: syscall.AF_INET, Address: []byte(ip4)}}
		if err := manager.CallWithContext(ctx, probeVirtualRouterLinuxResolvedManager+".SetLinkDNS", 0, ifIndex, items).Err; err != nil {
			return "", err
		}
		return "", nil
	case "domain":
		if len(args) < 3 {
			return "", errors.New("resolvectl domain interface and domain are required")
		}
		ifIndex, err := probeVirtualRouterLinuxResolvedInterfaceIndex(args[1])
		if err != nil {
			return "", err
		}
		domain := strings.TrimSpace(args[2])
		routeOnly := strings.HasPrefix(domain, "~")
		domain = strings.TrimPrefix(domain, "~")
		if domain == "" {
			return "", errors.New("resolved domain is empty")
		}
		items := []probeVirtualRouterLinuxResolvedDomain{{Name: domain, RouteOnly: routeOnly}}
		if err := manager.CallWithContext(ctx, probeVirtualRouterLinuxResolvedManager+".SetLinkDomains", 0, ifIndex, items).Err; err != nil {
			return "", err
		}
		return "", nil
	case "revert":
		if len(args) < 2 {
			return "", errors.New("resolvectl revert interface is required")
		}
		ifIndex, err := probeVirtualRouterLinuxResolvedInterfaceIndex(args[1])
		if err != nil {
			return "", err
		}
		return "", manager.CallWithContext(ctx, probeVirtualRouterLinuxResolvedManager+".RevertLink", 0, ifIndex).Err
	case "flush-caches":
		return "", manager.CallWithContext(ctx, probeVirtualRouterLinuxResolvedManager+".FlushCaches", 0).Err
	default:
		return "", fmt.Errorf("unsupported resolvectl command: %s", strings.TrimSpace(args[0]))
	}
}

func openProbeVirtualRouterLinuxResolvedDBus() (*dbus.Conn, error) {
	socketPath := currentProbeVirtualRouterLinuxHostDBusSocket()
	if info, err := os.Stat(socketPath); err != nil || info.IsDir() {
		if err == nil {
			err = errors.New("path is a directory")
		}
		return nil, fmt.Errorf("host system dbus socket unavailable: %w", err)
	}
	conn, err := dbus.Dial("unix:path=" + socketPath)
	if err != nil {
		return nil, err
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func probeVirtualRouterLinuxResolvedInterfaceIndex(name string) (int32, error) {
	dev := strings.TrimSpace(name)
	if dev == "" {
		return 0, errors.New("resolved interface is empty")
	}
	iface, err := net.InterfaceByName(dev)
	if err != nil {
		return 0, err
	}
	if iface.Index <= 0 {
		return 0, fmt.Errorf("resolved interface index is invalid: %s", dev)
	}
	return int32(iface.Index), nil
}

func probeVirtualRouterLinuxResolvedLinkDNSServers(ctx context.Context, manager dbus.BusObject, ifIndex int32) ([]string, error) {
	var value dbus.Variant
	call := manager.CallWithContext(
		ctx,
		probeVirtualRouterLinuxDBusProperties+".Get",
		0,
		probeVirtualRouterLinuxResolvedManager,
		"DNS",
	)
	if err := call.Store(&value); err != nil {
		return nil, err
	}
	var records []probeVirtualRouterLinuxResolvedDNSRecord
	if err := value.Store(&records); err != nil {
		return nil, err
	}
	return probeVirtualRouterLinuxResolvedDNSRecordsToServers(records, ifIndex), nil
}

func probeVirtualRouterLinuxResolvedDNSRecordsToServers(records []probeVirtualRouterLinuxResolvedDNSRecord, ifIndex int32) []string {
	servers := make([]string, 0, len(records))
	for _, record := range records {
		if (record.InterfaceIndex != 0 && record.InterfaceIndex != ifIndex) || record.Family != syscall.AF_INET || len(record.Address) != net.IPv4len {
			continue
		}
		servers = append(servers, net.IP(record.Address).String())
	}
	return filterProbeVirtualRouterLinuxDNSUpstreams(servers)
}
