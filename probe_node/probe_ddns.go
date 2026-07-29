package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	probeDDNSDirName             = "ddns"
	probeDDNSConfigFileName      = "config.json"
	probeDDNSStateFileName       = "state.json"
	probeDDNSConfigVersion       = 1
	probeDDNSMaxDomains          = 100
	probeDDNSSyncInterval        = 10 * time.Minute
	probeDDNSCertificateInterval = 6 * time.Hour
	probeDDNSOperationTimeout    = 10 * time.Minute
)

type probeDDNSConfig struct {
	Version             int      `json:"version"`
	Enabled             bool     `json:"enabled"`
	SelectedInterfaceID string   `json:"selected_interface_id,omitempty"`
	InterfaceDomains    []string `json:"interface_domains,omitempty"`
	PublicDomains       []string `json:"public_domains,omitempty"`
	APIToken            string   `json:"api_token,omitempty"`
	UpdatedAt           string   `json:"updated_at,omitempty"`
}

type probeDDNSConfigRequest struct {
	Enabled             bool     `json:"enabled"`
	SelectedInterfaceID string   `json:"selected_interface_id"`
	InterfaceDomains    []string `json:"interface_domains"`
	PublicDomains       []string `json:"public_domains"`
	APIToken            string   `json:"api_token"`
}

type probeDDNSConfigView struct {
	Version             int      `json:"version"`
	Enabled             bool     `json:"enabled"`
	SelectedInterfaceID string   `json:"selected_interface_id,omitempty"`
	InterfaceDomains    []string `json:"interface_domains"`
	PublicDomains       []string `json:"public_domains"`
	APITokenConfigured  bool     `json:"api_token_configured"`
	UpdatedAt           string   `json:"updated_at,omitempty"`
}

type probeDDNSManagedRecord struct {
	Source     string `json:"source"`
	Domain     string `json:"domain"`
	RecordType string `json:"record_type"`
	Content    string `json:"content"`
	ZoneID     string `json:"zone_id"`
	RecordID   string `json:"record_id"`
}

type probeDDNSState struct {
	Version                  int                      `json:"version"`
	ManagedRecords           []probeDDNSManagedRecord `json:"managed_records,omitempty"`
	InterfaceIPv4            []string                 `json:"interface_ipv4,omitempty"`
	InterfaceIPv6            []string                 `json:"interface_ipv6,omitempty"`
	PublicIPv4               []string                 `json:"public_ipv4,omitempty"`
	PublicIPv6               []string                 `json:"public_ipv6,omitempty"`
	LastSyncStartedAt        string                   `json:"last_sync_started_at,omitempty"`
	LastSyncAt               string                   `json:"last_sync_at,omitempty"`
	LastSyncStatus           string                   `json:"last_sync_status,omitempty"`
	LastSyncError            string                   `json:"last_sync_error,omitempty"`
	LastCertificateCheckAt   string                   `json:"last_certificate_check_at,omitempty"`
	LastCertificateStatus    string                   `json:"last_certificate_status,omitempty"`
	LastCertificateError     string                   `json:"last_certificate_error,omitempty"`
	CertificateDomains       []string                 `json:"certificate_domains,omitempty"`
	CertificateNotBefore     string                   `json:"certificate_not_before,omitempty"`
	CertificateNotAfter      string                   `json:"certificate_not_after,omitempty"`
	CertificateLastRenewedAt string                   `json:"certificate_last_renewed_at,omitempty"`
}

type probeDDNSAddressSet struct {
	IPv4 []string
	IPv6 []string
}

var probeDDNSRuntime = struct {
	mu                 sync.Mutex
	syncRunning        bool
	syncPending        bool
	certificateRunning bool
	certificatePending bool
}{}

var probeDDNSStoreMu sync.Mutex

var probeDDNSPublicIPSniffer = sniffPublicIPs

var probeDDNSReconcileFn = reconcileProbeDDNS
var probeDDNSEnsureCertificateFn = ensureProbeDDNSCertificate

func resolveProbeDDNSDir() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeDDNSDirName), nil
}

func resolveProbeDDNSPath(name string) (string, error) {
	dir, err := resolveProbeDDNSDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func defaultProbeDDNSConfig() probeDDNSConfig {
	return probeDDNSConfig{Version: probeDDNSConfigVersion, InterfaceDomains: []string{}, PublicDomains: []string{}}
}

func defaultProbeDDNSState() probeDDNSState {
	return probeDDNSState{Version: probeDDNSConfigVersion, ManagedRecords: []probeDDNSManagedRecord{}}
}

func loadProbeDDNSConfig() (probeDDNSConfig, error) {
	path, err := resolveProbeDDNSPath(probeDDNSConfigFileName)
	if err != nil {
		return probeDDNSConfig{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultProbeDDNSConfig(), nil
	}
	if err != nil {
		return probeDDNSConfig{}, err
	}
	config := probeDDNSConfig{}
	if err := decodeProbeLocalJSONStrict(raw, &config); err != nil {
		return probeDDNSConfig{}, fmt.Errorf("decode ddns config: %w", err)
	}
	return normalizeProbeDDNSConfig(config)
}

func loadProbeDDNSState() (probeDDNSState, error) {
	path, err := resolveProbeDDNSPath(probeDDNSStateFileName)
	if err != nil {
		return probeDDNSState{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultProbeDDNSState(), nil
	}
	if err != nil {
		return probeDDNSState{}, err
	}
	state := probeDDNSState{}
	if err := decodeProbeLocalJSONStrict(raw, &state); err != nil {
		return probeDDNSState{}, fmt.Errorf("decode ddns state: %w", err)
	}
	if state.Version <= 0 {
		state.Version = probeDDNSConfigVersion
	}
	state.ManagedRecords = normalizeProbeDDNSManagedRecords(state.ManagedRecords)
	return state, nil
}

func persistProbeDDNSConfig(config probeDDNSConfig) error {
	path, err := resolveProbeDDNSPath(probeDDNSConfigFileName)
	if err != nil {
		return err
	}
	return persistProbeDDNSJSON(path, config)
}

func persistProbeDDNSState(state probeDDNSState) error {
	state.Version = probeDDNSConfigVersion
	state.ManagedRecords = normalizeProbeDDNSManagedRecords(state.ManagedRecords)
	path, err := resolveProbeDDNSPath(probeDDNSStateFileName)
	if err != nil {
		return err
	}
	return persistProbeDDNSJSON(path, state)
}

func updateProbeDDNSState(update func(*probeDDNSState)) error {
	probeDDNSStoreMu.Lock()
	defer probeDDNSStoreMu.Unlock()
	state, err := loadProbeDDNSState()
	if err != nil {
		return err
	}
	update(&state)
	return persistProbeDDNSState(state)
}

func persistProbeDDNSJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func normalizeProbeDDNSConfig(config probeDDNSConfig) (probeDDNSConfig, error) {
	config.Version = probeDDNSConfigVersion
	config.SelectedInterfaceID = normalizeProbeIPReportInterfaceID(config.SelectedInterfaceID)
	config.APIToken = strings.TrimSpace(config.APIToken)
	var err error
	config.InterfaceDomains, err = normalizeProbeDDNSDomains(config.InterfaceDomains)
	if err != nil {
		return probeDDNSConfig{}, fmt.Errorf("interface domains: %w", err)
	}
	config.PublicDomains, err = normalizeProbeDDNSDomains(config.PublicDomains)
	if err != nil {
		return probeDDNSConfig{}, fmt.Errorf("public domains: %w", err)
	}
	all := append(append([]string{}, config.InterfaceDomains...), config.PublicDomains...)
	all, err = normalizeProbeDDNSDomains(all)
	if err != nil {
		return probeDDNSConfig{}, err
	}
	if len(all) > probeDDNSMaxDomains {
		return probeDDNSConfig{}, fmt.Errorf("at most %d unique domains are allowed", probeDDNSMaxDomains)
	}
	interfaceSet := make(map[string]struct{}, len(config.InterfaceDomains))
	for _, domain := range config.InterfaceDomains {
		interfaceSet[domain] = struct{}{}
	}
	for _, domain := range config.PublicDomains {
		if _, exists := interfaceSet[domain]; exists {
			return probeDDNSConfig{}, fmt.Errorf("domain %q cannot use both interface and public sources", domain)
		}
	}
	if len(config.InterfaceDomains) > 0 && config.SelectedInterfaceID == "" {
		return probeDDNSConfig{}, errors.New("selected interface is required for interface domains")
	}
	if config.Enabled && len(all) > 0 && config.APIToken == "" {
		return probeDDNSConfig{}, errors.New("cloudflare api token is required")
	}
	return config, nil
}

func normalizeProbeDDNSDomains(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
		if domain == "" {
			continue
		}
		if err := validateProbeDDNSDomain(domain); err != nil {
			return nil, err
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		out = append(out, domain)
	}
	sort.Strings(out)
	return out, nil
}

func validateProbeDDNSDomain(domain string) error {
	if len(domain) > 253 || !strings.Contains(domain, ".") || strings.ContainsAny(domain, "/:@*[]") {
		return fmt.Errorf("invalid fully qualified domain %q", domain)
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid fully qualified domain %q", domain)
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return fmt.Errorf("invalid fully qualified domain %q", domain)
		}
	}
	if net.ParseIP(domain) != nil {
		return fmt.Errorf("invalid fully qualified domain %q", domain)
	}
	return nil
}

func buildProbeDDNSConfigView(config probeDDNSConfig) probeDDNSConfigView {
	return probeDDNSConfigView{
		Version:             config.Version,
		Enabled:             config.Enabled,
		SelectedInterfaceID: config.SelectedInterfaceID,
		InterfaceDomains:    append([]string{}, config.InterfaceDomains...),
		PublicDomains:       append([]string{}, config.PublicDomains...),
		APITokenConfigured:  strings.TrimSpace(config.APIToken) != "",
		UpdatedAt:           config.UpdatedAt,
	}
}

func collectProbeDDNSInterfaceAddresses(selectedID string) (probeDDNSAddressSet, error) {
	selectedID = normalizeProbeIPReportInterfaceID(selectedID)
	if selectedID == "" {
		return probeDDNSAddressSet{}, errors.New("selected interface is required")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return probeDDNSAddressSet{}, err
	}
	for _, iface := range interfaces {
		identity := resolveProbeIPReportInterfaceIdentity(iface)
		matched := false
		for _, id := range probeIPReportInterfaceIdentityIDs(identity) {
			if id == selectedID {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return probeDDNSAddressSet{}, err
		}
		return normalizeProbeDDNSAddressesFromNet(addrs), nil
	}
	return probeDDNSAddressSet{}, fmt.Errorf("selected interface %q is not available", selectedID)
}

func normalizeProbeDDNSAddressesFromNet(addrs []net.Addr) probeDDNSAddressSet {
	v4 := map[string]struct{}{}
	v6 := map[string]struct{}{}
	for _, addr := range addrs {
		ip := probeReportIPFromAddr(addr)
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			v4[ip4.String()] = struct{}{}
			continue
		}
		if ip.To16() != nil {
			v6[ip.String()] = struct{}{}
		}
	}
	return probeDDNSAddressSet{IPv4: mapKeysSorted(v4), IPv6: mapKeysSorted(v6)}
}

func collectProbeDDNSPublicAddresses() (probeDDNSAddressSet, error) {
	v4, v6, ok := probeDDNSPublicIPSniffer()
	if !ok {
		return probeDDNSAddressSet{}, errors.New("public exit ip is unavailable")
	}
	v4 = normalizeProbeDDNSIPStrings(v4, true)
	v6 = normalizeProbeDDNSIPStrings(v6, false)
	if len(v4) == 0 && len(v6) == 0 {
		return probeDDNSAddressSet{}, errors.New("public exit ip is unavailable")
	}
	return probeDDNSAddressSet{IPv4: v4, IPv6: v6}, nil
}

func normalizeProbeDDNSIPStrings(values []string, ipv4 bool) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil {
			continue
		}
		if ipv4 {
			if ip4 := ip.To4(); ip4 != nil {
				seen[ip4.String()] = struct{}{}
			}
			continue
		}
		if ip.To4() == nil && ip.To16() != nil {
			seen[ip.String()] = struct{}{}
		}
	}
	return mapKeysSorted(seen)
}

func normalizeProbeDDNSManagedRecords(records []probeDDNSManagedRecord) []probeDDNSManagedRecord {
	out := make([]probeDDNSManagedRecord, 0, len(records))
	seen := map[string]struct{}{}
	for _, record := range records {
		record.Source = strings.ToLower(strings.TrimSpace(record.Source))
		record.Domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(record.Domain)), ".")
		record.RecordType = strings.ToUpper(strings.TrimSpace(record.RecordType))
		record.Content = strings.TrimSpace(record.Content)
		record.ZoneID = strings.TrimSpace(record.ZoneID)
		record.RecordID = strings.TrimSpace(record.RecordID)
		if record.Source == "" || record.Domain == "" || record.Content == "" || record.ZoneID == "" || record.RecordID == "" {
			continue
		}
		key := probeDDNSManagedRecordKey(record)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return probeDDNSManagedRecordKey(out[i]) < probeDDNSManagedRecordKey(out[j]) })
	return out
}

func probeDDNSManagedRecordKey(record probeDDNSManagedRecord) string {
	return strings.Join([]string{record.Source, record.Domain, record.RecordType, record.Content}, "|")
}

func allProbeDDNSDomains(config probeDDNSConfig) []string {
	values := append(append([]string{}, config.InterfaceDomains...), config.PublicDomains...)
	values, _ = normalizeProbeDDNSDomains(values)
	return values
}

func startProbeDDNSScheduler() {
	go func() {
		time.Sleep(5 * time.Second)
		triggerProbeDDNSSync("startup")
		triggerProbeDDNSCertificate("startup")
		syncTicker := time.NewTicker(probeDDNSSyncInterval)
		certTicker := time.NewTicker(probeDDNSCertificateInterval)
		defer syncTicker.Stop()
		defer certTicker.Stop()
		for {
			select {
			case <-syncTicker.C:
				triggerProbeDDNSSync("periodic")
			case <-certTicker.C:
				triggerProbeDDNSCertificate("periodic")
			}
		}
	}()
}

func triggerProbeDDNSSync(reason string) {
	probeDDNSRuntime.mu.Lock()
	probeDDNSRuntime.syncPending = true
	if probeDDNSRuntime.syncRunning {
		probeDDNSRuntime.mu.Unlock()
		return
	}
	probeDDNSRuntime.syncRunning = true
	probeDDNSRuntime.mu.Unlock()
	go runProbeDDNSSyncQueue(strings.TrimSpace(reason))
}

func runProbeDDNSSyncQueue(reason string) {
	for {
		probeDDNSRuntime.mu.Lock()
		if !probeDDNSRuntime.syncPending {
			probeDDNSRuntime.syncRunning = false
			probeDDNSRuntime.mu.Unlock()
			return
		}
		probeDDNSRuntime.syncPending = false
		probeDDNSRuntime.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), probeDDNSOperationTimeout)
		err := probeDDNSReconcileFn(ctx)
		cancel()
		if err != nil {
			logProbeWarnf("probe ddns sync failed: reason=%s err=%v", reason, err)
		}
	}
}

func triggerProbeDDNSCertificate(reason string) {
	probeDDNSRuntime.mu.Lock()
	probeDDNSRuntime.certificatePending = true
	if probeDDNSRuntime.certificateRunning {
		probeDDNSRuntime.mu.Unlock()
		return
	}
	probeDDNSRuntime.certificateRunning = true
	probeDDNSRuntime.mu.Unlock()
	go runProbeDDNSCertificateQueue(strings.TrimSpace(reason))
}

func runProbeDDNSCertificateQueue(reason string) {
	for {
		probeDDNSRuntime.mu.Lock()
		if !probeDDNSRuntime.certificatePending {
			probeDDNSRuntime.certificateRunning = false
			probeDDNSRuntime.mu.Unlock()
			return
		}
		probeDDNSRuntime.certificatePending = false
		probeDDNSRuntime.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), probeDDNSOperationTimeout)
		err := probeDDNSEnsureCertificateFn(ctx)
		cancel()
		if err != nil {
			logProbeWarnf("probe ddns certificate check failed: reason=%s err=%v", reason, err)
		}
	}
}

func probeLocalSystemDDNSHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeProbeDDNSPayload(w)
	case http.MethodPost:
		body := http.MaxBytesReader(w, r.Body, probeLocalRouteReadBodyMaxLen)
		defer body.Close()
		decoder := json.NewDecoder(body)
		decoder.DisallowUnknownFields()
		request := probeDDNSConfigRequest{}
		if err := decoder.Decode(&request); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		existing, err := loadProbeDDNSConfig()
		if err != nil {
			writeProbeLocalError(w, err)
			return
		}
		token := strings.TrimSpace(request.APIToken)
		if token == "" {
			token = existing.APIToken
		}
		config, err := normalizeProbeDDNSConfig(probeDDNSConfig{
			Version:             probeDDNSConfigVersion,
			Enabled:             request.Enabled,
			SelectedInterfaceID: request.SelectedInterfaceID,
			InterfaceDomains:    request.InterfaceDomains,
			PublicDomains:       request.PublicDomains,
			APIToken:            token,
			UpdatedAt:           time.Now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := persistProbeDDNSConfig(config); err != nil {
			writeProbeLocalError(w, err)
			return
		}
		if config.Enabled {
			triggerProbeDDNSSync("settings-save")
			triggerProbeDDNSCertificate("settings-save")
		}
		writeProbeDDNSPayload(w)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeProbeDDNSPayload(w http.ResponseWriter) {
	config, err := loadProbeDDNSConfig()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	state, err := loadProbeDDNSState()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	settings := loadProbeIPReportSettingsBestEffort()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"config":     buildProbeDDNSConfigView(config),
		"state":      state,
		"interfaces": listProbeIPReportInterfaces(settings),
	})
}
