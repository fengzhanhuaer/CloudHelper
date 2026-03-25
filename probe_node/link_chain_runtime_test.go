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

func TestReadProbeChainNonceChallenge(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(probeChainAuthNoncePrefix + "nonce-1\n"))
	nonce, err := readProbeChainNonceChallenge(reader)
	if err != nil {
		t.Fatalf("readProbeChainNonceChallenge failed: %v", err)
	}
	if nonce != "nonce-1" {
		t.Fatalf("unexpected nonce: %s", nonce)
	}
}

func TestVerifyProbeChainInboundAuthNonceMismatch(t *testing.T) {
	cfg := probeChainRuntimeConfig{
		chainID: "chain-a",
		secret:  "secret-1",
	}
	env := probeChainAuthEnvelope{
		ChainID: "chain-a",
		Nonce:   "nonce-a",
		MAC:     buildProbeChainHMAC("secret-1", "chain-a", "nonce-a"),
	}
	err := verifyProbeChainInboundAuth(cfg, env, "nonce-b")
	if err == nil {
		t.Fatalf("expected nonce mismatch error")
	}
	if !strings.Contains(err.Error(), "nonce mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyProbeChainInboundAuthNonceMatch(t *testing.T) {
	cfg := probeChainRuntimeConfig{
		chainID: "chain-a",
		secret:  "secret-1",
	}
	env := probeChainAuthEnvelope{
		ChainID: "chain-a",
		Nonce:   "nonce-a",
		MAC:     buildProbeChainHMAC("secret-1", "chain-a", "nonce-a"),
	}
	if err := verifyProbeChainInboundAuth(cfg, env, "nonce-a"); err != nil {
		t.Fatalf("verifyProbeChainInboundAuth failed: %v", err)
	}
}

func TestResolveProbeChainTLSServerName(t *testing.T) {
	if got := resolveProbeChainTLSServerName("http", "203.0.113.10", "api.example.com"); got != "203.0.113.10" {
		t.Fatalf("http sni should use dial ip, got: %s", got)
	}
	if got := resolveProbeChainTLSServerName("http2", "203.0.113.10", "api.example.com"); got != "api.example.com" {
		t.Fatalf("http2 sni should use api domain, got: %s", got)
	}
	if got := resolveProbeChainTLSServerName("http3", "203.0.113.10", "203.0.113.10"); got != "203.0.113.10" {
		t.Fatalf("http3 sni should fallback to dial ip when host is ip, got: %s", got)
	}
}

func resetProbeChainAuthIPStateForTest() {
	probeChainAuthIPStateMap.mu.Lock()
	probeChainAuthIPStateMap.items = make(map[string]probeChainAuthIPState)
	probeChainAuthIPStateMap.mu.Unlock()
}
