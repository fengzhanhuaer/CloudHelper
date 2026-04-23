package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func setupProbeLocalConsoleTest(t *testing.T) *http.ServeMux {
	t.Helper()
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	resetProbeLocalAuthManagerForTest()
	resetProbeLocalControlStateForTest()
	t.Cleanup(func() {
		resetProbeLocalAuthManagerForTest()
		resetProbeLocalControlStateForTest()
		resetProbeLocalProxyHooksForTest()
		resetProbeLocalTUNHooksForTest()
	})
	return buildProbeNodeHTTPMux(nodeIdentity{NodeID: "node-local", Secret: "secret-local"})
}

func doProbeLocalRequest(t *testing.T, mux *http.ServeMux, method, path string, payload any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload failed: %v", err)
		}
		body = raw
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func decodeProbeLocalJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	payload := map[string]any{}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json failed: %v body=%q", err, rr.Body.String())
	}
	return payload
}

func extractCookieByName(rr *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range rr.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func registerAndLoginProbeLocal(t *testing.T, mux *http.ServeMux, username, password string) *http.Cookie {
	t.Helper()
	registerResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/register", map[string]any{
		"username":         username,
		"password":         password,
		"confirm_password": password,
	})
	if registerResp.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", registerResp.Code, registerResp.Body.String())
	}
	loginResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/login", map[string]any{
		"username": username,
		"password": password,
	})
	if loginResp.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResp.Code, loginResp.Body.String())
	}
	cookie := extractCookieByName(loginResp, probeLocalSessionCookieName)
	if cookie == nil || cookie.Value == "" {
		t.Fatalf("missing session cookie from login response")
	}
	return cookie
}

func TestProbeLocalAuthFlowRegisterOnceAndSession(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)

	bootstrapResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/auth/bootstrap", nil)
	if bootstrapResp.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d", bootstrapResp.Code)
	}
	bootstrapPayload := decodeProbeLocalJSON(t, bootstrapResp)
	registered, ok := bootstrapPayload["registered"].(bool)
	if !ok || registered {
		t.Fatalf("bootstrap registered=%v ok=%v", bootstrapPayload["registered"], ok)
	}

	registerResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/register", map[string]any{
		"username":         "admin",
		"password":         "secret1234",
		"confirm_password": "secret1234",
	})
	if registerResp.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", registerResp.Code, registerResp.Body.String())
	}

	repeatedRegisterResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/register", map[string]any{
		"username":         "admin2",
		"password":         "secret1234",
		"confirm_password": "secret1234",
	})
	if repeatedRegisterResp.Code != http.StatusForbidden {
		t.Fatalf("repeated register status=%d body=%s", repeatedRegisterResp.Code, repeatedRegisterResp.Body.String())
	}

	loginResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/login", map[string]any{
		"username": "admin",
		"password": "secret1234",
	})
	if loginResp.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResp.Code, loginResp.Body.String())
	}
	sessionCookie := extractCookieByName(loginResp, probeLocalSessionCookieName)
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatalf("expected session cookie in login response")
	}

	sessionResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/auth/session", nil, sessionCookie)
	if sessionResp.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s", sessionResp.Code, sessionResp.Body.String())
	}
	sessionPayload := decodeProbeLocalJSON(t, sessionResp)
	authenticated, ok := sessionPayload["authenticated"].(bool)
	if !ok || !authenticated {
		t.Fatalf("session authenticated=%v ok=%v", sessionPayload["authenticated"], ok)
	}
	if sessionPayload["username"] != "admin" {
		t.Fatalf("session username=%v", sessionPayload["username"])
	}

	logoutResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/auth/logout", map[string]any{}, sessionCookie)
	if logoutResp.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", logoutResp.Code, logoutResp.Body.String())
	}

	afterLogoutSessionResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/auth/session", nil, sessionCookie)
	if afterLogoutSessionResp.Code != http.StatusUnauthorized {
		t.Fatalf("session-after-logout status=%d body=%s", afterLogoutSessionResp.Code, afterLogoutSessionResp.Body.String())
	}
}

func TestProbeLocalProtectedRoutesRequireSession(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)

	tunStatusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/tun/status", nil)
	if tunStatusResp.Code != http.StatusUnauthorized {
		t.Fatalf("tun/status without session status=%d", tunStatusResp.Code)
	}

	proxyStatusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/status", nil)
	if proxyStatusResp.Code != http.StatusUnauthorized {
		t.Fatalf("proxy/status without session status=%d", proxyStatusResp.Code)
	}

	panelResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/panel", nil)
	if panelResp.Code != http.StatusFound {
		t.Fatalf("panel without session status=%d", panelResp.Code)
	}
	if location := panelResp.Header().Get("Location"); location != "/local/login" {
		t.Fatalf("panel redirect location=%q", location)
	}
}

func TestProbeLocalProxyFlowWithSession(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	tunStatusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/tun/status", nil, sessionCookie)
	if tunStatusResp.Code != http.StatusOK {
		t.Fatalf("tun/status status=%d body=%s", tunStatusResp.Code, tunStatusResp.Body.String())
	}
	tunPayload := decodeProbeLocalJSON(t, tunStatusResp)
	if tunPayload["platform"] != runtime.GOOS {
		t.Fatalf("tun platform=%v want=%v", tunPayload["platform"], runtime.GOOS)
	}
	if installed, _ := tunPayload["installed"].(bool); installed {
		t.Fatalf("tun installed should be false at init")
	}

	proxyStatusResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/proxy/status", nil, sessionCookie)
	if proxyStatusResp.Code != http.StatusOK {
		t.Fatalf("proxy/status status=%d body=%s", proxyStatusResp.Code, proxyStatusResp.Body.String())
	}
	proxyPayload := decodeProbeLocalJSON(t, proxyStatusResp)
	if proxyPayload["mode"] != probeLocalProxyModeDirect {
		t.Fatalf("proxy mode=%v", proxyPayload["mode"])
	}
	if enabled, _ := proxyPayload["enabled"].(bool); enabled {
		t.Fatalf("proxy enabled should be false at init")
	}

	enableResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/enable", map[string]any{}, sessionCookie)
	if enableResp.Code != http.StatusConflict {
		t.Fatalf("proxy/enable status=%d body=%s", enableResp.Code, enableResp.Body.String())
	}
	enablePayload := decodeProbeLocalJSON(t, enableResp)
	if enablePayload["error"] == nil || enablePayload["error"] == "" {
		t.Fatalf("proxy/enable should return error message")
	}

	directResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/direct", map[string]any{}, sessionCookie)
	if directResp.Code != http.StatusOK {
		t.Fatalf("proxy/direct status=%d body=%s", directResp.Code, directResp.Body.String())
	}
	directPayload := decodeProbeLocalJSON(t, directResp)
	proxyObj, ok := directPayload["proxy"].(map[string]any)
	if !ok {
		t.Fatalf("proxy/direct proxy payload type=%T", directPayload["proxy"])
	}
	if proxyObj["mode"] != probeLocalProxyModeDirect {
		t.Fatalf("proxy/direct mode=%v", proxyObj["mode"])
	}
	if enabled, _ := proxyObj["enabled"].(bool); enabled {
		t.Fatalf("proxy/direct enabled should be false")
	}
}

func TestProbeLocalProxyEnableReturnsInternalErrorOnTakeoverFailure(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.mu.Unlock()

	probeLocalApplyProxyTakeover = func() error {
		return errors.New("takeover failed for test")
	}
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/enable", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("proxy/enable status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(errText, "takeover failed for test") {
		t.Fatalf("proxy/enable error=%q", errText)
	}
}

func TestProbeLocalProxyEnableReturnsNotImplementedOnUnsupported(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.mu.Unlock()

	probeLocalApplyProxyTakeover = func() error {
		return errProbeLocalProxyUnsupported
	}
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/enable", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("proxy/enable unsupported status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "not supported") {
		t.Fatalf("proxy/enable unsupported error=%q", errText)
	}
}

func TestProbeLocalProxyEnableAndDirectSuccessWithHooks(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalControl.mu.Lock()
	probeLocalControl.tun.Installed = true
	probeLocalControl.mu.Unlock()

	probeLocalApplyProxyTakeover = func() error { return nil }
	probeLocalRestoreProxyDirect = func() error { return nil }
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	enableResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/enable", map[string]any{}, sessionCookie)
	if enableResp.Code != http.StatusOK {
		t.Fatalf("proxy/enable success status=%d body=%s", enableResp.Code, enableResp.Body.String())
	}
	enablePayload := decodeProbeLocalJSON(t, enableResp)
	proxyObj, ok := enablePayload["proxy"].(map[string]any)
	if !ok {
		t.Fatalf("proxy/enable proxy payload type=%T", enablePayload["proxy"])
	}
	if proxyObj["mode"] != probeLocalProxyModeTUN {
		t.Fatalf("proxy/enable mode=%v", proxyObj["mode"])
	}
	if enabled, _ := proxyObj["enabled"].(bool); !enabled {
		t.Fatalf("proxy/enable enabled should be true")
	}
	tunObj, ok := enablePayload["tun"].(map[string]any)
	if !ok {
		t.Fatalf("proxy/enable tun payload type=%T", enablePayload["tun"])
	}
	if tunEnabled, _ := tunObj["enabled"].(bool); !tunEnabled {
		t.Fatalf("proxy/enable tun.enabled should be true")
	}

	directResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/direct", map[string]any{}, sessionCookie)
	if directResp.Code != http.StatusOK {
		t.Fatalf("proxy/direct success status=%d body=%s", directResp.Code, directResp.Body.String())
	}
	directPayload := decodeProbeLocalJSON(t, directResp)
	directProxyObj, ok := directPayload["proxy"].(map[string]any)
	if !ok {
		t.Fatalf("proxy/direct proxy payload type=%T", directPayload["proxy"])
	}
	if directProxyObj["mode"] != probeLocalProxyModeDirect {
		t.Fatalf("proxy/direct mode=%v", directProxyObj["mode"])
	}
	if enabled, _ := directProxyObj["enabled"].(bool); enabled {
		t.Fatalf("proxy/direct enabled should be false")
	}
}

func TestProbeLocalProxyDirectReturnsNotImplementedOnUnsupported(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalRestoreProxyDirect = func() error {
		return errProbeLocalProxyUnsupported
	}
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/direct", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("proxy/direct unsupported status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "not supported") {
		t.Fatalf("proxy/direct unsupported error=%q", errText)
	}
}

func TestProbeLocalProxyDirectReturnsInternalErrorOnRollbackFailure(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalRestoreProxyDirect = func() error {
		return errors.New("rollback failed for test")
	}
	t.Cleanup(func() { resetProbeLocalProxyHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/proxy/direct", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("proxy/direct status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(errText, "rollback failed for test") {
		t.Fatalf("proxy/direct error=%q", errText)
	}
}

func TestProbeLocalTUNInstallReturnsInternalErrorOnFailure(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalInstallTUNDriver = func() error {
		return errors.New("tun install failed for test")
	}
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/tun/install", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("tun/install status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(errText, "tun install failed for test") {
		t.Fatalf("tun/install error=%q", errText)
	}
}

func TestProbeLocalTUNInstallReturnsNotImplementedOnUnsupported(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalInstallTUNDriver = func() error {
		return errProbeLocalTUNUnsupported
	}
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/tun/install", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("tun/install unsupported status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "not supported") {
		t.Fatalf("tun/install unsupported error=%q", errText)
	}
}

func TestProbeLocalTUNInstallSuccessUpdatesState(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	probeLocalInstallTUNDriver = func() error { return nil }
	t.Cleanup(func() { resetProbeLocalTUNHooksForTest() })

	resp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/tun/install", map[string]any{}, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("tun/install success status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, resp)
	tunObj, ok := payload["tun"].(map[string]any)
	if !ok {
		t.Fatalf("tun/install payload type=%T", payload["tun"])
	}
	if installed, _ := tunObj["installed"].(bool); !installed {
		t.Fatalf("tun/install installed should be true")
	}
}
