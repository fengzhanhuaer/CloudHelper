package mobilecore

import (
	"bytes"
	"encoding/json"
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

func TestMobileUpgradeCacheAndParallelResume(t *testing.T) {
	payload := bytes.Repeat([]byte("01234567"), 3*(4<<20)/8)
	var inParallel atomic.Int32
	both := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "challenge") {
			fmt.Fprint(w, `{"challenge":"fixture"}`)
			return
		}
		if r.Header.Get("X-Probe-Signature") == "" || r.Header.Get("X-CloudHelper-Download-Cache") != "1" {
			t.Error("missing APK private authentication/cache header")
		}
		if r.Header.Get("Range") != "bytes=0-4194303" {
			if inParallel.Add(1) == 2 {
				close(both)
			}
			select {
			case <-both:
			case <-time.After(3 * time.Second):
				t.Error("APK chunks serial")
			}
		}
		w.Header().Set("ETag", "\"fixed-apk\"")
		http.ServeContent(w, r, "asset", time.Unix(1, 0), bytes.NewReader(payload))
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "upgrade.apk.part")
	target := server.URL + "/api/probe/proxy/download?url=fixture"
	raw := ControllerUpgradeRequest(server.URL, "9", "secret", target, output)
	var result struct {
		Error      string `json:"error"`
		Complete   bool   `json:"complete"`
		Downloaded int64  `json:"downloaded"`
	}
	if e := json.Unmarshal([]byte(raw), &result); e != nil {
		t.Fatal(e)
	}
	if result.Error != "" || !result.Complete || result.Downloaded != int64(len(payload)) || inParallel.Load() != 2 {
		t.Fatal(raw, inParallel.Load())
	}
	got, e := os.ReadFile(output)
	if e != nil || !bytes.Equal(got, payload) {
		t.Fatal("corrupted APK", e)
	}
}
