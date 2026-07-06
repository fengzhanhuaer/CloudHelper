package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func buildProbeChainUserAuthTicketForTest(t *testing.T, priv ed25519.PrivateKey, chainID string, rawPublicKey string, issuedAt ...time.Time) string {
	t.Helper()
	ts := time.Now().UTC()
	if len(issuedAt) > 0 {
		ts = issuedAt[0].UTC()
	}
	payload := probeChainUserAuthTicketPayload{
		Version:       "chain-auth-v1",
		ChainID:       strings.TrimSpace(chainID),
		UserPublicKey: strings.TrimSpace(rawPublicKey),
		IssuedAt:      ts.Format(time.RFC3339),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal ticket payload: %v", err)
	}
	sig := ed25519.Sign(priv, payloadBytes)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payloadBytes) + "." + enc.EncodeToString(sig)
}

func resetProbeChainAuthTicketStoreForTest() {
	probeChainAuthTicketStore.mu.Lock()
	probeChainAuthTicketStore.items = make(map[string]string)
	probeChainAuthTicketStore.mu.Unlock()
}

func resetProbeChainAuthIPStateForTest() {
	probeChainAuthIPStateMap.mu.Lock()
	probeChainAuthIPStateMap.items = make(map[string]probeChainAuthIPState)
	probeChainAuthIPStateMap.mu.Unlock()
}
