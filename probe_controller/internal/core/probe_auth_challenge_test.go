package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeAuthChallengeDoesNotDependOnNodeClockAndRejectsReplay(t *testing.T) {
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{data: probeConfigData{ProbeSecrets: map[string]string{"7": "node-secret"}}}
	resetProbeAuthChallengeStateForTest()
	t.Cleanup(func() {
		ProbeStore = oldStore
		resetProbeAuthChallengeStateForTest()
	})

	challenge, err := issueProbeAuthChallenge("7", time.Now())
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/probe/route/config", nil)
	req.Header.Set("X-Probe-Node-Id", "7")
	req.Header.Set("X-Probe-Rand", challenge)
	req.Header.Set("X-Probe-Signature", testProbeRequestSignature("node-secret", "7", challenge, req.Method, req.URL.EscapedPath()))
	if nodeID, err := authenticateProbeRequest(req); err != nil || nodeID != "7" {
		t.Fatalf("authenticate node=%q err=%v", nodeID, err)
	}
	if _, err := authenticateProbeRequest(req); err == nil {
		t.Fatal("replayed challenge should be rejected")
	}
}

func TestProbeAuthChallengeBindsRequestPath(t *testing.T) {
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{data: probeConfigData{ProbeSecrets: map[string]string{"7": "node-secret"}}}
	resetProbeAuthChallengeStateForTest()
	t.Cleanup(func() {
		ProbeStore = oldStore
		resetProbeAuthChallengeStateForTest()
	})

	challenge, err := issueProbeAuthChallenge("7", time.Now())
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/probe/certificate", nil)
	req.Header.Set("X-Probe-Node-Id", "7")
	req.Header.Set("X-Probe-Rand", challenge)
	req.Header.Set("X-Probe-Signature", testProbeRequestSignature("node-secret", "7", challenge, req.Method, "/api/probe/route/config"))
	if _, err := authenticateProbeRequest(req); err == nil {
		t.Fatal("signature for another path should be rejected")
	}
}

func TestProbeQuerySecretAuthenticationDisabledByDefault(t *testing.T) {
	t.Setenv("PROBE_ALLOW_LEGACY_QUERY_AUTH", "")
	req := httptest.NewRequest(http.MethodGet, "/api/probe/route/config?node_id=7&secret=node-secret", nil)
	if _, err := authenticateProbeRequestOrQuerySecret(req); err == nil {
		t.Fatal("query parameter secret should be rejected by default")
	}
}

func testProbeRequestSignature(secret, nodeID, challenge, method, requestPath string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.Join([]string{nodeID, challenge, strings.ToUpper(method), requestPath}, "\n")))
	return hex.EncodeToString(mac.Sum(nil))
}
