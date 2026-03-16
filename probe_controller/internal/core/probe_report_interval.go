package core

import (
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	defaultProbeReportIntervalSec = 60
	temporaryIntervalTTL          = 5 * time.Minute
)

type probeReportIntervalState struct {
	mu               sync.Mutex
	defaultSec       int
	overrideSec      int
	overrideExpires  time.Time
	activeAdminConns int
	expireTimer      *time.Timer
}

type probeReportIntervalSnapshot struct {
	DefaultSec             int    `json:"default_sec"`
	CurrentSec             int    `json:"current_sec"`
	OverrideSec            int    `json:"override_sec"`
	OverrideExpiresAt      string `json:"override_expires_at,omitempty"`
	ActiveAdminConnections int    `json:"active_admin_connections"`
}

var probeReportIntervalCtl = &probeReportIntervalState{defaultSec: defaultProbeReportIntervalSec}

func initProbeReportIntervalControl() {
	probeReportIntervalCtl.mu.Lock()
	defer probeReportIntervalCtl.mu.Unlock()

	probeReportIntervalCtl.defaultSec = parsePositiveIntEnv("PROBE_REPORT_INTERVAL_DEFAULT_SEC", defaultProbeReportIntervalSec)
	probeReportIntervalCtl.overrideSec = 0
	probeReportIntervalCtl.overrideExpires = time.Time{}
	if probeReportIntervalCtl.expireTimer != nil {
		probeReportIntervalCtl.expireTimer.Stop()
		probeReportIntervalCtl.expireTimer = nil
	}
}

func parsePositiveIntEnv(key string, fallback int) int {
	raw := os.Getenv(key)
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func currentProbeReportIntervalSec() int {
	probeReportIntervalCtl.mu.Lock()
	defer probeReportIntervalCtl.mu.Unlock()
	probeReportIntervalCtl.expireIfNeededLocked(time.Now())
	if probeReportIntervalCtl.overrideSec > 0 {
		return probeReportIntervalCtl.overrideSec
	}
	return probeReportIntervalCtl.defaultSec
}

func getProbeReportIntervalSnapshot() probeReportIntervalSnapshot {
	probeReportIntervalCtl.mu.Lock()
	defer probeReportIntervalCtl.mu.Unlock()
	now := time.Now()
	probeReportIntervalCtl.expireIfNeededLocked(now)

	snapshot := probeReportIntervalSnapshot{
		DefaultSec:             probeReportIntervalCtl.defaultSec,
		CurrentSec:             probeReportIntervalCtl.defaultSec,
		OverrideSec:            0,
		ActiveAdminConnections: probeReportIntervalCtl.activeAdminConns,
	}
	if probeReportIntervalCtl.overrideSec > 0 {
		snapshot.CurrentSec = probeReportIntervalCtl.overrideSec
		snapshot.OverrideSec = probeReportIntervalCtl.overrideSec
		snapshot.OverrideExpiresAt = probeReportIntervalCtl.overrideExpires.UTC().Format(time.RFC3339)
	}
	return snapshot
}

func setTemporaryProbeReportInterval(intervalSec int) (probeReportIntervalSnapshot, error) {
	if intervalSec <= 0 {
		return probeReportIntervalSnapshot{}, ErrBadRequest("interval_sec must be positive")
	}

	probeReportIntervalCtl.mu.Lock()
	now := time.Now()
	probeReportIntervalCtl.expireIfNeededLocked(now)
	if probeReportIntervalCtl.activeAdminConns <= 0 {
		probeReportIntervalCtl.mu.Unlock()
		return probeReportIntervalSnapshot{}, ErrBadRequest("no active manager websocket connection")
	}

	probeReportIntervalCtl.overrideSec = intervalSec
	probeReportIntervalCtl.overrideExpires = now.Add(temporaryIntervalTTL)
	probeReportIntervalCtl.resetTimerLocked()
	snapshot := probeReportIntervalSnapshot{
		DefaultSec:             probeReportIntervalCtl.defaultSec,
		CurrentSec:             probeReportIntervalCtl.overrideSec,
		OverrideSec:            probeReportIntervalCtl.overrideSec,
		OverrideExpiresAt:      probeReportIntervalCtl.overrideExpires.UTC().Format(time.RFC3339),
		ActiveAdminConnections: probeReportIntervalCtl.activeAdminConns,
	}
	probeReportIntervalCtl.mu.Unlock()

	broadcastProbeReportInterval(intervalSec)
	return snapshot, nil
}

func onAdminWSAuthenticated() {
	probeReportIntervalCtl.mu.Lock()
	probeReportIntervalCtl.activeAdminConns++
	probeReportIntervalCtl.mu.Unlock()
}

func onAdminWSDisconnected() {
	probeReportIntervalCtl.mu.Lock()
	if probeReportIntervalCtl.activeAdminConns > 0 {
		probeReportIntervalCtl.activeAdminConns--
	}
	shouldFallback := probeReportIntervalCtl.activeAdminConns == 0 && probeReportIntervalCtl.overrideSec > 0
	defaultSec := probeReportIntervalCtl.defaultSec
	if shouldFallback {
		probeReportIntervalCtl.clearOverrideLocked()
	}
	probeReportIntervalCtl.mu.Unlock()

	if shouldFallback {
		broadcastProbeReportInterval(defaultSec)
	}
}

func (s *probeReportIntervalState) expireIfNeededLocked(now time.Time) {
	if s.overrideSec <= 0 {
		return
	}
	if s.activeAdminConns <= 0 || now.After(s.overrideExpires) {
		s.clearOverrideLocked()
	}
}

func (s *probeReportIntervalState) clearOverrideLocked() {
	s.overrideSec = 0
	s.overrideExpires = time.Time{}
	if s.expireTimer != nil {
		s.expireTimer.Stop()
		s.expireTimer = nil
	}
}

func (s *probeReportIntervalState) resetTimerLocked() {
	if s.expireTimer != nil {
		s.expireTimer.Stop()
	}
	delay := time.Until(s.overrideExpires)
	if delay <= 0 {
		delay = time.Second
	}
	s.expireTimer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		if s.overrideSec <= 0 {
			s.mu.Unlock()
			return
		}
		if time.Now().Before(s.overrideExpires) {
			s.mu.Unlock()
			return
		}
		defaultSec := s.defaultSec
		s.clearOverrideLocked()
		s.mu.Unlock()
		broadcastProbeReportInterval(defaultSec)
	})
}

func broadcastProbeReportInterval(intervalSec int) {
	if intervalSec <= 0 {
		return
	}
	payload := map[string]interface{}{
		"type":         "report_interval",
		"interval_sec": intervalSec,
		"server_utc":   time.Now().UTC().Format(time.RFC3339),
	}

	probeSessions.mu.RLock()
	sessions := make([]*probeSession, 0, len(probeSessions.data))
	for _, s := range probeSessions.data {
		sessions = append(sessions, s)
	}
	probeSessions.mu.RUnlock()

	for _, s := range sessions {
		_ = s.writeJSON(payload)
	}
}

type badRequestError struct{ message string }

func (e badRequestError) Error() string { return e.message }

func ErrBadRequest(message string) error { return badRequestError{message: message} }
