package backend

import (
	"encoding/gob"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	dnsBidirectionalCacheFileName = "network_assistant_dns_bimap_cache.gob"
	dnsBidirectionalCacheTTL      = 30 * 24 * time.Hour
	dnsBidirectionalCacheMaxItems = 200000

	dnsBidirectionalFailureThreshold = 3
	dnsBidirectionalFailureWindow    = 10 * time.Minute
)

type dnsBiMapEntry struct {
	Domain    string
	IP        string
	Group     string
	NodeID    string
	ExpiresAt time.Time
	UpdatedAt time.Time
}

type dnsBiMapPersistPayload struct {
	Version int
	Entries []dnsBiMapEntry
}

type dnsBiMapFailureState struct {
	Count         int
	LastFailureAt time.Time
}

var dnsBiMapCache = struct {
	mu        sync.Mutex
	loaded    bool
	path      string
	entries   map[string]dnsBiMapEntry
	domainIPs map[string]map[string]struct{}
	ipDomains map[string]map[string]struct{}
	failures  map[string]dnsBiMapFailureState
}{
	entries:   make(map[string]dnsBiMapEntry),
	domainIPs: make(map[string]map[string]struct{}),
	ipDomains: make(map[string]map[string]struct{}),
	failures:  make(map[string]dnsBiMapFailureState),
}

func normalizeDNSBiMapGroup(group string) string {
	return strings.TrimSpace(strings.ToLower(group))
}

func dnsBiMapKey(domain, ip string) string {
	return domain + "|" + ip
}

func isDNSBiMapRouteCacheable(route tunnelRouteDecision) bool {
	if route.Direct || route.BypassTUN {
		return false
	}
	group := normalizeDNSBiMapGroup(route.Group)
	if group == "" || isDirectRuleGroupKey(group) {
		return false
	}
	return true
}

func ensureDNSBiMapPathLocked() (string, error) {
	if strings.TrimSpace(dnsBiMapCache.path) != "" {
		return dnsBiMapCache.path, nil
	}
	dataDir, err := ensureManagerDataDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, dnsBidirectionalCacheFileName)
	dnsBiMapCache.path = path
	return path, nil
}

func resetDNSBiMapCacheLocked() {
	dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
	dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
	dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
	dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
}

func addDNSBiMapIndexLocked(domain, ip string) {
	ips := dnsBiMapCache.domainIPs[domain]
	if ips == nil {
		ips = make(map[string]struct{})
		dnsBiMapCache.domainIPs[domain] = ips
	}
	ips[ip] = struct{}{}

	domains := dnsBiMapCache.ipDomains[ip]
	if domains == nil {
		domains = make(map[string]struct{})
		dnsBiMapCache.ipDomains[ip] = domains
	}
	domains[domain] = struct{}{}
}

func removeDNSBiMapEntryLocked(key string) {
	entry, ok := dnsBiMapCache.entries[key]
	if !ok {
		return
	}
	delete(dnsBiMapCache.entries, key)

	domain := normalizeDNSCacheHost(entry.Domain)
	ip := normalizeDNSCacheIP(entry.IP)
	if domain != "" {
		if ips, ok := dnsBiMapCache.domainIPs[domain]; ok {
			delete(ips, ip)
			if len(ips) == 0 {
				delete(dnsBiMapCache.domainIPs, domain)
			}
		}
	}
	if ip != "" {
		if domains, ok := dnsBiMapCache.ipDomains[ip]; ok {
			delete(domains, domain)
			if len(domains) == 0 {
				delete(dnsBiMapCache.ipDomains, ip)
			}
		}
	}
}

func enforceDNSBiMapCapacityLocked() bool {
	if len(dnsBiMapCache.entries) <= dnsBidirectionalCacheMaxItems {
		return false
	}
	countToDrop := len(dnsBiMapCache.entries) - dnsBidirectionalCacheMaxItems
	type kv struct {
		Key       string
		UpdatedAt time.Time
		ExpiresAt time.Time
	}
	items := make([]kv, 0, len(dnsBiMapCache.entries))
	for key, entry := range dnsBiMapCache.entries {
		items = append(items, kv{Key: key, UpdatedAt: entry.UpdatedAt, ExpiresAt: entry.ExpiresAt})
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.Before(items[j].UpdatedAt)
		}
		return items[i].ExpiresAt.Before(items[j].ExpiresAt)
	})
	if countToDrop > len(items) {
		countToDrop = len(items)
	}
	for i := 0; i < countToDrop; i++ {
		removeDNSBiMapEntryLocked(items[i].Key)
	}
	return countToDrop > 0
}

func pruneExpiredDNSBiMapLocked(now time.Time) bool {
	if len(dnsBiMapCache.entries) == 0 {
		return false
	}
	removed := false
	for key, entry := range dnsBiMapCache.entries {
		if entry.ExpiresAt.IsZero() || now.After(entry.ExpiresAt) {
			removeDNSBiMapEntryLocked(key)
			delete(dnsBiMapCache.failures, key)
			removed = true
		}
	}
	return removed
}

func loadDNSBiMapFromDiskLocked(path string) error {
	resetDNSBiMapCacheLocked()
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	var payload dnsBiMapPersistPayload
	if err := gob.NewDecoder(file).Decode(&payload); err != nil {
		return err
	}

	now := time.Now()
	for _, raw := range payload.Entries {
		domain := normalizeDNSCacheHost(raw.Domain)
		ip := normalizeDNSCacheIP(raw.IP)
		if domain == "" || ip == "" {
			continue
		}
		if raw.ExpiresAt.IsZero() || now.After(raw.ExpiresAt) {
			continue
		}
		entry := dnsBiMapEntry{
			Domain:    domain,
			IP:        ip,
			Group:     normalizeDNSBiMapGroup(raw.Group),
			NodeID:    strings.TrimSpace(raw.NodeID),
			ExpiresAt: raw.ExpiresAt,
			UpdatedAt: raw.UpdatedAt,
		}
		if entry.UpdatedAt.IsZero() {
			entry.UpdatedAt = now
		}
		key := dnsBiMapKey(domain, ip)
		dnsBiMapCache.entries[key] = entry
		addDNSBiMapIndexLocked(domain, ip)
	}
	return nil
}

func storeDNSBiMapToDiskLocked() error {
	path := strings.TrimSpace(dnsBiMapCache.path)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	entries := make([]dnsBiMapEntry, 0, len(dnsBiMapCache.entries))
	for _, entry := range dnsBiMapCache.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Domain != entries[j].Domain {
			return entries[i].Domain < entries[j].Domain
		}
		return entries[i].IP < entries[j].IP
	})
	payload := dnsBiMapPersistPayload{Version: 1, Entries: entries}

	tmpPath := path + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	enc := gob.NewEncoder(file)
	encodeErr := enc.Encode(payload)
	closeErr := file.Close()
	if encodeErr != nil {
		_ = os.Remove(tmpPath)
		return encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}

	_ = os.Remove(path)
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func ensureDNSBiMapCacheLoaded() error {
	dnsBiMapCache.mu.Lock()
	defer dnsBiMapCache.mu.Unlock()
	if dnsBiMapCache.loaded {
		return nil
	}
	path, err := ensureDNSBiMapPathLocked()
	if err != nil {
		return err
	}
	if err := loadDNSBiMapFromDiskLocked(path); err != nil {
		resetDNSBiMapCacheLocked()
		dnsBiMapCache.loaded = true
		return err
	}
	_ = pruneExpiredDNSBiMapLocked(time.Now())
	_ = enforceDNSBiMapCapacityLocked()
	dnsBiMapCache.loaded = true
	return nil
}

func (s *networkAssistantService) storeDNSBiMap(addrs []string, domain string, route tunnelRouteDecision) {
	if s == nil {
		return
	}
	if !isDNSBiMapRouteCacheable(route) {
		return
	}
	cleanDomain := normalizeRuleDomain(domain)
	cleanIPs := normalizeDNSCacheIPs(addrs)
	if cleanDomain == "" || len(cleanIPs) == 0 {
		return
	}
	if err := ensureDNSBiMapCacheLoaded(); err != nil {
		s.logf("load dns bi-map cache failed: %v", err)
		return
	}

	now := time.Now()
	expiresAt := now.Add(dnsBidirectionalCacheTTL)
	cleanGroup := normalizeDNSBiMapGroup(route.Group)
	cleanNodeID := strings.TrimSpace(route.NodeID)

	dnsBiMapCache.mu.Lock()
	changed := false
	for _, ip := range cleanIPs {
		key := dnsBiMapKey(cleanDomain, ip)
		if existing, ok := dnsBiMapCache.entries[key]; ok {
			if existing.Group == cleanGroup && strings.EqualFold(existing.NodeID, cleanNodeID) && time.Until(existing.ExpiresAt) > dnsBidirectionalCacheTTL/3 {
				continue
			}
		}
		dnsBiMapCache.entries[key] = dnsBiMapEntry{
			Domain:    cleanDomain,
			IP:        ip,
			Group:     cleanGroup,
			NodeID:    cleanNodeID,
			ExpiresAt: expiresAt,
			UpdatedAt: now,
		}
		addDNSBiMapIndexLocked(cleanDomain, ip)
		delete(dnsBiMapCache.failures, key)
		changed = true
	}
	if enforceDNSBiMapCapacityLocked() {
		changed = true
	}
	if changed {
		_ = storeDNSBiMapToDiskLocked()
	}
	dnsBiMapCache.mu.Unlock()
}

func collectDNSBiMapRecordsForPresentation() []dnsCachePresentationRecord {
	if err := ensureDNSBiMapCacheLoaded(); err != nil {
		return nil
	}
	now := time.Now()
	dnsBiMapCache.mu.Lock()
	removed := pruneExpiredDNSBiMapLocked(now)
	records := make([]dnsCachePresentationRecord, 0, len(dnsBiMapCache.entries))
	for _, entry := range dnsBiMapCache.entries {
		records = append(records, dnsCachePresentationRecord{
			Kind:   dnsCacheKindBiMap,
			Source: dnsCacheSourceBiMap,
			Domain: entry.Domain,
			IP:     entry.IP,
			Route: tunnelRouteDecision{
				Direct: false,
				NodeID: entry.NodeID,
				Group:  entry.Group,
			},
			FakeIP:  false,
			Expires: entry.ExpiresAt,
		})
	}
	if removed {
		_ = storeDNSBiMapToDiskLocked()
	}
	dnsBiMapCache.mu.Unlock()
	return records
}

func (s *networkAssistantService) recordDNSBiMapConnectResult(targetAddr, group string, success bool) {
	if s == nil {
		return
	}
	cleanGroup := normalizeDNSBiMapGroup(group)
	if cleanGroup == "" || isDirectRuleGroupKey(cleanGroup) {
		return
	}

	s.mu.RLock()
	mode := s.mode
	tunEnabled := s.tunEnabled
	s.mu.RUnlock()
	if mode != networkModeTUN || !tunEnabled {
		return
	}

	host := strings.TrimSpace(targetAddr)
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = splitHost
	}
	ip := normalizeDNSCacheIP(host)
	if ip == "" {
		return
	}

	if err := ensureDNSBiMapCacheLoaded(); err != nil {
		return
	}

	now := time.Now()
	dnsBiMapCache.mu.Lock()
	domains := dnsBiMapCache.ipDomains[ip]
	if len(domains) == 0 {
		dnsBiMapCache.mu.Unlock()
		return
	}
	removed := false
	for domain := range domains {
		key := dnsBiMapKey(domain, ip)
		if success {
			delete(dnsBiMapCache.failures, key)
			continue
		}
		state := dnsBiMapCache.failures[key]
		if state.Count <= 0 || now.Sub(state.LastFailureAt) > dnsBidirectionalFailureWindow {
			state.Count = 1
		} else {
			state.Count++
		}
		state.LastFailureAt = now
		dnsBiMapCache.failures[key] = state

		if state.Count >= dnsBidirectionalFailureThreshold {
			removeDNSBiMapEntryLocked(key)
			delete(dnsBiMapCache.failures, key)
			removed = true
		}
	}
	if removed {
		_ = storeDNSBiMapToDiskLocked()
	}
	dnsBiMapCache.mu.Unlock()
}
