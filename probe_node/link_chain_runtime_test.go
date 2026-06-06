package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

func TestStartProbeChainRuntimeSharesRelayPortAcrossChains(t *testing.T) {
	resetProbeChainRuntimeStateForTest(t)
	dataDir := t.TempDir()
	t.Setenv("PROBE_NODE_DATA_DIR", dataDir)
	writeProbeChainTestCertificate(t, dataDir)

	listenPort := reserveProbeChainTestTCPUDPPort(t)
	base := probeChainRuntimeConfig{
		secret:        "secret-shared",
		role:          "entry",
		listenHost:    "127.0.0.1",
		listenPort:    listenPort,
		linkLayer:     "",
		nextAuthMode:  "proxy",
		identity:      nodeIdentity{NodeID: "node-1", Secret: "node-secret"},
		controllerURL: "https://controller.example.com",
	}

	cfgA := base
	cfgA.chainID = "shared-chain-a"
	cfgB := base
	cfgB.chainID = "shared-chain-b"

	rtA, err := startProbeChainRuntime(cfgA)
	if err != nil {
		t.Fatalf("start first chain failed: %v", err)
	}
	defer stopProbeChainRuntime(rtA.cfg.chainID, "test cleanup")

	rtB, err := startProbeChainRuntime(cfgB)
	if err != nil {
		t.Fatalf("start second chain on same port failed: %v", err)
	}
	defer stopProbeChainRuntime(rtB.cfg.chainID, "test cleanup")

	listenAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(listenPort))
	probeChainSharedRelayState.mu.Lock()
	shared := probeChainSharedRelayState.servers[listenAddr]
	probeChainSharedRelayState.mu.Unlock()
	if shared == nil {
		t.Fatalf("expected shared relay for %s", listenAddr)
	}
	if shared.refCount != 2 {
		t.Fatalf("shared refCount=%d, want 2", shared.refCount)
	}

	stopProbeChainRuntime(cfgA.chainID, "test partial stop")
	probeChainSharedRelayState.mu.Lock()
	shared = probeChainSharedRelayState.servers[listenAddr]
	probeChainSharedRelayState.mu.Unlock()
	if shared == nil || shared.refCount != 1 {
		t.Fatalf("shared relay should remain after first chain stop: %+v", shared)
	}

	stopProbeChainRuntime(cfgB.chainID, "test final stop")
	probeChainSharedRelayState.mu.Lock()
	shared = probeChainSharedRelayState.servers[listenAddr]
	probeChainSharedRelayState.mu.Unlock()
	if shared != nil {
		t.Fatalf("shared relay should stop after last chain, got %+v", shared)
	}
}

func TestProbeChainRelayDispatchRoutesByChainID(t *testing.T) {
	resetProbeChainRuntimeStateForTest(t)
	rt := &probeChainRuntime{
		cfg: probeChainRuntimeConfig{
			chainID: "dispatch-chain",
			role:    "entry",
			secret:  "secret-1",
		},
		stopCh: make(chan struct{}),
	}
	probeChainRuntimeState.mu.Lock()
	probeChainRuntimeState.runtimes[rt.cfg.chainID] = rt
	probeChainRuntimeState.mu.Unlock()
	defer stopProbeChainRuntime(rt.cfg.chainID, "test cleanup")

	req := httptest.NewRequest(http.MethodGet, probeChainRelayAPIPath+"?chain_id=missing-chain", nil)
	rr := httptest.NewRecorder()
	handleProbeChainRelayDispatch(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing chain status=%d want 404", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, probeChainRelayAPIPath+"?chain_id=dispatch-chain", nil)
	rr = httptest.NewRecorder()
	handleProbeChainRelayDispatch(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("existing chain should reach runtime handler and reject non websocket method, status=%d", rr.Code)
	}
}

func TestIsSameProbeChainRuntimeConfigIgnoresAuthTicketRotation(t *testing.T) {
	resetProbeChainRuntimeStateForTest(t)
	rt := &probeChainRuntime{
		cfg: probeChainRuntimeConfig{
			chainID:       "ticket-rotation-chain",
			chainType:     "proxy_chain",
			role:          "entry",
			listenHost:    "127.0.0.1",
			listenPort:    17030,
			linkLayer:     "websocket-h3",
			nextLinkLayer: "websocket-h3",
			nextDialMode:  "forward",
			nextHost:      "relay.example.com",
			nextPort:      12113,
			nextAuthMode:  "secret",
			authTicket:    "ticket-issued-at-1",
			secret:        "secret-1",
			rawPublicKey:  "public-key-1",
			portForwards: []probeChainRuntimePortForward{{
				ID:         "pf-1",
				EntrySide:  "chain_entry",
				ListenHost: "127.0.0.1",
				ListenPort: 12112,
				TargetHost: "192.168.50.222",
				TargetPort: 3389,
				Network:    "tcp",
				Enabled:    true,
			}},
		},
		stopCh: make(chan struct{}),
	}
	probeChainRuntimeState.mu.Lock()
	probeChainRuntimeState.runtimes[rt.cfg.chainID] = rt
	probeChainRuntimeState.mu.Unlock()

	next := rt.cfg
	next.authTicket = "ticket-issued-at-2"
	if !isSameProbeChainRuntimeConfig(rt.cfg.chainID, next) {
		t.Fatalf("auth ticket rotation should not force chain runtime restart")
	}

	next.secret = "secret-2"
	if isSameProbeChainRuntimeConfig(rt.cfg.chainID, next) {
		t.Fatalf("secret change should still force chain runtime restart")
	}
}

func TestProbeChainPingPongStreamEchoesPayload(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleProbeChainProxyStream(nil, server)
	}()

	if err := json.NewEncoder(client).Encode(probeChainTunnelOpenRequest{Type: probeChainRelayModePingPong, PingBytes: 4}); err != nil {
		t.Fatalf("write ping-pong request failed: %v", err)
	}
	var response probeChainTunnelOpenResponse
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatalf("read ping-pong response failed: %v", err)
	}
	if !response.OK {
		t.Fatalf("ping-pong response not ok: %+v", response)
	}
	payload := []byte{1, 2, 3, 4}
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("write payload failed: %v", err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(client, echo); err != nil {
		t.Fatalf("read echo failed: %v", err)
	}
	if string(echo) != string(payload) {
		t.Fatalf("echo=%v want %v", echo, payload)
	}
	_ = client.Close()
	<-done
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

func TestVerifyProbeChainInboundAuthRejectsMissingUserAuthTicket(t *testing.T) {
	resetProbeChainAuthReplayStoreForTest()
	defer resetProbeChainAuthReplayStoreForTest()
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
		if strings.Contains(err.Error(), "ticket") {
			return
		}
		t.Fatalf("unexpected error: %v", err)
	}
	t.Fatalf("expected missing ticket error")
}

func TestVerifyProbeChainInboundAuthRejectsReplayNonce(t *testing.T) {
	resetProbeChainAuthReplayStoreForTest()
	defer resetProbeChainAuthReplayStoreForTest()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	rawPublicKey := base64.StdEncoding.EncodeToString(pub)
	ticket := buildProbeChainUserAuthTicketForTest(t, priv, "chain-a", rawPublicKey)
	cfg := probeChainRuntimeConfig{
		chainID:         "chain-a",
		secret:          "secret-1",
		rawPublicKey:    rawPublicKey,
		userPublicKey:   pub,
		requireUserAuth: true,
	}
	env := probeChainAuthEnvelope{
		ChainID:    "chain-a",
		Mode:       "secret_hmac",
		Nonce:      "nonce-replay",
		MAC:        buildProbeChainHMAC("secret-1", "chain-a", "nonce-replay"),
		AuthTicket: ticket,
	}
	if err := verifyProbeChainInboundAuth(cfg, env); err != nil {
		t.Fatalf("first verify failed: %v", err)
	}
	err = verifyProbeChainInboundAuth(cfg, env)
	if err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("expected replay error, got: %v", err)
	}
}

func TestVerifyProbeChainInboundAuthRequiresUserAuthTicket(t *testing.T) {
	resetProbeChainAuthReplayStoreForTest()
	defer resetProbeChainAuthReplayStoreForTest()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	rawPublicKey := base64.StdEncoding.EncodeToString(pub)
	cfg := probeChainRuntimeConfig{
		chainID:         "chain-a",
		secret:          "secret-1",
		requireUserAuth: true,
		rawPublicKey:    rawPublicKey,
		userPublicKey:   pub,
	}
	env := probeChainAuthEnvelope{
		ChainID: "chain-a",
		Mode:    "secret_hmac",
		Nonce:   "nonce-ticket-missing",
		MAC:     buildProbeChainHMAC("secret-1", "chain-a", "nonce-ticket-missing"),
	}
	err = verifyProbeChainInboundAuth(cfg, env)
	if err == nil || !strings.Contains(err.Error(), "ticket") {
		t.Fatalf("expected ticket error, got: %v", err)
	}
}

func TestVerifyProbeChainInboundAuthAcceptsSignedUserAuthTicket(t *testing.T) {
	resetProbeChainAuthReplayStoreForTest()
	defer resetProbeChainAuthReplayStoreForTest()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	rawPublicKey := base64.StdEncoding.EncodeToString(pub)
	ticket := buildProbeChainUserAuthTicketForTest(t, priv, "chain-a", rawPublicKey)
	cfg := probeChainRuntimeConfig{
		chainID:         "chain-a",
		secret:          "secret-1",
		requireUserAuth: true,
		rawPublicKey:    rawPublicKey,
		userPublicKey:   pub,
	}
	env := probeChainAuthEnvelope{
		ChainID:    "chain-a",
		Mode:       "secret_hmac",
		Nonce:      "nonce-ticket-valid",
		MAC:        buildProbeChainHMAC("secret-1", "chain-a", "nonce-ticket-valid"),
		AuthTicket: ticket,
	}
	if err := verifyProbeChainInboundAuth(cfg, env); err != nil {
		t.Fatalf("verifyProbeChainInboundAuth failed: %v", err)
	}
}

func TestProbeChainAuthTicketStorePersistsToFile(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PROBE_NODE_DATA_DIR", dataDir)
	resetProbeChainAuthTicketStoreForTest()
	defer resetProbeChainAuthTicketStoreForTest()

	rememberProbeChainAuthTicket("chain-a", "ticket-a")

	resetProbeChainAuthTicketStoreForTest()
	if got := lookupProbeChainAuthTicket("chain-a"); got != "ticket-a" {
		t.Fatalf("ticket=%q want ticket-a", got)
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
	if got := resolveProbeChainTLSServerName("websocket", "203.0.113.10", "api.example.com"); got != "api.example.com" {
		t.Fatalf("websocket sni should use api domain, got: %s", got)
	}
	if got := resolveProbeChainTLSServerName("websocket-h3", "203.0.113.10", "api.example.com"); got != "api.example.com" {
		t.Fatalf("websocket-h3 sni should use api domain, got: %s", got)
	}
	if got := resolveProbeChainTLSServerName("websocket-h3", "203.0.113.10", "203.0.113.10"); got != "203.0.113.10" {
		t.Fatalf("websocket-h3 sni should fallback to dial ip when host is ip, got: %s", got)
	}
}

func buildProbeChainUserAuthTicketForTest(t *testing.T, priv ed25519.PrivateKey, chainID string, rawPublicKey string) string {
	t.Helper()
	payload := probeChainUserAuthTicketPayload{
		Version:       "chain-auth-v1",
		ChainID:       strings.TrimSpace(chainID),
		UserPublicKey: strings.TrimSpace(rawPublicKey),
		IssuedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal ticket payload: %v", err)
	}
	sig := ed25519.Sign(priv, payloadBytes)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payloadBytes) + "." + enc.EncodeToString(sig)
}

func resetProbeChainAuthReplayStoreForTest() {
	probeChainAuthReplayStore.mu.Lock()
	probeChainAuthReplayStore.items = make(map[string]time.Time)
	probeChainAuthReplayStore.mu.Unlock()
}

func resetProbeChainAuthTicketStoreForTest() {
	probeChainAuthTicketStore.mu.Lock()
	probeChainAuthTicketStore.items = make(map[string]string)
	probeChainAuthTicketStore.mu.Unlock()
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

func TestWrapProbeChainRelayDialErrorForWebSocketH3UDPSocketResource(t *testing.T) {
	baseErr := errors.New("Post \"https://69.63.223.88:16030/api/node/chain/relay?chain_id=5\": listen udp :0: bind: An operation on a socket could not be performed because the system lacked sufficient buffer space or because a queue was full.")
	err := wrapProbeChainRelayDialError("websocket-h3", "69.63.223.88", 16030, baseErr)
	if err == nil {
		t.Fatalf("expected wrapped error")
	}
	text := err.Error()
	if !strings.Contains(text, "websocket-h3 udp socket unavailable") || !strings.Contains(text, "each_proxy_group_uses_independent_quic_connection") {
		t.Fatalf("unexpected wrapped error: %v", err)
	}
	if !errors.Is(err, baseErr) {
		t.Fatalf("wrapped error should keep base error: %v", err)
	}
	if got := wrapProbeChainRelayDialError("websocket", "69.63.223.88", 16030, baseErr); got != baseErr {
		t.Fatalf("websocket error should not be wrapped: %v", got)
	}
}

func TestOpenProbeChainRelayNetConnDefaultUsesWebSocketH3Primary(t *testing.T) {
	resetProbeChainRelayProtocolStateForTest()
	defer resetProbeChainRelayProtocolStateForTest()
	originalOpenLayer := probeChainRelayOpenLayer
	originalPingPong := probeChainRelayMeasurePingPongLatency
	defer func() {
		probeChainRelayOpenLayer = originalOpenLayer
		probeChainRelayMeasurePingPongLatency = originalPingPong
	}()

	var mu sync.Mutex
	calls := make([]string, 0, 3)
	probeChainRelayOpenLayer = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration) probeChainRelayProtocolDialResult {
		client, server := net.Pipe()
		_ = server.Close()
		protocol := normalizeProbeChainLinkLayer(layer)
		mu.Lock()
		calls = append(calls, protocol)
		mu.Unlock()
		return probeChainRelayProtocolDialResult{
			Protocol: protocol,
			Conn:     client,
			Latency:  50 * time.Millisecond,
		}
	}
	probeChainRelayMeasurePingPongLatency = func(conn net.Conn) (time.Duration, error) {
		return 2 * time.Millisecond, nil
	}

	conn, err := openProbeChainRelayNetConn("chain-a", "secret-a", "relay.example.com", 16030, "", probeChainBridgeRoleToNext)
	if err != nil {
		t.Fatalf("openProbeChainRelayNetConn returned error: %v", err)
	}
	_ = conn.Close()

	snapshot := snapshotProbeChainProtocolState("relay.example.com", 16030)
	if snapshot.SelectedProtocol != "websocket-h3" {
		t.Fatalf("selected protocol=%q want websocket-h3 snapshot=%+v", snapshot.SelectedProtocol, snapshot)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != "websocket-h3" {
		t.Fatalf("expected h3 websocket primary only, got calls=%v", calls)
	}
}

func TestOpenProbeChainRelayDataStreamNetConnDefaultExpandsProtocols(t *testing.T) {
	resetProbeChainRelayProtocolStateForTest()
	defer resetProbeChainRelayProtocolStateForTest()
	originalOpenDataStreamLayer := probeChainRelayOpenDataStreamLayer
	defer func() {
		probeChainRelayOpenDataStreamLayer = originalOpenDataStreamLayer
	}()

	var mu sync.Mutex
	calls := make([]string, 0, 1)
	probeChainRelayOpenDataStreamLayer = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, connToken string, openTimeout time.Duration) probeChainRelayProtocolDialResult {
		protocol := normalizeProbeChainLinkLayer(layer)
		mu.Lock()
		calls = append(calls, protocol)
		mu.Unlock()
		client, server := net.Pipe()
		_ = server.Close()
		return probeChainRelayProtocolDialResult{
			Protocol: protocol,
			Conn:     client,
			Latency:  3 * time.Millisecond,
		}
	}

	conn, err := openProbeChainRelayDataStreamNetConnWithRoleAndToken("chain-a", "secret-a", "relay.example.com", 16030, "", probeChainBridgeRoleToNext, "token-a", time.Second)
	if err != nil {
		t.Fatalf("openProbeChainRelayDataStreamNetConnWithRoleAndToken returned error: %v", err)
	}
	_ = conn.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != "websocket-h3" {
		t.Fatalf("expected default data stream to expand to websocket-h3, got calls=%v", calls)
	}
}

func TestOpenProbeChainRelayDataStreamNetConnDefaultFallsBackAfterWebSocketH3Failure(t *testing.T) {
	resetProbeChainRelayProtocolStateForTest()
	defer resetProbeChainRelayProtocolStateForTest()
	originalOpenDataStreamLayer := probeChainRelayOpenDataStreamLayer
	defer func() {
		probeChainRelayOpenDataStreamLayer = originalOpenDataStreamLayer
	}()

	var mu sync.Mutex
	calls := make([]string, 0, 2)
	probeChainRelayOpenDataStreamLayer = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, connToken string, openTimeout time.Duration) probeChainRelayProtocolDialResult {
		protocol := normalizeProbeChainLinkLayer(layer)
		mu.Lock()
		calls = append(calls, protocol)
		mu.Unlock()
		if protocol == "websocket-h3" {
			return probeChainRelayProtocolDialResult{
				Protocol: protocol,
				Err:      errors.New("i/o timeout"),
				Latency:  5 * time.Millisecond,
			}
		}
		client, server := net.Pipe()
		_ = server.Close()
		return probeChainRelayProtocolDialResult{
			Protocol: protocol,
			Conn:     client,
			Latency:  2 * time.Millisecond,
		}
	}

	conn, err := openProbeChainRelayDataStreamNetConnWithRoleAndToken("chain-a", "secret-a", "relay.example.com", 16030, "", probeChainBridgeRoleToNext, "token-a", time.Second)
	if err != nil {
		t.Fatalf("expected websocket fallback after websocket-h3 failure, got err=%v", err)
	}
	_ = conn.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != "websocket-h3" || calls[1] != "websocket" {
		t.Fatalf("expected h3 then websocket fallback, got calls=%v", calls)
	}
}

func TestOpenProbeChainRelayNetConnDefaultFallsBackAfterWebSocketH3Failure(t *testing.T) {
	resetProbeChainRelayProtocolStateForTest()
	defer resetProbeChainRelayProtocolStateForTest()
	originalOpenLayer := probeChainRelayOpenLayer
	originalPingPong := probeChainRelayMeasurePingPongLatency
	defer func() {
		probeChainRelayOpenLayer = originalOpenLayer
		probeChainRelayMeasurePingPongLatency = originalPingPong
	}()

	var mu sync.Mutex
	calls := make([]string, 0, 4)
	probeChainRelayOpenLayer = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration) probeChainRelayProtocolDialResult {
		protocol := normalizeProbeChainLinkLayer(layer)
		mu.Lock()
		calls = append(calls, protocol)
		mu.Unlock()
		if protocol == "websocket-h3" {
			return probeChainRelayProtocolDialResult{
				Protocol: protocol,
				Err:      errors.New("i/o timeout"),
				Latency:  20 * time.Millisecond,
			}
		}
		client, server := net.Pipe()
		_ = server.Close()
		return probeChainRelayProtocolDialResult{
			Protocol: protocol,
			Conn:     client,
			Latency:  10 * time.Millisecond,
		}
	}
	probeChainRelayMeasurePingPongLatency = func(conn net.Conn) (time.Duration, error) {
		return 2 * time.Millisecond, nil
	}

	conn, err := openProbeChainRelayNetConn("chain-a", "secret-a", "relay.example.com", 16030, "", probeChainBridgeRoleToNext)
	if err != nil {
		t.Fatalf("first openProbeChainRelayNetConn returned error: %v", err)
	}
	_ = conn.Close()
	conn, err = openProbeChainRelayNetConn("chain-a", "secret-a", "relay.example.com", 16030, "", probeChainBridgeRoleToNext)
	if err != nil {
		t.Fatalf("second openProbeChainRelayNetConn returned error: %v", err)
	}
	_ = conn.Close()

	mu.Lock()
	defer mu.Unlock()
	webSocketH3Calls := 0
	websocketCalls := 0
	for _, call := range calls {
		switch call {
		case "websocket-h3":
			webSocketH3Calls++
		case "websocket":
			websocketCalls++
		}
	}
	if websocketCalls < 2 {
		t.Fatalf("websocket fallback should be tried for both attempts, calls=%v", calls)
	}
	if webSocketH3Calls < 2 {
		t.Fatalf("h3 websocket primary should be tried for both attempts, calls=%v", calls)
	}
}

func TestOpenProbeChainRelayNetConnDefaultFallsBackOnH3WebSocketContextCanceled(t *testing.T) {
	resetProbeChainRelayProtocolStateForTest()
	defer resetProbeChainRelayProtocolStateForTest()
	originalOpenLayer := probeChainRelayOpenLayer
	originalPingPong := probeChainRelayMeasurePingPongLatency
	defer func() {
		probeChainRelayOpenLayer = originalOpenLayer
		probeChainRelayMeasurePingPongLatency = originalPingPong
	}()

	probeChainRelayOpenLayer = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration) probeChainRelayProtocolDialResult {
		protocol := normalizeProbeChainLinkLayer(layer)
		if protocol == "websocket-h3" {
			return probeChainRelayProtocolDialResult{
				Protocol: protocol,
				Err:      errors.New(`dial websocket-h3: context canceled`),
				Latency:  6 * time.Second,
			}
		}
		client, server := net.Pipe()
		t.Cleanup(func() {
			_ = server.Close()
		})
		return probeChainRelayProtocolDialResult{
			Protocol: protocol,
			Conn:     client,
			Latency:  2 * time.Millisecond,
		}
	}
	probeChainRelayMeasurePingPongLatency = func(conn net.Conn) (time.Duration, error) {
		return 2 * time.Millisecond, nil
	}

	conn, err := openProbeChainRelayNetConn("chain-a", "secret-a", "relay.example.com", 16030, "", probeChainBridgeRoleToNext)
	if err != nil {
		t.Fatalf("expected websocket fallback after websocket-h3 context canceled, got err=%v", err)
	}
	if conn == nil {
		t.Fatal("expected fallback connection")
	}
	_ = conn.Close()
}

func TestOpenProbeChainRelayNetConnDefaultFallsBackOnH3WebSocketExtendedConnectDisabled(t *testing.T) {
	resetProbeChainRelayProtocolStateForTest()
	defer resetProbeChainRelayProtocolStateForTest()
	originalOpenLayer := probeChainRelayOpenLayer
	originalPingPong := probeChainRelayMeasurePingPongLatency
	defer func() {
		probeChainRelayOpenLayer = originalOpenLayer
		probeChainRelayMeasurePingPongLatency = originalPingPong
	}()

	var mu sync.Mutex
	calls := make([]string, 0, 2)
	probeChainRelayOpenLayer = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration) probeChainRelayProtocolDialResult {
		protocol := normalizeProbeChainLinkLayer(layer)
		mu.Lock()
		calls = append(calls, protocol)
		mu.Unlock()
		if protocol == "websocket-h3" {
			return probeChainRelayProtocolDialResult{
				Protocol: protocol,
				Err:      errors.New("probe relay h3 websocket failed: server did not enable extended connect"),
				Latency:  3 * time.Millisecond,
			}
		}
		client, server := net.Pipe()
		t.Cleanup(func() {
			_ = server.Close()
		})
		return probeChainRelayProtocolDialResult{
			Protocol: protocol,
			Conn:     client,
			Latency:  2 * time.Millisecond,
		}
	}
	probeChainRelayMeasurePingPongLatency = func(conn net.Conn) (time.Duration, error) {
		return 2 * time.Millisecond, nil
	}

	conn, err := openProbeChainRelayNetConn("chain-a", "secret-a", "relay.example.com", 16030, "", probeChainBridgeRoleToNext)
	if err != nil {
		t.Fatalf("expected websocket fallback after h3 extended connect disabled, got err=%v", err)
	}
	if conn == nil {
		t.Fatal("expected fallback connection")
	}
	_ = conn.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(calls) < 2 || calls[0] != "websocket-h3" || calls[1] != "websocket" {
		t.Fatalf("expected h3 websocket then websocket fallback, calls=%v", calls)
	}
}

func TestOpenProbeChainRelayNetConnDefaultDoesNotSwitchOnAuthFailure(t *testing.T) {
	resetProbeChainRelayProtocolStateForTest()
	defer resetProbeChainRelayProtocolStateForTest()
	originalOpenLayer := probeChainRelayOpenLayer
	originalPingPong := probeChainRelayMeasurePingPongLatency
	defer func() {
		probeChainRelayOpenLayer = originalOpenLayer
		probeChainRelayMeasurePingPongLatency = originalPingPong
	}()

	var mu sync.Mutex
	calls := make([]string, 0, 2)
	probeChainRelayOpenLayer = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration) probeChainRelayProtocolDialResult {
		protocol := normalizeProbeChainLinkLayer(layer)
		mu.Lock()
		calls = append(calls, protocol)
		mu.Unlock()
		if protocol == "websocket-h3" {
			return probeChainRelayProtocolDialResult{
				Protocol: protocol,
				Err:      errors.New("probe relay failed: status=401 body=unauthorized"),
				Latency:  3 * time.Millisecond,
			}
		}
		client, server := net.Pipe()
		_ = server.Close()
		return probeChainRelayProtocolDialResult{
			Protocol: protocol,
			Conn:     client,
			Latency:  1 * time.Millisecond,
		}
	}

	conn, err := openProbeChainRelayNetConn("chain-a", "secret-a", "relay.example.com", 16030, "", probeChainBridgeRoleToNext)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("expected auth failure to stop protocol switching")
	}
	if !strings.Contains(err.Error(), "status=401") {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	seenWebSocketH3 := false
	for _, call := range calls {
		if call == "websocket-h3" {
			seenWebSocketH3 = true
		}
	}
	if !seenWebSocketH3 || len(calls) != 1 {
		t.Fatalf("expected h3 websocket auth failure to stop switching, calls=%v", calls)
	}
}

func TestSnapshotProbeChainProtocolStateIncludesListenerStatusByPort(t *testing.T) {
	resetProbeChainRelayProtocolStateForTest()
	defer resetProbeChainRelayProtocolStateForTest()

	markProbeChainRelayListenerStatus("0.0.0.0:16030", "websocket-h3", "failed", "bind failed")
	snapshot := snapshotProbeChainProtocolState("relay.example.com", 16030)
	if len(snapshot.ListenerStatuses) == 0 {
		t.Fatalf("expected listener status in snapshot: %+v", snapshot)
	}
	found := false
	for _, item := range snapshot.ListenerStatuses {
		if item.Protocol == "websocket-h3" && item.Status == "failed" && strings.Contains(item.LastError, "bind failed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing websocket-h3 failed listener status: %+v", snapshot.ListenerStatuses)
	}
}

func TestProbeChainRelayProtocolProbeAndChooseUsesPingPongLatency(t *testing.T) {
	resetProbeChainRelayProtocolStateForTest()
	defer resetProbeChainRelayProtocolStateForTest()
	originalOpenLayer := probeChainRelayOpenLayer
	originalPingPong := probeChainRelayMeasurePingPongLatency
	defer func() {
		probeChainRelayOpenLayer = originalOpenLayer
		probeChainRelayMeasurePingPongLatency = originalPingPong
	}()

	probeChainRelayOpenLayer = func(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, openTimeout time.Duration) probeChainRelayProtocolDialResult {
		time.Sleep(25 * time.Millisecond)
		client, server := net.Pipe()
		t.Cleanup(func() {
			_ = server.Close()
		})
		return probeChainRelayProtocolDialResult{
			Protocol: normalizeProbeChainLinkLayer(layer),
			Conn:     client,
			Latency:  25 * time.Millisecond,
		}
	}
	probeChainRelayMeasurePingPongLatency = func(conn net.Conn) (time.Duration, error) {
		return 2 * time.Millisecond, nil
	}

	result, err := probeChainRelayProtocolProbeAndChoose("chain-a", "secret-a", "relay.example.com", 16030, probeChainBridgeRoleToNext, probeChainRelayProtocolEndpointKey("relay.example.com", 16030), []string{"websocket-h3"})
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	defer result.Conn.Close()
	if result.Latency >= 25*time.Millisecond {
		t.Fatalf("latency=%s, want ping-pong latency instead of relay open latency", result.Latency)
	}
	snapshot := snapshotProbeChainProtocolState("relay.example.com", 16030)
	if len(snapshot.ProtocolQualities) != 1 {
		t.Fatalf("protocol qualities=%+v", snapshot.ProtocolQualities)
	}
	if snapshot.ProtocolQualities[0].LatencyMS >= 25 {
		t.Fatalf("quality latency=%dms, want ping-pong latency", snapshot.ProtocolQualities[0].LatencyMS)
	}
}

func TestConsumeProbeChainRelaySpeedTestDataAcceptsPartialDurationLimit(t *testing.T) {
	reader := &probeChainSpeedTestTimeoutReader{
		chunks: [][]byte{
			[]byte("a"),
			[]byte(strings.Repeat("b", 64)),
		},
	}
	var result probeChainRelaySpeedTestResult

	consumeProbeChainRelaySpeedTestData(reader, 128*1024*1024, time.Nanosecond, &result)

	if !result.OK {
		t.Fatalf("expected partial timeout result to be OK: %+v", result)
	}
	if result.Bytes != 65 {
		t.Fatalf("unexpected bytes: %d", result.Bytes)
	}
	if result.RateBPS <= 0 {
		t.Fatalf("expected positive rate: %+v", result)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
}

func reserveProbeChainTestTCPUDPPort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			continue
		}
		port := tcpLn.Addr().(*net.TCPAddr).Port
		udpConn, udpErr := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		_ = tcpLn.Close()
		if udpErr != nil {
			continue
		}
		_ = udpConn.Close()
		return port
	}
	t.Fatal("failed to reserve tcp/udp test port")
	return 0
}

func writeProbeChainTestCertificate(t *testing.T, dataDir string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject: pkix.Name{
			CommonName: "localhost",
		},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filepath.Join(dataDir, probeTLSCertFile), certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, probeTLSKeyFile), keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	meta := []byte(`{"node_id":"node-1","domain":"localhost","not_before":"` + tmpl.NotBefore.UTC().Format(time.RFC3339) + `","not_after":"` + tmpl.NotAfter.UTC().Format(time.RFC3339) + `"}`)
	if err := os.WriteFile(filepath.Join(dataDir, probeTLSMetaFile), meta, 0o600); err != nil {
		t.Fatalf("write meta: %v", err)
	}
}

func resetProbeChainRuntimeStateForTest(t *testing.T) {
	t.Helper()
	stopAllProbeChainRuntimes("test reset")
	probeChainSharedRelayState.mu.Lock()
	sharedServers := make([]*probeChainSharedRelayServer, 0, len(probeChainSharedRelayState.servers))
	for key, shared := range probeChainSharedRelayState.servers {
		delete(probeChainSharedRelayState.servers, key)
		sharedServers = append(sharedServers, shared)
	}
	probeChainSharedRelayState.mu.Unlock()
	for _, shared := range sharedServers {
		closeProbeChainSharedRelayServer(shared)
	}
	t.Cleanup(func() {
		stopAllProbeChainRuntimes("test cleanup")
		probeChainSharedRelayState.mu.Lock()
		leftovers := make([]*probeChainSharedRelayServer, 0, len(probeChainSharedRelayState.servers))
		for key, shared := range probeChainSharedRelayState.servers {
			delete(probeChainSharedRelayState.servers, key)
			leftovers = append(leftovers, shared)
		}
		probeChainSharedRelayState.mu.Unlock()
		for _, shared := range leftovers {
			closeProbeChainSharedRelayServer(shared)
		}
	})
}

type probeChainSpeedTestTimeoutReader struct {
	chunks [][]byte
	index  int
}

func (r *probeChainSpeedTestTimeoutReader) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, probeChainSpeedTestTimeoutErr{}
	}
	chunk := r.chunks[r.index]
	r.index++
	return copy(p, chunk), nil
}

type probeChainSpeedTestTimeoutErr struct{}

func (probeChainSpeedTestTimeoutErr) Error() string   { return "i/o timeout" }
func (probeChainSpeedTestTimeoutErr) Timeout() bool   { return true }
func (probeChainSpeedTestTimeoutErr) Temporary() bool { return true }

func resetProbeChainAuthIPStateForTest() {
	probeChainAuthIPStateMap.mu.Lock()
	probeChainAuthIPStateMap.items = make(map[string]probeChainAuthIPState)
	probeChainAuthIPStateMap.mu.Unlock()
}

func resetProbeChainRelayProtocolStateForTest() {
	probeChainRelayProtocolStateStore.mu.Lock()
	probeChainRelayProtocolStateStore.items = make(map[string]*probeChainRelayProtocolState)
	probeChainRelayProtocolStateStore.mu.Unlock()
	probeChainRelayListenerStateStore.mu.Lock()
	probeChainRelayListenerStateStore.items = make(map[string]map[string]probeChainRelayListenerStatus)
	probeChainRelayListenerStateStore.mu.Unlock()
}
