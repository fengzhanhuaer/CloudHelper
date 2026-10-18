package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxyParallelDownloadUsesTwoRangesAndMatchesFile(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefgh"), int(3*probeUpgradeProxyChunkSize/8))
	var parallel atomic.Int32
	both := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "auth/challenge") {
			fmt.Fprint(w, `{"challenge":"test-only"}`)
			return
		}
		if r.Header.Get("X-Probe-Signature") == "" || r.Header.Get("X-CloudHelper-Download-Cache") != "1" {
			t.Error("missing authenticated cache request")
		}
		if r.Header.Get("Range") == fmt.Sprintf("bytes=%d-%d", probeUpgradeProxyChunkSize, 2*probeUpgradeProxyChunkSize-1) || r.Header.Get("Range") == fmt.Sprintf("bytes=%d-%d", 2*probeUpgradeProxyChunkSize, 3*probeUpgradeProxyChunkSize-1) {
			if parallel.Add(1) == 2 {
				close(both)
			}
			select {
			case <-both:
			case <-time.After(3 * time.Second):
				t.Error("ranges were serial")
			}
		}
		w.Header().Set("ETag", "\"fixed-object\"")
		http.ServeContent(w, r, "asset", time.Unix(1, 0), bytes.NewReader(payload))
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "asset")
	if e := downloadProbeAsset(t.Context(), "proxy", "https://github.com/asset", server.URL, nodeIdentity{NodeID: "9", Secret: "test"}, output, nil); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(output)
	if e != nil || !bytes.Equal(got, payload) || parallel.Load() != 2 {
		t.Fatal("parallel result corrupt", e, parallel.Load())
	}
}
