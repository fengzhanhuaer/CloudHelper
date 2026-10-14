package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const probeTrafficDailyRetentionDays = 400
const probeTrafficCounterScope = "physical-egress-v1"

var probeTrafficStore = struct {
	mu  sync.Mutex
	dir string
}{
	dir: filepath.Join(dataDir, "probe_traffic"),
}

type probeTrafficDailyBucket struct {
	Date string `json:"date"`
	RX   uint64 `json:"rx"`
	TX   uint64 `json:"tx"`
}

type probeTrafficRecord struct {
	Version      int                       `json:"version"`
	CounterScope string                    `json:"counter_scope,omitempty"`
	Available    bool                      `json:"available"`
	LastRX       uint64                    `json:"last_rx"`
	LastTX       uint64                    `json:"last_tx"`
	LastSampleAt string                    `json:"last_sample_at"`
	TotalRX      uint64                    `json:"total_rx"`
	TotalTX      uint64                    `json:"total_tx"`
	Daily        []probeTrafficDailyBucket `json:"daily"`
}

type probeTrafficUsage struct {
	Available    bool   `json:"available"`
	TotalRXBytes uint64 `json:"total_rx_bytes"`
	TotalTXBytes uint64 `json:"total_tx_bytes"`
	MonthRXBytes uint64 `json:"month_rx_bytes"`
	MonthTXBytes uint64 `json:"month_tx_bytes"`
	StartDay     int    `json:"monthly_start_day"`
	PeriodStart  string `json:"period_start"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

func normalizeMonthlyTrafficStartDay(value int) int {
	if value < 1 || value > 31 {
		return 1
	}
	return value
}

func accumulateProbeTrafficFromReport(nodeID string, rx, tx uint64, timestamp string) error {
	cleanID := normalizeProbeNodeID(nodeID)
	if cleanID == "" {
		return fmt.Errorf("node id is required")
	}
	sampleTime := parseNetworkMonitorTimestamp(timestamp)
	if sampleTime.IsZero() {
		return fmt.Errorf("invalid traffic sample timestamp")
	}
	sampleTime = sampleTime.UTC()
	probeTrafficStore.mu.Lock()
	defer probeTrafficStore.mu.Unlock()

	record, err := loadProbeTrafficRecordLocked(cleanID)
	if err != nil {
		return err
	}
	if record.Available {
		if previousTime := parseNetworkMonitorTimestamp(record.LastSampleAt); !previousTime.IsZero() && !sampleTime.After(previousTime) {
			return nil
		}
		deltaRX, deltaTX := trafficCounterDelta(record.LastRX, rx), trafficCounterDelta(record.LastTX, tx)
		record.TotalRX = saturatingTrafficAdd(record.TotalRX, deltaRX)
		record.TotalTX = saturatingTrafficAdd(record.TotalTX, deltaTX)
		day := sampleTime.Format("2006-01-02")
		record.Daily = addProbeTrafficDailyDelta(record.Daily, day, deltaRX, deltaTX)
	} else {
		record.Available = true
	}
	record.LastRX = rx
	record.LastTX = tx
	record.LastSampleAt = sampleTime.Format(time.RFC3339Nano)
	record.Daily = pruneProbeTrafficDailyBuckets(record.Daily, sampleTime)
	return saveProbeTrafficRecordLocked(cleanID, record)
}

func trafficCounterDelta(previous, current uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	return current
}

func saturatingTrafficAdd(left, right uint64) uint64 {
	max := ^uint64(0)
	if max-left < right {
		return max
	}
	return left + right
}

func addProbeTrafficDailyDelta(items []probeTrafficDailyBucket, date string, rx, tx uint64) []probeTrafficDailyBucket {
	for i := range items {
		if items[i].Date != date {
			continue
		}
		items[i].RX = saturatingTrafficAdd(items[i].RX, rx)
		items[i].TX = saturatingTrafficAdd(items[i].TX, tx)
		return items
	}
	return append(items, probeTrafficDailyBucket{Date: date, RX: rx, TX: tx})
}

func pruneProbeTrafficDailyBuckets(items []probeTrafficDailyBucket, now time.Time) []probeTrafficDailyBucket {
	cutoff := now.UTC().AddDate(0, 0, -probeTrafficDailyRetentionDays).Format("2006-01-02")
	out := items[:0]
	for _, item := range items {
		if item.Date >= cutoff {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

func probeTrafficRecordPath(nodeID string) (string, error) {
	cleanID := normalizeProbeNodeID(nodeID)
	if number, err := strconv.Atoi(cleanID); err != nil || number <= 0 {
		return "", fmt.Errorf("invalid node id")
	}
	return filepath.Join(probeTrafficStore.dir, "node_"+cleanID+".json"), nil
}

func setProbeTrafficStoreDirForTest(dir string) func() {
	probeTrafficStore.mu.Lock()
	previous := probeTrafficStore.dir
	probeTrafficStore.dir = dir
	probeTrafficStore.mu.Unlock()
	return func() {
		probeTrafficStore.mu.Lock()
		probeTrafficStore.dir = previous
		probeTrafficStore.mu.Unlock()
	}
}

func loadProbeTrafficRecordLocked(nodeID string) (probeTrafficRecord, error) {
	path, err := probeTrafficRecordPath(nodeID)
	if err != nil {
		return probeTrafficRecord{}, err
	}
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return probeTrafficRecord{Version: 1, Daily: []probeTrafficDailyBucket{}}, nil
	}
	if err != nil {
		return probeTrafficRecord{}, err
	}
	var record probeTrafficRecord
	if err := json.Unmarshal(content, &record); err != nil {
		return probeTrafficRecord{}, err
	}
	if record.Version != 1 {
		return probeTrafficRecord{}, fmt.Errorf("unsupported probe traffic record version: %d", record.Version)
	}
	if record.CounterScope != probeTrafficCounterScope {
		record = probeTrafficRecord{Version: 1, CounterScope: probeTrafficCounterScope, Daily: []probeTrafficDailyBucket{}}
	}
	if record.Daily == nil {
		record.Daily = []probeTrafficDailyBucket{}
	}
	return record, nil
}

func saveProbeTrafficRecordLocked(nodeID string, record probeTrafficRecord) error {
	path, err := probeTrafficRecordPath(nodeID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	record.Version = 1
	record.CounterScope = probeTrafficCounterScope
	content, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, content, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func loadProbeTrafficUsage(nodeID string, startDay int, now time.Time) (probeTrafficUsage, error) {
	cleanID := normalizeProbeNodeID(nodeID)
	probeTrafficStore.mu.Lock()
	defer probeTrafficStore.mu.Unlock()
	record, err := loadProbeTrafficRecordLocked(cleanID)
	if err != nil {
		return probeTrafficUsage{}, err
	}
	startDay = normalizeMonthlyTrafficStartDay(startDay)
	periodStart := probeTrafficPeriodStart(now.UTC(), startDay)
	usage := probeTrafficUsage{
		Available:    record.Available,
		TotalRXBytes: record.TotalRX,
		TotalTXBytes: record.TotalTX,
		StartDay:     startDay,
		PeriodStart:  periodStart.Format("2006-01-02"),
		UpdatedAt:    strings.TrimSpace(record.LastSampleAt),
	}
	for _, bucket := range record.Daily {
		if bucket.Date >= usage.PeriodStart {
			usage.MonthRXBytes = saturatingTrafficAdd(usage.MonthRXBytes, bucket.RX)
			usage.MonthTXBytes = saturatingTrafficAdd(usage.MonthTXBytes, bucket.TX)
		}
	}
	return usage, nil
}

func probeTrafficPeriodStart(now time.Time, startDay int) time.Time {
	startDay = normalizeMonthlyTrafficStartDay(startDay)
	current := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	start := dateWithClampedDay(current, startDay)
	if now.Before(start) {
		current = current.AddDate(0, -1, 0)
		start = dateWithClampedDay(current, startDay)
	}
	return start
}

func dateWithClampedDay(month time.Time, day int) time.Time {
	first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastDay := first.AddDate(0, 1, -1).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
}
