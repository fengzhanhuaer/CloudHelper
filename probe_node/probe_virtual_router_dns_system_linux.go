//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	probeVirtualRouterLinuxDNSBackupFileName = "virtual_router_dns_backup.json"
	probeVirtualRouterLinuxDNSModeResolved   = "systemd_resolved"
	probeVirtualRouterLinuxDNSModeResolvConf = "resolv_conf"
	probeVirtualRouterLinuxResolvConfPath    = "/etc/resolv.conf"
)

type probeVirtualRouterLinuxDNSBackup struct {
	Mode             string   `json:"mode"`
	Interface        string   `json:"interface,omitempty"`
	UpstreamServers  []string `json:"upstream_servers,omitempty"`
	ResolvConfPath   string   `json:"resolv_conf_path,omitempty"`
	ResolvConf       []byte   `json:"resolv_conf,omitempty"`
	ResolvConfMode   uint32   `json:"resolv_conf_mode,omitempty"`
	AppliedDNSServer string   `json:"applied_dns_server"`
	UpdatedAt        string   `json:"updated_at"`
}

var (
	probeVirtualRouterLinuxDNSLookPath  = probeVirtualRouterLinuxDNSCommandLookPath
	probeVirtualRouterLinuxDNSRun       = runProbeVirtualRouterLinuxDNSCommand
	probeVirtualRouterLinuxDNSReadFile  = os.ReadFile
	probeVirtualRouterLinuxDNSWriteFile = func(path string, data []byte, mode os.FileMode) error {
		return os.WriteFile(path, data, mode)
	}
	probeVirtualRouterLinuxDNSStat     = os.Stat
	probeVirtualRouterLinuxDNSReadlink = os.Readlink
)

func applyProbeVirtualRouterSystemDNS() error {
	backup, ok := loadProbeVirtualRouterLinuxDNSBackupBestEffort()
	if ok {
		var err error
		backup, err = migrateProbeVirtualRouterLinuxDNSBackupForRuntime(backup)
		if err != nil {
			return err
		}
		return ensureProbeVirtualRouterLinuxDNSApplied(backup)
	}

	dnsLink := probeRouteLinuxTUNDeviceName()
	target, err := resolveProbeRouteLinuxSelectedEgressRoute(probeRouteLinuxTUNDeviceName())
	if err != nil {
		return fmt.Errorf("resolve linux dns egress interface: %w", err)
	}
	if strings.TrimSpace(target.Dev) == "" {
		return errors.New("linux dns egress interface is empty")
	}

	if probeVirtualRouterLinuxResolvedAvailable(dnsLink) {
		backup = probeVirtualRouterLinuxDNSBackup{
			Mode:             probeVirtualRouterLinuxDNSModeResolved,
			Interface:        dnsLink,
			UpstreamServers:  currentProbeVirtualRouterLinuxDNSUpstreams(target.Dev),
			AppliedDNSServer: probeVirtualRouterDNSListenHost,
		}
		if err := persistProbeVirtualRouterLinuxDNSBackup(backup); err != nil {
			return err
		}
		if err := ensureProbeVirtualRouterLinuxDNSApplied(backup); err != nil {
			_ = removeProbeVirtualRouterLinuxDNSBackup()
			return err
		}
		return nil
	}

	path := currentProbeVirtualRouterLinuxResolvConfPath()
	raw, err := probeVirtualRouterLinuxDNSReadFile(path)
	if err != nil {
		return fmt.Errorf("read linux resolv.conf: %w", err)
	}
	mode := os.FileMode(0o644)
	if info, statErr := probeVirtualRouterLinuxDNSStat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	backup = probeVirtualRouterLinuxDNSBackup{
		Mode:             probeVirtualRouterLinuxDNSModeResolvConf,
		UpstreamServers:  parseProbeVirtualRouterLinuxDNSServers(raw),
		ResolvConfPath:   path,
		ResolvConf:       append([]byte(nil), raw...),
		ResolvConfMode:   uint32(mode.Perm()),
		AppliedDNSServer: probeVirtualRouterDNSListenHost,
	}
	if err := persistProbeVirtualRouterLinuxDNSBackup(backup); err != nil {
		return err
	}
	if err := ensureProbeVirtualRouterLinuxDNSApplied(backup); err != nil {
		// Keep the original file backup even if a write was partially applied.
		return err
	}
	return nil
}

func migrateProbeVirtualRouterLinuxDNSBackupForRuntime(backup probeVirtualRouterLinuxDNSBackup) (probeVirtualRouterLinuxDNSBackup, error) {
	changed := false
	desired := strings.TrimSpace(probeVirtualRouterDNSListenHost)
	if strings.TrimSpace(backup.AppliedDNSServer) != desired {
		backup.AppliedDNSServer = desired
		changed = true
	}

	switch backup.Mode {
	case probeVirtualRouterLinuxDNSModeResolved:
		dnsLink := strings.TrimSpace(probeRouteLinuxTUNDeviceName())
		if dnsLink == "" {
			return backup, errors.New("linux dns tun interface is empty")
		}
		if !strings.EqualFold(strings.TrimSpace(backup.Interface), dnsLink) {
			backup.Interface = dnsLink
			changed = true
		}
	case probeVirtualRouterLinuxDNSModeResolvConf:
		currentPath := currentProbeVirtualRouterLinuxResolvConfPath()
		backupPath := strings.TrimSpace(backup.ResolvConfPath)
		if backupPath == "" || filepath.Clean(backupPath) != filepath.Clean(currentPath) {
			raw, err := probeVirtualRouterLinuxDNSReadFile(currentPath)
			if err != nil {
				return backup, fmt.Errorf("read current linux resolv.conf during migration: %w", err)
			}
			mode := os.FileMode(0o644)
			if info, statErr := probeVirtualRouterLinuxDNSStat(currentPath); statErr == nil {
				mode = info.Mode().Perm()
			}
			backup.ResolvConfPath = currentPath
			backup.ResolvConf = append([]byte(nil), raw...)
			backup.ResolvConfMode = uint32(mode.Perm())
			backup.UpstreamServers = parseProbeVirtualRouterLinuxDNSServers(raw)
			changed = true
		}
	}

	if changed {
		if err := persistProbeVirtualRouterLinuxDNSBackup(backup); err != nil {
			return backup, err
		}
	}
	return backup, nil
}

func restoreProbeVirtualRouterSystemDNS() error {
	backup, ok := loadProbeVirtualRouterLinuxDNSBackupBestEffort()
	if !ok {
		return nil
	}
	var err error
	switch backup.Mode {
	case probeVirtualRouterLinuxDNSModeResolved:
		dev := strings.TrimSpace(backup.Interface)
		if dev == "" {
			return errors.New("linux dns backup interface is empty")
		}
		_, err = probeVirtualRouterLinuxDNSRun(5*time.Second, "resolvectl", "revert", dev)
		if err == nil {
			_, _ = probeVirtualRouterLinuxDNSRun(5*time.Second, "resolvectl", "flush-caches")
		}
	case probeVirtualRouterLinuxDNSModeResolvConf:
		path := firstNonEmpty(strings.TrimSpace(backup.ResolvConfPath), currentProbeVirtualRouterLinuxResolvConfPath())
		mode := os.FileMode(backup.ResolvConfMode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		err = probeVirtualRouterLinuxDNSWriteFile(path, backup.ResolvConf, mode)
	default:
		return fmt.Errorf("unsupported linux dns backup mode: %s", strings.TrimSpace(backup.Mode))
	}
	if err != nil {
		return fmt.Errorf("restore linux system dns: %w", err)
	}
	return removeProbeVirtualRouterLinuxDNSBackup()
}

func ensureProbeVirtualRouterLinuxDNSApplied(backup probeVirtualRouterLinuxDNSBackup) error {
	desired := firstNonEmpty(strings.TrimSpace(backup.AppliedDNSServer), probeVirtualRouterDNSListenHost)
	switch backup.Mode {
	case probeVirtualRouterLinuxDNSModeResolved:
		dev := strings.TrimSpace(backup.Interface)
		if dev == "" {
			return errors.New("linux dns backup interface is empty")
		}
		if _, err := probeVirtualRouterLinuxDNSRun(5*time.Second, "resolvectl", "dns", dev, desired); err != nil {
			return fmt.Errorf("set systemd-resolved dns for %s: %w", dev, err)
		}
		if _, err := probeVirtualRouterLinuxDNSRun(5*time.Second, "resolvectl", "domain", dev, "~."); err != nil {
			_, _ = probeVirtualRouterLinuxDNSRun(5*time.Second, "resolvectl", "revert", dev)
			return fmt.Errorf("set systemd-resolved default dns domain for %s: %w", dev, err)
		}
		_, _ = probeVirtualRouterLinuxDNSRun(5*time.Second, "resolvectl", "flush-caches")
		return nil
	case probeVirtualRouterLinuxDNSModeResolvConf:
		path := firstNonEmpty(strings.TrimSpace(backup.ResolvConfPath), currentProbeVirtualRouterLinuxResolvConfPath())
		mode := os.FileMode(backup.ResolvConfMode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		current, err := probeVirtualRouterLinuxDNSReadFile(path)
		if err == nil && sameProbeVirtualRouterLinuxDNSServers(parseProbeVirtualRouterLinuxDNSServers(current), []string{desired}) {
			return nil
		}
		managed := buildProbeVirtualRouterLinuxManagedResolvConf(backup.ResolvConf, desired)
		if err := probeVirtualRouterLinuxDNSWriteFile(path, managed, mode); err != nil {
			return fmt.Errorf("write linux resolv.conf: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported linux dns backup mode: %s", strings.TrimSpace(backup.Mode))
	}
}

func probeVirtualRouterLinuxResolvedAvailable(dev string) bool {
	if strings.TrimSpace(dev) == "" {
		return false
	}
	if _, err := probeVirtualRouterLinuxDNSLookPath("resolvectl"); err != nil {
		return false
	}
	if !probeVirtualRouterLinuxSystemResolverUsesResolved() {
		return false
	}
	_, err := probeVirtualRouterLinuxDNSRun(5*time.Second, "resolvectl", "dns", strings.TrimSpace(dev))
	return err == nil
}

func probeVirtualRouterLinuxSystemResolverUsesResolved() bool {
	if probeVirtualRouterLinuxDockerHostDNSEnabled() {
		return probeVirtualRouterLinuxResolvedDBusAvailable() == nil
	}
	resolvConfPath := currentProbeVirtualRouterLinuxResolvConfPath()
	if target, err := probeVirtualRouterLinuxDNSReadlink(resolvConfPath); err == nil {
		cleanTarget := strings.ToLower(strings.TrimSpace(target))
		if strings.Contains(cleanTarget, "systemd/resolve") {
			return true
		}
	}
	if raw, err := probeVirtualRouterLinuxDNSReadFile(resolvConfPath); err == nil {
		for _, server := range parseProbeVirtualRouterLinuxDNSServers(raw) {
			if server == "127.0.0.53" || server == "127.0.0.54" {
				return true
			}
		}
	}
	return false
}

func currentProbeLocalSystemDNSServers() []string {
	if backup, ok := loadProbeVirtualRouterLinuxDNSBackupBestEffort(); ok {
		return filterProbeVirtualRouterLinuxDNSUpstreams(backup.UpstreamServers)
	}
	dev := ""
	if target, err := resolveProbeRouteLinuxSelectedEgressRoute(probeRouteLinuxTUNDeviceName()); err == nil {
		dev = target.Dev
	}
	return currentProbeVirtualRouterLinuxDNSUpstreams(dev)
}

func currentProbeVirtualRouterLinuxDNSUpstreams(dev string) []string {
	resolvConfPath := currentProbeVirtualRouterLinuxResolvConfPath()
	paths := []string{resolvConfPath}
	if probeVirtualRouterLinuxSystemResolverUsesResolved() {
		if probeVirtualRouterLinuxDockerHostDNSEnabled() && strings.TrimSpace(dev) != "" {
			if output, err := probeVirtualRouterLinuxDNSRun(5*time.Second, "resolvectl", "dns", strings.TrimSpace(dev)); err == nil {
				if servers := filterProbeVirtualRouterLinuxDNSUpstreams(parseProbeVirtualRouterLinuxDNSWords(output)); len(servers) > 0 {
					return servers
				}
			}
		}
		paths = []string{"/run/systemd/resolve/resolv.conf", resolvConfPath}
	}
	for _, path := range paths {
		if raw, err := probeVirtualRouterLinuxDNSReadFile(path); err == nil {
			if servers := filterProbeVirtualRouterLinuxDNSUpstreams(parseProbeVirtualRouterLinuxDNSServers(raw)); len(servers) > 0 {
				return servers
			}
		}
	}
	if strings.TrimSpace(dev) != "" {
		if output, err := probeVirtualRouterLinuxDNSRun(5*time.Second, "resolvectl", "dns", strings.TrimSpace(dev)); err == nil {
			return filterProbeVirtualRouterLinuxDNSUpstreams(parseProbeVirtualRouterLinuxDNSWords(output))
		}
	}
	return nil
}

func parseProbeVirtualRouterLinuxDNSServers(raw []byte) []string {
	servers := make([]string, 0)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || !strings.EqualFold(fields[0], "nameserver") {
			continue
		}
		servers = append(servers, fields[1])
	}
	return filterProbeVirtualRouterLinuxDNSUpstreamsAllowLoopback(servers)
}

func parseProbeVirtualRouterLinuxDNSWords(raw string) []string {
	servers := make([]string, 0)
	for _, field := range strings.Fields(raw) {
		field = strings.Trim(strings.TrimSpace(field), "[](),")
		if ip := net.ParseIP(field); ip != nil && ip.To4() != nil {
			servers = append(servers, ip.To4().String())
		}
	}
	return filterProbeVirtualRouterLinuxDNSUpstreams(servers)
}

func filterProbeVirtualRouterLinuxDNSUpstreams(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	desired := net.ParseIP(strings.TrimSpace(probeVirtualRouterDNSListenHost)).To4()
	for _, raw := range items {
		ip4 := net.ParseIP(strings.TrimSpace(raw)).To4()
		if ip4 == nil || ip4[0] == 127 || (desired != nil && ip4.Equal(desired)) {
			continue
		}
		value := ip4.String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func sameProbeVirtualRouterLinuxDNSServers(left, right []string) bool {
	a := filterProbeVirtualRouterLinuxDNSUpstreamsAllowLoopback(left)
	b := filterProbeVirtualRouterLinuxDNSUpstreamsAllowLoopback(right)
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func filterProbeVirtualRouterLinuxDNSUpstreamsAllowLoopback(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, raw := range items {
		ip4 := net.ParseIP(strings.TrimSpace(raw)).To4()
		if ip4 == nil {
			continue
		}
		value := ip4.String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func buildProbeVirtualRouterLinuxManagedResolvConf(original []byte, desired string) []byte {
	lines := []string{"# Managed by CloudHelper while virtual DNS is enabled", "nameserver " + strings.TrimSpace(desired)}
	for _, line := range strings.Split(strings.ReplaceAll(string(original), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(strings.ToLower(trimmed), "nameserver ") || strings.EqualFold(trimmed, "# Managed by CloudHelper while virtual DNS is enabled") {
			continue
		}
		lines = append(lines, line)
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func resolveProbeVirtualRouterLinuxDNSBackupPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeVirtualRouterLinuxDNSBackupFileName), nil
}

func loadProbeVirtualRouterLinuxDNSBackupBestEffort() (probeVirtualRouterLinuxDNSBackup, bool) {
	path, err := resolveProbeVirtualRouterLinuxDNSBackupPath()
	if err != nil {
		return probeVirtualRouterLinuxDNSBackup{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return probeVirtualRouterLinuxDNSBackup{}, false
	}
	var backup probeVirtualRouterLinuxDNSBackup
	if err := json.Unmarshal(raw, &backup); err != nil {
		return probeVirtualRouterLinuxDNSBackup{}, false
	}
	backup.Mode = strings.TrimSpace(backup.Mode)
	backup.Interface = strings.TrimSpace(backup.Interface)
	backup.UpstreamServers = filterProbeVirtualRouterLinuxDNSUpstreams(backup.UpstreamServers)
	return backup, backup.Mode != ""
}

func persistProbeVirtualRouterLinuxDNSBackup(backup probeVirtualRouterLinuxDNSBackup) error {
	backup.Mode = strings.TrimSpace(backup.Mode)
	backup.Interface = strings.TrimSpace(backup.Interface)
	backup.UpstreamServers = filterProbeVirtualRouterLinuxDNSUpstreams(backup.UpstreamServers)
	backup.AppliedDNSServer = firstNonEmpty(strings.TrimSpace(backup.AppliedDNSServer), probeVirtualRouterDNSListenHost)
	backup.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	path, err := resolveProbeVirtualRouterLinuxDNSBackupPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func removeProbeVirtualRouterLinuxDNSBackup() error {
	path, err := resolveProbeVirtualRouterLinuxDNSBackupPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
