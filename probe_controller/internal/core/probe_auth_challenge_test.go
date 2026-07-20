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

func TestProbeLegacyHeaderAuthenticationRejectedAfterMigration(t *testing.T) {
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{data: probeConfigData{ProbeSecrets: map[string]string{"7": "node-secret"}}}
	t.Cleanup(func() { ProbeStore = oldStore })

	req := httptest.NewRequest(http.MethodGet, "/api/probe", nil)
	req.Header.Set("X-Probe-Node-Id", "7")
	req.Header.Set("X-Probe-Timestamp", "1")
	req.Header.Set("X-Probe-Rand", "old-probe-random-token")
	req.Header.Set("X-Probe-Signature", testLegacyProbeRequestSignature("node-secret", "7", "1", "old-probe-random-token"))
	if _, err := authenticateProbeRequest(req); err == nil {
		t.Fatal("legacy timestamp authentication should be rejected after probe migration")
	}
}

func TestProbeChallengeWebSocketHandshakeReachesUpgraderBehindUnconfiguredProxy(t *testing.T) {
	t.Setenv("PROBE_TRUSTED_PROXY_CIDRS", "")
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
	req := httptest.NewRequest(http.MethodGet, "/api/probe", nil)
	req.RemoteAddr = "172.18.0.2:12345"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Probe-Node-Id", "7")
	req.Header.Set("X-Probe-Rand", challenge)
	req.Header.Set("X-Probe-Signature", testProbeRequestSignature("node-secret", "7", challenge, req.Method, req.URL.EscapedPath()))
	w := httptest.NewRecorder()
	enforceSensitiveTransportMiddleware(enforceProbeScopeMiddleware(NewMux())).ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusUpgradeRequired {
		t.Fatalf("challenge probe handshake blocked before websocket upgrade: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestProbeQuerySecretAuthenticationEnabledByDefault(t *testing.T) {
	t.Setenv("PROBE_ALLOW_LEGACY_QUERY_AUTH", "")
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{data: probeConfigData{ProbeSecrets: map[string]string{"7": "node-secret"}}}
	t.Cleanup(func() { ProbeStore = oldStore })
	req := httptest.NewRequest(http.MethodGet, "/api/probe/proxy/download?node_id=7&secret=node-secret", nil)
	if nodeID, err := authenticateProbeProxyRequest(req); err != nil || nodeID != "7" {
		t.Fatalf("default query authentication node=%q err=%v", nodeID, err)
	}
}

func TestProbeQuerySecretAuthenticationCanBeExplicitlyDisabled(t *testing.T) {
	t.Setenv("PROBE_ALLOW_LEGACY_QUERY_AUTH", "false")
	req := httptest.NewRequest(http.MethodGet, "/api/probe/proxy/download?node_id=7&secret=node-secret", nil)
	if _, err := authenticateProbeProxyRequest(req); err == nil {
		t.Fatal("query parameter secret should be rejected when explicitly disabled")
	}
}

func TestProbeAPIQuerySecretRejectedAfterMigration(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/probe/route/config?node_id=7&secret=node-secret", nil)
	if _, err := authenticateProbeRequest(req); err == nil {
		t.Fatal("non-proxy probe APIs must require challenge authentication")
	}
}

func testProbeRequestSignature(secret, nodeID, challenge, method, requestPath string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.Join([]string{nodeID, challenge, strings.ToUpper(method), requestPath}, "\n")))
	return hex.EncodeToString(mac.Sum(nil))
}

func applyProbeChallengeAuthForTest(t *testing.T, req *http.Request, nodeID, secret string) {
	t.Helper()
	challenge, err := issueProbeAuthChallenge(nodeID, time.Now())
	if err != nil {
		t.Fatalf("issue probe challenge: %v", err)
	}
	requestTarget := req.URL.EscapedPath()
	if strings.TrimSpace(req.URL.RawQuery) != "" {
		requestTarget += "?" + req.URL.RawQuery
	}
	req.Header.Set("X-Probe-Node-Id", nodeID)
	req.Header.Set("X-Probe-Rand", challenge)
	req.Header.Set("X-Probe-Signature", testProbeRequestSignature(secret, nodeID, challenge, req.Method, requestTarget))
}

func testLegacyProbeRequestSignature(secret, nodeID, timestamp, randomToken string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.Join([]string{nodeID, timestamp, randomToken}, "\n")))
	return hex.EncodeToString(mac.Sum(nil))
}
