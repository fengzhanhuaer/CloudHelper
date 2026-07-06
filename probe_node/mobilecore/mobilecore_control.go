package mobilecore

import (
	"encoding/json"
	"net"
	"sync"
	"time"
)

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
