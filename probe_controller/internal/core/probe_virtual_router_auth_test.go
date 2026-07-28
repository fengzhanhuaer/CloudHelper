package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildProbeVirtualRouterAuthTicketUsesPrivateIdentityOnly(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate auth key: %v", err)
	}
	oldNow := probeVirtualRouterAuthTicketNow
	probeVirtualRouterAuthTicketNow = func() time.Time { return time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { probeVirtualRouterAuthTicketNow = oldNow })

	rule := probeVirtualRouterTopologyRule{
		ID:                "vr-private-auth",
		FromNodeID:        "1",
		ToNodeID:          "2",
		Secret:            "route-secret",
		UserID:            "admin",
		UserPublicKey:     base64.StdEncoding.EncodeToString(pub),
		FromTLSSPKISHA256: strings.Repeat("a", 64),
		ToTLSSPKISHA256:   strings.Repeat("b", 64),
	}
	ticket, err := buildProbeVirtualRouterAuthTicket(rule, priv)
	if err != nil {
		t.Fatalf("build auth ticket: %v", err)
	}
	parts := strings.Split(ticket, ".")
	if len(parts) != 2 {
		t.Fatalf("ticket parts=%d", len(parts))
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["version"] != "route-auth-v3" {
		t.Fatalf("ticket version=%v", payload["version"])
	}
	if _, exists := payload["from_tls_spki_sha256"]; exists {
		t.Fatalf("private auth ticket must not contain from TLS SPKI")
	}
	if _, exists := payload["to_tls_spki_sha256"]; exists {
		t.Fatalf("private auth ticket must not contain to TLS SPKI")
	}
}
