package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeProbeChainNodeID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "plain numeric", in: " 001 ", want: "1"},
		{name: "node dash numeric", in: "node-21", want: "21"},
		{name: "node underscore numeric", in: "Node_003", want: "3"},
		{name: "node dash text", in: "NODE-ABC", want: "abc"},
		{name: "custom id", in: " custom-id ", want: "custom-id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeProbeChainNodeID(tc.in); got != tc.want {
				t.Fatalf("normalizeProbeChainNodeID(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildChainRouteAndResolveProbeNodeChainRole(t *testing.T) {
	item := probeLinkChainServerItem{
		EntryNodeID:    "node-10",
		CascadeNodeIDs: []string{"21", "node_35", "21", ""},
		ExitNodeID:     "35",
	}

	if got, want := buildChainRoute(item), []string{"10", "21", "35"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("buildChainRoute()=%v, want %v", got, want)
	}

	if role := resolveProbeNodeChainRole(item, "10"); role != "entry" {
		t.Fatalf("role for node 10=%q, want entry", role)
	}
	if role := resolveProbeNodeChainRole(item, "node-21"); role != "relay" {
		t.Fatalf("role for node 21=%q, want relay", role)
	}
	if role := resolveProbeNodeChainRole(item, "node_35"); role != "exit" {
		t.Fatalf("role for node 35=%q, want exit", role)
	}
}

func TestResolveProbeNodeChainRoleFallbackWhenEntryMissing(t *testing.T) {
	item := probeLinkChainServerItem{
		EntryNodeID:    "",
		CascadeNodeIDs: []string{"9"},
		ExitNodeID:     "10",
	}
	if got, want := buildChainRoute(item), []string{"9", "10"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("buildChainRoute()=%v, want %v", got, want)
	}
	if role := resolveProbeNodeChainRole(item, "9"); role != "entry" {
		t.Fatalf("role for node 9=%q, want entry", role)
	}
	if role := resolveProbeNodeChainRole(item, "10"); role != "exit" {
		t.Fatalf("role for node 10=%q, want exit", role)
	}
}

func TestFindHopConfigForNodeIdentityAndLegacyFallback(t *testing.T) {
	identityItem := probeLinkChainServerItem{
		EntryNodeID:    "10",
		CascadeNodeIDs: []string{"21"},
		ExitNodeID:     "35",
		HopConfigs: []probeLinkChainHopServerItem{
			{NodeNo: 10, ListenPort: 11010},
			{NodeNo: 21, ListenPort: 12021},
			{NodeNo: 35, ListenPort: 13035},
		},
	}
	hop := findHopConfigForNode(identityItem, "node-21")
	if hop.ListenPort != 12021 || hop.NodeNo != 21 {
		t.Fatalf("identity hop match failed: %+v", hop)
	}

	legacyItem := probeLinkChainServerItem{
		EntryNodeID:    "10",
		CascadeNodeIDs: []string{"21"},
		ExitNodeID:     "35",
		HopConfigs: []probeLinkChainHopServerItem{
			{NodeNo: 1, ListenPort: 21001},
			{NodeNo: 2, ListenPort: 22002},
			{NodeNo: 3, ListenPort: 23003},
		},
	}
	hop = findHopConfigForNode(legacyItem, "21")
	if hop.ListenPort != 22002 || hop.NodeNo != 2 {
		t.Fatalf("legacy positional fallback failed: %+v", hop)
	}
}

func TestResolveProbeChainNextPrevHopFromItemWithNonContiguousNodeIDs(t *testing.T) {
	item := probeLinkChainServerItem{
		EntryNodeID:    "10",
		CascadeNodeIDs: []string{"21"},
		ExitNodeID:     "35",
		LinkLayer:      "http",
		HopConfigs: []probeLinkChainHopServerItem{
			{NodeNo: 10, ListenPort: 11010, ExternalPort: 11110, DialMode: "reverse", LinkLayer: "http2", RelayHost: "entry.example"},
			{NodeNo: 21, ListenPort: 12021, ExternalPort: 12121, DialMode: "forward", LinkLayer: "http", RelayHost: "relay.example"},
			{NodeNo: 35, ListenPort: 13035, ExternalPort: 0, DialMode: "forward", LinkLayer: "http3", RelayHost: "exit.example"},
		},
	}

	nextHost, nextPort, nextLayer, nextDialMode, nextAuthMode := resolveProbeChainNextHopFromItem(item, "10", "entry")
	if nextHost != "relay.example" || nextPort != 12121 {
		t.Fatalf("unexpected next hop: host=%q port=%d", nextHost, nextPort)
	}
	if nextLayer != "http" {
		t.Fatalf("unexpected next layer: %q", nextLayer)
	}
	if nextDialMode != probeChainDialModeReverse {
		t.Fatalf("unexpected next dial mode: %q", nextDialMode)
	}
	if nextAuthMode != "secret" {
		t.Fatalf("unexpected next auth mode: %q", nextAuthMode)
	}

	nextHost, nextPort, _, nextDialMode, nextAuthMode = resolveProbeChainNextHopFromItem(item, "35", "exit")
	if nextHost != "" || nextPort != 0 || nextDialMode != probeChainDialModeNone || nextAuthMode != "proxy" {
		t.Fatalf("unexpected exit next hop: host=%q port=%d dial=%q auth=%q", nextHost, nextPort, nextDialMode, nextAuthMode)
	}

	prevHost, prevPort, prevLayer, prevDialMode := resolveProbeChainPrevHopFromItem(item, "35", "exit")
	if prevHost != "relay.example" || prevPort != 12121 {
		t.Fatalf("unexpected prev hop for exit: host=%q port=%d", prevHost, prevPort)
	}
	if prevLayer != "http" {
		t.Fatalf("unexpected prev layer for exit: %q", prevLayer)
	}
	if prevDialMode != probeChainDialModeForward {
		t.Fatalf("unexpected prev dial mode for exit: %q", prevDialMode)
	}

	prevHost, prevPort, _, prevDialMode = resolveProbeChainPrevHopFromItem(item, "10", "entry")
	if prevHost != "" || prevPort != 0 || prevDialMode != probeChainDialModeNone {
		t.Fatalf("unexpected prev hop for entry: host=%q port=%d dial=%q", prevHost, prevPort, prevDialMode)
	}
}

func TestSignProbeControllerNonceWithLocalKeyFromDataDir(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	keyAny, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8 private key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyAny})
	if len(pemBytes) == 0 {
		t.Fatalf("encode private key pem failed")
	}

	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, probeControllerAdminPrivateKeyFileName), pemBytes, 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	t.Setenv("PROBE_NODE_DATA_DIR", dataDir)
	t.Setenv(probeControllerAdminPrivateKeyPathEnv, "")

	nonce := "nonce-for-sign"
	signature, err := signProbeControllerNonceWithLocalKey(nonce)
	if err != nil {
		t.Fatalf("sign nonce: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if !ed25519.Verify(pub, []byte(nonce), raw) {
		t.Fatalf("signature verification failed")
	}
}

func TestLoginProbeControllerSessionTokenWithLocalPrivateKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	keyAny, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8 private key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyAny})
	if len(pemBytes) == 0 {
		t.Fatalf("encode private key pem failed")
	}

	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, probeControllerAdminPrivateKeyFileName), pemBytes, 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	t.Setenv("PROBE_NODE_DATA_DIR", dataDir)
	t.Setenv(probeControllerAdminPrivateKeyPathEnv, "")

	const wantNonce = "abc123-nonce"
	const wantToken = "session-token-xyz"
	var nonceRequested bool
	var loginRequested bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case probeControllerAuthNonceAPIPath:
			nonceRequested = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"nonce": wantNonce})
			return
		case probeControllerAuthLoginAPIPath:
			loginRequested = true
			defer r.Body.Close()
			raw, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
			var payload map[string]string
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("decode login payload: %v", err)
			}
			if got := strings.TrimSpace(payload["nonce"]); got != wantNonce {
				t.Fatalf("login nonce=%q, want %q", got, wantNonce)
			}
			sig := strings.TrimSpace(payload["signature"])
			sigBytes, err := base64.StdEncoding.DecodeString(sig)
			if err != nil {
				t.Fatalf("decode login signature: %v", err)
			}
			if !ed25519.Verify(pub, []byte(wantNonce), sigBytes) {
				t.Fatalf("invalid login signature")
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"session_token": wantToken})
			return
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	token, err := loginProbeControllerSessionToken(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("login controller: %v", err)
	}
	if token != wantToken {
		t.Fatalf("session token=%q, want %q", token, wantToken)
	}
	if !nonceRequested {
		t.Fatalf("nonce endpoint not requested")
	}
	if !loginRequested {
		t.Fatalf("login endpoint not requested")
	}
}
