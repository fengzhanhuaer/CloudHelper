package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func buildProbeRouteUserAuthTicketForTest(t *testing.T, priv ed25519.PrivateKey, routeID string, rawPublicKey string, fromNodeID string, toNodeID string, issuedAt ...time.Time) string {
	t.Helper()
	ts := time.Now().UTC()
	if len(issuedAt) > 0 {
		ts = issuedAt[0].UTC()
	}
	payload := probeRouteUserAuthTicketPayload{
		Version:       "route-auth-v3",
		RouteID:       strings.TrimSpace(routeID),
		ClientEntryID: "test-entry",
		UserID:        "admin",
		UserPublicKey: strings.TrimSpace(rawPublicKey),
		FromNodeID:    normalizeProbeRouteNodeID(fromNodeID),
		ToNodeID:      normalizeProbeRouteNodeID(toNodeID),
		TicketID:      "test-ticket-id",
		IssuedAt:      ts.Format(time.RFC3339),
		ExpiresAt:     ts.Add(24 * time.Hour).Format(time.RFC3339),
	}
	return signProbeRouteUserAuthTicketForTest(t, priv, payload)
}

func signProbeRouteUserAuthTicketForTest(t *testing.T, priv ed25519.PrivateKey, payload probeRouteUserAuthTicketPayload) string {
	t.Helper()
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal ticket payload: %v", err)
	}
	sig := ed25519.Sign(priv, payloadBytes)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payloadBytes) + "." + enc.EncodeToString(sig)
}

func resetProbeRouteAuthTicketStoreForTest() {
	probeRouteAuthTicketStore.mu.Lock()
	probeRouteAuthTicketStore.items = make(map[string]string)
	probeRouteAuthTicketStore.mu.Unlock()
}

func resetProbeRouteAuthIPStateForTest() {
	probeRouteAuthIPStateMap.mu.Lock()
	probeRouteAuthIPStateMap.items = make(map[string]probeRouteAuthIPState)
	probeRouteAuthIPStateMap.mu.Unlock()
}
