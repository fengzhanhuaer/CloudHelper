package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cloudflareStoreFile                         = "cloudflare.json"
	cloudflareManagedBusinessNamePrefix         = "api.codex."
	cloudflareZeroTrustDefaultSyncIntervalSec   = 300
	cloudflareZeroTrustMinSyncIntervalSec       = 30
	cloudflareZeroTrustSchedulerTickIntervalSec = 15
)

var cloudflareManagedLabelPrefixes = [...]string{"ddns_go_", "local_go_"}

type cloudflareStore struct {
	mu   sync.RWMutex
	path string
	data cloudflareStoreData
}

type cloudflareStoreData struct {
	APIToken          string                               `json:"api_token"`
	ZoneName          string                               `json:"zone_name"`
	Records           []cloudflareDDNSRecord               `json:"records"`
	ZeroTrust         cloudflareZeroTrustWhitelistConfig   `json:"zerotrust_whitelist"`
	ZeroTrustLastSync cloudflareZeroTrustWhitelistSyncInfo `json:"zerotrust_last_sync"`
}

type cloudflareZeroTrustWhitelistConfig struct {
	Enabled         bool   `json:"enabled"`
	PolicyName      string `json:"policy_name"`
	WhitelistRaw    string `json:"whitelist_raw"`
	SyncIntervalSec int    `json:"sync_interval_sec"`
}

type cloudflareZeroTrustWhitelistSyncInfo struct {
	Running         bool     `json:"running"`
	LastRunAt       string   `json:"last_run_at"`
	LastSuccessAt   string   `json:"last_success_at"`
	LastStatus      string   `json:"last_status"`
	LastMessage     string   `json:"last_message"`
	LastPolicyID    string   `json:"last_policy_id"`
	LastPolicyName  string   `json:"last_policy_name"`
	LastAppliedIPs  []string `json:"last_applied_ips"`
	LastSourceLines int      `json:"last_source_lines"`
}

type cloudflareZeroTrustWhitelistRequest struct {
	Enabled         bool   `json:"enabled"`
	PolicyName      string `json:"policy_name"`
	WhitelistRaw    string `json:"whitelist_raw"`
	SyncIntervalSec int    `json:"sync_interval_sec"`
}

type cloudflareZeroTrustWhitelistResponse struct {
	Enabled         bool     `json:"enabled"`
	PolicyName      string   `json:"policy_name"`
	WhitelistRaw    string   `json:"whitelist_raw"`
	SyncIntervalSec int      `json:"sync_interval_sec"`
	Running         bool     `json:"running"`
	LastRunAt       string   `json:"last_run_at"`
	LastSuccessAt   string   `json:"last_success_at"`
	LastStatus      string   `json:"last_status"`
	LastMessage     string   `json:"last_message"`
	LastPolicyID    string   `json:"last_policy_id"`
	LastPolicyName  string   `json:"last_policy_name"`
	LastAppliedIPs  []string `json:"last_applied_ips"`
	LastSourceLines int      `json:"last_source_lines"`
}

type cloudflareZeroTrustRunRequest struct {
	Force bool `json:"force"`
}

type cloudflareZeroTrustPolicyItem struct {
	ID      string                 `json:"id"`
	Name    string                 `json:"name"`
	Include []map[string]interface{} `json:"include"`
	Raw     map[string]interface{} `json:"raw"`
}

type cloudflareAPIKeyResponse struct {
	APIKey     string `json:"api_key"`
	ZoneName   string `json:"zone_name"`
	Configured bool   `json:"configured"`
}

type cloudflareAPIKeyRequest struct {
	APIKey string `json:"api_key"`
}

type cloudflareZoneRequest struct {
	ZoneName string `json:"zone_name"`
}

type cloudflareZoneResponse struct {
	ZoneName string `json:"zone_name"`
}

type cloudflareDDNSApplyRequest struct {
	ZoneName string `json:"zone_name"`
}

type cloudflareDDNSRecord struct {
	NodeID      string `json:"node_id"`
	NodeNo      int    `json:"node_no"`
	NodeName    string `json:"node_name"`
	ZoneName    string `json:"zone_name"`
	ZoneID      string `json:"zone_id"`
	RecordClass string `json:"record_class"`
	RecordName  string `json:"record_name"`
	RecordID    string `json:"record_id"`
	RecordType  string `json:"record_type"`
	Sequence    int    `json:"sequence"`
	ContentIP   string `json:"content_ip"`
	UpdatedAt   string `json:"updated_at"`
	LastMessage string `json:"last_message,omitempty"`
}

type cloudflareDDNSApplyItem struct {
	NodeID      string `json:"node_id"`
	NodeNo      int    `json:"node_no"`
	NodeName    string `json:"node_name"`
	RecordClass string `json:"record_class"`
	RecordName  string `json:"record_name"`
	RecordType  string `json:"record_type"`
	Sequence    int    `json:"sequence"`
	RecordID    string `json:"record_id,omitempty"`
	ContentIP   string `json:"content_ip,omitempty"`
	Status      string `json:"status"`
	Message     string `json:"message"`
}

type cloudflareDDNSApplyResponse struct {
	ZoneName string                    `json:"zone_name"`
	Applied  int                       `json:"applied"`
	Skipped  int                       `json:"skipped"`
	Items    []cloudflareDDNSApplyItem `json:"items"`
	Records  []cloudflareDDNSRecord    `json:"records"`
}

type cloudflareZoneListResponse struct {
	Success bool `json:"success"`
	Result  []struct {
		ID      string `json:"id"`
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
	} `json:"result"`
}

type cloudflareDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

type cloudflareDNSRecordListResponse struct {
	Success    bool                  `json:"success"`
	Result     []cloudflareDNSRecord `json:"result"`
	ResultInfo struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

type cloudflareDNSRecordWriteResponse struct {
	Success bool                `json:"success"`
	Result  cloudflareDNSRecord `json:"result"`
}

type cloudflareDesiredRecord struct {
	NodeID      string
	NodeNo      int
	NodeName    string
	ZoneName    string
	RecordClass string
	RecordType  string
	Sequence    int
	RecordName  string
	ContentIP   string
}

type cloudflareIPGroups struct {
	PublicIPv4 []string
	PublicIPv6 []string
	LocalIPv4  []string
	LocalIPv6  []string
}

var CloudflareStore *cloudflareStore

var cloudflareAutoDDNSSyncState = struct {
	mu      sync.Mutex
	running map[string]bool
	pending map[string]bool
}{
	running: map[string]bool{},
	pending: map[string]bool{},
}

func initCloudflareStore() {
	storePath := filepath.Join(dataDir, cloudflareStoreFile)
	CloudflareStore = &cloudflareStore{
		path: storePath,
		data: cloudflareStoreData{
			Records:           []cloudflareDDNSRecord{},
			ZeroTrust:         defaultCloudflareZeroTrustWhitelistConfig(),
			ZeroTrustLastSync: cloudflareZeroTrustWhitelistSyncInfo{LastAppliedIPs: []string{}},
		},
	}

	if _, err := os.Stat(storePath); err == nil {
		content, readErr := os.ReadFile(storePath)
		if readErr != nil {
			log.Fatalf("failed to read cloudflare store file: %v", readErr)
		}
		if len(strings.TrimSpace(string(content))) > 0 {
			var raw cloudflareStoreData
			if unmarshalErr := json.Unmarshal(content, &raw); unmarshalErr != nil {
				log.Fatalf("failed to parse cloudflare store file: %v", unmarshalErr)
			}
			raw.APIToken = strings.TrimSpace(raw.APIToken)
			raw.ZoneName = normalizeCloudflareZoneName(raw.ZoneName)
			raw.Records = normalizeCloudflareRecords(raw.Records)
			raw.ZeroTrust = normalizeCloudflareZeroTrustWhitelistConfig(raw.ZeroTrust)
			raw.ZeroTrustLastSync = normalizeCloudflareZeroTrustWhitelistSyncInfo(raw.ZeroTrustLastSync)
			CloudflareStore.data = raw
		}
	} else if os.IsNotExist(err) {
		if saveErr := CloudflareStore.Save(); saveErr != nil {
			log.Fatalf("failed to initialize cloudflare store file: %v", saveErr)
		}
	} else {
		log.Fatalf("failed to check cloudflare store file: %v", err)
	}
	log.Println("Cloudflare datastore initialized at", storePath)
}

func (s *cloudflareStore) Save() error {
	s.mu.RLock()
	content, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, content, 0o644); err != nil {
		return err
	}
	triggerAutoBackupControllerDataAsync("cloudflare_store_save")
	return nil
}

func getCloudflareAPIKey() cloudflareAPIKeyResponse {
	if CloudflareStore == nil {
		return cloudflareAPIKeyResponse{}
	}
	CloudflareStore.mu.RLock()
	token := strings.TrimSpace(CloudflareStore.data.APIToken)
	zoneName := normalizeCloudflareZoneName(CloudflareStore.data.ZoneName)
	CloudflareStore.mu.RUnlock()
	return cloudflareAPIKeyResponse{APIKey: token, ZoneName: zoneName, Configured: token != ""}
}

func setCloudflareAPIKey(req cloudflareAPIKeyRequest) (cloudflareAPIKeyResponse, error) {
	if CloudflareStore == nil {
		return cloudflareAPIKeyResponse{}, errors.New("cloudflare datastore is not initialized")
	}
	token := strings.TrimSpace(req.APIKey)
	if token == "" {
		return cloudflareAPIKeyResponse{}, errors.New("api_key is required")
	}
	CloudflareStore.mu.Lock()
	CloudflareStore.data.APIToken = token
	CloudflareStore.mu.Unlock()
	if err := CloudflareStore.Save(); err != nil {
		return cloudflareAPIKeyResponse{}, err
	}
	return getCloudflareAPIKey(), nil
}

func getCloudflareZone() cloudflareZoneResponse {
	if CloudflareStore == nil {
		return cloudflareZoneResponse{}
	}
	CloudflareStore.mu.RLock()
	zoneName := normalizeCloudflareZoneName(CloudflareStore.data.ZoneName)
	CloudflareStore.mu.RUnlock()
	return cloudflareZoneResponse{ZoneName: zoneName}
}

func setCloudflareZone(req cloudflareZoneRequest) (cloudflareZoneResponse, error) {
	if CloudflareStore == nil {
		return cloudflareZoneResponse{}, errors.New("cloudflare datastore is not initialized")
	}
	zoneName := normalizeCloudflareZoneName(req.ZoneName)
	if zoneName == "" {
		return cloudflareZoneResponse{}, errors.New("zone_name is required")
	}
	CloudflareStore.mu.Lock()
	CloudflareStore.data.ZoneName = zoneName
	CloudflareStore.mu.Unlock()
	if err := CloudflareStore.Save(); err != nil {
		return cloudflareZoneResponse{}, err
	}
	return cloudflareZoneResponse{ZoneName: zoneName}, nil
}

func listCloudflareRecords() []cloudflareDDNSRecord {
	if CloudflareStore == nil {
		return []cloudflareDDNSRecord{}
	}
	CloudflareStore.mu.RLock()
	out := make([]cloudflareDDNSRecord, len(CloudflareStore.data.Records))
	copy(out, CloudflareStore.data.Records)
	CloudflareStore.mu.RUnlock()
	return normalizeCloudflareRecords(out)
}

func applyCloudflareDDNS(req cloudflareDDNSApplyRequest) (cloudflareDDNSApplyResponse, error) {
	if CloudflareStore == nil {
		return cloudflareDDNSApplyResponse{}, errors.New("cloudflare datastore is not initialized")
	}
	zoneName := normalizeCloudflareZoneName(req.ZoneName)

	CloudflareStore.mu.RLock()
	token := strings.TrimSpace(CloudflareStore.data.APIToken)
	savedZoneName := normalizeCloudflareZoneName(CloudflareStore.data.ZoneName)
	existing := make([]cloudflareDDNSRecord, len(CloudflareStore.data.Records))
	copy(existing, CloudflareStore.data.Records)
	CloudflareStore.mu.RUnlock()
	if zoneName == "" {
		zoneName = savedZoneName
	}
	if zoneName == "" {
		return cloudflareDDNSApplyResponse{}, errors.New("zone_name is required")
	}
	if token == "" {
		return cloudflareDDNSApplyResponse{}, errors.New("cloudflare api key is not configured")
	}

	ProbeStore.mu.RLock()
	statusItems := loadProbeNodeStatusLocked()
	ProbeStore.mu.RUnlock()

	zoneID, err := cloudflareResolveZoneID(token, zoneName)
	if err != nil {
		return cloudflareDDNSApplyResponse{}, err
	}

	recordByKey := map[string]cloudflareDDNSRecord{}
	for _, item := range existing {
		key := cloudflareRecordKey(item.NodeID, item.RecordClass, item.RecordType, item.Sequence)
		recordByKey[key] = item
	}

	items := make([]cloudflareDDNSApplyItem, 0, len(statusItems)*4)
	next := make([]cloudflareDDNSRecord, 0, len(statusItems)*4)
	desiredRecordKeys := map[string]struct{}{}
	applied, skipped := 0, 0

	for _, node := range statusItems {
		desired := buildCloudflareDesiredRecords(node, zoneName)
		if len(desired) == 0 {
			items = append(items, cloudflareDDNSApplyItem{
				NodeID:      normalizeProbeNodeID(strconv.Itoa(node.NodeNo)),
				NodeNo:      node.NodeNo,
				NodeName:    strings.TrimSpace(node.NodeName),
				RecordClass: "-",
				RecordName:  "-",
				RecordType:  "-",
				Sequence:    0,
				Status:      "skipped",
				Message:     "no eligible public/local ip",
			})
			skipped++
			continue
		}

		for _, d := range desired {
			desiredKey := cloudflareRecordNameTypeKey(d.RecordName, d.RecordType)
			if desiredKey != "" {
				desiredRecordKeys[desiredKey] = struct{}{}
			}
			key := cloudflareRecordKey(d.NodeID, d.RecordClass, d.RecordType, d.Sequence)
			prev := recordByKey[key]
			targetZoneID := strings.TrimSpace(prev.ZoneID)
			if targetZoneID == "" {
				targetZoneID = zoneID
			}

			row := cloudflareDDNSApplyItem{
				NodeID:      d.NodeID,
				NodeNo:      d.NodeNo,
				NodeName:    d.NodeName,
				RecordClass: d.RecordClass,
				RecordName:  d.RecordName,
				RecordType:  d.RecordType,
				Sequence:    d.Sequence,
				ContentIP:   d.ContentIP,
			}
			action, ensuredRecordID, msg, ensureErr := cloudflareEnsureDNSRecord(token, targetZoneID, d.RecordName, d.RecordType, d.ContentIP, prev.RecordID)
			if ensureErr != nil {
				row.Status = "failed"
				row.Message = ensureErr.Error()
				items = append(items, row)
				skipped++
				continue
			}

			row.Status = action
			row.RecordID = ensuredRecordID
			row.Message = msg
			items = append(items, row)
			applied++
			next = append(next, cloudflareDDNSRecord{
				NodeID:      d.NodeID,
				NodeNo:      d.NodeNo,
				NodeName:    d.NodeName,
				ZoneName:    zoneName,
				ZoneID:      targetZoneID,
				RecordClass: d.RecordClass,
				RecordName:  d.RecordName,
				RecordID:    ensuredRecordID,
				RecordType:  d.RecordType,
				Sequence:    d.Sequence,
				ContentIP:   d.ContentIP,
				UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
				LastMessage: msg,
			})
		}
	}

	zoneRecords, listErr := cloudflareListDNSRecords(token, zoneID)
	if listErr != nil {
		items = append(items, cloudflareDDNSApplyItem{
			NodeID:      "cleanup",
			NodeNo:      0,
			NodeName:    "cleanup",
			RecordClass: "-",
			RecordName:  "-",
			RecordType:  "-",
			Sequence:    0,
			Status:      "failed",
			Message:     "list prefixed records failed: " + listErr.Error(),
		})
		skipped++
	} else {
		for _, zoneRecord := range zoneRecords {
			recordName := normalizeCloudflareRecordName(zoneRecord.Name)
			if !isCloudflareManagedDDNSRecordName(recordName) {
				continue
			}
			recordType := strings.TrimSpace(strings.ToUpper(zoneRecord.Type))
			if recordType == "" {
				continue
			}
			desiredKey := cloudflareRecordNameTypeKey(recordName, recordType)
			if desiredKey == "" {
				continue
			}
			if _, ok := desiredRecordKeys[desiredKey]; ok {
				continue
			}
			row := cloudflareDDNSApplyItem{
				NodeID:      "cleanup:" + strings.TrimSpace(zoneRecord.ID),
				NodeNo:      0,
				NodeName:    "cleanup",
				RecordClass: normalizeCloudflareRecordClass("", recordName),
				RecordName:  recordName,
				RecordType:  recordType,
				Sequence:    inferRecordSequence(recordName),
				RecordID:    strings.TrimSpace(zoneRecord.ID),
				ContentIP:   strings.TrimSpace(zoneRecord.Content),
			}
			if deleteErr := cloudflareDeleteDNSRecord(token, zoneID, zoneRecord.ID); deleteErr != nil {
				row.Status = "failed"
				row.Message = "delete stale prefixed record failed: " + deleteErr.Error()
				items = append(items, row)
				skipped++
				continue
			}
			row.Status = "deleted"
			row.Message = "deleted stale prefixed record not in desired ddns list"
			items = append(items, row)
			applied++
		}
	}

	next = normalizeCloudflareRecords(next)
	CloudflareStore.mu.Lock()
	CloudflareStore.data.ZoneName = zoneName
	CloudflareStore.data.Records = next
	CloudflareStore.mu.Unlock()
	if err := CloudflareStore.Save(); err != nil {
		return cloudflareDDNSApplyResponse{}, err
	}
	return cloudflareDDNSApplyResponse{ZoneName: zoneName, Applied: applied, Skipped: skipped, Items: items, Records: next}, nil
}

func notifyCloudflareRuntimeChanged(nodeID string, previousIPv4 []string, previousIPv6 []string, currentIPv4 []string, currentIPv6 []string) {
	normalizedNodeID := normalizeProbeNodeID(nodeID)
	if normalizedNodeID == "" {
		return
	}
	oldGroups := collectCloudflareIPs(previousIPv4, previousIPv6)
	newGroups := collectCloudflareIPs(currentIPv4, currentIPv6)
	if ipGroupsEqual(oldGroups, newGroups) {
		return
	}
	queueCloudflareAutoDDNSSync(normalizedNodeID)
}

func queueCloudflareAutoDDNSSync(nodeID string) {
	nodeID = normalizeProbeNodeID(nodeID)
	if nodeID == "" {
		return
	}
	cloudflareAutoDDNSSyncState.mu.Lock()
	cloudflareAutoDDNSSyncState.pending[nodeID] = true
	if cloudflareAutoDDNSSyncState.running[nodeID] {
		cloudflareAutoDDNSSyncState.mu.Unlock()
		return
	}
	cloudflareAutoDDNSSyncState.running[nodeID] = true
	cloudflareAutoDDNSSyncState.mu.Unlock()
	go runCloudflareAutoDDNSSync(nodeID)
}

func runCloudflareAutoDDNSSync(nodeID string) {
	for {
		cloudflareAutoDDNSSyncState.mu.Lock()
		hasPending := cloudflareAutoDDNSSyncState.pending[nodeID]
		if hasPending {
			delete(cloudflareAutoDDNSSyncState.pending, nodeID)
		} else {
			delete(cloudflareAutoDDNSSyncState.running, nodeID)
			cloudflareAutoDDNSSyncState.mu.Unlock()
			return
		}
		cloudflareAutoDDNSSyncState.mu.Unlock()

		if err := applyCloudflareAutoDDNSForNode(nodeID); err != nil {
			log.Printf("cloudflare auto ddns sync failed: node_id=%s err=%v", nodeID, err)
		}
	}
}

func applyCloudflareAutoDDNSForNode(nodeID string) error {
	if CloudflareStore == nil {
		return nil
	}
	nodeID = normalizeProbeNodeID(nodeID)
	if nodeID == "" {
		return nil
	}

	CloudflareStore.mu.RLock()
	token := strings.TrimSpace(CloudflareStore.data.APIToken)
	defaultZoneName := normalizeCloudflareZoneName(CloudflareStore.data.ZoneName)
	records := make([]cloudflareDDNSRecord, len(CloudflareStore.data.Records))
	copy(records, CloudflareStore.data.Records)
	CloudflareStore.mu.RUnlock()
	if token == "" {
		return nil
	}

	runtime, ok := getProbeRuntime(nodeID)
	if !ok {
		return nil
	}

	nodeNo := parsePositiveInt(nodeID)
	nodeName := nodeID
	zoneName := ""
	recordByKey := map[string]cloudflareDDNSRecord{}
	for _, item := range records {
		if normalizeProbeNodeID(item.NodeID) != nodeID {
			continue
		}
		itemZoneName := normalizeCloudflareZoneName(item.ZoneName)
		if itemZoneName != "" {
			zoneName = itemZoneName
		}
		if nodeNo <= 0 && item.NodeNo > 0 {
			nodeNo = item.NodeNo
		}
		if nodeName == nodeID && strings.TrimSpace(item.NodeName) != "" {
			nodeName = strings.TrimSpace(item.NodeName)
		}
		key := cloudflareRecordKey(item.NodeID, item.RecordClass, item.RecordType, item.Sequence)
		recordByKey[key] = item
	}
	if zoneName == "" {
		zoneName = defaultZoneName
	}
	if zoneName == "" {
		return nil
	}
	status := probeNodeStatusRecord{NodeNo: nodeNo, NodeName: nodeName, Runtime: runtime}
	desired := buildCloudflareDesiredRecords(status, zoneName)
	if len(desired) == 0 {
		return nil
	}

	resolvedZoneID := ""
	upserts := make([]cloudflareDDNSRecord, 0, len(desired))
	var firstErr error
	for _, d := range desired {
		key := cloudflareRecordKey(d.NodeID, d.RecordClass, d.RecordType, d.Sequence)
		prev := recordByKey[key]
		targetZoneID := strings.TrimSpace(prev.ZoneID)
		if targetZoneID == "" {
			if resolvedZoneID == "" {
				zoneID, err := cloudflareResolveZoneID(token, zoneName)
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				resolvedZoneID = zoneID
			}
			targetZoneID = resolvedZoneID
		}
		action, ensuredRecordID, msg, err := cloudflareEnsureDNSRecord(token, targetZoneID, d.RecordName, d.RecordType, d.ContentIP, prev.RecordID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		log.Printf("cloudflare auto ddns %s: node=%s class=%s type=%s seq=%d ip=%s", action, d.NodeID, d.RecordClass, d.RecordType, d.Sequence, d.ContentIP)
		upserts = append(upserts, cloudflareDDNSRecord{
			NodeID:      d.NodeID,
			NodeNo:      d.NodeNo,
			NodeName:    d.NodeName,
			ZoneName:    zoneName,
			ZoneID:      targetZoneID,
			RecordClass: d.RecordClass,
			RecordName:  d.RecordName,
			RecordID:    ensuredRecordID,
			RecordType:  d.RecordType,
			Sequence:    d.Sequence,
			ContentIP:   d.ContentIP,
			UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
			LastMessage: "auto " + strings.TrimSpace(msg),
		})
	}
	if len(upserts) == 0 {
		return firstErr
	}

	CloudflareStore.mu.Lock()
	merged := make([]cloudflareDDNSRecord, 0, len(CloudflareStore.data.Records)+len(upserts))
	merged = append(merged, CloudflareStore.data.Records...)
	for _, next := range upserts {
		nextKey := cloudflareRecordKey(next.NodeID, next.RecordClass, next.RecordType, next.Sequence)
		replaced := false
		for i := range merged {
			if cloudflareRecordKey(merged[i].NodeID, merged[i].RecordClass, merged[i].RecordType, merged[i].Sequence) != nextKey {
				continue
			}
			merged[i] = next
			replaced = true
			break
		}
		if !replaced {
			merged = append(merged, next)
		}
	}
	CloudflareStore.data.Records = normalizeCloudflareRecords(merged)
	CloudflareStore.mu.Unlock()

	if err := CloudflareStore.Save(); err != nil {
		return err
	}
	return firstErr
}

func buildCloudflareDesiredRecords(node probeNodeStatusRecord, zoneName string) []cloudflareDesiredRecord {
	zoneName = strings.TrimSpace(strings.ToLower(zoneName))
	if zoneName == "" {
		return []cloudflareDesiredRecord{}
	}

	nodeNo := node.NodeNo
	if nodeNo <= 0 {
		nodeNo = parsePositiveInt(node.Runtime.NodeID)
	}
	if nodeNo <= 0 {
		return []cloudflareDesiredRecord{}
	}
	nodeID := normalizeProbeNodeID(strconv.Itoa(nodeNo))
	if nodeID == "" {
		nodeID = normalizeProbeNodeID(node.Runtime.NodeID)
	}
	if nodeID == "" {
		return []cloudflareDesiredRecord{}
	}
	nodeName := strings.TrimSpace(node.NodeName)
	if nodeName == "" {
		nodeName = nodeID
	}

	customTag := ""
	if probe, ok := getProbeNodeByID(nodeID); ok {
		customTag = strings.TrimSpace(probe.DDNS)
	}
	basePublic := buildCloudflareRecordBase(nodeNo, "public", customTag)
	baseLocal := buildCloudflareRecordBase(nodeNo, "local", customTag)
	baseBusiness := buildCloudflareBusinessRecordBase(nodeNo)
	if basePublic == "" || baseLocal == "" || baseBusiness == "" {
		return []cloudflareDesiredRecord{}
	}

	groups := collectCloudflareIPs(node.Runtime.IPv4, node.Runtime.IPv6)
	out := make([]cloudflareDesiredRecord, 0, len(groups.PublicIPv4)+len(groups.PublicIPv6)+len(groups.LocalIPv4)+len(groups.LocalIPv6)+len(groups.PublicIPv4)+len(groups.PublicIPv6))
	appendRecords := func(recordClass string, recordType string, base string, values []string) {
		for i, ip := range values {
			seq := i + 1
			recordName := buildCloudflareRecordName(base, zoneName, seq)
			if recordName == "" {
				continue
			}
			out = append(out, cloudflareDesiredRecord{
				NodeID:      nodeID,
				NodeNo:      nodeNo,
				NodeName:    nodeName,
				ZoneName:    zoneName,
				RecordClass: recordClass,
				RecordType:  recordType,
				Sequence:    seq,
				RecordName:  recordName,
				ContentIP:   ip,
			})
		}
	}
	appendRecords("public", "A", basePublic, groups.PublicIPv4)
	appendRecords("public", "AAAA", basePublic, groups.PublicIPv6)
	appendRecords("business", "A", baseBusiness, groups.PublicIPv4)
	appendRecords("business", "AAAA", baseBusiness, groups.PublicIPv6)
	appendRecords("local", "A", baseLocal, groups.LocalIPv4)
	appendRecords("local", "AAAA", baseLocal, groups.LocalIPv6)
	return out
}

func collectCloudflareIPs(ipv4 []string, ipv6 []string) cloudflareIPGroups {
	result := cloudflareIPGroups{PublicIPv4: []string{}, PublicIPv6: []string{}, LocalIPv4: []string{}, LocalIPv6: []string{}}
	seenPublic4 := map[string]struct{}{}
	seenPublic6 := map[string]struct{}{}
	seenLocal4 := map[string]struct{}{}
	seenLocal6 := map[string]struct{}{}

	for _, raw := range ipv4 {
		parsed := net.ParseIP(strings.TrimSpace(raw))
		ip4 := parsed.To4()
		if ip4 == nil {
			continue
		}
		canonical := ip4.String()
		if isPublicIPv4(ip4) {
			if _, ok := seenPublic4[canonical]; ok {
				continue
			}
			seenPublic4[canonical] = struct{}{}
			result.PublicIPv4 = append(result.PublicIPv4, canonical)
			continue
		}
		if isLocalIPv4(ip4) {
			if _, ok := seenLocal4[canonical]; ok {
				continue
			}
			seenLocal4[canonical] = struct{}{}
			result.LocalIPv4 = append(result.LocalIPv4, canonical)
		}
	}

	for _, raw := range ipv6 {
		parsed := net.ParseIP(strings.TrimSpace(raw))
		if parsed == nil || parsed.To16() == nil || parsed.To4() != nil {
			continue
		}
		canonical := parsed.String()
		if isPublicIPv6(parsed) {
			if _, ok := seenPublic6[canonical]; ok {
				continue
			}
			seenPublic6[canonical] = struct{}{}
			result.PublicIPv6 = append(result.PublicIPv6, canonical)
			continue
		}
		if isLocalIPv6(parsed) {
			if _, ok := seenLocal6[canonical]; ok {
				continue
			}
			seenLocal6[canonical] = struct{}{}
			result.LocalIPv6 = append(result.LocalIPv6, canonical)
		}
	}
	return result
}

func ipGroupsEqual(a cloudflareIPGroups, b cloudflareIPGroups) bool {
	return stringSliceEqual(a.PublicIPv4, b.PublicIPv4) &&
		stringSliceEqual(a.PublicIPv6, b.PublicIPv6) &&
		stringSliceEqual(a.LocalIPv4, b.LocalIPv4) &&
		stringSliceEqual(a.LocalIPv6, b.LocalIPv6)
}

func stringSliceEqual(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func buildCloudflareRecordBase(nodeNo int, recordClass string, customTag string) string {
	customTag = sanitizeCloudflareCustomTag(customTag)
	if customTag != "" {
		if normalizeCloudflareRecordClass(recordClass, "") == "local" {
			return "local_go_" + customTag
		}
		return "ddns_go_" + customTag
	}
	if nodeNo <= 0 {
		return ""
	}
	encoded := buildCloudflareNodeBase64Tag(nodeNo)
	if encoded == "" {
		return ""
	}
	if normalizeCloudflareRecordClass(recordClass, "") == "local" {
		return "local_go_" + encoded
	}
	return "ddns_go_" + encoded
}

func buildCloudflareBusinessRecordBase(nodeNo int) string {
	tag := buildCloudflareNodeBase64Tag(nodeNo)
	if tag == "" {
		return ""
	}
	return "api.codex." + tag
}

func buildCloudflareNodeBase64Tag(nodeNo int) string {
	if nodeNo <= 0 {
		return ""
	}
	return sanitizeCloudflareBase64Tag(base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(nodeNo))))
}

func buildCloudflareRecordName(base string, zoneName string, sequence int) string {
	base = strings.TrimSpace(strings.ToLower(base))
	zoneName = strings.TrimSpace(strings.ToLower(zoneName))
	if base == "" || zoneName == "" || sequence <= 0 {
		return ""
	}
	label := base
	if sequence > 1 {
		label = fmt.Sprintf("%s_%d", label, sequence)
	}
	return label + "." + zoneName
}

func sanitizeCloudflareCustomTag(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return ""
	}
	builder := strings.Builder{}
	for _, r := range value {
		isLetter := r >= 'a' && r <= 'z'
		isNumber := r >= '0' && r <= '9'
		if isLetter || isNumber || r == '-' || r == '_' {
			builder.WriteRune(r)
		}
	}
	return strings.Trim(builder.String(), "-_")
}

func sanitizeCloudflareBase64Tag(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return ""
	}
	builder := strings.Builder{}
	for _, r := range value {
		isLetter := r >= 'a' && r <= 'z'
		isNumber := r >= '0' && r <= '9'
		if isLetter || isNumber {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func cloudflareResolveZoneID(token, zoneName string) (string, error) {
	u := "https://api.cloudflare.com/client/v4/zones?name=" + url.QueryEscape(zoneName)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	withCloudflareAuth(req, token)
	resp, err := cloudflareHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("cloudflare zones request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("cloudflare zones status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed cloudflareZoneListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if !parsed.Success || len(parsed.Result) == 0 {
		return "", fmt.Errorf("cloudflare zone not found: %s", zoneName)
	}
	return strings.TrimSpace(parsed.Result[0].ID), nil
}

func cloudflareListDNSRecords(token, zoneID string) ([]cloudflareDNSRecord, error) {
	const perPage = 100
	records := make([]cloudflareDNSRecord, 0, perPage)
	for page := 1; page <= 200; page++ {
		u := "https://api.cloudflare.com/client/v4/zones/" + url.PathEscape(zoneID) + "/dns_records?per_page=" + strconv.Itoa(perPage) + "&page=" + strconv.Itoa(page)
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		withCloudflareAuth(req, token)
		resp, err := cloudflareHTTPClient().Do(req)
		if err != nil {
			return nil, fmt.Errorf("cloudflare list dns records failed: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("cloudflare list dns records status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var parsed cloudflareDNSRecordListResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, err
		}
		if !parsed.Success {
			return nil, errors.New("cloudflare list dns records failed")
		}
		records = append(records, parsed.Result...)
		totalPages := parsed.ResultInfo.TotalPages
		if totalPages > 0 && page >= totalPages {
			return records, nil
		}
		if totalPages <= 0 && len(parsed.Result) < perPage {
			return records, nil
		}
	}
	return nil, errors.New("cloudflare list dns records exceeded page limit")
}

func cloudflareEnsureDNSRecord(token, zoneID, recordName, recordType, contentIP, preferredRecordID string) (string, string, string, error) {
	recordType = normalizeCloudflareRecordType(recordType)
	recordName = strings.TrimSpace(strings.ToLower(recordName))
	contentIP = strings.TrimSpace(contentIP)
	if recordName == "" || contentIP == "" {
		return "", "", "", fmt.Errorf("record_name and content_ip are required")
	}
	recordID, currentIP, err := cloudflareFindDNSRecord(token, zoneID, recordName, recordType, preferredRecordID)
	if err != nil {
		return "", "", "", err
	}
	if recordID == "" {
		createdID, err := cloudflareCreateDNSRecord(token, zoneID, recordName, recordType, contentIP)
		if err != nil {
			return "", "", "", err
		}
		return "created", createdID, "created dns record", nil
	}
	if currentIP == contentIP {
		return "unchanged", recordID, "record already up-to-date", nil
	}
	if err := cloudflareUpdateDNSRecord(token, zoneID, recordID, recordName, recordType, contentIP); err != nil {
		return "", "", "", err
	}
	return "updated", recordID, "updated record ip", nil
}

func cloudflareFindDNSRecord(token, zoneID, recordName, recordType, preferredRecordID string) (string, string, error) {
	recordType = normalizeCloudflareRecordType(recordType)
	recordName = strings.TrimSpace(strings.ToLower(recordName))
	if strings.TrimSpace(preferredRecordID) != "" {
		record, err := cloudflareGetDNSRecordByID(token, zoneID, preferredRecordID)
		if err == nil && strings.EqualFold(strings.TrimSpace(record.Type), recordType) && strings.EqualFold(strings.TrimSpace(record.Name), recordName) {
			return strings.TrimSpace(record.ID), strings.TrimSpace(record.Content), nil
		}
	}
	u := "https://api.cloudflare.com/client/v4/zones/" + url.PathEscape(zoneID) + "/dns_records?type=" + url.QueryEscape(recordType) + "&name=" + url.QueryEscape(recordName)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", "", err
	}
	withCloudflareAuth(req, token)
	resp, err := cloudflareHTTPClient().Do(req)
	if err != nil {
		return "", "", fmt.Errorf("cloudflare list dns records failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("cloudflare list dns records status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed cloudflareDNSRecordListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", err
	}
	if !parsed.Success || len(parsed.Result) == 0 {
		return "", "", nil
	}
	return strings.TrimSpace(parsed.Result[0].ID), strings.TrimSpace(parsed.Result[0].Content), nil
}

func cloudflareGetDNSRecordByID(token, zoneID, recordID string) (struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}, error) {
	u := "https://api.cloudflare.com/client/v4/zones/" + url.PathEscape(zoneID) + "/dns_records/" + url.PathEscape(recordID)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
		}{}, err
	}
	withCloudflareAuth(req, token)
	resp, err := cloudflareHTTPClient().Do(req)
	if err != nil {
		return struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
		}{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
		}{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
		}{}, fmt.Errorf("cloudflare get dns record status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Success bool `json:"success"`
		Result  struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
		}{}, err
	}
	if !parsed.Success {
		return struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
		}{}, errors.New("cloudflare get dns record failed")
	}
	return parsed.Result, nil
}

func cloudflareCreateDNSRecord(token, zoneID, recordName, recordType, contentIP string) (string, error) {
	payload := map[string]interface{}{
		"type":    normalizeCloudflareRecordType(recordType),
		"name":    strings.TrimSpace(strings.ToLower(recordName)),
		"content": strings.TrimSpace(contentIP),
		"ttl":     60,
	}
	recordType = normalizeCloudflareRecordType(recordType)
	if recordType == "A" || recordType == "AAAA" || recordType == "CNAME" {
		payload["proxied"] = false
	}
	body, _ := json.Marshal(payload)
	u := "https://api.cloudflare.com/client/v4/zones/" + url.PathEscape(zoneID) + "/dns_records"
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	withCloudflareAuth(req, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cloudflareHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("cloudflare create dns record failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("cloudflare create dns record status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed cloudflareDNSRecordWriteResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if !parsed.Success {
		return "", errors.New("cloudflare create dns record failed")
	}
	return strings.TrimSpace(parsed.Result.ID), nil
}

func cloudflareUpdateDNSRecord(token, zoneID, recordID, recordName, recordType, contentIP string) error {
	payload := map[string]interface{}{
		"type":    normalizeCloudflareRecordType(recordType),
		"name":    strings.TrimSpace(strings.ToLower(recordName)),
		"content": strings.TrimSpace(contentIP),
		"ttl":     60,
	}
	recordType = normalizeCloudflareRecordType(recordType)
	if recordType == "A" || recordType == "AAAA" || recordType == "CNAME" {
		payload["proxied"] = false
	}
	body, _ := json.Marshal(payload)
	u := "https://api.cloudflare.com/client/v4/zones/" + url.PathEscape(zoneID) + "/dns_records/" + url.PathEscape(recordID)
	req, err := http.NewRequest(http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	withCloudflareAuth(req, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cloudflareHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare update dns record failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloudflare update dns record status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed cloudflareDNSRecordWriteResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}
	if !parsed.Success {
		return errors.New("cloudflare update dns record failed")
	}
	return nil
}

func cloudflareDeleteDNSRecord(token, zoneID, recordID string) error {
	recordID = strings.TrimSpace(recordID)
	if recordID == "" {
		return errors.New("record_id is required")
	}
	u := "https://api.cloudflare.com/client/v4/zones/" + url.PathEscape(zoneID) + "/dns_records/" + url.PathEscape(recordID)
	req, err := http.NewRequest(http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	withCloudflareAuth(req, token)
	resp, err := cloudflareHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare delete dns record failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloudflare delete dns record status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}
	if !parsed.Success {
		return errors.New("cloudflare delete dns record failed")
	}
	return nil
}

var cloudflareZeroTrustSyncState = struct {
	mu       sync.Mutex
	started  bool
	running  bool
	nextRun  time.Time
	lastTick time.Time
}{}

func defaultCloudflareZeroTrustWhitelistConfig() cloudflareZeroTrustWhitelistConfig {
	return cloudflareZeroTrustWhitelistConfig{
		Enabled:         false,
		PolicyName:      "",
		WhitelistRaw:    "",
		SyncIntervalSec: cloudflareZeroTrustDefaultSyncIntervalSec,
	}
}

func normalizeCloudflareZeroTrustWhitelistConfig(raw cloudflareZeroTrustWhitelistConfig) cloudflareZeroTrustWhitelistConfig {
	cfg := cloudflareZeroTrustWhitelistConfig{
		Enabled:         raw.Enabled,
		PolicyName:      strings.TrimSpace(raw.PolicyName),
		WhitelistRaw:    normalizeCloudflareZeroTrustWhitelistRaw(raw.WhitelistRaw),
		SyncIntervalSec: raw.SyncIntervalSec,
	}
	if cfg.SyncIntervalSec < cloudflareZeroTrustMinSyncIntervalSec {
		cfg.SyncIntervalSec = cloudflareZeroTrustDefaultSyncIntervalSec
	}
	return cfg
}

func normalizeCloudflareZeroTrustWhitelistSyncInfo(raw cloudflareZeroTrustWhitelistSyncInfo) cloudflareZeroTrustWhitelistSyncInfo {
	out := cloudflareZeroTrustWhitelistSyncInfo{
		Running:         raw.Running,
		LastRunAt:       strings.TrimSpace(raw.LastRunAt),
		LastSuccessAt:   strings.TrimSpace(raw.LastSuccessAt),
		LastStatus:      strings.TrimSpace(raw.LastStatus),
		LastMessage:     strings.TrimSpace(raw.LastMessage),
		LastPolicyID:    strings.TrimSpace(raw.LastPolicyID),
		LastPolicyName:  strings.TrimSpace(raw.LastPolicyName),
		LastAppliedIPs:  append([]string{}, raw.LastAppliedIPs...),
		LastSourceLines: raw.LastSourceLines,
	}
	for i := range out.LastAppliedIPs {
		out.LastAppliedIPs[i] = strings.TrimSpace(out.LastAppliedIPs[i])
	}
	out.LastAppliedIPs = uniqueSortedStrings(out.LastAppliedIPs)
	if out.LastSourceLines < 0 {
		out.LastSourceLines = 0
	}
	return out
}

func normalizeCloudflareZeroTrustWhitelistRaw(raw string) string {
	v := strings.ReplaceAll(raw, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	return strings.TrimSpace(v)
}

func cloudflareZeroTrustResponseFromStore(cfg cloudflareZeroTrustWhitelistConfig, syncInfo cloudflareZeroTrustWhitelistSyncInfo) cloudflareZeroTrustWhitelistResponse {
	normalizedCfg := normalizeCloudflareZeroTrustWhitelistConfig(cfg)
	normalizedSync := normalizeCloudflareZeroTrustWhitelistSyncInfo(syncInfo)
	return cloudflareZeroTrustWhitelistResponse{
		Enabled:         normalizedCfg.Enabled,
		PolicyName:      normalizedCfg.PolicyName,
		WhitelistRaw:    normalizedCfg.WhitelistRaw,
		SyncIntervalSec: normalizedCfg.SyncIntervalSec,
		Running:         normalizedSync.Running,
		LastRunAt:       normalizedSync.LastRunAt,
		LastSuccessAt:   normalizedSync.LastSuccessAt,
		LastStatus:      normalizedSync.LastStatus,
		LastMessage:     normalizedSync.LastMessage,
		LastPolicyID:    normalizedSync.LastPolicyID,
		LastPolicyName:  normalizedSync.LastPolicyName,
		LastAppliedIPs:  append([]string{}, normalizedSync.LastAppliedIPs...),
		LastSourceLines: normalizedSync.LastSourceLines,
	}
}

func getCloudflareZeroTrustWhitelist() cloudflareZeroTrustWhitelistResponse {
	if CloudflareStore == nil {
		return cloudflareZeroTrustWhitelistResponse{}
	}
	CloudflareStore.mu.RLock()
	cfg := CloudflareStore.data.ZeroTrust
	syncInfo := CloudflareStore.data.ZeroTrustLastSync
	CloudflareStore.mu.RUnlock()
	return cloudflareZeroTrustResponseFromStore(cfg, syncInfo)
}

func setCloudflareZeroTrustWhitelist(req cloudflareZeroTrustWhitelistRequest) (cloudflareZeroTrustWhitelistResponse, error) {
	if CloudflareStore == nil {
		return cloudflareZeroTrustWhitelistResponse{}, errors.New("cloudflare datastore is not initialized")
	}
	cfg := normalizeCloudflareZeroTrustWhitelistConfig(cloudflareZeroTrustWhitelistConfig{
		Enabled:         req.Enabled,
		PolicyName:      req.PolicyName,
		WhitelistRaw:    req.WhitelistRaw,
		SyncIntervalSec: req.SyncIntervalSec,
	})
	if cfg.Enabled {
		if strings.TrimSpace(cfg.PolicyName) == "" {
			return cloudflareZeroTrustWhitelistResponse{}, errors.New("policy_name is required when enabled")
		}
		if strings.TrimSpace(cfg.WhitelistRaw) == "" {
			return cloudflareZeroTrustWhitelistResponse{}, errors.New("whitelist_raw is required when enabled")
		}
	}

	CloudflareStore.mu.Lock()
	CloudflareStore.data.ZeroTrust = cfg
	syncInfo := normalizeCloudflareZeroTrustWhitelistSyncInfo(CloudflareStore.data.ZeroTrustLastSync)
	if !cfg.Enabled {
		syncInfo.Running = false
		syncInfo.LastStatus = "disabled"
		syncInfo.LastMessage = "zerotrust whitelist sync disabled"
	}
	CloudflareStore.data.ZeroTrustLastSync = syncInfo
	CloudflareStore.mu.Unlock()
	if err := CloudflareStore.Save(); err != nil {
		return cloudflareZeroTrustWhitelistResponse{}, err
	}
	triggerCloudflareZeroTrustSyncNow()
	return getCloudflareZeroTrustWhitelist(), nil
}

func runCloudflareZeroTrustWhitelistSync(force bool) (cloudflareZeroTrustWhitelistResponse, error) {
	if CloudflareStore == nil {
		return cloudflareZeroTrustWhitelistResponse{}, errors.New("cloudflare datastore is not initialized")
	}
	CloudflareStore.mu.RLock()
	token := strings.TrimSpace(CloudflareStore.data.APIToken)
	zoneName := normalizeCloudflareZoneName(CloudflareStore.data.ZoneName)
	cfg := normalizeCloudflareZeroTrustWhitelistConfig(CloudflareStore.data.ZeroTrust)
	CloudflareStore.mu.RUnlock()

	if token == "" {
		return cloudflareZeroTrustWhitelistResponse{}, errors.New("cloudflare api key is not configured")
	}
	if zoneName == "" {
		return cloudflareZeroTrustWhitelistResponse{}, errors.New("zone_name is required")
	}
	if !cfg.Enabled && !force {
		CloudflareStore.mu.Lock()
		syncInfo := normalizeCloudflareZeroTrustWhitelistSyncInfo(CloudflareStore.data.ZeroTrustLastSync)
		syncInfo.LastRunAt = time.Now().UTC().Format(time.RFC3339)
		syncInfo.LastStatus = "skipped"
		syncInfo.LastMessage = "zerotrust whitelist sync is disabled"
		syncInfo.Running = false
		CloudflareStore.data.ZeroTrustLastSync = syncInfo
		CloudflareStore.mu.Unlock()
		_ = CloudflareStore.Save()
		return getCloudflareZeroTrustWhitelist(), nil
	}

	policyName := strings.TrimSpace(cfg.PolicyName)
	if policyName == "" {
		return cloudflareZeroTrustWhitelistResponse{}, errors.New("policy_name is required")
	}

	ips, sourceLines, warnings, parseErr := parseCloudflareZeroTrustWhitelistIPs(cfg.WhitelistRaw)
	if parseErr != nil {
		updateCloudflareZeroTrustSyncInfo("failed", parseErr.Error(), "", policyName, nil, sourceLines, false)
		return getCloudflareZeroTrustWhitelist(), parseErr
	}
	if len(ips) == 0 {
		err := errors.New("whitelist has no valid ip after parsing")
		updateCloudflareZeroTrustSyncInfo("failed", err.Error(), "", policyName, nil, sourceLines, false)
		return getCloudflareZeroTrustWhitelist(), err
	}

	updateCloudflareZeroTrustSyncInfo("running", "running", "", policyName, nil, sourceLines, true)

	accountID, err := cloudflareResolveAccountIDByZone(token, zoneName)
	if err != nil {
		updateCloudflareZeroTrustSyncInfo("failed", err.Error(), "", policyName, nil, sourceLines, false)
		return getCloudflareZeroTrustWhitelist(), err
	}

	policies, err := cloudflareListZeroTrustPolicies(token, accountID)
	if err != nil {
		updateCloudflareZeroTrustSyncInfo("failed", err.Error(), "", policyName, nil, sourceLines, false)
		return getCloudflareZeroTrustWhitelist(), err
	}

	target, exists := findCloudflareZeroTrustPolicyByName(policies, policyName)
	ipIncludes := buildCloudflareZeroTrustIPIncludeEntries(ips)
	if !exists {
		created, createErr := cloudflareCreateZeroTrustBypassPolicy(token, accountID, policyName, ipIncludes)
		if createErr != nil {
			updateCloudflareZeroTrustSyncInfo("failed", createErr.Error(), "", policyName, nil, sourceLines, false)
			return getCloudflareZeroTrustWhitelist(), createErr
		}
		msg := "created zerotrust bypass policy and synchronized ip include"
		if len(warnings) > 0 {
			msg += "; warnings=" + strings.Join(warnings, "; ")
		}
		updateCloudflareZeroTrustSyncInfo("success", msg, created.ID, created.Name, ips, sourceLines, false)
		return getCloudflareZeroTrustWhitelist(), nil
	}

	mergedInclude := mergeCloudflareZeroTrustPolicyInclude(target.Include, ipIncludes)
	if strings.EqualFold(resolveCloudflareZeroTrustDecision(target.Raw), "bypass") && cloudflareZeroTrustIncludeEqual(target.Include, mergedInclude) {
		msg := "zerotrust policy already up-to-date"
		if len(warnings) > 0 {
			msg += "; warnings=" + strings.Join(warnings, "; ")
		}
		updateCloudflareZeroTrustSyncInfo("success", msg, target.ID, target.Name, ips, sourceLines, false)
		return getCloudflareZeroTrustWhitelist(), nil
	}

	updated, updateErr := cloudflareUpdateZeroTrustBypassPolicy(token, accountID, target.ID, target.Name, mergedInclude)
	if updateErr != nil {
		updateCloudflareZeroTrustSyncInfo("failed", updateErr.Error(), target.ID, target.Name, nil, sourceLines, false)
		return getCloudflareZeroTrustWhitelist(), updateErr
	}
	msg := "updated zerotrust bypass policy ip include"
	if len(warnings) > 0 {
		msg += "; warnings=" + strings.Join(warnings, "; ")
	}
	updateCloudflareZeroTrustSyncInfo("success", msg, updated.ID, updated.Name, ips, sourceLines, false)
	return getCloudflareZeroTrustWhitelist(), nil
}

func initCloudflareZeroTrustSyncEngine() {
	cloudflareZeroTrustSyncState.mu.Lock()
	if cloudflareZeroTrustSyncState.started {
		cloudflareZeroTrustSyncState.mu.Unlock()
		return
	}
	cloudflareZeroTrustSyncState.started = true
	cloudflareZeroTrustSyncState.nextRun = time.Now().Add(time.Duration(cloudflareZeroTrustDefaultSyncIntervalSec) * time.Second)
	cloudflareZeroTrustSyncState.mu.Unlock()

	go func() {
		ticker := time.NewTicker(time.Duration(cloudflareZeroTrustSchedulerTickIntervalSec) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			tryRunCloudflareZeroTrustSyncBySchedule()
		}
	}()
}

func triggerCloudflareZeroTrustSyncNow() {
	cloudflareZeroTrustSyncState.mu.Lock()
	cloudflareZeroTrustSyncState.nextRun = time.Now()
	cloudflareZeroTrustSyncState.mu.Unlock()
}

func tryRunCloudflareZeroTrustSyncBySchedule() {
	now := time.Now()
	cloudflareZeroTrustSyncState.mu.Lock()
	if cloudflareZeroTrustSyncState.running {
		cloudflareZeroTrustSyncState.mu.Unlock()
		return
	}
	if !cloudflareZeroTrustSyncState.nextRun.IsZero() && now.Before(cloudflareZeroTrustSyncState.nextRun) {
		cloudflareZeroTrustSyncState.mu.Unlock()
		return
	}
	cloudflareZeroTrustSyncState.running = true
	cloudflareZeroTrustSyncState.lastTick = now
	cloudflareZeroTrustSyncState.mu.Unlock()

	defer func() {
		interval := cloudflareZeroTrustDefaultSyncIntervalSec
		if CloudflareStore != nil {
			CloudflareStore.mu.RLock()
			cfg := normalizeCloudflareZeroTrustWhitelistConfig(CloudflareStore.data.ZeroTrust)
			CloudflareStore.mu.RUnlock()
			interval = cfg.SyncIntervalSec
		}
		cloudflareZeroTrustSyncState.mu.Lock()
		cloudflareZeroTrustSyncState.running = false
		cloudflareZeroTrustSyncState.nextRun = time.Now().Add(time.Duration(interval) * time.Second)
		cloudflareZeroTrustSyncState.mu.Unlock()
	}()

	if _, err := runCloudflareZeroTrustWhitelistSync(false); err != nil {
		log.Printf("cloudflare zerotrust scheduled sync failed: %v", err)
	}
}

func updateCloudflareZeroTrustSyncInfo(status, message, policyID, policyName string, appliedIPs []string, sourceLines int, running bool) {
	if CloudflareStore == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	CloudflareStore.mu.Lock()
	syncInfo := normalizeCloudflareZeroTrustWhitelistSyncInfo(CloudflareStore.data.ZeroTrustLastSync)
	syncInfo.Running = running
	syncInfo.LastRunAt = now
	syncInfo.LastStatus = strings.TrimSpace(status)
	syncInfo.LastMessage = strings.TrimSpace(message)
	syncInfo.LastPolicyID = strings.TrimSpace(policyID)
	syncInfo.LastPolicyName = strings.TrimSpace(policyName)
	syncInfo.LastSourceLines = sourceLines
	if appliedIPs != nil {
		syncInfo.LastAppliedIPs = uniqueSortedStrings(appliedIPs)
	}
	if strings.EqualFold(strings.TrimSpace(status), "success") {
		syncInfo.LastSuccessAt = now
	}
	CloudflareStore.data.ZeroTrustLastSync = syncInfo
	CloudflareStore.mu.Unlock()
	_ = CloudflareStore.Save()
}

func parseCloudflareZeroTrustWhitelistIPs(raw string) ([]string, int, []string, error) {
	normalized := normalizeCloudflareZeroTrustWhitelistRaw(raw)
	if normalized == "" {
		return []string{}, 0, nil, nil
	}
	lines := strings.Split(normalized, "\n")
	result := make([]string, 0, len(lines)*2)
	seen := map[string]struct{}{}
	warnings := make([]string, 0)
	sourceLines := 0
	for idx, line := range lines {
		v := strings.TrimSpace(line)
		if v == "" {
			continue
		}
		sourceLines++
		if sharp := strings.Index(v, "#"); sharp >= 0 {
			v = strings.TrimSpace(v[:sharp])
		}
		if v == "" {
			continue
		}
		normalizedCIDR, ok := normalizeCloudflareZeroTrustIPOrCIDR(v)
		if ok {
			if _, exists := seen[normalizedCIDR]; !exists {
				seen[normalizedCIDR] = struct{}{}
				result = append(result, normalizedCIDR)
			}
			continue
		}
		if !isPotentialDomainToken(v) {
			warnings = append(warnings, fmt.Sprintf("line %d ignored: invalid token %q", idx+1, v))
			continue
		}
		resolved, err := resolveCloudflareZeroTrustDomain(v)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("line %d domain resolve failed: %s", idx+1, err.Error()))
			continue
		}
		for _, cidr := range resolved {
			if _, exists := seen[cidr]; exists {
				continue
			}
			seen[cidr] = struct{}{}
			result = append(result, cidr)
		}
	}
	sort.Strings(result)
	return result, sourceLines, warnings, nil
}

func normalizeCloudflareZeroTrustIPOrCIDR(raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", false
	}
	if ip := net.ParseIP(v); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String() + "/32", true
		}
		if ip16 := ip.To16(); ip16 != nil {
			masked := ip16.Mask(net.CIDRMask(56, 128))
			return masked.String() + "/56", true
		}
	}
	if strings.Contains(v, "/") {
		ip, ipNet, err := net.ParseCIDR(v)
		if err != nil || ipNet == nil {
			return "", false
		}
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String() + "/32", true
		}
		if ip16 := ip.To16(); ip16 != nil {
			masked := ip16.Mask(net.CIDRMask(56, 128))
			return masked.String() + "/56", true
		}
	}
	return "", false
}

func resolveCloudflareZeroTrustDomain(domain string) ([]string, error) {
	host := strings.TrimSpace(strings.ToLower(domain))
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return nil, errors.New("empty domain")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ips))
	seen := map[string]struct{}{}
	for _, item := range ips {
		ip := item.IP
		if ip == nil {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			v := ip4.String() + "/32"
			if _, ok := seen[v]; !ok {
				seen[v] = struct{}{}
				out = append(out, v)
			}
			continue
		}
		if ip16 := ip.To16(); ip16 != nil {
			v := ip16.Mask(net.CIDRMask(56, 128)).String() + "/56"
			if _, ok := seen[v]; !ok {
				seen[v] = struct{}{}
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func isPotentialDomainToken(v string) bool {
	value := strings.TrimSpace(strings.ToLower(v))
	if value == "" || strings.Contains(value, " ") || strings.Contains(value, "/") {
		return false
	}
	if strings.Contains(value, ":") {
		return false
	}
	if net.ParseIP(value) != nil {
		return false
	}
	if !strings.Contains(value, ".") {
		return false
	}
	for _, r := range value {
		isLetter := r >= 'a' && r <= 'z'
		isNumber := r >= '0' && r <= '9'
		if isLetter || isNumber || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func uniqueSortedStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func cloudflareResolveAccountIDByZone(token, zoneName string) (string, error) {
	u := "https://api.cloudflare.com/client/v4/zones?name=" + url.QueryEscape(strings.TrimSpace(zoneName))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	withCloudflareAuth(req, token)
	resp, err := cloudflareHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("cloudflare zones request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("cloudflare zones status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed cloudflareZoneListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if !parsed.Success || len(parsed.Result) == 0 {
		return "", fmt.Errorf("cloudflare zone not found: %s", zoneName)
	}
	accountID := strings.TrimSpace(parsed.Result[0].Account.ID)
	if accountID == "" {
		return "", errors.New("cloudflare account id not found from zone")
	}
	return accountID, nil
}

func cloudflareListZeroTrustPolicies(token, accountID string) ([]cloudflareZeroTrustPolicyItem, error) {
	u := "https://api.cloudflare.com/client/v4/accounts/" + url.PathEscape(strings.TrimSpace(accountID)) + "/access/policies"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	withCloudflareAuth(req, token)
	resp, err := cloudflareHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare list zerotrust policies failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cloudflare list zerotrust policies status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Success bool                             `json:"success"`
		Result  []map[string]interface{}         `json:"result"`
		Errors  []map[string]interface{}         `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	if !parsed.Success {
		return nil, errors.New("cloudflare list zerotrust policies failed")
	}
	out := make([]cloudflareZeroTrustPolicyItem, 0, len(parsed.Result))
	for _, raw := range parsed.Result {
		id, _ := raw["id"].(string)
		name, _ := raw["name"].(string)
		include := parseCloudflarePolicyInclude(raw["include"])
		out = append(out, cloudflareZeroTrustPolicyItem{
			ID:      strings.TrimSpace(id),
			Name:    strings.TrimSpace(name),
			Include: include,
			Raw:     raw,
		})
	}
	return out, nil
}

func cloudflareCreateZeroTrustBypassPolicy(token, accountID, policyName string, include []map[string]interface{}) (cloudflareZeroTrustPolicyItem, error) {
	payload := map[string]interface{}{
		"name":     strings.TrimSpace(policyName),
		"decision": "bypass",
		"include":  include,
	}
	rawBody, _ := json.Marshal(payload)
	u := "https://api.cloudflare.com/client/v4/accounts/" + url.PathEscape(strings.TrimSpace(accountID)) + "/access/policies"
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(rawBody))
	if err != nil {
		return cloudflareZeroTrustPolicyItem{}, err
	}
	withCloudflareAuth(req, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cloudflareHTTPClient().Do(req)
	if err != nil {
		return cloudflareZeroTrustPolicyItem{}, fmt.Errorf("cloudflare create zerotrust policy failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return cloudflareZeroTrustPolicyItem{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cloudflareZeroTrustPolicyItem{}, fmt.Errorf("cloudflare create zerotrust policy status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return parseCloudflareZeroTrustPolicyFromWriteBody(body)
}

func cloudflareUpdateZeroTrustBypassPolicy(token, accountID, policyID, policyName string, include []map[string]interface{}) (cloudflareZeroTrustPolicyItem, error) {
	payload := map[string]interface{}{
		"name":     strings.TrimSpace(policyName),
		"decision": "bypass",
		"include":  include,
	}
	rawBody, _ := json.Marshal(payload)
	u := "https://api.cloudflare.com/client/v4/accounts/" + url.PathEscape(strings.TrimSpace(accountID)) + "/access/policies/" + url.PathEscape(strings.TrimSpace(policyID))
	req, err := http.NewRequest(http.MethodPut, u, bytes.NewReader(rawBody))
	if err != nil {
		return cloudflareZeroTrustPolicyItem{}, err
	}
	withCloudflareAuth(req, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cloudflareHTTPClient().Do(req)
	if err != nil {
		return cloudflareZeroTrustPolicyItem{}, fmt.Errorf("cloudflare update zerotrust policy failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return cloudflareZeroTrustPolicyItem{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cloudflareZeroTrustPolicyItem{}, fmt.Errorf("cloudflare update zerotrust policy status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return parseCloudflareZeroTrustPolicyFromWriteBody(body)
}

func parseCloudflareZeroTrustPolicyFromWriteBody(body []byte) (cloudflareZeroTrustPolicyItem, error) {
	var parsed struct {
		Success bool                   `json:"success"`
		Result  map[string]interface{} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return cloudflareZeroTrustPolicyItem{}, err
	}
	if !parsed.Success {
		return cloudflareZeroTrustPolicyItem{}, errors.New("cloudflare zerotrust policy write failed")
	}
	id, _ := parsed.Result["id"].(string)
	name, _ := parsed.Result["name"].(string)
	include := parseCloudflarePolicyInclude(parsed.Result["include"])
	return cloudflareZeroTrustPolicyItem{ID: strings.TrimSpace(id), Name: strings.TrimSpace(name), Include: include, Raw: parsed.Result}, nil
}

func parseCloudflarePolicyInclude(raw interface{}) []map[string]interface{} {
	values, ok := raw.([]interface{})
	if !ok {
		if typed, ok2 := raw.([]map[string]interface{}); ok2 {
			return typed
		}
		return []map[string]interface{}{}
	}
	out := make([]map[string]interface{}, 0, len(values))
	for _, item := range values {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		out = append(out, m)
	}
	return out
}

func findCloudflareZeroTrustPolicyByName(items []cloudflareZeroTrustPolicyItem, policyName string) (cloudflareZeroTrustPolicyItem, bool) {
	target := strings.TrimSpace(strings.ToLower(policyName))
	for _, item := range items {
		if strings.TrimSpace(strings.ToLower(item.Name)) == target {
			return item, true
		}
	}
	return cloudflareZeroTrustPolicyItem{}, false
}

func buildCloudflareZeroTrustIPIncludeEntries(ips []string) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(ips))
	for _, ip := range uniqueSortedStrings(ips) {
		out = append(out, map[string]interface{}{
			"ip": map[string]interface{}{
				"ip": ip,
			},
		})
	}
	return out
}

func mergeCloudflareZeroTrustPolicyInclude(existing []map[string]interface{}, ipInclude []map[string]interface{}) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(existing)+len(ipInclude))
	for _, item := range existing {
		if isCloudflareZeroTrustIPIncludeEntry(item) {
			continue
		}
		out = append(out, item)
	}
	out = append(out, ipInclude...)
	return out
}

func isCloudflareZeroTrustIPIncludeEntry(item map[string]interface{}) bool {
	if item == nil {
		return false
	}
	v, ok := item["ip"]
	if !ok {
		return false
	}
	if ipMap, ok := v.(map[string]interface{}); ok {
		if value, ok := ipMap["ip"].(string); ok {
			return strings.TrimSpace(value) != ""
		}
	}
	if ipText, ok := v.(string); ok {
		return strings.TrimSpace(ipText) != ""
	}
	return false
}

func cloudflareZeroTrustIncludeEqual(left []map[string]interface{}, right []map[string]interface{}) bool {
	leftRaw, _ := json.Marshal(left)
	rightRaw, _ := json.Marshal(right)
	return bytes.Equal(leftRaw, rightRaw)
}

func resolveCloudflareZeroTrustDecision(raw map[string]interface{}) string {
	if raw == nil {
		return ""
	}
	value, _ := raw["decision"].(string)
	return strings.TrimSpace(strings.ToLower(value))
}

func withCloudflareAuth(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cloudhelper-cloudflare-assistant")
}

func cloudflareHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}

func cloudflareRecordKey(nodeID string, recordClass string, recordType string, sequence int) string {
	nodeID = normalizeProbeNodeID(nodeID)
	recordClass = normalizeCloudflareRecordClass(recordClass, "")
	recordType = normalizeCloudflareRecordType(recordType)
	if sequence <= 0 {
		sequence = 1
	}
	return nodeID + "|" + recordClass + "|" + recordType + "|" + strconv.Itoa(sequence)
}

func normalizeCloudflareRecordType(raw string) string {
	value := strings.TrimSpace(strings.ToUpper(raw))
	if value == "AAAA" {
		return "AAAA"
	}
	if value == "TXT" {
		return "TXT"
	}
	if value == "CNAME" {
		return "CNAME"
	}
	return "A"
}

func normalizeCloudflareRecordName(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	value = strings.TrimSuffix(value, ".")
	return strings.TrimSpace(value)
}

func cloudflareRecordNameTypeKey(recordName string, recordType string) string {
	recordName = normalizeCloudflareRecordName(recordName)
	recordType = strings.TrimSpace(strings.ToUpper(recordType))
	if recordName == "" || recordType == "" {
		return ""
	}
	return recordName + "|" + recordType
}

func isCloudflareManagedDDNSRecordName(recordName string) bool {
	recordName = normalizeCloudflareRecordName(recordName)
	if recordName == "" {
		return false
	}
	if strings.HasPrefix(recordName, cloudflareManagedBusinessNamePrefix) {
		return true
	}
	label := recordName
	if dot := strings.Index(label, "."); dot > 0 {
		label = label[:dot]
	}
	for _, prefix := range cloudflareManagedLabelPrefixes {
		if strings.HasPrefix(label, prefix) {
			return true
		}
	}
	return false
}

func normalizeCloudflareZoneName(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	value = strings.TrimSuffix(value, ".")
	return strings.TrimSpace(value)
}

func normalizeCloudflareRecordClass(raw string, recordName string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "public" || value == "local" || value == "business" {
		return value
	}
	fullName := strings.TrimSpace(strings.ToLower(recordName))
	if strings.HasPrefix(fullName, "api.codex.") {
		return "business"
	}
	name := fullName
	if dot := strings.Index(name, "."); dot > 0 {
		name = name[:dot]
	}
	if strings.HasPrefix(name, "local_go_") {
		return "local"
	}
	return "public"
}

func normalizeCloudflareRecords(records []cloudflareDDNSRecord) []cloudflareDDNSRecord {
	normalized := make([]cloudflareDDNSRecord, 0, len(records))
	seen := map[string]struct{}{}
	for _, item := range records {
		item.NodeID = normalizeProbeNodeID(item.NodeID)
		if item.NodeID == "" {
			item.NodeID = normalizeProbeNodeID(strconv.Itoa(item.NodeNo))
		}
		item.NodeName = strings.TrimSpace(item.NodeName)
		item.ZoneName = normalizeCloudflareZoneName(item.ZoneName)
		item.ZoneID = strings.TrimSpace(item.ZoneID)
		item.RecordClass = normalizeCloudflareRecordClass(item.RecordClass, item.RecordName)
		item.RecordName = strings.TrimSpace(strings.ToLower(item.RecordName))
		item.RecordID = strings.TrimSpace(item.RecordID)
		item.RecordType = normalizeCloudflareRecordType(item.RecordType)
		if item.Sequence <= 0 {
			item.Sequence = inferRecordSequence(item.RecordName)
		}
		if item.Sequence <= 0 {
			item.Sequence = 1
		}
		item.ContentIP = strings.TrimSpace(item.ContentIP)
		item.UpdatedAt = strings.TrimSpace(item.UpdatedAt)
		item.LastMessage = strings.TrimSpace(item.LastMessage)
		if item.NodeID == "" || item.RecordName == "" {
			continue
		}
		key := cloudflareRecordKey(item.NodeID, item.RecordClass, item.RecordType, item.Sequence)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, item)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].NodeNo != normalized[j].NodeNo {
			return normalized[i].NodeNo < normalized[j].NodeNo
		}
		if normalized[i].RecordClass != normalized[j].RecordClass {
			return normalized[i].RecordClass < normalized[j].RecordClass
		}
		if normalized[i].RecordType != normalized[j].RecordType {
			return normalized[i].RecordType < normalized[j].RecordType
		}
		if normalized[i].Sequence != normalized[j].Sequence {
			return normalized[i].Sequence < normalized[j].Sequence
		}
		return normalized[i].RecordName < normalized[j].RecordName
	})
	return normalized
}

func inferRecordSequence(recordName string) int {
	name := strings.TrimSpace(strings.ToLower(recordName))
	if name == "" {
		return 0
	}
	label := name
	if dot := strings.Index(label, "."); dot > 0 {
		label = label[:dot]
	}
	idx := strings.LastIndex(label, "_")
	if idx <= 0 || idx >= len(label)-1 {
		return 1
	}
	seq, err := strconv.Atoi(strings.TrimSpace(label[idx+1:]))
	if err != nil || seq <= 0 {
		return 1
	}
	return seq
}

func parsePositiveInt(raw string) int {
	v := strings.TrimSpace(raw)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func isPublicIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	if ip4[0] == 10 || ip4[0] == 127 || ip4[0] == 0 {
		return false
	}
	if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
		return false
	}
	if ip4[0] == 192 && ip4[1] == 168 {
		return false
	}
	if ip4[0] == 169 && ip4[1] == 254 {
		return false
	}
	if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return false
	}
	if ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19) {
		return false
	}
	if ip4[0] >= 224 {
		return false
	}
	return true
}

func isLocalIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	if ip4[0] == 10 {
		return true
	}
	if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
		return true
	}
	if ip4[0] == 192 && ip4[1] == 168 {
		return true
	}
	if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true
	}
	return false
}

func isPublicIPv6(ip net.IP) bool {
	if ip == nil || ip.To16() == nil || ip.To4() != nil {
		return false
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
		return false
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if isUniqueLocalIPv6(ip) {
		return false
	}
	return true
}

func isLocalIPv6(ip net.IP) bool {
	if ip == nil || ip.To16() == nil || ip.To4() != nil {
		return false
	}
	return isUniqueLocalIPv6(ip)
}

func isUniqueLocalIPv6(ip net.IP) bool {
	ip16 := ip.To16()
	if ip16 == nil || ip.To4() != nil {
		return false
	}
	return (ip16[0] & 0xfe) == 0xfc
}
