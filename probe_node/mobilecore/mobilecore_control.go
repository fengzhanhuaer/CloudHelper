package mobilecore

// Shared control-message types and helpers used by both:
// - the controller reporter session
// - probe-to-probe chain link control on the Android side

import (
	"encoding/json"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

type chainLinkControlResult struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	NodeID    string `json:"node_id"`
	OK        bool   `json:"ok"`
	Action    string `json:"action,omitempty"`
	ChainID   string `json:"chain_id,omitempty"`
	Role      string `json:"role,omitempty"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
	Timestamp string `json:"timestamp"`
}

func (b *androidLogBuffer) snapshot(lines int) []androidLogEntry {
	if b == nil {
		return nil
	}
	limit := normalizeAndroidLogLines(lines)
	b.mu.Lock()
	defer b.mu.Unlock()
	out := append([]androidLogEntry(nil), b.entries...)
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func sendChainLinkControlResult(stream net.Conn, writeMu *sync.Mutex, result chainLinkControlResult) {
	if err := writeMobileStreamJSON(stream, writeMu, result); err != nil {
		log.Printf("chain link result send failed: request_id=%s err=%v", strings.TrimSpace(result.RequestID), err)
	}
}

func writeReporterJSON(stream net.Conn, writeMu *sync.Mutex, payload any) error {
	if writeMu != nil {
		writeMu.Lock()
		defer writeMu.Unlock()
	}
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := json.NewEncoder(stream).Encode(payload)
	_ = stream.SetWriteDeadline(time.Time{})
	return err
}
