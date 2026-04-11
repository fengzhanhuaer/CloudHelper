package core

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAdminWSProxyDownloadTimeoutEnv_Default(t *testing.T) {
	old := os.Getenv("CLOUDHELPER_CONTROLLER_PROXY_DOWNLOAD_TIMEOUT")
	_ = os.Unsetenv("CLOUDHELPER_CONTROLLER_PROXY_DOWNLOAD_TIMEOUT")
	defer func() { _ = os.Setenv("CLOUDHELPER_CONTROLLER_PROXY_DOWNLOAD_TIMEOUT", old) }()

	got := adminWSProxyDownloadTimeout()
	if got != defaultAdminWSProxyDownloadTimeout {
		t.Fatalf("adminWSProxyDownloadTimeout()=%s, want %s", got, defaultAdminWSProxyDownloadTimeout)
	}
}

func TestAdminWSProxyDownloadTimeoutEnv_Bounds(t *testing.T) {
	old := os.Getenv("CLOUDHELPER_CONTROLLER_PROXY_DOWNLOAD_TIMEOUT")
	defer func() { _ = os.Setenv("CLOUDHELPER_CONTROLLER_PROXY_DOWNLOAD_TIMEOUT", old) }()

	_ = os.Setenv("CLOUDHELPER_CONTROLLER_PROXY_DOWNLOAD_TIMEOUT", "1s")
	if got := adminWSProxyDownloadTimeout(); got != minAdminWSProxyDownloadTimeout {
		t.Fatalf("min bound failed: got=%s want=%s", got, minAdminWSProxyDownloadTimeout)
	}

	_ = os.Setenv("CLOUDHELPER_CONTROLLER_PROXY_DOWNLOAD_TIMEOUT", "999h")
	if got := adminWSProxyDownloadTimeout(); got != maxAdminWSProxyDownloadTimeout {
		t.Fatalf("max bound failed: got=%s want=%s", got, maxAdminWSProxyDownloadTimeout)
	}

	_ = os.Setenv("CLOUDHELPER_CONTROLLER_PROXY_DOWNLOAD_TIMEOUT", "120")
	if got := adminWSProxyDownloadTimeout(); got != minAdminWSProxyDownloadTimeout {
		t.Fatalf("seconds fallback failed: got=%s want=%s", got, minAdminWSProxyDownloadTimeout)
	}

	_ = os.Setenv("CLOUDHELPER_CONTROLLER_PROXY_DOWNLOAD_TIMEOUT", "not-a-duration")
	if got := adminWSProxyDownloadTimeout(); got != defaultAdminWSProxyDownloadTimeout {
		t.Fatalf("invalid value fallback failed: got=%s want=%s", got, defaultAdminWSProxyDownloadTimeout)
	}
}

func TestHandleAdminWSProxyDownloadStream_Status416(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer upstream.Close()

	oldClient := http.DefaultClient
	http.DefaultClient = upstream.Client()
	defer func() { http.DefaultClient = oldClient }()

	payload := []byte(`{"url":"` + upstream.URL + `","offset":10}`)
	got, err := handleAdminWSProxyDownloadStream("req-416", payload, func(v interface{}) error { return nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if int(got["status"].(int)) != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("unexpected status: %#v", got["status"])
	}
	if got["downloaded"].(int64) != 10 || got["total"].(int64) != 10 {
		t.Fatalf("unexpected downloaded/total: %#v", got)
	}
}

func TestHandleAdminWSProxyDownloadStream_PropagatesUpstreamStatusError(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad upstream"))
	}))
	defer upstream.Close()

	oldClient := http.DefaultClient
	http.DefaultClient = upstream.Client()
	defer func() { http.DefaultClient = oldClient }()

	payload := []byte(`{"url":"` + upstream.URL + `"}`)
	_, err := handleAdminWSProxyDownloadStream("req-status", payload, func(v interface{}) error { return nil })
	if err == nil {
		t.Fatalf("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "stage=upstream.status") || !strings.Contains(msg, "request_id=req-status") {
		t.Fatalf("unexpected error message: %s", msg)
	}
}

func TestHandleAdminWSProxyDownloadStream_PushChunkAndDone(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "hello")
	}))
	defer upstream.Close()

	oldClient := http.DefaultClient
	http.DefaultClient = upstream.Client()
	defer func() { http.DefaultClient = oldClient }()

	pushed := 0
	send := func(v interface{}) error {
		push, ok := v.(adminWSPush)
		if !ok {
			return nil
		}
		if strings.TrimSpace(push.Type) == "proxy.download.chunk" {
			pushed++
		}
		return nil
	}

	payload := []byte(`{"url":"` + upstream.URL + `","chunk_size":2}`)
	got, err := handleAdminWSProxyDownloadStream("req-ok", payload, send)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pushed == 0 {
		t.Fatalf("expected at least one pushed chunk")
	}
	if got["status"].(int) != http.StatusOK {
		t.Fatalf("unexpected status: %#v", got["status"])
	}
	if got["downloaded"].(int64) != 5 {
		t.Fatalf("unexpected downloaded: %#v", got["downloaded"])
	}
}
