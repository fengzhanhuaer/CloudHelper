package core

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeDownloadCacheCoalescesRangesAndExpires(t *testing.T) {
	var count atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("ETag", "\"upstream\"")
		io.WriteString(w, "0123456789abcdef")
	}))
	defer upstream.Close()
	useProbeProxyTestUpstream(t, upstream)
	cache := &probeDownloadCache{dir: t.TempDir(), entries: map[string]*probeDownloadCacheEntry{}}
	// Exercise the cache directly; handler authentication is independently tested.
	get := func(rng string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/probe/proxy/download", nil)
		r.Header.Set("Range", rng)
		w := httptest.NewRecorder()
		cache.serve(w, r, "https://github.com/file")
		return w
	}
	if w := get("bytes=0-3"); w.Code != 503 || w.Header().Get("X-CloudHelper-Cache") != "preparing" {
		t.Fatal(w.Code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		cache.mu.Lock()
		ready := false
		for _, e := range cache.entries {
			ready = e.ready
		}
		cache.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cache fill timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := get("bytes=4-7")
			if w.Code != 206 || w.Body.String() != "4567" || !strings.HasPrefix(w.Header().Get("ETag"), "\"") {
				t.Error("bad range", w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal("duplicate origin fetches", count.Load())
	}
	// A new controller process can reuse the complete cached file and receipt.
	restored := &probeDownloadCache{dir: cache.dir, entries: map[string]*probeDownloadCacheEntry{}}
	r := httptest.NewRequest("GET", "/api/probe/proxy/download", nil)
	r.Header.Set("Range", "bytes=0-3")
	w := httptest.NewRecorder()
	restored.serve(w, r, "https://github.com/file")
	if w.Code != 206 || w.Body.String() != "0123" || count.Load() != 1 {
		t.Fatal("cache restart missed", w.Code, count.Load())
	}
	cache.mu.Lock()
	var path string
	for _, e := range cache.entries {
		e.touched = time.Now().Add(-31 * time.Minute)
		path = e.path
	}
	cache.mu.Unlock()
	cache.clean(time.Now())
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("expired cache retained", e)
	}
}
