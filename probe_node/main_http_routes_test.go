package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeNodeHTTPMuxRootWelcome(t *testing.T) {
	mux := buildProbeNodeHTTPMux(nodeIdentity{NodeID: "node-a", Secret: "secret-a"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET / status=%d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("GET / content-type=%q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body failed: %v", err)
	}
	if _, exists := payload["service"]; exists {
		t.Fatalf("service should not be exposed on root payload: %#v", payload["service"])
	}
	if payload["api_base"] != "/v1" {
		t.Fatalf("unexpected api_base: %#v", payload["api_base"])
	}
	if payload["message"] != "OpenAI-compatible API endpoint" {
		t.Fatalf("unexpected message: %#v", payload["message"])
	}
}

func TestProbeNodeHTTPMuxV1Unauthorized(t *testing.T) {
	mux := buildProbeNodeHTTPMux(nodeIdentity{NodeID: "node-a", Secret: "secret-a"})
	req := httptest.NewRequest(http.MethodGet, "/v1", nil)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET /v1 status=%d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if got := rr.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("unexpected WWW-Authenticate: %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body failed: %v", err)
	}
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("error should be object, got %T", payload["error"])
	}
	if errObj["code"] != "invalid_api_key" {
		t.Fatalf("unexpected error.code: %#v", errObj["code"])
	}
}

func TestProbeNodeHTTPMuxV1ModelsPathUnauthorized(t *testing.T) {
	mux := buildProbeNodeHTTPMux(nodeIdentity{NodeID: "node-a", Secret: "secret-a"})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET /v1/models status=%d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if got := rr.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("unexpected WWW-Authenticate: %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body failed: %v", err)
	}
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("error should be object, got %T", payload["error"])
	}
	if errObj["code"] != "invalid_api_key" {
		t.Fatalf("unexpected error.code: %#v", errObj["code"])
	}
}

func TestProbeNodeHTTPMuxV1SubPathReturnsStructuredError(t *testing.T) {
	mux := buildProbeNodeHTTPMux(nodeIdentity{NodeID: "node-a", Secret: "secret-a"})
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET /v1/* status=%d, want %d", rr.Code, http.StatusUnauthorized)
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body failed: %v", err)
	}
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("error should be object, got %T", payload["error"])
	}
	if errObj["code"] != "invalid_api_key" {
		t.Fatalf("unexpected error.code: %#v", errObj["code"])
	}
	if errObj["message"] != "Incorrect API key provided." {
		t.Fatalf("unexpected error.message: %#v", errObj["message"])
	}
	if errObj["type"] != "invalid_request_error" {
		t.Fatalf("unexpected error.type: %#v", errObj["type"])
	}
}

func TestProbeNodeHTTPMuxV1MethodUnauthorized(t *testing.T) {
	mux := buildProbeNodeHTTPMux(nodeIdentity{NodeID: "node-a", Secret: "secret-a"})
	req := httptest.NewRequest(http.MethodPost, "/v1", nil)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("POST /v1 status=%d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if got := rr.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("unexpected WWW-Authenticate: %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body failed: %v", err)
	}
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("error should be object, got %T", payload["error"])
	}
	if errObj["code"] != "invalid_api_key" {
		t.Fatalf("unexpected error.code: %#v", errObj["code"])
	}
}

func TestProbeNodeHTTPMuxUnknownPathStillNotFound(t *testing.T) {
	mux := buildProbeNodeHTTPMux(nodeIdentity{NodeID: "node-a", Secret: "secret-a"})
	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET /unknown status=%d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestProbeNodeHTTPMuxHealthzInfoExposureMinimized(t *testing.T) {
	mux := buildProbeNodeHTTPMux(nodeIdentity{NodeID: "node-a", Secret: "secret-a"})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /healthz status=%d, want %d", rr.Code, http.StatusOK)
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body failed: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("unexpected status: %#v", payload["status"])
	}
	if _, exists := payload["service"]; exists {
		t.Fatalf("service should not be exposed: %#v", payload["service"])
	}
	if _, exists := payload["has_secret"]; exists {
		t.Fatalf("has_secret should not be exposed: %#v", payload["has_secret"])
	}
}

func TestProbeNodeHTTPMuxNodeInfoExposureMinimized(t *testing.T) {
	mux := buildProbeNodeHTTPMux(nodeIdentity{NodeID: "node-a", Secret: "secret-a"})
	req := httptest.NewRequest(http.MethodGet, "/api/node/info", nil)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/node/info status=%d, want %d", rr.Code, http.StatusOK)
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body failed: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("unexpected status: %#v", payload["status"])
	}
	if payload["node_id"] != "node-a" {
		t.Fatalf("unexpected node_id: %#v", payload["node_id"])
	}
	if _, exists := payload["service"]; exists {
		t.Fatalf("service should not be exposed: %#v", payload["service"])
	}
	if _, exists := payload["has_secret"]; exists {
		t.Fatalf("has_secret should not be exposed: %#v", payload["has_secret"])
	}
}

func TestProbeOpenAIStyleJitterDurationRange(t *testing.T) {
	for i := 0; i < 32; i++ {
		d := probeOpenAIStyleJitterDuration()
		if d < 300*time.Millisecond || d > 1000*time.Millisecond {
			t.Fatalf("jitter duration out of range: %s", d)
		}
	}
}
