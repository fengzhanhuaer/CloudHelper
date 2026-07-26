package core

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbeInfoBoxHandlerSharesPersistsAndClearsMessages(t *testing.T) {
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{data: probeConfigData{
		ProbeNodes:   []probeNodeRecord{{NodeNo: 7, NodeName: "Tokyo"}, {NodeNo: 9, NodeName: "Taipei"}},
		ProbeSecrets: map[string]string{"7": "secret-7", "9": "secret-9"},
	}}
	resetProbeAuthChallengeStateForTest()
	restorePath := setProbeInfoBoxStorePathForTest(filepath.Join(t.TempDir(), "temp", "probe_info_box.json"))
	t.Cleanup(func() {
		restorePath()
		ProbeStore = oldStore
		resetProbeAuthChallengeStateForTest()
	})

	postBody := bytes.NewBufferString(`{"message":"shared message"}`)
	postReq := httptest.NewRequest(http.MethodPost, "/api/probe/info_box", postBody)
	postReq.TLS = &tls.ConnectionState{}
	applyProbeChallengeAuthForTest(t, postReq, "7", "secret-7")
	postRR := httptest.NewRecorder()
	ProbeInfoBoxHandler(postRR, postReq)
	if postRR.Code != http.StatusOK {
		t.Fatalf("post status=%d body=%s", postRR.Code, postRR.Body.String())
	}
	var posted probeInfoBoxFile
	if err := json.Unmarshal(postRR.Body.Bytes(), &posted); err != nil {
		t.Fatalf("decode post: %v", err)
	}
	if len(posted.Items) != 1 || posted.Items[0].Message != "shared message" || posted.Items[0].NodeName != "Tokyo" {
		t.Fatalf("unexpected posted items: %+v", posted.Items)
	}
	if _, err := os.Stat(probeInfoBoxStore.path); err != nil {
		t.Fatalf("shared file not persisted: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/probe/info_box", nil)
	getReq.TLS = &tls.ConnectionState{}
	applyProbeChallengeAuthForTest(t, getReq, "9", "secret-9")
	getRR := httptest.NewRecorder()
	ProbeInfoBoxHandler(getRR, getReq)
	if getRR.Code != http.StatusOK || !strings.Contains(getRR.Body.String(), "shared message") {
		t.Fatalf("get status=%d body=%s", getRR.Code, getRR.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/probe/info_box", nil)
	deleteReq.TLS = &tls.ConnectionState{}
	applyProbeChallengeAuthForTest(t, deleteReq, "9", "secret-9")
	deleteRR := httptest.NewRecorder()
	ProbeInfoBoxHandler(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteRR.Code, deleteRR.Body.String())
	}
	var cleared probeInfoBoxFile
	if err := json.Unmarshal(deleteRR.Body.Bytes(), &cleared); err != nil || len(cleared.Items) != 0 {
		t.Fatalf("cleared payload=%+v err=%v", cleared, err)
	}
}

func TestProbeInfoBoxRejectsUnauthorizedAndOversizedMessages(t *testing.T) {
	restorePath := setProbeInfoBoxStorePathForTest(filepath.Join(t.TempDir(), "probe_info_box.json"))
	t.Cleanup(restorePath)

	unauthorized := httptest.NewRequest(http.MethodGet, "/api/probe/info_box", nil)
	unauthorized.TLS = &tls.ConnectionState{}
	unauthorizedRR := httptest.NewRecorder()
	ProbeInfoBoxHandler(unauthorizedRR, unauthorized)
	if unauthorizedRR.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorizedRR.Code)
	}

	if _, err := appendProbeInfoBoxItem("7", strings.Repeat("x", probeInfoBoxMaxMessageRunes+1)); err == nil {
		t.Fatal("expected oversized message rejection")
	}
}

func TestBroadcastProbeInfoBoxChangedUsesOnlineProbeSession(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	session := &probeSession{nodeID: "7", stream: serverConn, enc: json.NewEncoder(serverConn)}

	probeSessions.mu.Lock()
	oldSessions := probeSessions.data
	probeSessions.data = map[string]*probeSession{"7": session}
	probeSessions.mu.Unlock()
	t.Cleanup(func() {
		probeSessions.mu.Lock()
		probeSessions.data = oldSessions
		probeSessions.mu.Unlock()
	})

	received := make(chan probeInfoBoxChangedCommand, 1)
	go func() {
		var command probeInfoBoxChangedCommand
		if err := json.NewDecoder(clientConn).Decode(&command); err == nil {
			received <- command
		}
	}()
	broadcastProbeInfoBoxChanged("2026-07-26T12:00:00Z")
	select {
	case command := <-received:
		if command.Type != "info_box_changed" || command.UpdatedAt != "2026-07-26T12:00:00Z" {
			t.Fatalf("unexpected command: %+v", command)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for info box change command")
	}
}
