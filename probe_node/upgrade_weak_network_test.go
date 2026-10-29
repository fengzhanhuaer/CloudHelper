package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeUpgradeDownloadFailureDoesNotExtract(t *testing.T) {
	resetProbeLocalUpgradeRuntimeStateForTest()
	t.Cleanup(resetProbeLocalUpgradeRuntimeStateForTest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/probe/auth/challenge":
			json.NewEncoder(w).Encode(map[string]string{"challenge": "test"})
		case "/api/probe/proxy/github/latest":
			json.NewEncoder(w).Encode(releaseInfo{TagName: "v999.0.0", Assets: []releaseAsset{{Name: activeProbeProductProfile.UpgradeAssetPrefix + "-" + runtime.GOOS + "-" + runtime.GOARCH, DownloadURL: "https://github.com/test/asset"}}})
		default:
			http.Error(w, "asset unavailable", http.StatusNotFound)
		}
	}))
	defer server.Close()
	runProbeUpgrade(probeControlMessage{Mode: "proxy", ControllerBaseURL: server.URL}, nodeIdentity{NodeID: "9", Secret: "test"})
	state := currentProbeLocalUpgradeState()
	if state.Status != "failed" || state.Step != "download" || !strings.Contains(state.Error, "404") || strings.Contains(state.Error, "no such file") {
		t.Fatalf("download failure was hidden: %+v", state)
	}
}

func TestProbeUpgradeWeakNetworkProxyChunks(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), int(probeUpgradeProxyChunkSize)+21)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/probe/auth/challenge" {
			json.NewEncoder(w).Encode(map[string]string{"challenge": "test"})
			return
		}
		if requests.Add(1) == 1 {
			http.Error(w, "temporarily unavailable", 503)
			return
		}
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error(err)
			return
		}
		if end >= int64(len(payload)) {
			end = int64(len(payload)) - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("ETag", `"v1"`)
		if start > 0 && r.Header.Get("If-Range") != `"v1"` {
			t.Error("missing resume validator")
		}
		w.WriteHeader(206)
		w.Write(payload[start : end+1])
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "asset")
	if err := downloadProbeAsset(t.Context(), "proxy", "https://github.com/asset", server.URL, nodeIdentity{NodeID: "9", Secret: "test"}, output, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(got, payload) || requests.Load() != 12 {
		t.Fatalf("requests=%d size=%d err=%v", requests.Load(), len(got), err)
	}
}

func TestProbeUpgradeWeakNetworkRejectsInvalidResume(t *testing.T) {
	for _, tc := range []struct {
		name, contentRange string
		status             int
	}{
		{"wrong-offset", "bytes 0-4/11", 206},
		{"false-complete", "bytes */11", 416},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Range", tc.contentRange)
				w.WriteHeader(tc.status)
				w.Write([]byte("world"))
			}))
			defer server.Close()
			output := filepath.Join(t.TempDir(), "asset")
			os.WriteFile(output+".part", []byte("hello "), 0600)
			err := downloadProbeAsset(t.Context(), "direct", server.URL, "", nodeIdentity{}, output, nil)
			if err == nil {
				t.Fatal("invalid range accepted")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("invalid asset published")
			}
		})
	}
}

func TestProbeUpgradeWeakNetwork416ConfirmsComplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes */6")
		w.WriteHeader(416)
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "asset")
	os.WriteFile(output+".part", []byte("hello!"), 0600)
	if err := downloadProbeAsset(t.Context(), "direct", server.URL, "", nodeIdentity{}, output, nil); err != nil {
		t.Fatal(err)
	}
}

func TestProbeUpgradeWeakNetworkCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503); cancel() }))
	defer server.Close()
	started := time.Now()
	err := downloadProbeAsset(ctx, "direct", server.URL, "", nodeIdentity{}, filepath.Join(t.TempDir(), "asset"), nil)
	if !errors.Is(err, context.Canceled) && ctx.Err() == nil {
		t.Fatalf("err=%v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("cancel waited for retry delay")
	}
}

func TestProbeUpgradeWeakNetworkResumesAcrossAttempts(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("ETag", `"asset-v1"`)
		if requests == 1 {
			w.Header().Set("Content-Length", "11")
			io.WriteString(w, "hello ")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if r.Header.Get("Range") != "bytes=6-" || r.Header.Get("If-Range") != `"asset-v1"` {
			t.Errorf("lost saved breakpoint: %v", r.Header)
		}
		w.Header().Set("Content-Range", "bytes 6-10/11")
		w.WriteHeader(206)
		io.WriteString(w, "world")
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "asset")
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if err := downloadProbeAsset(ctx, "direct", server.URL, "", nodeIdentity{}, output, nil); err == nil {
		t.Fatal("first transfer should be interrupted")
	}
	if _, err := os.Stat(output + ".part.json"); err != nil {
		t.Fatal("breakpoint metadata missing", err)
	}
	if err := downloadProbeAsset(t.Context(), "direct", server.URL, "", nodeIdentity{}, output, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil || string(got) != "hello world" {
		t.Fatalf("body=%q err=%v", got, err)
	}
	if _, err := os.Stat(output + ".part.json"); !os.IsNotExist(err) {
		t.Fatal("completed breakpoint not cleaned")
	}
}

func TestProbeUpgradeWeakNetworkRetainsWorkspaceForRetry(t *testing.T) {
	resetProbeLocalUpgradeRuntimeStateForTest()
	t.Cleanup(resetProbeLocalUpgradeRuntimeStateForTest)
	asset := releaseAsset{Name: activeProbeProductProfile.UpgradeAssetPrefix + "-" + runtime.GOOS + "-" + runtime.GOARCH, DownloadURL: "https://github.com/test/retained-upgrade-asset"}
	version := "v999.0.1"
	workDir, err := createProbeUpgradeWorkspaceForAsset(version, asset)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupProbeUpgradeWorkspace(workDir) })
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/probe/auth/challenge":
			json.NewEncoder(w).Encode(map[string]string{"challenge": "test"})
		case "/api/probe/proxy/github/latest":
			json.NewEncoder(w).Encode(releaseInfo{TagName: version, Assets: []releaseAsset{asset}})
		default:
			requests++
			w.Header().Set("ETag", `"v1"`)
			switch requests {
			case 1:
				w.Header().Set("Content-Length", "11")
				io.WriteString(w, "hello ")
			case 2:
				w.Header().Set("Content-Range", "bytes */12")
				w.WriteHeader(416)
			case 3:
				if r.Header.Get("Range") != "bytes=6-4194309" || r.Header.Get("If-Range") != `"v1"` {
					t.Errorf("upgrade restart lost checkpoint: %v", r.Header)
				}
				w.Header().Set("Content-Range", "bytes 6-10/11")
				w.WriteHeader(206)
				io.WriteString(w, "world")
			default:
				t.Error("unexpected download attempt")
				w.WriteHeader(404)
			}
		}
	}))
	defer server.Close()
	cmd := probeControlMessage{Mode: "proxy", ControllerBaseURL: server.URL}
	identity := nodeIdentity{NodeID: "9", Secret: "test"}
	runProbeUpgrade(cmd, identity)
	if state := currentProbeLocalUpgradeState(); state.Step != "download" {
		t.Fatalf("wrong first failure: %+v", state)
	}
	if st, err := os.Stat(filepath.Join(workDir, asset.Name) + ".part"); err != nil || st.Size() != 6 {
		t.Fatalf("partial lost: %v", err)
	}
	runProbeUpgrade(cmd, identity)
	// Deliberately invalid test executable must stop before any install or restart.
	wantStep := "verify_runtime"
	if state := currentProbeLocalUpgradeState(); state.Step != wantStep {
		t.Fatalf("retry failed before verification: %+v", state)
	}
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("workspace not cleaned after download: %v", err)
	}
}

func TestProbeUpgradeWeakNetworkReusesVerifiedCompletedAsset(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; io.WriteString(w, "asset-content") }))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "asset")
	for range 2 {
		if err := downloadProbeAsset(t.Context(), "direct", server.URL, "", nodeIdentity{}, output, nil); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 1 {
		t.Fatal("completed asset downloaded again", requests)
	}
	if err := os.WriteFile(output, []byte("other-content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := downloadProbeAsset(t.Context(), "direct", server.URL, "", nodeIdentity{}, output, nil); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatal("corrupt cached asset was trusted")
	}
}
