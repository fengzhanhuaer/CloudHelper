package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	probeDDNSCloudflareBaseURL       = "https://api.cloudflare.com/client/v4"
	probeDDNSCloudflareRecordComment = "Managed by CloudHelper Probe DDNS"
)

type probeDDNSCloudflareZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type probeDDNSCloudflareRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Comment string `json:"comment"`
}

type probeDDNSCloudflareResultInfo struct {
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
}

var probeDDNSCloudflareHTTPClient = func() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}

func reconcileProbeDDNS(ctx context.Context) error {
	config, err := loadProbeDDNSConfig()
	if err != nil {
		return err
	}
	if !config.Enabled {
		return nil
	}
	if strings.TrimSpace(config.APIToken) == "" {
		return errors.New("cloudflare api token is required")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_ = updateProbeDDNSState(func(state *probeDDNSState) {
		state.LastSyncStartedAt = now
		state.LastSyncStatus = "running"
		state.LastSyncError = ""
	})

	state, err := loadProbeDDNSState()
	if err != nil {
		return finishProbeDDNSSync(err)
	}
	zones, err := listProbeDDNSCloudflareZones(ctx, config.APIToken)
	if err != nil {
		return finishProbeDDNSSync(err)
	}

	nextRecords := dropProbeDDNSUnconfiguredSourceRecords(state.ManagedRecords, "interface", config.InterfaceDomains)
	nextRecords = dropProbeDDNSUnconfiguredSourceRecords(nextRecords, "public", config.PublicDomains)
	var allErrors []error
	interfaceAddresses := probeDDNSAddressSet{}
	if len(config.InterfaceDomains) > 0 {
		interfaceAddresses, err = collectProbeDDNSInterfaceAddresses(config.SelectedInterfaceID)
		if err != nil {
			allErrors = append(allErrors, fmt.Errorf("interface source: %w", err))
		} else if len(interfaceAddresses.IPv4) == 0 && len(interfaceAddresses.IPv6) == 0 {
			allErrors = append(allErrors, errors.New("interface source has no eligible ip"))
		} else {
			nextRecords, err = reconcileProbeDDNSSource(ctx, config.APIToken, zones, "interface", config.InterfaceDomains, interfaceAddresses, nextRecords)
			if err != nil {
				allErrors = append(allErrors, err)
			}
		}
	}

	publicAddresses := probeDDNSAddressSet{}
	if len(config.PublicDomains) > 0 {
		publicAddresses, err = collectProbeDDNSPublicAddresses()
		if err != nil {
			allErrors = append(allErrors, fmt.Errorf("public source: %w", err))
		} else {
			nextRecords, err = reconcileProbeDDNSSource(ctx, config.APIToken, zones, "public", config.PublicDomains, publicAddresses, nextRecords)
			if err != nil {
				allErrors = append(allErrors, err)
			}
		}
	}

	resultErr := errors.Join(allErrors...)
	persistErr := updateProbeDDNSState(func(latest *probeDDNSState) {
		latest.ManagedRecords = nextRecords
		latest.InterfaceIPv4 = append([]string{}, interfaceAddresses.IPv4...)
		latest.InterfaceIPv6 = append([]string{}, interfaceAddresses.IPv6...)
		latest.PublicIPv4 = append([]string{}, publicAddresses.IPv4...)
		latest.PublicIPv6 = append([]string{}, publicAddresses.IPv6...)
		latest.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
		if resultErr != nil {
			latest.LastSyncStatus = "failed"
			latest.LastSyncError = resultErr.Error()
		} else {
			latest.LastSyncStatus = "success"
			latest.LastSyncError = ""
		}
	})
	if persistErr != nil {
		resultErr = errors.Join(resultErr, persistErr)
	}
	return resultErr
}

func finishProbeDDNSSync(syncErr error) error {
	_ = updateProbeDDNSState(func(state *probeDDNSState) {
		state.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
		state.LastSyncStatus = "failed"
		state.LastSyncError = syncErr.Error()
	})
	return syncErr
}

func reconcileProbeDDNSSource(ctx context.Context, token string, zones []probeDDNSCloudflareZone, source string, domains []string, addresses probeDDNSAddressSet, existing []probeDDNSManagedRecord) ([]probeDDNSManagedRecord, error) {
	domainSet := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		domainSet[domain] = struct{}{}
	}

	kept := make([]probeDDNSManagedRecord, 0, len(existing))
	current := make(map[string]probeDDNSManagedRecord)
	for _, record := range existing {
		if record.Source != source {
			kept = append(kept, record)
			continue
		}
		if _, configured := domainSet[record.Domain]; !configured {
			// Removing a configured domain intentionally leaves its remote record intact.
			continue
		}
		current[probeDDNSManagedRecordKey(record)] = record
	}

	type desiredRecord struct {
		domain     string
		recordType string
		content    string
	}
	desired := make([]desiredRecord, 0, len(domains)*(len(addresses.IPv4)+len(addresses.IPv6)))
	for _, domain := range domains {
		for _, ip := range addresses.IPv4 {
			desired = append(desired, desiredRecord{domain: domain, recordType: "A", content: ip})
		}
		for _, ip := range addresses.IPv6 {
			desired = append(desired, desiredRecord{domain: domain, recordType: "AAAA", content: ip})
		}
	}

	var operationErrors []error
	for _, item := range desired {
		candidate := probeDDNSManagedRecord{Source: source, Domain: item.domain, RecordType: item.recordType, Content: item.content}
		key := probeDDNSManagedRecordKey(candidate)
		previous := current[key]
		zone, zoneErr := matchProbeDDNSCloudflareZone(item.domain, zones)
		if zoneErr != nil {
			operationErrors = append(operationErrors, zoneErr)
			if previous.RecordID != "" {
				kept = append(kept, previous)
			}
			delete(current, key)
			continue
		}
		recordID, ensureErr := ensureProbeDDNSCloudflareRecord(ctx, token, zone.ID, item.domain, item.recordType, item.content, previous.RecordID)
		if ensureErr != nil {
			operationErrors = append(operationErrors, fmt.Errorf("ensure %s %s %s: %w", item.domain, item.recordType, item.content, ensureErr))
			if previous.RecordID != "" {
				kept = append(kept, previous)
			}
			delete(current, key)
			continue
		}
		candidate.ZoneID = zone.ID
		candidate.RecordID = recordID
		kept = append(kept, candidate)
		delete(current, key)
	}

	for _, stale := range current {
		if err := deleteProbeDDNSCloudflareRecord(ctx, token, stale.ZoneID, stale.RecordID); err != nil {
			operationErrors = append(operationErrors, fmt.Errorf("delete stale %s %s %s: %w", stale.Domain, stale.RecordType, stale.Content, err))
			kept = append(kept, stale)
		}
	}
	return normalizeProbeDDNSManagedRecords(kept), errors.Join(operationErrors...)
}

func dropProbeDDNSUnconfiguredSourceRecords(records []probeDDNSManagedRecord, source string, configuredDomains []string) []probeDDNSManagedRecord {
	configured := map[string]struct{}{}
	for _, domain := range configuredDomains {
		configured[domain] = struct{}{}
	}
	out := make([]probeDDNSManagedRecord, 0, len(records))
	for _, record := range records {
		if record.Source != source {
			out = append(out, record)
			continue
		}
		if _, ok := configured[record.Domain]; ok {
			out = append(out, record)
		}
	}
	return normalizeProbeDDNSManagedRecords(out)
}

func listProbeDDNSCloudflareZones(ctx context.Context, token string) ([]probeDDNSCloudflareZone, error) {
	zones := []probeDDNSCloudflareZone{}
	for page := 1; page <= 20; page++ {
		endpoint := probeDDNSCloudflareBaseURL + "/zones?per_page=50&page=" + strconv.Itoa(page)
		var response struct {
			Success    bool                          `json:"success"`
			Result     []probeDDNSCloudflareZone     `json:"result"`
			ResultInfo probeDDNSCloudflareResultInfo `json:"result_info"`
			Errors     []map[string]any              `json:"errors"`
		}
		if err := doProbeDDNSCloudflareJSON(ctx, token, http.MethodGet, endpoint, nil, &response); err != nil {
			return nil, err
		}
		if !response.Success {
			return nil, errors.New("cloudflare list zones failed")
		}
		zones = append(zones, response.Result...)
		if response.ResultInfo.TotalPages <= page || len(response.Result) == 0 {
			break
		}
	}
	for i := range zones {
		zones[i].ID = strings.TrimSpace(zones[i].ID)
		zones[i].Name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zones[i].Name)), ".")
	}
	sort.Slice(zones, func(i, j int) bool { return len(zones[i].Name) > len(zones[j].Name) })
	return zones, nil
}

func matchProbeDDNSCloudflareZone(domain string, zones []probeDDNSCloudflareZone) (probeDDNSCloudflareZone, error) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	best := probeDDNSCloudflareZone{}
	for _, zone := range zones {
		if zone.ID == "" || zone.Name == "" {
			continue
		}
		if (domain == zone.Name || strings.HasSuffix(domain, "."+zone.Name)) && len(zone.Name) > len(best.Name) {
			best = zone
		}
	}
	if best.ID != "" {
		return best, nil
	}
	return probeDDNSCloudflareZone{}, fmt.Errorf("no accessible cloudflare zone matches %s", domain)
}

func ensureProbeDDNSCloudflareRecord(ctx context.Context, token, zoneID, domain, recordType, content, preferredID string) (string, error) {
	if preferredID != "" {
		record, err := getProbeDDNSCloudflareRecord(ctx, token, zoneID, preferredID)
		if err == nil && strings.EqualFold(record.Name, domain) && strings.EqualFold(record.Type, recordType) && probeDDNSCloudflareRecordIsManaged(record) {
			if strings.TrimSpace(record.Content) != content {
				if err := updateProbeDDNSCloudflareRecord(ctx, token, zoneID, preferredID, domain, recordType, content); err != nil {
					return "", err
				}
			}
			return preferredID, nil
		}
	}
	records, err := listProbeDDNSCloudflareRecords(ctx, token, zoneID, domain, recordType)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if strings.TrimSpace(record.Content) == content && probeDDNSCloudflareRecordIsManaged(record) {
			return strings.TrimSpace(record.ID), nil
		}
	}
	return createProbeDDNSCloudflareRecord(ctx, token, zoneID, domain, recordType, content)
}

func probeDDNSCloudflareRecordIsManaged(record probeDDNSCloudflareRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Comment), probeDDNSCloudflareRecordComment)
}

func listProbeDDNSCloudflareRecords(ctx context.Context, token, zoneID, domain, recordType string) ([]probeDDNSCloudflareRecord, error) {
	endpoint := probeDDNSCloudflareBaseURL + "/zones/" + url.PathEscape(zoneID) + "/dns_records?name=" + url.QueryEscape(domain) + "&type=" + url.QueryEscape(recordType) + "&per_page=100"
	var response struct {
		Success bool                        `json:"success"`
		Result  []probeDDNSCloudflareRecord `json:"result"`
	}
	if err := doProbeDDNSCloudflareJSON(ctx, token, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	if !response.Success {
		return nil, errors.New("cloudflare list dns records failed")
	}
	return response.Result, nil
}

func getProbeDDNSCloudflareRecord(ctx context.Context, token, zoneID, recordID string) (probeDDNSCloudflareRecord, error) {
	endpoint := probeDDNSCloudflareBaseURL + "/zones/" + url.PathEscape(zoneID) + "/dns_records/" + url.PathEscape(recordID)
	var response struct {
		Success bool                      `json:"success"`
		Result  probeDDNSCloudflareRecord `json:"result"`
	}
	if err := doProbeDDNSCloudflareJSON(ctx, token, http.MethodGet, endpoint, nil, &response); err != nil {
		return probeDDNSCloudflareRecord{}, err
	}
	if !response.Success {
		return probeDDNSCloudflareRecord{}, errors.New("cloudflare get dns record failed")
	}
	return response.Result, nil
}

func createProbeDDNSCloudflareRecord(ctx context.Context, token, zoneID, domain, recordType, content string) (string, error) {
	payload := map[string]any{"type": recordType, "name": domain, "content": content, "ttl": 60, "comment": probeDDNSCloudflareRecordComment}
	if recordType == "A" || recordType == "AAAA" || recordType == "CNAME" {
		payload["proxied"] = false
	}
	endpoint := probeDDNSCloudflareBaseURL + "/zones/" + url.PathEscape(zoneID) + "/dns_records"
	var response struct {
		Success bool                      `json:"success"`
		Result  probeDDNSCloudflareRecord `json:"result"`
	}
	if err := doProbeDDNSCloudflareJSON(ctx, token, http.MethodPost, endpoint, payload, &response); err != nil {
		return "", err
	}
	if !response.Success || strings.TrimSpace(response.Result.ID) == "" {
		return "", errors.New("cloudflare create dns record failed")
	}
	return strings.TrimSpace(response.Result.ID), nil
}

func updateProbeDDNSCloudflareRecord(ctx context.Context, token, zoneID, recordID, domain, recordType, content string) error {
	payload := map[string]any{"type": recordType, "name": domain, "content": content, "ttl": 60, "comment": probeDDNSCloudflareRecordComment}
	if recordType == "A" || recordType == "AAAA" || recordType == "CNAME" {
		payload["proxied"] = false
	}
	endpoint := probeDDNSCloudflareBaseURL + "/zones/" + url.PathEscape(zoneID) + "/dns_records/" + url.PathEscape(recordID)
	var response struct {
		Success bool `json:"success"`
	}
	if err := doProbeDDNSCloudflareJSON(ctx, token, http.MethodPut, endpoint, payload, &response); err != nil {
		return err
	}
	if !response.Success {
		return errors.New("cloudflare update dns record failed")
	}
	return nil
}

func deleteProbeDDNSCloudflareRecord(ctx context.Context, token, zoneID, recordID string) error {
	if strings.TrimSpace(zoneID) == "" || strings.TrimSpace(recordID) == "" {
		return errors.New("cloudflare zone id and record id are required")
	}
	endpoint := probeDDNSCloudflareBaseURL + "/zones/" + url.PathEscape(zoneID) + "/dns_records/" + url.PathEscape(recordID)
	var response struct {
		Success bool `json:"success"`
	}
	if err := doProbeDDNSCloudflareJSON(ctx, token, http.MethodDelete, endpoint, nil, &response); err != nil {
		return err
	}
	if !response.Success {
		return errors.New("cloudflare delete dns record failed")
	}
	return nil
}

func doProbeDDNSCloudflareJSON(ctx context.Context, token, method, endpoint string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cloudhelper-probe-ddns")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := probeDDNSCloudflareHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloudflare request status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return err
	}
	return nil
}
