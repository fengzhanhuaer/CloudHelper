package main

import (
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProbeRouteTimedNonceRemainsHMACAuthenticated(t *testing.T) {
	headers := http.Header{}
	previous := ""
	for i := 0; i < 2; i++ {
		if err := applyProbeRouteSecretAuthHeaders(headers, "route-timed", "secret", "25", http.MethodGet, probeRouteRelayAPIPath, "to_next"); err != nil {
			t.Fatal(err)
		}
		nonce, err := parseProbeRouteBearerToken(headers.Get("Authorization"))
		parts := strings.Split(nonce, ":")
		if err != nil || len(parts) != 3 || parts[0] != "ts1" {
			t.Fatalf("invalid timed nonce: %q %v", nonce, err)
		}
		issued, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || time.Now().Unix()-issued > 2 || issued > time.Now().Unix() {
			t.Fatal("invalid nonce timestamp", nonce)
		}
		if random, err := hex.DecodeString(parts[2]); err != nil || len(random) != 16 || nonce == previous {
			t.Fatal("nonce must retain fresh random bytes")
		}
		if headers.Get(probeRouteCodexMACHeader) != buildProbeRouteHMAC("secret", "route-timed", nonce, http.MethodGet, probeRouteRelayAPIPath, "25", "to_next") {
			t.Fatal("timestamp and randomness must both be authenticated")
		}
		previous = nonce
	}
}
