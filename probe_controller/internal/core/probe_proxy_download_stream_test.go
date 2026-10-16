package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeProxyWeakNetworkRetriesAndResumes(t *testing.T) {
	t.Setenv("PROBE_ALLOW_LEGACY_QUERY_AUTH", "true")
	withProbeStoreSecretForTest(t, "1", "test")
	requests := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("ETag", `"asset-v1"`)
		if requests == 2 {
			w.Header().Set("Content-Length", "11")
			w.WriteHeader(200)
			io.WriteString(w, "hello ")
			return
		}
		if r.Header.Get("Range") != "bytes=6-10" || r.Header.Get("If-Range") != `"asset-v1"` {
			t.Errorf("bad resume headers: %v", r.Header)
		}
		w.Header().Set("Content-Range", "bytes 6-10/11")
		w.WriteHeader(206)
		io.WriteString(w, "world")
	}))
	defer upstream.Close()
	useProbeProxyTestUpstream(t, upstream)
	proxy := httptest.NewServer(http.HandlerFunc(ProbeProxyDownloadHandler))
	defer proxy.Close()
	resp, err := proxy.Client().Get(proxy.URL + "/api/probe/proxy/download?url=https://github.com/test/asset&node_id=1&secret=test")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "hello world" || requests != 3 {
		t.Fatalf("body=%q requests=%d err=%v", body, requests, err)
	}
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("proxy buffering enabled")
	}
}

func TestProbeProxyWeakNetworkRejectsChangedResume(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			requests := 0
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("ETag", `"v1"`)
				if requests == 1 {
					w.Header().Set("Content-Length", "11")
					io.WriteString(w, "hello ")
					return
				}
				w.Header().Set("Content-Range", "bytes 0-4/11")
				if changed {
					w.Header().Set("Content-Range", "bytes 6-10/11")
					w.Header().Set("ETag", `"v2"`)
				}
				w.WriteHeader(206)
				io.WriteString(w, "world")
			}))
			defer upstream.Close()
			useProbeProxyTestUpstream(t, upstream)
			req, _ := newProbeProxyDownloadRequest(t.Context(), "https://github.com/asset")
			initial, err := doProbeProxyDownload(req)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			if err := streamProbeProxyDownload(w, req, initial); err == nil {
				t.Fatal("invalid resume accepted")
			}
			if w.Body.String() != "hello " {
				t.Fatalf("mixed object forwarded: %q", w.Body.String())
			}
		})
	}
}

func TestProbeProxyWeakNetwork416AndIfRange(t *testing.T) {
	t.Setenv("PROBE_ALLOW_LEGACY_QUERY_AUTH", "true")
	withProbeStoreSecretForTest(t, "1", "test")
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Range") != `"v1"` {
			t.Error("If-Range lost")
		}
		w.Header().Set("Content-Range", "bytes */11")
		w.WriteHeader(416)
	}))
	defer upstream.Close()
	useProbeProxyTestUpstream(t, upstream)
	req := httptest.NewRequest("GET", "/api/probe/proxy/download?url=https://github.com/asset&node_id=1&secret=test", nil)
	req.Header.Set("Range", "bytes=11-")
	req.Header.Set("If-Range", `"v1"`)
	w := httptest.NewRecorder()
	ProbeProxyDownloadHandler(w, req)
	if w.Code != 416 || w.Header().Get("Content-Range") != "bytes */11" {
		t.Fatalf("status=%d headers=%v", w.Code, w.Header())
	}
}

func TestProbeProxyWeakNetworkCancelRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: probeProxyRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("busy")), Request: r}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = old })
	req, _ := newProbeProxyDownloadRequest(ctx, "https://github.com/asset")
	start := time.Now()
	resp, _ := doProbeProxyDownloadWithRetry(req)
	if resp != nil {
		resp.Body.Close()
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancel did not stop retry")
	}
}

func TestProbeProxyWeakNetworkFlushesBeforeUpstreamCompletes(t *testing.T) {
	t.Setenv("PROBE_ALLOW_LEGACY_QUERY_AUTH", "true")
	withProbeStoreSecretForTest(t, "1", "test")
	release := make(chan struct{})
	defer close(release)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "11")
		io.WriteString(w, "hello ")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "world")
	}))
	defer upstream.Close()
	useProbeProxyTestUpstream(t, upstream)
	proxy := httptest.NewServer(http.HandlerFunc(ProbeProxyDownloadHandler))
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", proxy.URL+"/api/probe/proxy/download?url=https://github.com/asset&node_id=1&secret=test", nil)
	resp, err := proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, 6)
	if _, err := io.ReadFull(resp.Body, first); err != nil || string(first) != "hello " {
		t.Fatalf("buffered or broken response: %q %v", first, err)
	}
}
