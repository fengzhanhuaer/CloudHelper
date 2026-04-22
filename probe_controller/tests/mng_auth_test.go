package tests

import (
	"bytes"
	"crypto/ed25519"
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

	authMgr, err := core.NewAuthManagerForTest(filepath.Join(t.TempDir(), "blacklist.json"))
	if err != nil {
		t.Fatalf("NewAuthManagerForTest failed: %v", err)
	}
	pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
	for i := range pub {
		pub[i] = byte(i + 1)
	}
	authMgr.SetAdminPublicKeyForTest(pub)
	core.SetAuthManagerForTest(authMgr)

	t.Cleanup(func() {
		core.SetStoreForTest(oldStore)
		core.ResetMngAuthManagerForTest()
		core.SetAuthManagerForTest(nil)
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

	linkReqWithoutCookie := httptest.NewRequest(http.MethodGet, "/mng/link", nil)
	linkRRWithoutCookie := httptest.NewRecorder()
	mux.ServeHTTP(linkRRWithoutCookie, linkReqWithoutCookie)
	if linkRRWithoutCookie.Code != http.StatusFound {
		t.Fatalf("expected /mng/link without cookie to redirect, got %d body=%s", linkRRWithoutCookie.Code, linkRRWithoutCookie.Body.String())
	}
	if loc := linkRRWithoutCookie.Header().Get("Location"); loc != "/mng" {
		t.Fatalf("expected /mng/link redirect location /mng, got %q", loc)
	}

	linkUsersReqWithoutCookie := httptest.NewRequest(http.MethodGet, "/mng/api/link/users", nil)
	linkUsersRRWithoutCookie := httptest.NewRecorder()
	mux.ServeHTTP(linkUsersRRWithoutCookie, linkUsersReqWithoutCookie)
	if linkUsersRRWithoutCookie.Code != http.StatusUnauthorized {
		t.Fatalf("expected /mng/api/link/users without cookie to return 401, got %d body=%s", linkUsersRRWithoutCookie.Code, linkUsersRRWithoutCookie.Body.String())
	}

	cloudflareReqWithoutCookie := httptest.NewRequest(http.MethodGet, "/mng/cloudflare", nil)
	cloudflareRRWithoutCookie := httptest.NewRecorder()
	mux.ServeHTTP(cloudflareRRWithoutCookie, cloudflareReqWithoutCookie)
	if cloudflareRRWithoutCookie.Code != http.StatusFound {
		t.Fatalf("expected /mng/cloudflare without cookie to redirect, got %d body=%s", cloudflareRRWithoutCookie.Code, cloudflareRRWithoutCookie.Body.String())
	}
	if loc := cloudflareRRWithoutCookie.Header().Get("Location"); loc != "/mng" {
		t.Fatalf("expected /mng/cloudflare redirect location /mng, got %q", loc)
	}

	cloudflareAPIReqWithoutCookie := httptest.NewRequest(http.MethodGet, "/mng/api/cloudflare/api", nil)
	cloudflareAPIRRWithoutCookie := httptest.NewRecorder()
	mux.ServeHTTP(cloudflareAPIRRWithoutCookie, cloudflareAPIReqWithoutCookie)
	if cloudflareAPIRRWithoutCookie.Code != http.StatusUnauthorized {
		t.Fatalf("expected /mng/api/cloudflare/api without cookie to return 401, got %d body=%s", cloudflareAPIRRWithoutCookie.Code, cloudflareAPIRRWithoutCookie.Body.String())
	}

	tgReqWithoutCookie := httptest.NewRequest(http.MethodGet, "/mng/tg", nil)
	tgRRWithoutCookie := httptest.NewRecorder()
	mux.ServeHTTP(tgRRWithoutCookie, tgReqWithoutCookie)
	if tgRRWithoutCookie.Code != http.StatusFound {
		t.Fatalf("expected /mng/tg without cookie to redirect, got %d body=%s", tgRRWithoutCookie.Code, tgRRWithoutCookie.Body.String())
	}
	if loc := tgRRWithoutCookie.Header().Get("Location"); loc != "/mng" {
		t.Fatalf("expected /mng/tg redirect location /mng, got %q", loc)
	}

	tgAPIReqWithoutCookie := httptest.NewRequest(http.MethodGet, "/mng/api/tg/api/get", nil)
	tgAPIRRWithoutCookie := httptest.NewRecorder()
	mux.ServeHTTP(tgAPIRRWithoutCookie, tgAPIReqWithoutCookie)
	if tgAPIRRWithoutCookie.Code != http.StatusUnauthorized {
		t.Fatalf("expected /mng/api/tg/api/get without cookie to return 401, got %d body=%s", tgAPIRRWithoutCookie.Code, tgAPIRRWithoutCookie.Body.String())
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
	if !strings.Contains(panelRR.Body.String(), "链路管理") {
		t.Fatalf("expected /mng/panel html to include link management tile")
	}
	if !strings.Contains(panelRR.Body.String(), "Cloudflare 管理") {
		t.Fatalf("expected /mng/panel html to include cloudflare tile")
	}
	if !strings.Contains(panelRR.Body.String(), "TG 助手") {
		t.Fatalf("expected /mng/panel html to include tg tile")
	}
	if !strings.Contains(panelRR.Body.String(), "探针面板") {
		t.Fatalf("expected /mng/panel html to include dashboard tile")
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

	linkReq := httptest.NewRequest(http.MethodGet, "/mng/link", nil)
	linkReq.AddCookie(cookie)
	linkRR := httptest.NewRecorder()
	mux.ServeHTTP(linkRR, linkReq)
	if linkRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/link with session to return 200, got %d body=%s", linkRR.Code, linkRR.Body.String())
	}
	if !strings.Contains(linkRR.Body.String(), "链路管理") {
		t.Fatalf("expected /mng/link html to include page title")
	}
	if !strings.Contains(linkRR.Body.String(), "链路添加") {
		t.Fatalf("expected /mng/link html to include add tab")
	}
	if !strings.Contains(linkRR.Body.String(), "链路查看") {
		t.Fatalf("expected /mng/link html to include list tab")
	}
	if !strings.Contains(linkRR.Body.String(), "端口转发") {
		t.Fatalf("expected /mng/link html to include port forward tab")
	}

	linkUsersReq := httptest.NewRequest(http.MethodGet, "/mng/api/link/users", nil)
	linkUsersReq.AddCookie(cookie)
	linkUsersRR := httptest.NewRecorder()
	mux.ServeHTTP(linkUsersRR, linkUsersReq)
	if linkUsersRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/api/link/users with session to return 200, got %d body=%s", linkUsersRR.Code, linkUsersRR.Body.String())
	}
	linkUsersPayload := decodeJSONMap(t, linkUsersRR)
	if _, ok := linkUsersPayload["users"]; !ok {
		t.Fatalf("expected link users payload to include users, got %+v", linkUsersPayload)
	}

	linkPublicKeyReq := httptest.NewRequest(http.MethodGet, "/mng/api/link/user/public_key", nil)
	linkPublicKeyReq.AddCookie(cookie)
	linkPublicKeyRR := httptest.NewRecorder()
	mux.ServeHTTP(linkPublicKeyRR, linkPublicKeyReq)
	if linkPublicKeyRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/api/link/user/public_key with session to return 200, got %d body=%s", linkPublicKeyRR.Code, linkPublicKeyRR.Body.String())
	}
	linkPublicKeyPayload := decodeJSONMap(t, linkPublicKeyRR)
	if _, ok := linkPublicKeyPayload["public_key"]; !ok {
		t.Fatalf("expected link public key payload to include public_key, got %+v", linkPublicKeyPayload)
	}

	linkChainsReq := httptest.NewRequest(http.MethodGet, "/mng/api/link/chains", nil)
	linkChainsReq.AddCookie(cookie)
	linkChainsRR := httptest.NewRecorder()
	mux.ServeHTTP(linkChainsRR, linkChainsReq)
	if linkChainsRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/api/link/chains with session to return 200, got %d body=%s", linkChainsRR.Code, linkChainsRR.Body.String())
	}
	linkChainsPayload := decodeJSONMap(t, linkChainsRR)
	if _, ok := linkChainsPayload["items"]; !ok {
		t.Fatalf("expected link chains payload to include items, got %+v", linkChainsPayload)
	}

	cloudflareReq := httptest.NewRequest(http.MethodGet, "/mng/cloudflare", nil)
	cloudflareReq.AddCookie(cookie)
	cloudflareRR := httptest.NewRecorder()
	mux.ServeHTTP(cloudflareRR, cloudflareReq)
	if cloudflareRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/cloudflare with session to return 200, got %d body=%s", cloudflareRR.Code, cloudflareRR.Body.String())
	}
	if !strings.Contains(cloudflareRR.Body.String(), "基础设置") {
		t.Fatalf("expected /mng/cloudflare html to include settings tab")
	}
	if !strings.Contains(cloudflareRR.Body.String(), "DDNS") {
		t.Fatalf("expected /mng/cloudflare html to include ddns tab")
	}
	if !strings.Contains(cloudflareRR.Body.String(), "ZeroTrust") {
		t.Fatalf("expected /mng/cloudflare html to include zerotrust tab")
	}

	cloudflareAPIReq := httptest.NewRequest(http.MethodGet, "/mng/api/cloudflare/api", nil)
	cloudflareAPIReq.AddCookie(cookie)
	cloudflareAPIRR := httptest.NewRecorder()
	mux.ServeHTTP(cloudflareAPIRR, cloudflareAPIReq)
	if cloudflareAPIRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/api/cloudflare/api with session to return 200, got %d body=%s", cloudflareAPIRR.Code, cloudflareAPIRR.Body.String())
	}
	cloudflareAPIPayload := decodeJSONMap(t, cloudflareAPIRR)
	if _, ok := cloudflareAPIPayload["configured"]; !ok {
		t.Fatalf("expected cloudflare api payload to include configured, got %+v", cloudflareAPIPayload)
	}

	tgReq := httptest.NewRequest(http.MethodGet, "/mng/tg", nil)
	tgReq.AddCookie(cookie)
	tgRR := httptest.NewRecorder()
	mux.ServeHTTP(tgRR, tgReq)
	if tgRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/tg with session to return 200, got %d body=%s", tgRR.Code, tgRR.Body.String())
	}
	if !strings.Contains(tgRR.Body.String(), "共享 API Key") {
		t.Fatalf("expected /mng/tg html to include api key section")
	}
	if !strings.Contains(tgRR.Body.String(), "账号与登录") {
		t.Fatalf("expected /mng/tg html to include account/login section")
	}
	if !strings.Contains(tgRR.Body.String(), "任务管理") {
		t.Fatalf("expected /mng/tg html to include schedule section")
	}
	if !strings.Contains(tgRR.Body.String(), "Bot 管理") {
		t.Fatalf("expected /mng/tg html to include bot section")
	}

	tgAPIReq := httptest.NewRequest(http.MethodGet, "/mng/api/tg/api/get", nil)
	tgAPIReq.AddCookie(cookie)
	tgAPIRR := httptest.NewRecorder()
	mux.ServeHTTP(tgAPIRR, tgAPIReq)
	if tgAPIRR.Code != http.StatusOK {
		t.Fatalf("expected /mng/api/tg/api/get with session to return 200, got %d body=%s", tgAPIRR.Code, tgAPIRR.Body.String())
	}
	tgAPIPayload := decodeJSONMap(t, tgAPIRR)
	if _, ok := tgAPIPayload["configured"]; !ok {
		t.Fatalf("expected tg api payload to include configured, got %+v", tgAPIPayload)
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

func TestMngCloudflareAPIErrorBranches(t *testing.T) {
	setupMngTestState(t)
	mux := core.NewMux()

	registerBody := []byte(`{"username":"cf-admin","password":"Passw0rd!","confirm_password":"Passw0rd!"}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/mng/api/register", bytes.NewReader(registerBody))
	registerReq.Header.Set("Content-Type", "application/json")
	registerRR := httptest.NewRecorder()
	mux.ServeHTTP(registerRR, registerReq)
	if registerRR.Code != http.StatusOK {
		t.Fatalf("expected register to return 200, got %d body=%s", registerRR.Code, registerRR.Body.String())
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/mng/api/login", bytes.NewReader([]byte(`{"username":"cf-admin","password":"Passw0rd!"}`)))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.RemoteAddr = "10.9.9.9:3456"
	loginRR := httptest.NewRecorder()
	mux.ServeHTTP(loginRR, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("expected login to return 200, got %d body=%s", loginRR.Code, loginRR.Body.String())
	}
	cookie := findCookieByName(loginRR.Result().Cookies(), "mng_session")
	if cookie == nil {
		t.Fatalf("expected mng_session cookie after login")
	}

	ddnsInvalidReq := httptest.NewRequest(http.MethodPost, "/mng/api/cloudflare/ddns/apply", bytes.NewReader([]byte(`{"zone_name"`)))
	ddnsInvalidReq.Header.Set("Content-Type", "application/json")
	ddnsInvalidReq.AddCookie(cookie)
	ddnsInvalidRR := httptest.NewRecorder()
	mux.ServeHTTP(ddnsInvalidRR, ddnsInvalidReq)
	if ddnsInvalidRR.Code != http.StatusBadRequest {
		t.Fatalf("expected ddns apply invalid json to return 400, got %d body=%s", ddnsInvalidRR.Code, ddnsInvalidRR.Body.String())
	}

	ddnsApplyReq := httptest.NewRequest(http.MethodPost, "/mng/api/cloudflare/ddns/apply", bytes.NewReader([]byte(`{}`)))
	ddnsApplyReq.Header.Set("Content-Type", "application/json")
	ddnsApplyReq.AddCookie(cookie)
	ddnsApplyRR := httptest.NewRecorder()
	mux.ServeHTTP(ddnsApplyRR, ddnsApplyReq)
	if ddnsApplyRR.Code != http.StatusInternalServerError {
		t.Fatalf("expected ddns apply to return 500 when cloudflare store is not initialized, got %d body=%s", ddnsApplyRR.Code, ddnsApplyRR.Body.String())
	}
	ddnsApplyPayload := decodeJSONMap(t, ddnsApplyRR)
	ddnsApplyErr, _ := ddnsApplyPayload["error"].(string)
	if !strings.Contains(ddnsApplyErr, "cloudflare datastore is not initialized") {
		t.Fatalf("expected ddns apply error to mention cloudflare datastore not initialized, got %+v", ddnsApplyPayload)
	}

	zeroTrustInvalidReq := httptest.NewRequest(http.MethodPost, "/mng/api/cloudflare/zerotrust/whitelist", bytes.NewReader([]byte(`{"enabled"`)))
	zeroTrustInvalidReq.Header.Set("Content-Type", "application/json")
	zeroTrustInvalidReq.AddCookie(cookie)
	zeroTrustInvalidRR := httptest.NewRecorder()
	mux.ServeHTTP(zeroTrustInvalidRR, zeroTrustInvalidReq)
	if zeroTrustInvalidRR.Code != http.StatusBadRequest {
		t.Fatalf("expected zerotrust whitelist invalid json to return 400, got %d body=%s", zeroTrustInvalidRR.Code, zeroTrustInvalidRR.Body.String())
	}

	zeroTrustReq := httptest.NewRequest(http.MethodPost, "/mng/api/cloudflare/zerotrust/whitelist", bytes.NewReader([]byte(`{"enabled":true}`)))
	zeroTrustReq.Header.Set("Content-Type", "application/json")
	zeroTrustReq.AddCookie(cookie)
	zeroTrustRR := httptest.NewRecorder()
	mux.ServeHTTP(zeroTrustRR, zeroTrustReq)
	if zeroTrustRR.Code != http.StatusInternalServerError {
		t.Fatalf("expected zerotrust whitelist to return 500 when cloudflare store is not initialized, got %d body=%s", zeroTrustRR.Code, zeroTrustRR.Body.String())
	}
	zeroTrustPayload := decodeJSONMap(t, zeroTrustRR)
	zeroTrustErr, _ := zeroTrustPayload["error"].(string)
	if !strings.Contains(zeroTrustErr, "cloudflare datastore is not initialized") {
		t.Fatalf("expected zerotrust whitelist error to mention cloudflare datastore not initialized, got %+v", zeroTrustPayload)
	}

	zeroTrustRunInvalidReq := httptest.NewRequest(http.MethodPost, "/mng/api/cloudflare/zerotrust/whitelist/run", bytes.NewReader([]byte(`{"force"`)))
	zeroTrustRunInvalidReq.Header.Set("Content-Type", "application/json")
	zeroTrustRunInvalidReq.AddCookie(cookie)
	zeroTrustRunInvalidRR := httptest.NewRecorder()
	mux.ServeHTTP(zeroTrustRunInvalidRR, zeroTrustRunInvalidReq)
	if zeroTrustRunInvalidRR.Code != http.StatusBadRequest {
		t.Fatalf("expected zerotrust run invalid json to return 400, got %d body=%s", zeroTrustRunInvalidRR.Code, zeroTrustRunInvalidRR.Body.String())
	}

	zeroTrustRunReq := httptest.NewRequest(http.MethodPost, "/mng/api/cloudflare/zerotrust/whitelist/run", bytes.NewReader([]byte(`{}`)))
	zeroTrustRunReq.Header.Set("Content-Type", "application/json")
	zeroTrustRunReq.AddCookie(cookie)
	zeroTrustRunRR := httptest.NewRecorder()
	mux.ServeHTTP(zeroTrustRunRR, zeroTrustRunReq)
	if zeroTrustRunRR.Code != http.StatusInternalServerError {
		t.Fatalf("expected zerotrust run to return 500 when cloudflare store is not initialized, got %d body=%s", zeroTrustRunRR.Code, zeroTrustRunRR.Body.String())
	}
	zeroTrustRunPayload := decodeJSONMap(t, zeroTrustRunRR)
	zeroTrustRunErr, _ := zeroTrustRunPayload["error"].(string)
	if !strings.Contains(zeroTrustRunErr, "cloudflare datastore is not initialized") {
		t.Fatalf("expected zerotrust run error to mention cloudflare datastore not initialized, got %+v", zeroTrustRunPayload)
	}
}
