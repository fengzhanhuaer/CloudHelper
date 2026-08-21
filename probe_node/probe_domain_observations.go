package main

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
	probeDomainAllowlistFileName     = "probe_domain_allowlist.json"
	probeDomainObservationMaxRecords = 5000
	probeDomainObservationMaxSources = 64
)

type probeDomainObservation struct {
	Domain          string   `json:"domain"`
	Status          string   `json:"status"`
	FirstSeen       string   `json:"first_seen"`
	LastSeen        string   `json:"last_seen"`
	Events          int64    `json:"events"`
	DNSQueries      int64    `json:"dns_queries"`
	SNIObservations int64    `json:"sni_observations"`
	ObservedVia     []string `json:"observed_via"`
	Sources         []string `json:"sources"`
	LastSource      string   `json:"last_source,omitempty"`
	LastAction      string   `json:"last_action,omitempty"`
	ResolvedIPs     []string `json:"resolved_ips,omitempty"`
	LastError       string   `json:"last_error,omitempty"`
}

type probeDomainAllowlistFile struct {
	Version int      `json:"version"`
	SavedAt string   `json:"saved_at"`
	Domains []string `json:"domains"`
}

var probeDomainObservationState = struct {
	mu              sync.Mutex
	items           map[string]probeDomainObservation
	allowlistLoaded bool
	allowlistPath   string
	allowlist       map[string]struct{}
}{
	items:     make(map[string]probeDomainObservation),
	allowlist: make(map[string]struct{}),
}

func recordProbeDomainObservation(domain string, via string, source string, action string, resolvedIPs []string, eventErr error) {
	domain = normalizeProbeVirtualRouterDomain(domain)
	via = strings.ToLower(strings.TrimSpace(via))
	if domain == "" || (via != "dns" && via != "sni") {
		return
	}
	source = normalizeProbeDomainObservationSource(source)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	probeDomainObservationState.mu.Lock()
	defer probeDomainObservationState.mu.Unlock()
	if err := loadProbeDomainAllowlistLocked(); err != nil {
		logProbeWarnf("probe domain allowlist load failed: %v", err)
	}
	item, exists := probeDomainObservationState.items[domain]
	if !exists {
		makeProbeDomainObservationRoomLocked()
		status := "tracking"
		if _, allowed := probeDomainObservationState.allowlist[domain]; allowed {
			status = "allowed"
		}
		item = probeDomainObservation{Domain: domain, Status: status, FirstSeen: now}
	}
	item.LastSeen = now
	item.Events++
	if via == "dns" {
		item.DNSQueries++
	} else {
		item.SNIObservations++
	}
	item.ObservedVia = appendProbeDomainObservationUnique(item.ObservedVia, via, 2)
	if source != "" {
		item.LastSource = source
		item.Sources = appendProbeDomainObservationUnique(item.Sources, source, probeDomainObservationMaxSources)
	}
	item.LastAction = strings.TrimSpace(action)
	item.ResolvedIPs = sanitizeProbeDomainObservationIPs(resolvedIPs)
	item.LastError = ""
	if eventErr != nil {
		item.LastError = strings.TrimSpace(eventErr.Error())
	}
	probeDomainObservationState.items[domain] = item
}

func snapshotProbeDomainObservations() ([]probeDomainObservation, []string, error) {
	probeDomainObservationState.mu.Lock()
	defer probeDomainObservationState.mu.Unlock()
	if err := loadProbeDomainAllowlistLocked(); err != nil {
		return nil, nil, err
	}
	items := cloneProbeDomainObservationsLocked()
	sourceSet := make(map[string]struct{})
	for _, item := range items {
		for _, source := range item.Sources {
			if source != "" {
				sourceSet[source] = struct{}{}
			}
		}
	}
	sources := make([]string, 0, len(sourceSet))
	for source := range sourceSet {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	return items, sources, nil
}

func setProbeDomainObservationStatus(domain string, status string) (probeDomainObservation, error) {
	domain = normalizeProbeVirtualRouterDomain(domain)
	status = strings.ToLower(strings.TrimSpace(status))
	if domain == "" {
		return probeDomainObservation{}, errors.New("domain is invalid")
	}
	if status != "tracking" && status != "allowed" {
		return probeDomainObservation{}, errors.New("status must be tracking or allowed")
	}
	probeDomainObservationState.mu.Lock()
	defer probeDomainObservationState.mu.Unlock()
	if err := loadProbeDomainAllowlistLocked(); err != nil {
		return probeDomainObservation{}, err
	}
	item, exists := probeDomainObservationState.items[domain]
	if !exists {
		return probeDomainObservation{}, errors.New("domain observation is unavailable")
	}
	if item.Status == status {
		return item, nil
	}
	previousStatus := item.Status
	item.Status = status
	probeDomainObservationState.items[domain] = item
	if status == "allowed" {
		probeDomainObservationState.allowlist[domain] = struct{}{}
	} else {
		delete(probeDomainObservationState.allowlist, domain)
	}
	if err := persistProbeDomainAllowlistLocked(); err != nil {
		item.Status = previousStatus
		probeDomainObservationState.items[domain] = item
		if previousStatus == "allowed" {
			probeDomainObservationState.allowlist[domain] = struct{}{}
		} else {
			delete(probeDomainObservationState.allowlist, domain)
		}
		return probeDomainObservation{}, err
	}
	return item, nil
}

func loadProbeDomainAllowlistLocked() error {
	if probeDomainObservationState.allowlistLoaded {
		return nil
	}
	dataDir, err := resolveDataDir()
	if err != nil {
		return err
	}
	probeDomainObservationState.allowlistPath = filepath.Join(dataDir, probeDomainAllowlistFileName)
	probeDomainObservationState.allowlist = make(map[string]struct{})
	raw, err := os.ReadFile(probeDomainObservationState.allowlistPath)
	if errors.Is(err, os.ErrNotExist) {
		probeDomainObservationState.allowlistLoaded = true
		return nil
	}
	if err != nil {
		return err
	}
	var payload probeDomainAllowlistFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	for _, rawDomain := range payload.Domains {
		if domain := normalizeProbeVirtualRouterDomain(rawDomain); domain != "" {
			probeDomainObservationState.allowlist[domain] = struct{}{}
		}
	}
	probeDomainObservationState.allowlistLoaded = true
	return nil
}

func persistProbeDomainAllowlistLocked() error {
	domains := make([]string, 0, len(probeDomainObservationState.allowlist))
	for domain := range probeDomainObservationState.allowlist {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	payload, err := json.MarshalIndent(probeDomainAllowlistFile{
		Version: 1,
		SavedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Domains: domains,
	}, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(probeDomainObservationState.allowlistPath, payload, 0o600)
}

func resetProbeDomainObservations() {
	probeDomainObservationState.mu.Lock()
	probeDomainObservationState.items = make(map[string]probeDomainObservation)
	probeDomainObservationState.allowlistLoaded = false
	probeDomainObservationState.allowlistPath = ""
	probeDomainObservationState.allowlist = make(map[string]struct{})
	probeDomainObservationState.mu.Unlock()
}

func cloneProbeDomainObservationsLocked() []probeDomainObservation {
	items := make([]probeDomainObservation, 0, len(probeDomainObservationState.items))
	for _, item := range probeDomainObservationState.items {
		item.ObservedVia = append([]string(nil), item.ObservedVia...)
		item.Sources = append([]string(nil), item.Sources...)
		item.ResolvedIPs = append([]string(nil), item.ResolvedIPs...)
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].LastSeen != items[j].LastSeen {
			return items[i].LastSeen > items[j].LastSeen
		}
		return items[i].Domain < items[j].Domain
	})
	return items
}

func makeProbeDomainObservationRoomLocked() {
	if len(probeDomainObservationState.items) < probeDomainObservationMaxRecords {
		return
	}
	oldestDomain := ""
	oldestSeen := ""
	for domain, item := range probeDomainObservationState.items {
		if oldestDomain == "" || item.LastSeen < oldestSeen {
			oldestDomain = domain
			oldestSeen = item.LastSeen
		}
	}
	delete(probeDomainObservationState.items, oldestDomain)
}

func normalizeProbeDomainObservationSource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(source); err == nil {
		source = strings.Trim(host, "[]")
	}
	if ip := net.ParseIP(source); ip != nil {
		return ip.String()
	}
	return source
}

func appendProbeDomainObservationUnique(items []string, value string, limit int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, item := range items {
		if item == value {
			return items
		}
	}
	if limit > 0 && len(items) >= limit {
		items = append([]string(nil), items[len(items)-limit+1:]...)
	}
	items = append(items, value)
	sort.Strings(items)
	return items
}

func sanitizeProbeDomainObservationIPs(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if ip := net.ParseIP(strings.TrimSpace(item)); ip != nil {
			out = appendProbeDomainObservationUnique(out, ip.String(), 16)
		}
	}
	return out
}
