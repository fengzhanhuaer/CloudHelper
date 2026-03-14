package tests

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudhelper/probe_controller/internal/core"
)

func newTestAuthManager(t *testing.T) *core.AuthManager {
	t.Helper()

	blacklistPath := filepath.Join(t.TempDir(), "blacklist.json")
	am, err := core.NewAuthManagerForTest(blacklistPath)
	if err != nil {
		t.Fatalf("NewAuthManagerForTest failed: %v", err)
	}
	return am
}

func TestBlacklistStorePersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blacklist.json")
	store, err := core.InitBlacklistStore(path)
	if err != nil {
		t.Fatalf("initBlacklistStore failed: %v", err)
	}

	cidr := "10.20.0.0/16"
	if err := store.AddCIDR(cidr); err != nil {
		t.Fatalf("AddCIDR failed: %v", err)
	}

	reloaded, err := core.InitBlacklistStore(path)
	if err != nil {
		t.Fatalf("reloading blacklist failed: %v", err)
	}
	if !reloaded.HasCIDR(cidr) {
		t.Fatalf("expected cidr %s to be present after reload", cidr)
	}
}

func TestValidateAndConsumeNonce(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	authManager.AddNonceForTest("n1", time.Now().Add(5*time.Second))

	if err := core.ValidateAndConsumeNonce("n1"); err != nil {
		t.Fatalf("expected valid nonce, got error: %v", err)
	}
	if err := core.ValidateAndConsumeNonce("n1"); err == nil {
		t.Fatalf("expected nonce reuse to fail")
	}
}

func TestValidateAndConsumeNonceExpired(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	authManager.AddNonceForTest("n-expired", time.Now().Add(-1*time.Second))

	err := core.ValidateAndConsumeNonce("n-expired")
	if err == nil || err.Error() != "nonce expired" {
		t.Fatalf("expected nonce expired, got: %v", err)
	}
}

func TestNonceHandlerBlacklistsAfterFiveRequests(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)

	var lastStatus int
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/nonce", nil)
		req.RemoteAddr = "10.20.30.40:4567"
		rr := httptest.NewRecorder()
		core.NonceHandler(rr, req)
		lastStatus = rr.Code
	}

	if lastStatus != http.StatusForbidden {
		t.Fatalf("expected 5th nonce request to be forbidden, got %d", lastStatus)
	}

	expectedCIDR := "10.20.0.0/16"
	if !authManager.HasCIDRForTest(expectedCIDR) {
		t.Fatalf("expected blacklist to contain %s", expectedCIDR)
	}
}

func TestLoginHandlerSuccess(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate test key pair: %v", err)
	}
	authManager.SetAdminPublicKeyForTest(pub)

	nonce := "abc123"
	authManager.AddNonceForTest(nonce, time.Now().Add(10*time.Second))

	signature := ed25519.Sign(priv, []byte(nonce))
	reqBody, err := json.Marshal(core.LoginRequest{
		Nonce:     nonce,
		Signature: base64.StdEncoding.EncodeToString(signature),
	})
	if err != nil {
		t.Fatalf("marshal request failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.20.30.40:5678"
	rr := httptest.NewRecorder()

	core.LoginHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 login success, got %d body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse login response: %v", err)
	}
	token, ok := resp["session_token"].(string)
	if !ok || token == "" {
		t.Fatalf("expected session_token in response")
	}

	if !core.IsTokenValid(token) {
		t.Fatalf("expected issued token to be valid")
	}
}

func TestLoginHandlerRequiresChallengeSecretLoaded(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)

	reqBody := bytes.NewReader([]byte(`{"nonce":"n1","signature":"abc"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", reqBody)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.20.30.40:5678"
	rr := httptest.NewRecorder()

	core.LoginHandler(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when challenge secret is missing, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminStatusHandlerAuth(t *testing.T) {
	authManager := newTestAuthManager(t)
	core.SetAuthManagerForTest(authManager)
	core.SetServerStartTimeForTest(time.Now().Add(-2 * time.Minute))

	req1 := httptest.NewRequest(http.MethodGet, "/api/admin/status", nil)
	rr1 := httptest.NewRecorder()
	core.AdminStatusHandler(rr1, req1)
	if rr1.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized without token, got %d", rr1.Code)
	}

	token := "tok123"
	authManager.AddSessionForTest(token, time.Now().Add(1*time.Minute))

	req2 := httptest.NewRequest(http.MethodGet, "/api/admin/status", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	rr2 := httptest.NewRecorder()
	core.AdminStatusHandler(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected authorized status check, got %d body=%s", rr2.Code, rr2.Body.String())
	}
}
