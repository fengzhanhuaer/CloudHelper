package mobilecore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMobileInfoBoxUsesProbeAuthForListSendAndClear(t *testing.T) {
	methods := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMobileProbeChallengeForTest(w, r) {
			return
		}
		if r.URL.Path != mobileInfoBoxAPIPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Probe-Node-Id") != "9" || r.Header.Get("X-Probe-Signature") == "" {
			t.Fatalf("missing auth headers: %+v", r.Header)
		}
		methods = append(methods, r.Method)
		message := ""
		if r.Method == http.MethodPost {
			var request map[string]string
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			message = request["message"]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": 1,
			"items":   []map[string]string{{"id": "info-1", "node_id": "9", "message": message, "created_at": "2026-07-26T12:00:00Z"}},
		})
	}))
	defer server.Close()

	responses := []string{
		InfoBoxList(server.URL, "9", "secret-9"),
		InfoBoxSend(server.URL, "9", "secret-9", "hello"),
		InfoBoxClear(server.URL, "9", "secret-9"),
	}
	if strings.Join(methods, ",") != "GET,POST,DELETE" {
		t.Fatalf("methods=%v", methods)
	}
	for _, response := range responses {
		if !strings.Contains(response, `"ok":true`) {
			t.Fatalf("unexpected response: %s", response)
		}
	}
	if !strings.Contains(responses[1], "hello") {
		t.Fatalf("send response missing message: %s", responses[1])
	}
}

func TestMobileInfoBoxControlMessageAdvancesRevision(t *testing.T) {
	before := InfoBoxRevision()
	raw := json.RawMessage(`{"type":"info_box_changed","updated_at":"2026-07-26T12:00:00Z"}`)
	processControlMessage(raw, nil, nil, mobileNodeIdentity{})
	after := InfoBoxRevision()
	if after == before {
		t.Fatalf("revision did not advance: before=%s after=%s", before, after)
	}
}
