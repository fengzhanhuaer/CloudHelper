package mobilecore

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type androidVPNDiagnosticLogRecord struct {
	last       time.Time
	suppressed int
}

var androidVPNDiagnosticLogState = struct {
	mu      sync.Mutex
	records map[string]androidVPNDiagnosticLogRecord
}{records: make(map[string]androidVPNDiagnosticLogRecord)}

func logAndroidVPNDiagnostic(key string, level string, message string, minInterval time.Duration) {
	key = strings.TrimSpace(key)
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if key == "" || minInterval <= 0 {
		androidLogStore.add("vpn", level, message)
		return
	}
	now := time.Now()
	androidVPNDiagnosticLogState.mu.Lock()
	record := androidVPNDiagnosticLogState.records[key]
	if !record.last.IsZero() && now.Sub(record.last) < minInterval {
		record.suppressed++
		androidVPNDiagnosticLogState.records[key] = record
		androidVPNDiagnosticLogState.mu.Unlock()
		return
	}
	suppressed := record.suppressed
	androidVPNDiagnosticLogState.records[key] = androidVPNDiagnosticLogRecord{last: now}
	androidVPNDiagnosticLogState.mu.Unlock()
	if suppressed > 0 {
		message += " suppressed=" + strconv.Itoa(suppressed)
	}
	androidLogStore.add("vpn", level, message)
}

func androidVPNPacketSummary(packet []byte) string {
	info, ok := parseAndroidVPNIPv4TransportPacket(packet)
	if !ok {
		return "bytes=" + strconv.Itoa(len(packet))
	}
	protocol := "udp"
	if info.Protocol == 6 {
		protocol = "tcp"
	}
	return fmt.Sprintf("proto=%s src=%s:%d dst=%s:%d bytes=%d", protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, info.TotalLength)
}
