package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type externalSpeedRoundTrip func(*http.Request) (*http.Response, error)

func (f externalSpeedRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProbeExternalSpeedBoundedAndRejectsHTTPError(t *testing.T) {
	var last probeExternalSpeedResult
	transport := externalSpeedRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "speed.cloudflare.com" || r.URL.Scheme != "https" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Fatalf("request %v", r)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(make([]byte, 256<<10)))}, nil
	})
	result := runProbeExternalSpeed(context.Background(), &http.Client{Transport: transport}, 64<<10, time.Second, func(r probeExternalSpeedResult) { last = r })
	if !result.OK || result.Bytes != 64<<10 || result.Running || last.Bytes > 64<<10 || result.Mbps <= 0 {
		t.Fatalf("result=%+v", result)
	}
	client := &http.Client{Transport: externalSpeedRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("denied"))}, nil
	})}
	result = runProbeExternalSpeed(context.Background(), client, 64<<10, time.Second, nil)
	if result.OK || result.HTTPStatus != 403 || result.Bytes != 0 {
		t.Fatalf("error counted as success: %+v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = runProbeExternalSpeed(ctx, &http.Client{Transport: transport}, 64<<10, time.Second, nil)
	if result.OK || !result.Cancelled {
		t.Fatalf("cancel=%+v", result)
	}
}

func TestProbeExternalSpeedRequiresSession(t *testing.T) {
	w := httptest.NewRecorder()
	probeLocalExternalSpeedHandler(w, httptest.NewRequest(http.MethodPost, "/local/api/virtual_router/external_speed", strings.NewReader(`{"action":"start"}`)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestProbeExternalSpeedAuthenticatedStatusAndMalformedInput(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	cookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	response := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/virtual_router/external_speed", nil, cookie)
	if response.Code != 200 {
		t.Fatal(response.Code)
	}
	for _, body := range []string{`{"action":"start","url":"http://127.0.0.1"}`, `{"action":"start"} {}`, `{"action":"unknown"}`} {
		request := httptest.NewRequest(http.MethodPost, "/local/api/virtual_router/external_speed", strings.NewReader(body))
		request.AddCookie(cookie)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, request)
		if w.Code != 400 {
			t.Fatalf("malformed input status %d", w.Code)
		}
	}
}
