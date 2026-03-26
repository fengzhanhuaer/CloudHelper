package backend

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	probeDNSCacheFileName   = "probe_dns_cache.json"
	probeDNSCacheTTL        = 24 * time.Hour
	probeDNSCacheMaxEntries = 51200
)

type probeDNSCacheRecord struct {
	Host      string `json:"host"`
	IP        string `json:"ip"`
	UpdatedAt string `json:"updated_at"`
}

type probeDNSCacheFilePayload struct {
	Entries []probeDNSCacheRecord `json:"entries"`
}

type probeDNSCacheEntry struct {
	IP        string
	UpdatedAt time.Time
}

var probeDNSCacheState = struct {
	mu      sync.Mutex
	loaded  bool
	path    string
	entries map[string]probeDNSCacheEntry
}{
	entries: make(map[string]probeDNSCacheEntry),
}

func normalizeProbeDNSCacheHost(rawHost string) string {
	host := strings.TrimSpace(strings.Trim(rawHost, "[]"))
	if host == "" {
		return ""
	}
	if parsed := net.ParseIP(host); parsed != nil {
		return ""
	}
	if strings.Contains(host, " ") {
		return ""
	}
	return strings.ToLower(host)
}

func normalizeProbeDNSCacheIP(rawIP string) string {
	parsed := net.ParseIP(strings.TrimSpace(rawIP))
	if parsed == nil {
		return ""
	}
	if ipv4 := parsed.To4(); ipv4 != nil {
		return ipv4.String()
	}
	return parsed.String()
}

func loadProbeDNSCacheFromFileLocked() error {
	if probeDNSCacheState.loaded {
		return nil
	}
	probeDNSCacheState.loaded = true
	probeDNSCacheState.entries = make(map[string]probeDNSCacheEntry)

	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return err
	}
	probeDNSCacheState.path = filepath.Join(dataDir, probeDNSCacheFileName)

	raw, err := os.ReadFile(probeDNSCacheState.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}

	var payload probeDNSCacheFilePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	now := time.Now()
	for _, item := range payload.Entries {
		host := normalizeProbeDNSCacheHost(item.Host)
		ipValue := normalizeProbeDNSCacheIP(item.IP)
		if host == "" || ipValue == "" {
			continue
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.UpdatedAt))
		if err != nil {
			continue
		}
		if now.Sub(updatedAt) > probeDNSCacheTTL {
			continue
		}
		probeDNSCacheState.entries[host] = probeDNSCacheEntry{IP: ipValue, UpdatedAt: updatedAt}
	}
	pruneProbeDNSCacheLocked(now)
	return nil
}

func pruneProbeDNSCacheLocked(now time.Time) {
	for host, entry := range probeDNSCacheState.entries {
		if now.Sub(entry.UpdatedAt) > probeDNSCacheTTL {
			delete(probeDNSCacheState.entries, host)
		}
	}
	if len(probeDNSCacheState.entries) <= probeDNSCacheMaxEntries {
		return
	}
	type sortableItem struct {
		Host      string
		UpdatedAt time.Time
	}
	items := make([]sortableItem, 0, len(probeDNSCacheState.entries))
	for host, entry := range probeDNSCacheState.entries {
		items = append(items, sortableItem{Host: host, UpdatedAt: entry.UpdatedAt})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	keep := make(map[string]struct{}, probeDNSCacheMaxEntries)
	for i := 0; i < len(items) && i < probeDNSCacheMaxEntries; i++ {
		keep[items[i].Host] = struct{}{}
	}
	for host := range probeDNSCacheState.entries {
		if _, ok := keep[host]; ok {
			continue
		}
		delete(probeDNSCacheState.entries, host)
	}
}

func persistProbeDNSCacheLocked() error {
	if strings.TrimSpace(probeDNSCacheState.path) == "" {
		return nil
	}
	now := time.Now()
	pruneProbeDNSCacheLocked(now)
	items := make([]probeDNSCacheRecord, 0, len(probeDNSCacheState.entries))
	for host, entry := range probeDNSCacheState.entries {
		items = append(items, probeDNSCacheRecord{
			Host:      host,
			IP:        entry.IP,
			UpdatedAt: entry.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.Compare(items[i].Host, items[j].Host) < 0
	})
	payload := probeDNSCacheFilePayload{Entries: items}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(probeDNSCacheState.path, raw, 0o644)
}

func getProbeDNSCachedIP(host string) (string, bool) {
	normalizedHost := normalizeProbeDNSCacheHost(host)
	if normalizedHost == "" {
		return "", false
	}
	probeDNSCacheState.mu.Lock()
	defer probeDNSCacheState.mu.Unlock()
	if err := loadProbeDNSCacheFromFileLocked(); err != nil {
		return "", false
	}
	entry, ok := probeDNSCacheState.entries[normalizedHost]
	if !ok {
		return "", false
	}
	if time.Since(entry.UpdatedAt) > probeDNSCacheTTL {
		delete(probeDNSCacheState.entries, normalizedHost)
		_ = persistProbeDNSCacheLocked()
		return "", false
	}
	return entry.IP, true
}

func setProbeDNSCachedIP(host string, ipValue string) error {
	normalizedHost := normalizeProbeDNSCacheHost(host)
	normalizedIP := normalizeProbeDNSCacheIP(ipValue)
	if normalizedHost == "" || normalizedIP == "" {
		return nil
	}
	now := time.Now()
	probeDNSCacheState.mu.Lock()
	defer probeDNSCacheState.mu.Unlock()
	if err := loadProbeDNSCacheFromFileLocked(); err != nil {
		return err
	}
	if existing, ok := probeDNSCacheState.entries[normalizedHost]; ok {
		if existing.IP == normalizedIP && now.Sub(existing.UpdatedAt) < 10*time.Minute {
			return nil
		}
	}
	probeDNSCacheState.entries[normalizedHost] = probeDNSCacheEntry{IP: normalizedIP, UpdatedAt: now}
	return persistProbeDNSCacheLocked()
}

func clearProbeDNSCacheFile() error {
	probeDNSCacheState.mu.Lock()
	defer probeDNSCacheState.mu.Unlock()
	if err := loadProbeDNSCacheFromFileLocked(); err != nil {
		return err
	}
	probeDNSCacheState.entries = make(map[string]probeDNSCacheEntry)
	if strings.TrimSpace(probeDNSCacheState.path) == "" {
		return nil
	}
	err := os.Remove(probeDNSCacheState.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
