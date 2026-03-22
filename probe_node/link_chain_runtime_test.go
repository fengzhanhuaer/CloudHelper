package main

import (
	"bufio"
	"strings"
	"testing"
	"time"
)

func TestReadProbeChainAuthEnvelopeCopilotStyle(t *testing.T) {
	payload := `{"type":"github_copilot_auth_request","api_version":"2025-03-22","request_id":"req-1","timestamp":"2026-03-22T12:00:00Z","auth":{"mode":"secret_hmac","chain_id":"chain-a","nonce":"nonce-1","mac":"abc123"}}`
	reader := bufio.NewReader(strings.NewReader(payload + "\n"))

	env, err := readProbeChainAuthEnvelope(reader)
	if err != nil {
		t.Fatalf("readProbeChainAuthEnvelope failed: %v", err)
	}
	if env.Type != probeChainAuthPacketType {
		t.Fatalf("unexpected type: %s", env.Type)
	}
	if env.APIVersion != probeChainAuthPacketVersion {
		t.Fatalf("unexpected api version: %s", env.APIVersion)
	}
	if env.RequestID != "req-1" {
		t.Fatalf("unexpected request id: %s", env.RequestID)
	}
	if env.Mode != "secret_hmac" || env.ChainID != "chain-a" || env.Nonce != "nonce-1" || env.MAC != "abc123" {
		t.Fatalf("unexpected envelope body: %+v", env)
	}
}

func TestProbeChainAuthFailureBlacklistAfterFiveAttempts(t *testing.T) {
	resetProbeChainAuthIPStateForTest()
	defer resetProbeChainAuthIPStateForTest()

	ip := "203.0.113.10"
	for i := 1; i <= probeChainAuthFailureThreshold; i++ {
		failures, blacklisted, _ := recordProbeChainAuthFailure(ip)
		if i < probeChainAuthFailureThreshold {
			if blacklisted {
				t.Fatalf("should not be blacklisted at attempt %d", i)
			}
			if failures != i {
				t.Fatalf("unexpected failure count at attempt %d: %d", i, failures)
			}
			continue
		}
		if !blacklisted {
			t.Fatalf("expected blacklist at attempt %d", i)
		}
		if failures != probeChainAuthFailureThreshold {
			t.Fatalf("unexpected failures when blacklisted: %d", failures)
		}
	}

	blocked, until := isProbeChainAuthIPBlacklisted(ip)
	if !blocked {
		t.Fatalf("expected ip blacklisted")
	}
	minUntil := time.Now().Add(4*time.Hour + 59*time.Minute)
	maxUntil := time.Now().Add(5*time.Hour + 1*time.Minute)
	if until.Before(minUntil) || until.After(maxUntil) {
		t.Fatalf("unexpected blacklist ttl: until=%s", until.Format(time.RFC3339))
	}
}

func TestProbeChainAuthFailureDelayRange(t *testing.T) {
	for i := 0; i < 64; i++ {
		delay := probeChainAuthFailureDelay()
		if delay < time.Duration(probeChainAuthFailureMinDelayMs)*time.Millisecond {
			t.Fatalf("delay too short: %s", delay)
		}
		if delay > time.Duration(probeChainAuthFailureMaxDelayMs)*time.Millisecond {
			t.Fatalf("delay too long: %s", delay)
		}
	}
}

func resetProbeChainAuthIPStateForTest() {
	probeChainAuthIPStateMap.mu.Lock()
	probeChainAuthIPStateMap.items = make(map[string]probeChainAuthIPState)
	probeChainAuthIPStateMap.mu.Unlock()
}
