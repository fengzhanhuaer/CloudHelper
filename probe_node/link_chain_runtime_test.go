package main

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReadProbeChainAuthEnvelopeFromHeadersCodexStyle(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer nonce-1")
	headers.Set(probeChainCodexVersionHeader, probeChainAuthPacketVersion)
	headers.Set(probeChainCodexAuthModeHeader, "secret_hmac")
	headers.Set(probeChainCodexMACHeader, "abc123")

	env, err := readProbeChainAuthEnvelopeFromHeaders(headers, "chain-a")
	if err != nil {
		t.Fatalf("readProbeChainAuthEnvelopeFromHeaders failed: %v", err)
	}
	if env.Type != probeChainAuthPacketType {
		t.Fatalf("unexpected type: %s", env.Type)
	}
	if env.APIVersion != probeChainAuthPacketVersion {
		t.Fatalf("unexpected api version: %s", env.APIVersion)
	}
	if env.RequestID != "" {
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

func TestVerifyProbeChainInboundAuthRejectsUnsupportedMode(t *testing.T) {
	cfg := probeChainRuntimeConfig{
		chainID: "chain-a",
		secret:  "secret-1",
	}
	env := probeChainAuthEnvelope{
		ChainID: "chain-a",
		Mode:    "user_signature",
		Nonce:   "nonce-a",
		MAC:     buildProbeChainHMAC("secret-1", "chain-a", "nonce-a"),
	}
	err := verifyProbeChainInboundAuth(cfg, env)
	if err == nil {
		t.Fatalf("expected unsupported auth mode error")
	}
	if !strings.Contains(err.Error(), "unsupported auth mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyProbeChainInboundAuthAcceptsSecretHMAC(t *testing.T) {
	cfg := probeChainRuntimeConfig{
		chainID: "chain-a",
		secret:  "secret-1",
	}
	env := probeChainAuthEnvelope{
		ChainID: "chain-a",
		Mode:    "secret_hmac",
		Nonce:   "nonce-a",
		MAC:     buildProbeChainHMAC("secret-1", "chain-a", "nonce-a"),
	}
	if err := verifyProbeChainInboundAuth(cfg, env); err != nil {
		t.Fatalf("verifyProbeChainInboundAuth failed: %v", err)
	}
}

func TestVerifyProbeChainInboundAuthRejectsInvalidMACWithNeutralMessage(t *testing.T) {
	cfg := probeChainRuntimeConfig{
		chainID: "chain-a",
		secret:  "secret-1",
	}
	env := probeChainAuthEnvelope{
		ChainID: "chain-a",
		Mode:    "secret_hmac",
		Nonce:   "nonce-a",
		MAC:     "bad-mac",
	}
	err := verifyProbeChainInboundAuth(cfg, env)
	if err == nil {
		t.Fatalf("expected invalid mac error")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("unexpected error: %v", err)
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

func TestResolveProbeChainDialIPHostUsesFreshCache(t *testing.T) {
	resetProbeChainRelayResolveCacheForTest()
	defer resetProbeChainRelayResolveCacheForTest()

	originalLookup := probeChainRelayLookupIP
	originalNow := probeChainRelayResolveNow
	baseNow := time.Date(2026, 5, 16, 15, 0, 0, 0, time.UTC)
	probeChainRelayResolveNow = func() time.Time { return baseNow }
	probeChainRelayLookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) {
		t.Fatalf("lookup should not run when cache is fresh")
		return nil, nil
	}
	defer func() {
		probeChainRelayLookupIP = originalLookup
		probeChainRelayResolveNow = originalNow
	}()

	storeProbeChainRelayResolveCache("relay.example.com", "203.0.113.7", "relay.example.com")
	dialHost, hostHeader, err := resolveProbeChainDialIPHost("relay.example.com")
	if err != nil {
		t.Fatalf("resolveProbeChainDialIPHost returned error: %v", err)
	}
	if dialHost != "203.0.113.7" || hostHeader != "relay.example.com" {
		t.Fatalf("unexpected cache result: dialHost=%s hostHeader=%s", dialHost, hostHeader)
	}
}

func TestResolveProbeChainDialIPHostLookupDoesNotPersistWithoutConnectSuccess(t *testing.T) {
	resetProbeChainRelayResolveCacheForTest()
	defer resetProbeChainRelayResolveCacheForTest()

	originalLookup := probeChainRelayLookupIP
	originalNow := probeChainRelayResolveNow
	baseNow := time.Date(2026, 5, 16, 15, 0, 0, 0, time.UTC)
	probeChainRelayResolveNow = func() time.Time { return baseNow }
	lookupCalls := 0
	probeChainRelayLookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) {
		lookupCalls++
		return []net.IP{net.ParseIP("203.0.113.13")}, nil
	}
	defer func() {
		probeChainRelayLookupIP = originalLookup
		probeChainRelayResolveNow = originalNow
	}()

	dialHost, hostHeader, err := resolveProbeChainDialIPHost("relay.example.com")
	if err != nil {
		t.Fatalf("resolveProbeChainDialIPHost returned error: %v", err)
	}
	if dialHost != "203.0.113.13" || hostHeader != "relay.example.com" {
		t.Fatalf("unexpected lookup result: dialHost=%s hostHeader=%s", dialHost, hostHeader)
	}
	if lookupCalls != 1 {
		t.Fatalf("lookupCalls=%d", lookupCalls)
	}
	if _, _, ok := loadProbeChainRelayResolveCache("relay.example.com", false); ok {
		t.Fatalf("lookup result should not be cached before connect success")
	}
}

func TestResolveProbeChainDialIPHostFallsBackToStaleCacheOnLookupTimeout(t *testing.T) {
	resetProbeChainRelayResolveCacheForTest()
	defer resetProbeChainRelayResolveCacheForTest()

	originalLookup := probeChainRelayLookupIP
	originalNow := probeChainRelayResolveNow
	baseNow := time.Date(2026, 5, 16, 15, 0, 0, 0, time.UTC)
	currentNow := baseNow
	probeChainRelayResolveNow = func() time.Time { return currentNow }
	probeChainRelayLookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) {
		return nil, errors.New("i/o timeout")
	}
	defer func() {
		probeChainRelayLookupIP = originalLookup
		probeChainRelayResolveNow = originalNow
	}()

	storeProbeChainRelayResolveCache("relay.example.com", "203.0.113.9", "relay.example.com")
	currentNow = baseNow.Add(probeChainRelayResolveCacheTTL + 5*time.Second)

	dialHost, hostHeader, err := resolveProbeChainDialIPHost("relay.example.com")
	if err != nil {
		t.Fatalf("resolveProbeChainDialIPHost returned error: %v", err)
	}
	if dialHost != "203.0.113.9" || hostHeader != "relay.example.com" {
		t.Fatalf("unexpected stale cache result: dialHost=%s hostHeader=%s", dialHost, hostHeader)
	}
}

func TestRefreshProbeChainRelayResolveCacheOnConnectSuccessExtendsTTLToOneDay(t *testing.T) {
	resetProbeChainRelayResolveCacheForTest()
	defer resetProbeChainRelayResolveCacheForTest()

	originalNow := probeChainRelayResolveNow
	baseNow := time.Date(2026, 5, 16, 15, 0, 0, 0, time.UTC)
	probeChainRelayResolveNow = func() time.Time { return baseNow }
	defer func() {
		probeChainRelayResolveNow = originalNow
	}()

	refreshProbeChainRelayResolveCacheOnConnectSuccess("relay.example.com", "203.0.113.21", "relay.example.com")

	probeChainRelayResolveCache.mu.Lock()
	entry, ok := probeChainRelayResolveCache.items["relay.example.com"]
	probeChainRelayResolveCache.mu.Unlock()
	if !ok {
		t.Fatalf("expected cache entry after connect success")
	}
	if got := entry.ExpiresAt.Sub(baseNow); got != 24*time.Hour {
		t.Fatalf("ttl=%s want=%s", got, 24*time.Hour)
	}
	if got := entry.StaleUntil.Sub(baseNow); got != 24*time.Hour+15*time.Minute {
		t.Fatalf("stale=%s want=%s", got, 24*time.Hour+15*time.Minute)
	}
}

func TestResolveProbeChainDialIPHostReturnsErrorWhenStaleCacheExpired(t *testing.T) {
	resetProbeChainRelayResolveCacheForTest()
	defer resetProbeChainRelayResolveCacheForTest()

	originalLookup := probeChainRelayLookupIP
	originalNow := probeChainRelayResolveNow
	baseNow := time.Date(2026, 5, 16, 15, 0, 0, 0, time.UTC)
	currentNow := baseNow
	probeChainRelayResolveNow = func() time.Time { return currentNow }
	probeChainRelayLookupIP = func(ctx context.Context, network string, host string) ([]net.IP, error) {
		return nil, errors.New("i/o timeout")
	}
	defer func() {
		probeChainRelayLookupIP = originalLookup
		probeChainRelayResolveNow = originalNow
	}()

	storeProbeChainRelayResolveCache("relay.example.com", "203.0.113.11", "relay.example.com")
	currentNow = baseNow.Add(probeChainRelayResolveMaxStale + 5*time.Second)

	_, _, err := resolveProbeChainDialIPHost("relay.example.com")
	if err == nil {
		t.Fatalf("expected error after stale cache expiry")
	}
	if !strings.Contains(err.Error(), "resolve relay host failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNextProbeChainListenRetryBackoff(t *testing.T) {
	if got := nextProbeChainListenRetryBackoff(0); got != probeChainPortForwardListenRetryInterval*2 {
		t.Fatalf("unexpected backoff for zero current: %s", got)
	}
	if got := nextProbeChainListenRetryBackoff(probeChainPortForwardListenRetryInterval); got != probeChainPortForwardListenRetryInterval*2 {
		t.Fatalf("unexpected doubled backoff: %s", got)
	}
	if got := nextProbeChainListenRetryBackoff(probeChainPortForwardListenRetryMaxBackoff); got != probeChainPortForwardListenRetryMaxBackoff {
		t.Fatalf("unexpected capped backoff at max: %s", got)
	}
	if got := nextProbeChainListenRetryBackoff(probeChainPortForwardListenRetryMaxBackoff * 2); got != probeChainPortForwardListenRetryMaxBackoff {
		t.Fatalf("unexpected backoff over cap: %s", got)
	}
}

func TestWrapProbeChainRelayDialErrorForHTTP3UDPSocketResource(t *testing.T) {
	baseErr := errors.New("Post \"https://69.63.223.88:16030/api/node/chain/relay?chain_id=5\": listen udp :0: bind: An operation on a socket could not be performed because the system lacked sufficient buffer space or because a queue was full.")
	err := wrapProbeChainRelayDialError("http3", "69.63.223.88", 16030, baseErr)
	if err == nil {
		t.Fatalf("expected wrapped error")
	}
	text := err.Error()
	if !strings.Contains(text, "http3 udp socket unavailable") || !strings.Contains(text, "each_proxy_group_uses_independent_quic_connection") {
		t.Fatalf("unexpected wrapped error: %v", err)
	}
	if !errors.Is(err, baseErr) {
		t.Fatalf("wrapped error should keep base error: %v", err)
	}
	if got := wrapProbeChainRelayDialError("http2", "69.63.223.88", 16030, baseErr); got != baseErr {
		t.Fatalf("http2 error should not be wrapped: %v", got)
	}
}

func resetProbeChainAuthIPStateForTest() {
	probeChainAuthIPStateMap.mu.Lock()
	probeChainAuthIPStateMap.items = make(map[string]probeChainAuthIPState)
	probeChainAuthIPStateMap.mu.Unlock()
}
