package main

import (
	"bytes"
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

func TestUpgradeResumeAcrossSourcesValidatesContent(t *testing.T) {
	for _, mode := range []string{"direct", "proxy"} {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/changed=%v", mode, changed), func(t *testing.T) {
				payload := []byte("hello world")
				if changed {
					payload = []byte("HELLO world")
				}
				var mu sync.Mutex
				var ranges []string
				used := map[string]bool{}
				nonces := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					if r.URL.Path == "/api/probe/auth/challenge" {
						nonces++
						fmt.Fprintf(w, `{"challenge":"nonce-%d"}`, nonces)
						return
					}
					if mode == "proxy" {
						nonce := r.Header.Get("X-Probe-Rand")
						if nonce == "" || used[nonce] || r.Header.Get("X-Probe-Signature") != signProbeConnect("secret", "9", nonce, r.Method, r.URL.RequestURI()) || r.Header.Get("X-CloudHelper-Download-Cache") != "1" {
							t.Error("content verification lost private auth or reused nonce")
							w.WriteHeader(401)
							return
						}
						used[nonce] = true
					}
					ranges = append(ranges, r.Header.Get("Range"))
					// Reproduce an upstream that returns 206 with a different ETag.
					r.Header.Del("If-Range")
					w.Header().Set("ETag", `"new-source"`)
					http.ServeContent(w, r, "asset", time.Time{}, bytes.NewReader(payload))
				}))
				defer server.Close()
				asset := server.URL + "/asset"
				output := filepath.Join(t.TempDir(), "asset")
				seedUpgradeCheckpoint(t, output, asset, []byte("hello "), `"old-source"`, 11)
				if err := downloadProbeAsset(t.Context(), mode, asset, server.URL, nodeIdentity{NodeID: "9", Secret: "secret"}, output, nil); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(output)
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("mixed or incomplete file: %q %v", got, err)
				}
				mu.Lock()
				defer mu.Unlock()
				want := 2
				if changed {
					want = 3
				}
				if len(ranges) != want || ranges[1] != "bytes=0-5" {
					t.Fatalf("must validate all stored bytes then resume or restart: %v", ranges)
				}
			})
		}
	}
}

func seedUpgradeCheckpoint(t *testing.T, output, asset string, prefix []byte, validator string, total int64) {
	t.Helper()
	if err := os.WriteFile(output+".part", prefix, 0600); err != nil {
		t.Fatal(err)
	}
	state := probeUpgradePartialState{AssetHash: fmt.Sprintf("%x", sha256.Sum256([]byte(asset))), Validator: validator, Total: total}
	b, _ := json.Marshal(state)
	if err := os.WriteFile(output+".part.json", b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeChangedValidatorCannotAppendUnverifiedPrefix(t *testing.T) {
	for _, failure := range []string{"range", "validator", "truncated"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"new"`)
				if r.Header.Get("Range") == "bytes=0-5" {
					w.Header().Set("Content-Range", "bytes 0-5/11")
					w.Header().Set("Content-Length", "6")
					switch failure {
					case "range":
						w.Header().Set("Content-Range", "bytes 1-6/11")
					case "validator":
						w.Header().Set("ETag", `"third"`)
					}
					w.WriteHeader(206)
					if failure == "truncated" {
						fmt.Fprint(w, "he")
					} else {
						fmt.Fprint(w, "hello ")
					}
					return
				}
				w.Header().Set("Content-Range", "bytes 6-10/11")
				w.WriteHeader(206)
				fmt.Fprint(w, "world")
			}))
			defer server.Close()
			part := filepath.Join(t.TempDir(), "part")
			os.WriteFile(part, []byte("hello "), 0600)
			validator := `"old"`
			_, _, err := downloadProbeAssetChunk(t.Context(), "direct", server.URL, "", nodeIdentity{}, part, 6, &validator, 11, func(int64, int64) {})
			if err == nil {
				t.Fatal("unverified bytes appended")
			}
			got, _ := os.ReadFile(part)
			if string(got) != "hello " || validator != `"old"` {
				t.Fatalf("failed verification modified checkpoint: %q %s", got, validator)
			}
		})
	}
}

func TestUpgradeParallelChunkChecksItsAbsolutePrefixRange(t *testing.T) {
	payload := []byte("unused0123456789")
	var verified atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=6-8" {
			verified.Store(true)
		}
		r.Header.Del("If-Range")
		w.Header().Set("ETag", `"new"`)
		http.ServeContent(w, r, "asset", time.Time{}, bytes.NewReader(payload))
	}))
	defer server.Close()
	part := filepath.Join(t.TempDir(), "chunk")
	os.WriteFile(part, []byte("012"), 0600)
	validator := `"old"`
	_, _, err := downloadProbeAssetChunk(t.Context(), "direct", server.URL, "", nodeIdentity{}, part, 9, &validator, int64(len(payload)), func(int64, int64) {}, int64(len(payload)-1))
	got, _ := os.ReadFile(part)
	if err != nil || !verified.Load() || strings.TrimSpace(string(got)) != "0123456789" {
		t.Fatalf("parallel chunk prefix origin lost: %q verified=%v err=%v", got, verified.Load(), err)
	}
}

func TestUpgradeParallelChangedSourceFallsBackToSerial(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefgh"), int(3*probeUpgradeProxyChunkSize/8))
	var changed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "auth/challenge") {
			fmt.Fprint(w, `{"challenge":"fixture"}`)
			return
		}
		if strings.HasPrefix(r.Header.Get("Range"), fmt.Sprintf("bytes=%d-", probeUpgradeProxyInitialSize)) && changed.CompareAndSwap(false, true) {
			// If-Range can legitimately return 200 after a source's ETag changes.
			w.WriteHeader(200)
			return
		}
		w.Header().Set("ETag", `"source"`)
		http.ServeContent(w, r, "asset", time.Time{}, bytes.NewReader(payload))
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "asset")
	if err := downloadProbeAsset(t.Context(), "proxy", server.URL+"/asset", server.URL, nodeIdentity{NodeID: "9", Secret: "secret"}, output, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil || !changed.Load() || !bytes.Equal(got, payload) {
		t.Fatal("serial fallback lost or mixed content", err)
	}
}
