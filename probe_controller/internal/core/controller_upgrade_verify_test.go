package core

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeControllerUpgradeVerifyDurationSec(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "too small", in: 0, want: minControllerUpgradeVerifyDurationSec},
		{name: "lower bound", in: minControllerUpgradeVerifyDurationSec, want: minControllerUpgradeVerifyDurationSec},
		{name: "middle", in: 19, want: 19},
		{name: "too large", in: maxControllerUpgradeVerifyDurationSec + 10, want: maxControllerUpgradeVerifyDurationSec},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeControllerUpgradeVerifyDurationSec(tc.in); got != tc.want {
				t.Fatalf("normalizeControllerUpgradeVerifyDurationSec(%d)=%d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseControllerUpgradeVerifyOptions(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		opts, enabled, err := parseControllerUpgradeVerifyOptions([]string{"--listen=:15030"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if enabled {
			t.Fatalf("expected disabled verify mode, got enabled")
		}
		if opts.DurationSec != defaultControllerUpgradeVerifyDurationSec {
			t.Fatalf("unexpected default duration: %d", opts.DurationSec)
		}
	})

	t.Run("enabled with inline duration", func(t *testing.T) {
		opts, enabled, err := parseControllerUpgradeVerifyOptions([]string{"--upgrade-verify", "--upgrade-verify-duration=33"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !enabled {
			t.Fatalf("expected verify mode enabled")
		}
		if opts.DurationSec != 33 {
			t.Fatalf("unexpected duration: %d", opts.DurationSec)
		}
	})

	t.Run("enabled with separated duration", func(t *testing.T) {
		opts, enabled, err := parseControllerUpgradeVerifyOptions([]string{"--upgrade-verify-duration", "2"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !enabled {
			t.Fatalf("expected verify mode enabled")
		}
		if opts.DurationSec != minControllerUpgradeVerifyDurationSec {
			t.Fatalf("expected normalized duration=%d, got %d", minControllerUpgradeVerifyDurationSec, opts.DurationSec)
		}
	})

	t.Run("invalid duration", func(t *testing.T) {
		_, enabled, err := parseControllerUpgradeVerifyOptions([]string{"--upgrade-verify-duration=abc"})
		if !enabled {
			t.Fatalf("expected verify mode to be recognized as enabled")
		}
		if err == nil {
			t.Fatalf("expected parse error")
		}
	})
}

func TestTrimControllerUpgradeVerifyOutputForLog(t *testing.T) {
	cases := []struct {
		name  string
		raw   []byte
		limit int
		want  string
	}{
		{
			name:  "empty",
			raw:   []byte("   "),
			limit: 8,
			want:  "",
		},
		{
			name:  "within limit",
			raw:   []byte("hello"),
			limit: 8,
			want:  "hello",
		},
		{
			name:  "truncate with suffix",
			raw:   []byte("123456789"),
			limit: 8,
			want:  "12345...",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trimControllerUpgradeVerifyOutputForLog(tc.raw, tc.limit)
			if got != tc.want {
				t.Fatalf("trimControllerUpgradeVerifyOutputForLog(%q, %d)=%q, want %q", string(tc.raw), tc.limit, got, tc.want)
			}
		})
	}
}

func TestDownloadReleaseAssetResume(t *testing.T) {
	partial := []byte("hello ")
	remaining := []byte("world")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got != "bytes=6-" {
			t.Fatalf("unexpected range header: %q", got)
		}
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(remaining)
	}))
	defer server.Close()

	dir := t.TempDir()
	output := filepath.Join(dir, "controller.bin")
	if err := os.WriteFile(output+".part", partial, 0o644); err != nil {
		t.Fatalf("write part file: %v", err)
	}

	var progressDownloaded, progressTotal int64
	if err := downloadReleaseAsset(t.Context(), server.URL, output, func(downloaded, total int64) {
		progressDownloaded, progressTotal = downloaded, total
	}); err != nil {
		t.Fatalf("downloadReleaseAsset returned error: %v", err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("unexpected output content: %q", string(got))
	}
	if progressDownloaded != 11 || progressTotal != 11 {
		t.Fatalf("unexpected progress downloaded=%d total=%d", progressDownloaded, progressTotal)
	}
}

func TestProbeProxyDownloadHandlerPassesRange(t *testing.T) {
	oldToken := os.Getenv("GITHUB_TOKEN")
	_ = os.Unsetenv("GITHUB_TOKEN")
	defer func() { _ = os.Setenv("GITHUB_TOKEN", oldToken) }()

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got != "bytes=10-" {
			t.Fatalf("unexpected range header: %q", got)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Range", "bytes 10-14/15")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "world")
	}))
	defer upstream.Close()

	oldClient := http.DefaultClient
	http.DefaultClient = upstream.Client()
	defer func() { http.DefaultClient = oldClient }()

	req := httptest.NewRequest(http.MethodGet, "/api/probe/proxy/download?url="+upstream.URL+"&node_id=1&secret=test-secret", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Range", "bytes=10-")
	w := httptest.NewRecorder()

	if ProbeStore == nil {
		ProbeStore = &probeConfigStore{data: probeConfigData{ProbeSecrets: map[string]string{}}}
	}
	ProbeStore.mu.Lock()
	oldNodes := append([]probeNodeRecord(nil), ProbeStore.data.ProbeNodes...)
	oldSecrets := make(map[string]string, len(ProbeStore.data.ProbeSecrets))
	for key, value := range ProbeStore.data.ProbeSecrets {
		oldSecrets[key] = value
	}
	ProbeStore.data.ProbeNodes = []probeNodeRecord{{NodeNo: 1, NodeName: "node-1", NodeSecret: "test-secret"}}
	ProbeStore.data.ProbeSecrets = map[string]string{"1": "test-secret"}
	ProbeStore.mu.Unlock()
	defer func() {
		ProbeStore.mu.Lock()
		ProbeStore.data.ProbeNodes = oldNodes
		ProbeStore.data.ProbeSecrets = oldSecrets
		ProbeStore.mu.Unlock()
	}()

	ProbeProxyDownloadHandler(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 10-14/15" {
		t.Fatalf("unexpected content-range: %q", got)
	}
}
