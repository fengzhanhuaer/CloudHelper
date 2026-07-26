package main

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeLocalInfoBoxRequiresSessionAndForwardsOperations(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	unauthorized := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/info_box", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	unauthorizedEvents := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/info_box/events", nil)
	if unauthorizedEvents.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized events status=%d body=%s", unauthorizedEvents.Code, unauthorizedEvents.Body.String())
	}

	session := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	setprobeLocalRouteRuntimeContext(nodeIdentity{NodeID: "7", Secret: "secret-7"}, "https://controller.example")
	oldRequest := probeLocalRequestInfoBox
	t.Cleanup(func() { probeLocalRequestInfoBox = oldRequest })
	var gotMethod string
	var gotMessage string
	probeLocalRequestInfoBox = func(_ context.Context, runtime probeLocalRouteRuntimeContext, method string, message string) (probeInfoBoxPayload, error) {
		gotMethod = method
		gotMessage = message
		if runtime.Identity.NodeID != "7" || runtime.ControllerBaseURL != "https://controller.example" {
			t.Fatalf("unexpected runtime: %+v", runtime)
		}
		return probeInfoBoxPayload{Version: 1, Items: []probeInfoBoxItem{{ID: "info-1", NodeID: "7", Message: message, CreatedAt: "2026-07-26T12:00:00Z"}}}, nil
	}

	post := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/info_box", map[string]string{"message": "hello"}, session)
	if post.Code != http.StatusOK || gotMethod != http.MethodPost || gotMessage != "hello" || !strings.Contains(post.Body.String(), "hello") {
		t.Fatalf("post status=%d method=%s message=%q body=%s", post.Code, gotMethod, gotMessage, post.Body.String())
	}
	clear := doProbeLocalRequest(t, mux, http.MethodDelete, "/local/api/info_box", nil, session)
	if clear.Code != http.StatusOK || gotMethod != http.MethodDelete {
		t.Fatalf("clear status=%d method=%s body=%s", clear.Code, gotMethod, clear.Body.String())
	}
}

func TestProbeInfoBoxControlMessagePublishesLocalChange(t *testing.T) {
	changes, unsubscribe := subscribeProbeInfoBoxChanges()
	defer unsubscribe()
	revision := time.Now().UTC().Format(time.RFC3339Nano)
	processProbeControlMessage(probeControlMessage{Type: "info_box_changed", UpdatedAt: revision}, nodeIdentity{}, nil, nil, nil)
	select {
	case event := <-changes:
		if event.UpdatedAt != revision {
			t.Fatalf("updated_at=%q want=%q", event.UpdatedAt, revision)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for local info box change")
	}
}

func TestProbeLocalInfoBoxEventsStreamChange(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	session := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/local/api/info_box/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(session)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("open local info box events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("unexpected response: status=%d content_type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	revision := time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)
	publishProbeInfoBoxChanged(revision)
	scanner := bufio.NewScanner(resp.Body)
	found := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), revision) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("event stream missing revision, err=%v", scanner.Err())
	}
}

func TestProbeLocalInfoBoxEventsRejectsOneShotControllerBridge(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/local/api/info_box/events", nil)
	req = req.WithContext(withProbeLocalConsoleTrusted(req.Context()))
	recorder := httptest.NewRecorder()
	probeLocalInfoBoxEventsHandler(recorder, req)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestProbeLocalInformationPageIsEmbedded(t *testing.T) {
	if !strings.Contains(probeLocalInformationPageHTML, "共享信息框") ||
		!strings.Contains(probeLocalInformationPageHTML, "EventSource('/local/api/info_box/events')") ||
		!strings.Contains(probeLocalPanelPageHTML, "/local/information") {
		t.Fatal("information page or panel tile is missing")
	}
}
