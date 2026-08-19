package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProbeLocalSystemWebListenSettingsAPI(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	state, _, err := loadProbeLocalAuthStateRaw()
	if err != nil {
		t.Fatal(err)
	}
	state.ListenIP = "127.0.0.2"
	state.ListenPort = 17777
	if err := persistProbeLocalAuthState(state); err != nil {
		t.Fatal(err)
	}

	restarted := make(chan struct{}, 1)
	probeLocalApplyWebListenRestart = func() { restarted <- struct{}{} }
	t.Cleanup(resetProbeLocalWebListenHooksForTest)

	saveResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/web_listen", map[string]any{
		"listen_ip": "127.0.0.1",
	}, sessionCookie)
	if saveResp.Code != http.StatusOK {
		t.Fatalf("save web listen status=%d body=%s", saveResp.Code, saveResp.Body.String())
	}
	saved := decodeProbeLocalJSON(t, saveResp)
	if saved["listen_ip"] != "127.0.0.1" || saved["restarting"] != true {
		t.Fatalf("unexpected saved web listen payload: %+v", saved)
	}
	wantPort := 17777
	if activeProbeProductProfile.PreferLocalConsoleConfig {
		_, wantPort = probeLocalConsoleDefaultHostPort()
	}
	if got := int(saved["listen_port"].(float64)); got != wantPort {
		t.Fatalf("saved listen port=%d want=%d", got, wantPort)
	}

	select {
	case <-restarted:
	case <-time.After(2 * time.Second):
		t.Fatal("web listen save did not schedule restart")
	}

	getResp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/system/web_listen", nil, sessionCookie)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get web listen status=%d body=%s", getResp.Code, getResp.Body.String())
	}
	loaded := decodeProbeLocalJSON(t, getResp)
	if loaded["listen_ip"] != "127.0.0.1" || int(loaded["listen_port"].(float64)) != wantPort {
		t.Fatalf("unexpected loaded web listen payload: %+v", loaded)
	}

	invalidResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/web_listen", map[string]any{
		"listen_ip": "203.0.113.10",
	}, sessionCookie)
	if invalidResp.Code != http.StatusBadRequest {
		t.Fatalf("invalid web listen status=%d body=%s", invalidResp.Code, invalidResp.Body.String())
	}
}

func TestProbeLocalWebListenPayloadUsesProductDefault(t *testing.T) {
	payload := probeLocalWebListenPayload(probeLocalAuthState{}, false)
	wantIP, wantPort := probeLocalConsoleDefaultHostPort()
	if payload["listen_ip"] != wantIP || payload["listen_port"] != wantPort {
		t.Fatalf("default web listen payload=%+v want_ip=%s want_port=%d", payload, wantIP, wantPort)
	}
	if currentProbeBuildKind() == probeBuildKindLinuxRouter && wantIP != "0.0.0.0" {
		t.Fatalf("linux router default listen ip=%q want=0.0.0.0", wantIP)
	}
}

func TestProbeLocalSystemPageIncludesWebListenSettings(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	resp := doProbeLocalRequest(t, mux, http.MethodGet, "/local/system", nil, sessionCookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("system page status=%d body=%s", resp.Code, resp.Body.String())
	}
	for _, marker := range []string{"Web 控制台", `id="webListenIPSelect"`, `id="webListenSaveBtn"`, "/local/api/system/web_listen"} {
		if !strings.Contains(resp.Body.String(), marker) {
			t.Fatalf("system page should contain %q", marker)
		}
	}
}
