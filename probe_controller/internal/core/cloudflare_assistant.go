package core

import (
	"bytes"
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

const cloudflareStoreFile = "cloudflare.json"

type cloudflareStore struct {
	mu   sync.RWMutex
	path string
	data cloudflareStoreData
}

type cloudflareStoreData struct {
	APIToken string                 `json:"api_token"`
	ZoneName string                 `json:"zone_name"`
	Records  []cloudflareDDNSRecord `json:"records"`
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
		ID string `json:"id"`
	} `json:"result"`
}

type cloudflareDNSRecordListResponse struct {
	Success bool `json:"success"`
	Result  []struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Name    string `json:"name"`
		Content string `json:"content"`
	} `json:"result"`
}

type cloudflareDNSRecordWriteResponse struct {
	Success bool `json:"success"`
	Result  struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Name    string `json:"name"`
		Content string `json:"content"`
	} `json:"result"`
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
	CloudflareStore = &cloudflareStore{path: storePath, data: cloudflareStoreData{Records: []cloudflareDDNSRecord{}}}

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
	if len(statusItems) == 0 {
		return cloudflareDDNSApplyResponse{ZoneName: zoneName, Items: []cloudflareDDNSApplyItem{}, Records: normalizeCloudflareRecords(existing)}, nil
	}

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
	payload := map[string]interface{}{"type": normalizeCloudflareRecordType(recordType), "name": strings.TrimSpace(strings.ToLower(recordName)), "content": strings.TrimSpace(contentIP), "ttl": 60, "proxied": false}
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
	payload := map[string]interface{}{"type": normalizeCloudflareRecordType(recordType), "name": strings.TrimSpace(strings.ToLower(recordName)), "content": strings.TrimSpace(contentIP), "ttl": 60, "proxied": false}
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
	return "A"
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
