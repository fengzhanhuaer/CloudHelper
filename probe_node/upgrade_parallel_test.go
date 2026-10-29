package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxyParallelDownloadUsesOneMiBPartsWithTenWorkers(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefgh"), int(3*probeUpgradeProxyChunkSize/8))
	var parallel atomic.Int32
	var active, peak atomic.Int32
	var mu sync.Mutex
	ranges := map[int64]int64{}
	used := map[string]bool{}
	var sequence atomic.Int32
	both := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "auth/challenge") {
			fmt.Fprintf(w, `{"challenge":"nonce-%d"}`, sequence.Add(1))
			return
		}
		nonce := r.Header.Get("X-Probe-Rand")
		mu.Lock()
		replay := used[nonce]
		used[nonce] = true
		mu.Unlock()
		if nonce == "" || replay || r.Header.Get("X-Probe-Signature") != signProbeConnect("test", "9", nonce, r.Method, r.URL.RequestURI()) || r.Header.Get("X-CloudHelper-Download-Cache") != "1" {
			t.Error("missing authenticated cache request")
		}
		var first, last int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &first, &last); err != nil {
			t.Error("missing range", err)
		}
		if first > 0 && first < int64(len(payload)) {
			current := active.Add(1)
			defer active.Add(-1)
			for previous := peak.Load(); current > previous && !peak.CompareAndSwap(previous, current); previous = peak.Load() {
			}
			if last-first+1 > probeUpgradeProxyInitialSize {
				t.Error("request exceeds 64KiB")
			}
			if r.Header.Get("If-Range") != `"fixed-object"` {
				t.Error("missing object validator")
			}
			mu.Lock()
			ranges[first] = last
			mu.Unlock()
			if parallel.Add(1) == 10 {
				close(both)
			}
			select {
			case <-both:
			case <-time.After(3 * time.Second):
				t.Error("ten ranges did not run concurrently")
			}
		} else if first == 0 && last != probeUpgradeProxyInitialSize-1 {
			t.Error("first range should discover size with only 64KiB")
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
	if e != nil || !bytes.Equal(got, payload) || parallel.Load() != 191 || peak.Load() != 10 {
		t.Fatal("parallel result or concurrency incorrect", e, parallel.Load(), peak.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	for next := probeUpgradeProxyInitialSize; next < int64(len(payload)); {
		end, ok := ranges[next]
		if !ok || end < next || end >= int64(len(payload)) {
			t.Fatal("gap or overlap in parallel ranges", next, ranges)
		}
		next = end + 1
	}
}

func TestProxyParallelDownloadResumesInterruptedPart(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefgh"), 8192)
	var interrupted, resumed atomic.Bool
	const prefix int64 = 16
	span := min(probeUpgradeProxyPartSize, int64(len(payload))-prefix)
	firstEnd := prefix + span - 1
	cut := span / 2
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "auth/challenge") {
			fmt.Fprint(w, `{"challenge":"fixture"}`)
			return
		}
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || r.Header.Get("If-Range") != `"fixed"` {
			t.Error("resume lost range or object identity", err)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("ETag", `"fixed"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(206)
		if start == prefix && interrupted.CompareAndSwap(false, true) {
			w.Write(payload[start : start+cut])
			return // A short response forces this part to reconnect at its new offset.
		}
		if start == prefix+cut && end == firstEnd {
			resumed.Store(true)
		}
		w.Write(payload[start : end+1])
	}))
	defer server.Close()
	part := filepath.Join(t.TempDir(), "asset.part")
	if err := os.WriteFile(part, payload[:prefix], 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := downloadProbeParallelRemainder(ctx, "proxy", "https://github.com/asset", server.URL, nodeIdentity{NodeID: "9", Secret: "test"}, part, prefix, int64(len(payload)), `"fixed"`, nil)
	got, readErr := os.ReadFile(part)
	if err != nil || readErr != nil || !interrupted.Load() || !resumed.Load() || !bytes.Equal(got, payload) {
		t.Fatal("parallel resume lost or repeated bytes", err, readErr, interrupted.Load(), resumed.Load())
	}
}

func TestProxyResumeStartsParallelWithoutSerialBootstrap(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefgh"), 128*1024)
	const prefix int64 = 100000
	asset := "https://github.com/resume-bootstrap"
	var workers atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "auth/challenge") {
			fmt.Fprint(w, `{"challenge":"fixture"}`)
			return
		}
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		workers.Add(1)
		if end-start+1 > probeUpgradeProxyInitialSize {
			t.Errorf("resumed download started a serial bootstrap: %d-%d", start, end)
		}
		w.Header().Set("ETag", `"fixed"`)
		http.ServeContent(w, r, "asset", time.Unix(1, 0), bytes.NewReader(payload))
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "asset")
	if err := os.WriteFile(output+".part", payload[:prefix], 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(probeUpgradePartialState{AssetHash: fmt.Sprintf("%x", sha256.Sum256([]byte(asset))), Validator: `"fixed"`, Total: int64(len(payload))})
	if err := os.WriteFile(output+".part.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := downloadProbeAsset(t.Context(), "proxy", asset, server.URL, nodeIdentity{NodeID: "9", Secret: "test"}, output, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(got, payload) || workers.Load() != 15 {
		t.Fatal("resume did not enter fixed-size parts safely", err, workers.Load())
	}
}

func TestProxyFixedPartsResumeAcrossUpgradeAttempts(t *testing.T) {
	payload := bytes.Repeat([]byte("resume64"), 32*1024)
	var failed atomic.Bool
	var completedBlockRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "auth/challenge") {
			fmt.Fprint(w, `{"challenge":"fixture"}`)
			return
		}
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || end-start+1 > probeUpgradeProxyInitialSize {
			t.Error("missing 64KiB range", r.Header.Get("Range"))
			w.WriteHeader(400)
			return
		}
		if start == probeUpgradeProxyInitialSize {
			completedBlockRequests.Add(1)
		}
		if start == 2*probeUpgradeProxyInitialSize && failed.CompareAndSwap(false, true) {
			http.Error(w, "first attempt interrupted", 400)
			return
		}
		w.Header().Set("ETag", `"fixed"`)
		http.ServeContent(w, r, "asset", time.Unix(1, 0), bytes.NewReader(payload))
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "asset")
	id := nodeIdentity{NodeID: "9", Secret: "test"}
	if err := downloadProbeAsset(t.Context(), "proxy", "https://github.com/resume64", server.URL, id, output, nil); err == nil {
		t.Fatal("first interruption should be reported")
	}
	if err := downloadProbeAsset(t.Context(), "proxy", "https://github.com/resume64", server.URL, id, output, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(got, payload) || completedBlockRequests.Load() != 1 {
		t.Fatal("finished 64KiB block redownloaded or content corrupt", err, completedBlockRequests.Load())
	}
}
