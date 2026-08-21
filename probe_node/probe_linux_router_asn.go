//go:build linux_router

package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

const (
	probeLinuxRouterASNStatInterval = time.Minute
)

var probeLinuxRouterASNDatabaseState = struct {
	sync.RWMutex
	reader     *maxminddb.Reader
	path       string
	size       int64
	modifiedAt time.Time
	nextStatAt time.Time
}{}

var probeLinuxRouterASNForIP = lookupProbeLinuxRouterASN

func probeLinuxRouterVirtualRouterConfigApplied(config probeVirtualRouterConfig) {
	if !probeVirtualRouterConfigHasASN(config) {
		return
	}
	go func() {
		if err := ensureProbeMihomoASNDatabaseCache(); err != nil {
			logProbeWarnf("probe linux router ASN cache refresh failed: %v", err)
		}
		if ensureProbeLinuxRouterASNDatabase() {
			cleanupProbeRouteDirectBypassForVirtualRouterRules(config)
		}
	}()
}

func probeVirtualRouterConfigHasASN(config probeVirtualRouterConfig) bool {
	for _, rule := range config.RouteRules {
		for _, entry := range rule.Entries {
			kind, value, ok := strings.Cut(strings.TrimSpace(entry), ":")
			if ok && strings.EqualFold(strings.TrimSpace(kind), "asn") && strings.TrimSpace(value) != "" {
				return true
			}
		}
	}
	return false
}

func probeLinuxRouterRouteRuleEntryMatchesIP(ip net.IP, entry string) bool {
	kind, value, ok := strings.Cut(strings.TrimSpace(entry), ":")
	if !ok || !strings.EqualFold(strings.TrimSpace(kind), "asn") {
		return false
	}
	expected, err := strconv.ParseUint(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(value)), "AS"), 10, 32)
	if err != nil || expected == 0 {
		return false
	}
	actual, ok := probeLinuxRouterASNForIP(ip)
	return ok && uint64(actual) == expected
}

func lookupProbeLinuxRouterASN(ip net.IP) (uint, bool) {
	ip = ip.To4()
	if ip == nil {
		return 0, false
	}
	if !ensureProbeLinuxRouterASNDatabase() {
		return 0, false
	}
	var record struct {
		AutonomousSystemNumber uint `maxminddb:"autonomous_system_number"`
	}
	probeLinuxRouterASNDatabaseState.RLock()
	reader := probeLinuxRouterASNDatabaseState.reader
	if reader == nil || reader.Lookup(ip, &record) != nil {
		probeLinuxRouterASNDatabaseState.RUnlock()
		return 0, false
	}
	probeLinuxRouterASNDatabaseState.RUnlock()
	return record.AutonomousSystemNumber, record.AutonomousSystemNumber != 0
}

func ensureProbeLinuxRouterASNDatabase() bool {
	now := time.Now()
	probeLinuxRouterASNDatabaseState.RLock()
	ready := probeLinuxRouterASNDatabaseState.reader != nil
	if now.Before(probeLinuxRouterASNDatabaseState.nextStatAt) {
		probeLinuxRouterASNDatabaseState.RUnlock()
		return ready
	}
	probeLinuxRouterASNDatabaseState.RUnlock()

	probeLinuxRouterASNDatabaseState.Lock()
	defer probeLinuxRouterASNDatabaseState.Unlock()
	if now.Before(probeLinuxRouterASNDatabaseState.nextStatAt) {
		return probeLinuxRouterASNDatabaseState.reader != nil
	}
	probeLinuxRouterASNDatabaseState.nextStatAt = now.Add(probeLinuxRouterASNStatInterval)
	dataDir, err := resolveDataDir()
	if err != nil {
		return probeLinuxRouterASNDatabaseState.reader != nil
	}
	path := filepath.Join(dataDir, probeMihomoASNDatabaseFileName)
	info, err := os.Stat(path)
	if err != nil {
		return probeLinuxRouterASNDatabaseState.reader != nil
	}
	if probeLinuxRouterASNDatabaseState.reader != nil && probeLinuxRouterASNDatabaseState.path == path && probeLinuxRouterASNDatabaseState.size == info.Size() && probeLinuxRouterASNDatabaseState.modifiedAt.Equal(info.ModTime()) {
		return true
	}
	reader, err := maxminddb.Open(path)
	if err != nil || reader.Verify() != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return probeLinuxRouterASNDatabaseState.reader != nil
	}
	previous := probeLinuxRouterASNDatabaseState.reader
	probeLinuxRouterASNDatabaseState.reader = reader
	probeLinuxRouterASNDatabaseState.path = path
	probeLinuxRouterASNDatabaseState.size = info.Size()
	probeLinuxRouterASNDatabaseState.modifiedAt = info.ModTime()
	if previous != nil {
		_ = previous.Close()
	}
	return true
}

func activateProbeLinuxRouterASNDatabase(candidatePath, databasePath string) error {
	reader, err := maxminddb.Open(candidatePath)
	if err != nil {
		return err
	}
	if err = reader.Verify(); err == nil && !strings.Contains(strings.ToUpper(reader.Metadata.DatabaseType), "ASN") {
		err = os.ErrInvalid
	}
	closeErr := reader.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(candidatePath, databasePath); err != nil {
		return err
	}
	probeLinuxRouterASNDatabaseState.Lock()
	probeLinuxRouterASNDatabaseState.nextStatAt = time.Time{}
	probeLinuxRouterASNDatabaseState.Unlock()
	return nil
}

func closeProbeLinuxRouterASNDatabase() {
	probeLinuxRouterASNDatabaseState.Lock()
	defer probeLinuxRouterASNDatabaseState.Unlock()
	if probeLinuxRouterASNDatabaseState.reader != nil {
		_ = probeLinuxRouterASNDatabaseState.reader.Close()
	}
	probeLinuxRouterASNDatabaseState.reader = nil
	probeLinuxRouterASNDatabaseState.path = ""
	probeLinuxRouterASNDatabaseState.size = 0
	probeLinuxRouterASNDatabaseState.modifiedAt = time.Time{}
	probeLinuxRouterASNDatabaseState.nextStatAt = time.Time{}
}
