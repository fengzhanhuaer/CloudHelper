package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudhelper/probe_controller/internal/core"
)

func setupMngTestState(t *testing.T) {
	t.Helper()

	oldStore := core.Store
	storePath := filepath.Join(t.TempDir(), "cloudhelper.json")
	core.SetStoreForTest(core.NewDataStoreForTest(storePath))
	core.ResetMngAuthManagerForTest()
	core.SetServerStartTimeForTest(time.Now().Add(-90 * time.Second))

	t.Cleanup(func() {
		core.SetStoreForTest(oldStore)
		core.ResetMngAuthManagerForTest()
	})
}

func decodeJSONMap(t *testing.T, rr *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to decode json response: %v body=%s", err, rr.Body.String())
	}
	return out
}

func findCookieByName(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c != nil && strings.TrimSpace(c.Name) == name {
			return c
		}
	}
	return nil
}

func TestMngBootstrapRegisterLoginLogoutFlow(t *testing.T) {
	setupMngTestState(t)
	mux := core.NewMux()

	bootstrapReq := httptest.NewRequest(http.MethodGet, "/mng/api/bootstrap", nil)
	bootstrapRR := httptest.NewRecorder()
	mux.ServeHTTP(bootstrapRR, bootstrapReq)
	if bootstrapRR.Code != http.StatusOK {
		t.Fatalf("expected bootstrap to return 200, got %d body=%s", bootstrapRR.Code, bootstrapRR.Body.String())
	}
	bootstrapPayload := decodeJSONMap(t, bootstrapRR)
	if registered, _ := bootstrapPayload["registered"].(bool); registered {
		t.Fatalf("expected initial registered=false, got payload=%+v", bootstrapPayload)
	}

	registerBody := []byte(`{"username":"mng-admin","password":"Passw0rd!","confirm_password":"Passw0rd!"}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/mng/api/register", bytes.NewReader(registerBody))
	registerReq.Header.Set("Content-Type", "application/json")
	registerRR := httptest.NewRecorder()
	mux.ServeHTTP(registerRR, registerReq)
	if registerRR.Code != http.StatusOK {
		t.Fatalf("expected register to return 200, got %d body=%s", registerRR.Code, registerRR.Body.String())
	}

	bootstrapReq2 := httptest.NewRequest(http.MethodGet, "/mng/api/bootstrap", nil)
	bootstrapRR2 := httptest.NewRecorder()
	mux.ServeHTTP(bootstrapRR2, bootstrapReq2)
	if bootstrapRR2.Code != http.StatusOK {
		t.Fatalf("expected bootstrap after register to return 200, got %d", bootstrapRR2.Code)
	}
	bootstrapPayload2 := decodeJSONMap(t, bootstrapRR2)
	if registered, _ := bootstrapPayload2["registered"].(bool); !registered {
		t.Fatalf("expected registered=true after register, got payload=%+v", bootstrapPayload2)
	}

	registerAgainReq := httptest.NewRequest(http.MethodPost, "/mng/api/register", bytes.NewReader(registerBody))
	registerAgainReq.Header.Set("Content-Type", "application/json")
	registerAgainRR := httptest.NewRecorder()
	mux.ServeHTTP(registerAgainRR, registerAgainReq)
	if registerAgainRR.Code != http.StatusForbidden {
		t.Fatalf("expected second register to return 403, got %d body=%s", registerAgainRR.Code, registerAgainRR.Body.String())
	}

	loginBadReq := httptest.NewRequest(http.MethodPost, "/mng/api/login", bytes.NewReader([]byte(`{"username":"mng-admin","password":"wrong"}`)))
	loginBadReq.Header.Set("Content-Type", "application/json")
	loginBadReq.RemoteAddr = "10.10.10.10:1234"
	loginBadRR := httptest.NewRecorder()
	mux.ServeHTTP(loginBadRR, loginBadReq)
	if loginBadRR.Code != http.StatusUnauthorized {
		t.Fatalf("expected invalid login to return 401, got %d body=%s", loginBadRR.Code, loginBadRR.Body.String())
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/mng/api/login", bytes.NewReader([]byte(`{"username":"mng-admin","password":"Passw0rd!"}`)))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.RemoteAddr = "10.10.10.10:1234"
	loginRR := httptest.NewRecorder()
	mux.ServeHTTP(loginRR, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("expected valid login to return 200, got %d body=%s", loginRR.Code, loginRR.Body.String())
	}

	cookie := findCookieByName(loginRR.Result().Cookies(), "mng_session")
	if cookie == nil {
		t.Fatalf("expected login response to include mng_session cookie")
	}
	if cookie.Path != "/mng" {
		t.Fatalf("expected mng cookie path /mng, got %q", cookie.Path)
	}
	if !cookie.HttpOnly {
		t.Fatalf("expected mng cookie to be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("expected mng cookie SameSite=Strict, got %v", cookie.SameSite)
	}

	sessionReq := httptest.NewRequest(http.MethodGet, "/mng/api/session", nil)
	sessionReq.AddCookie(cookie)
	sessionRR := httptest.NewRecorder()
	mux.ServeHTTP(sessionRR, sessionReq)
	if sessionRR.Code != http.StatusOK {
		t.Fatalf("expected session route with cookie to return 200, got %d body=%s", sessionRR.Code, sessionRR.Body.String())
	}
	sessionPayload := decodeJSONMap(t, sessionRR)
	if authenticated, _ := sessionPayload["authenticated"].(bool); !authenticated {
		t.Fatalf("expected authenticated=true on session route, got payload=%+v", sessionPayload)
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/mng/api/logout", nil)
	logoutReq.AddCookie(cookie)
	logoutRR := httptest.NewRecorder()
	mux.ServeHTTP(logoutRR, logoutReq)
	if logoutRR.Code != http.StatusOK {
		t.Fatalf("expected logout to return 200, got %d body=%s", logoutRR.Code, logoutRR.Body.String())
	}

	sessionAfterLogoutReq := httptest.NewRequest(http.MethodGet, "/mng/api/session", nil)
	sessionAfterLogoutReq.AddCookie(cookie)
	sessionAfterLogoutRR := httptest.NewRecorder()
	mux.ServeHTTP(sessionAfterLogoutRR, sessionAfterLogoutReq)
	if sessionAfterLogoutRR.Code != http.StatusOK {
		t.Fatalf("expected session route after logout to return 200, got %d", sessionAfterLogoutRR.Code)
	}
	sessionAfterLogoutPayload := decodeJSONMap(t, sessionAfterLogoutRR)
	if authenticated, _ := sessionAfterLogoutPayload["authenticated"].(bool); authenticated {
		t.Fatalf("expected authenticated=false after logout, got payload=%+v", sessionAfterLogoutPayload)
	}
}

func TestMngPanelProtectionAndSummary(t *testing.T) {
	setupMngTestState(t)
	mux := core.NewMux()

	panelReqWithoutCookie := httptest.NewRequest(http.MethodGet, "/mng/panel", nil)
	panelRRWithoutCookie := httptest.NewRecorder()
	mux.ServeHTTP(panelRRWithoutCookie, panelReqWithoutCookie)
	if panelRRWithoutCookie.Code != http.StatusFound {
		t.Fatalf("expected /mng/panel without cookie to redirect, got %d body=%s", panelRRWithoutCookie.Code, panelRRWithoutCookie.Body.String())
	}
	if loc := panelRRWithoutCookie.Header().Get("Location"); loc != "/mng" {
		t.Fatalf("expected /mng/panel redirect location /mng, got %q", loc)
	}

	settingsReqWithoutCookie := httptest.NewRequest(http.MethodGet, "/mng/settings", nil)
	settingsRRWithoutCookie := httptest.NewRecorder()
	mux.ServeHTTP(settingsRRWithoutCookie, settingsReqWithoutCookie)
	if settingsRRWithoutCookie.Code != http.StatusFound {
		t.Fatalf("expected /mng/settings without cookie to redirect, got %d body=%s", settingsRRWithoutCookie.Code, settingsRRWithoutCookie.Body.String())
	}
	if loc := settingsRRWithoutCookie.Header().Get("Location"); loc != "/mng" {
		t.Fatalf("expected /mng/settings redirect location /mng, got %q", loc)
	}

	summaryReqWithoutCookie := httptest.NewRequest(http.MethodGet, "/mng/api/panel/summary", nil)
	summaryRRWithoutCookie := httptest.NewRecorder()
	mux.ServeHTTP(summaryRRWithoutCookie, summaryReqWithoutCookie)
	if summaryRRWithoutCookie.Code != http.StatusUnauthorized {
		t.Fatalf("expected /mng/api/panel/summary without cookie to return 401, got %d body=%s", summaryRRWithoutCookie.Code, summaryRRWithoutCookie.Body.String())
	}

	probeReqWithoutCookie := httptest.NewRequest(http.MethodGet, "/mng/probe", nil)
	probeRRWithoutCookie := httptest.NewRecorder()
	mux.ServeHTTP(probeRRWithoutCookie, probeReqWithoutCookie)
	if probeRRWithoutCookie.Code != http.StatusFound {
		t.Fatalf("expected /mng/probe without cookie to redirect, got %d body=%s", probeRRWithoutCookie.Code, probeRRWithoutCookie.Body.String())
	}
	if loc := probeRRWithoutCookie.Header().Get("Location"); loc != "/mng" {
		t.Fatalf("expected /mng/probe redirect location /mng, got %q", loc)
	}

	probeNodesReqWithoutCookie := httptest.NewRequest(http.MethodGet, "/mng/api/probe/nodes", nil)
	probeNodesRRWithoutCookie := httptest.NewRecorder()
	mux.ServeHTTP(probeNodesRRWithoutCookie, probeNodesReqWithoutCookie)
	if probeNodesRRWithoutCookie.Code != http.StatusUnauthorized {
		t.Fatalf("expected /mng/api/probe/nodes without cookie to return 401, got %d body=%s", probeNodesRRWithoutCookie.Code, probeNodesRRWithoutCookie.Body.String())
	}

	registerBody := []byte(`{"username":"panel-admin","password":"Passw0rd!","confirm_password":"Passw0rd!"}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/mng/api/register", bytes.NewReader(registerBody))
	registerReq.Header.Set("Content-Type", "application/json")
	registerRR := httptest.NewRecorder()
	mux.ServeHTTP(registerRR, registerReq)
	if registerRR.Code != http.StatusOK {
		t.Fatalf("expected register to return 200, got %d body=%s", registerRR.Code, registerRR.Body.String())
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/mng/api/login", bytes.NewReader([]byte(`{"username":"panel-admin","password":"Passw0rd!"}`)))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.RemoteAddr = "10.1.1.2:3456"
	loginRR := httptest.NewRecorder()
	mux.ServeHTTP(loginRR, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("expected login to return 200, got %d body=%s", loginRR.Code, loginRR.Body.String())
	}
	cookie := findCookieByName(loginRR.Result().Cookies(), "mng_session")
	if cookie == nil {
		t.Fatalf("expected mng_session cookie after login")
	}

	panelReq := httptest.NewRequest(http.MethodGet, "/mng/panel", nil)
	panelReq.AddCookie(cookie)
	panelRR := httptest.NewRecorder()
	mux.ServeHTTP(panelRR, panelReq)
	if panelRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/panel with session to return 200, got %d body=%s", panelRR.Code, panelRR.Body.String())
	}
	if !strings.Contains(panelRR.Body.String(), "系统设置") {
		t.Fatalf("expected /mng/panel html to include settings tile")
	}
	if !strings.Contains(panelRR.Body.String(), "探针管理") {
		t.Fatalf("expected /mng/panel html to include probe management tile")
	}

	settingsReq := httptest.NewRequest(http.MethodGet, "/mng/settings", nil)
	settingsReq.AddCookie(cookie)
	settingsRR := httptest.NewRecorder()
	mux.ServeHTTP(settingsRR, settingsReq)
	if settingsRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/settings with session to return 200, got %d body=%s", settingsRR.Code, settingsRR.Body.String())
	}
	if !strings.Contains(settingsRR.Body.String(), "检查更新") {
		t.Fatalf("expected /mng/settings html to include check update button")
	}

	probeReq := httptest.NewRequest(http.MethodGet, "/mng/probe", nil)
	probeReq.AddCookie(cookie)
	probeRR := httptest.NewRecorder()
	mux.ServeHTTP(probeRR, probeReq)
	if probeRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/probe with session to return 200, got %d body=%s", probeRR.Code, probeRR.Body.String())
	}
	if !strings.Contains(probeRR.Body.String(), "探针列表") {
		t.Fatalf("expected /mng/probe html to include probe list tab")
	}
	if !strings.Contains(probeRR.Body.String(), "探针 Shell") {
		t.Fatalf("expected /mng/probe html to include probe shell tab")
	}

	summaryReq := httptest.NewRequest(http.MethodGet, "/mng/api/panel/summary", nil)
	summaryReq.AddCookie(cookie)
	summaryRR := httptest.NewRecorder()
	mux.ServeHTTP(summaryRR, summaryReq)
	if summaryRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/api/panel/summary with session to return 200, got %d body=%s", summaryRR.Code, summaryRR.Body.String())
	}
	summaryPayload := decodeJSONMap(t, summaryRR)
	if _, ok := summaryPayload["uptime"]; !ok {
		t.Fatalf("expected summary to include uptime, payload=%+v", summaryPayload)
	}
	version, _ := summaryPayload["version"].(string)
	if strings.TrimSpace(version) == "" {
		t.Fatalf("expected summary to include version")
	}

	versionReqWithoutCookie := httptest.NewRequest(http.MethodGet, "/mng/api/system/version", nil)
	versionRRWithoutCookie := httptest.NewRecorder()
	mux.ServeHTTP(versionRRWithoutCookie, versionReqWithoutCookie)
	if versionRRWithoutCookie.Code != http.StatusUnauthorized {
		t.Fatalf("expected /mng/api/system/version without cookie to return 401, got %d body=%s", versionRRWithoutCookie.Code, versionRRWithoutCookie.Body.String())
	}

	progressReqWithoutCookie := httptest.NewRequest(http.MethodGet, "/mng/api/system/upgrade/progress", nil)
	progressRRWithoutCookie := httptest.NewRecorder()
	mux.ServeHTTP(progressRRWithoutCookie, progressReqWithoutCookie)
	if progressRRWithoutCookie.Code != http.StatusUnauthorized {
		t.Fatalf("expected /mng/api/system/upgrade/progress without cookie to return 401, got %d body=%s", progressRRWithoutCookie.Code, progressRRWithoutCookie.Body.String())
	}

	progressReq := httptest.NewRequest(http.MethodGet, "/mng/api/system/upgrade/progress", nil)
	progressReq.AddCookie(cookie)
	progressRR := httptest.NewRecorder()
	mux.ServeHTTP(progressRR, progressReq)
	if progressRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/api/system/upgrade/progress with session to return 200, got %d body=%s", progressRR.Code, progressRR.Body.String())
	}
	progressPayload := decodeJSONMap(t, progressRR)
	if _, ok := progressPayload["active"]; !ok {
		t.Fatalf("expected progress payload to include active, got %+v", progressPayload)
	}
	if _, ok := progressPayload["percent"]; !ok {
		t.Fatalf("expected progress payload to include percent, got %+v", progressPayload)
	}

	probeNodesReq := httptest.NewRequest(http.MethodGet, "/mng/api/probe/nodes", nil)
	probeNodesReq.AddCookie(cookie)
	probeNodesRR := httptest.NewRecorder()
	mux.ServeHTTP(probeNodesRR, probeNodesReq)
	if probeNodesRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/api/probe/nodes with session to return 200, got %d body=%s", probeNodesRR.Code, probeNodesRR.Body.String())
	}
	probeNodesPayload := decodeJSONMap(t, probeNodesRR)
	if _, ok := probeNodesPayload["nodes"]; !ok {
		t.Fatalf("expected probe nodes payload to include nodes, got %+v", probeNodesPayload)
	}

	reconnectReq := httptest.NewRequest(http.MethodGet, "/mng/api/system/reconnect/check", nil)
	reconnectRR := httptest.NewRecorder()
	mux.ServeHTTP(reconnectRR, reconnectReq)
	if reconnectRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/api/system/reconnect/check to return 200, got %d body=%s", reconnectRR.Code, reconnectRR.Body.String())
	}
	reconnectPayload := decodeJSONMap(t, reconnectRR)
	if ok, _ := reconnectPayload["ok"].(bool); !ok {
		t.Fatalf("expected reconnect check payload ok=true, got %+v", reconnectPayload)
	}
}
