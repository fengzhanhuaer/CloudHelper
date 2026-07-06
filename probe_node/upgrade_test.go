package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestPickProbeNodeAssetPrefersWorkflowPrefixName(t *testing.T) {
	assets := []releaseAsset{
		{Name: "cloudhelper-probe-node-alpine-amd64", DownloadURL: "https://example.com/alpine"},
		{Name: "cloudhelper-probe-node-linux-amd64.tar.gz", DownloadURL: "https://example.com/linux-tar"},
		{Name: "cloudhelper-probe-node-linux-amd64", DownloadURL: "https://example.com/linux"},
	}

	platform := runtimePlatformInfo{
		GOOS:   "linux",
		GOARCH: "amd64",
	}

	selected, err := pickProbeNodeAsset(assets, platform)
	if err != nil {
		t.Fatalf("pickProbeNodeAsset returned error: %v", err)
	}
	if selected.Name != "cloudhelper-probe-node-linux-amd64" {
		t.Fatalf("expected workflow prefix exact name asset, got %q", selected.Name)
	}
}

func TestPickProbeNodeAssetSelectsAndroidAPK(t *testing.T) {
	assets := []releaseAsset{
		{Name: "cloudhelper-probe-node-linux-arm64", DownloadURL: "https://example.com/linux"},
		{Name: "cloudhelper-probe-node-android-arm64.apk", DownloadURL: "https://example.com/android"},
	}

	platform := runtimePlatformInfo{
		GOOS:   "android",
		GOARCH: "arm64",
	}

	selected, err := pickProbeNodeAsset(assets, platform)
	if err != nil {
		t.Fatalf("pickProbeNodeAsset returned error: %v", err)
	}
	if selected.Name != "cloudhelper-probe-node-android-arm64.apk" {
		t.Fatalf("expected android apk asset, got %q", selected.Name)
	}
}

func TestPickProbeNodeAssetPrefersLinuxOnGlibc(t *testing.T) {
	assets := []releaseAsset{
		{Name: "cloudhelper-probe-node-alpine-amd64.tar.gz", DownloadURL: "https://example.com/alpine"},
		{Name: "cloudhelper-probe-node-amd64.tar.gz", DownloadURL: "https://example.com/generic"},
		{Name: "cloudhelper-probe-node-linux-amd64.tar.gz", DownloadURL: "https://example.com/linux"},
	}

	platform := runtimePlatformInfo{
		GOOS:   "linux",
		GOARCH: "amd64",
		IsMusl: false,
		Libc:   "glibc-or-static",
	}

	selected, err := pickProbeNodeAsset(assets, platform)
	if err != nil {
		t.Fatalf("pickProbeNodeAsset returned error: %v", err)
	}
	if selected.Name != "cloudhelper-probe-node-linux-amd64.tar.gz" {
		t.Fatalf("expected linux asset, got %q", selected.Name)
	}
}

func TestPickProbeNodeAssetPrefersLinuxOnAlpineWhenBothExist(t *testing.T) {
	assets := []releaseAsset{
		{Name: "cloudhelper-probe-node-alpine-amd64.tar.gz", DownloadURL: "https://example.com/alpine"},
		{Name: "cloudhelper-probe-node-linux-amd64.tar.gz", DownloadURL: "https://example.com/linux"},
	}

	platform := runtimePlatformInfo{
		GOOS:     "linux",
		GOARCH:   "amd64",
		IsAlpine: true,
		IsMusl:   true,
		Libc:     "musl",
	}

	selected, err := pickProbeNodeAsset(assets, platform)
	if err != nil {
		t.Fatalf("pickProbeNodeAsset returned error: %v", err)
	}
	if selected.Name != "cloudhelper-probe-node-linux-amd64.tar.gz" {
		t.Fatalf("expected linux asset, got %q", selected.Name)
	}
}

func TestPickProbeNodeAssetFallsBackToAlpineWhenLinuxNameMissing(t *testing.T) {
	assets := []releaseAsset{
		{Name: "cloudhelper-probe-node-alpine-amd64.tar.gz", DownloadURL: "https://example.com/alpine"},
		{Name: "cloudhelper-probe-node-amd64.tar.gz", DownloadURL: "https://example.com/generic"},
	}

	platform := runtimePlatformInfo{
		GOOS:     "linux",
		GOARCH:   "amd64",
		IsAlpine: true,
		IsMusl:   true,
		Libc:     "musl",
	}

	selected, err := pickProbeNodeAsset(assets, platform)
	if err != nil {
		t.Fatalf("pickProbeNodeAsset returned error: %v", err)
	}
	if selected.Name != "cloudhelper-probe-node-alpine-amd64.tar.gz" {
		t.Fatalf("expected alpine fallback asset, got %q", selected.Name)
	}
}

func TestNormalizeExecutablePathForUpgradeTarget(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain binary path",
			in:   "/opt/cloudhelper/probe_node/probe_node",
			want: "/opt/cloudhelper/probe_node/probe_node",
		},
		{
			name: "single bak suffix",
			in:   "/opt/cloudhelper/probe_node/probe_node.bak",
			want: "/opt/cloudhelper/probe_node/probe_node",
		},
		{
			name: "multiple bak suffixes",
			in:   "/opt/cloudhelper/probe_node/probe_node.bak.bak.bak",
			want: "/opt/cloudhelper/probe_node/probe_node",
		},
		{
			name: "mixed case bak suffixes",
			in:   "C:\\cloudhelper\\probe_node\\probe_node.BAK.bAk",
			want: "C:\\cloudhelper\\probe_node\\probe_node",
		},
		{
			name: "timestamp backup suffix",
			in:   "/opt/cloudhelper/probe_node/probe_node.bak.20260317084600",
			want: "/opt/cloudhelper/probe_node/probe_node",
		},
		{
			name: "timestamp and bak suffix sequence",
			in:   "/opt/cloudhelper/probe_node/probe_node.bak.20260317084600.bak.bak",
			want: "/opt/cloudhelper/probe_node/probe_node",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeExecutablePathForUpgradeTarget(tc.in)
			if got != tc.want {
				t.Fatalf("normalizeExecutablePathForUpgradeTarget(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLooksLikeLegacyUpgradeBackup(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		fileName string
		want     bool
	}{
		{name: "keep current binary", base: "probe_node", fileName: "probe_node", want: false},
		{name: "keep single bak", base: "probe_node", fileName: "probe_node.bak", want: false},
		{name: "remove repeated bak", base: "probe_node", fileName: "probe_node.bak.bak", want: true},
		{name: "remove timestamp backup", base: "probe_node", fileName: "probe_node.bak.20260317084600", want: true},
		{name: "remove timestamp bak sequence", base: "probe_node", fileName: "probe_node.bak.20260317084600.bak", want: true},
		{name: "ignore unrelated file", base: "probe_node", fileName: "probe_node.failed-20260317", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := looksLikeLegacyUpgradeBackup(tc.base, tc.fileName)
			if got != tc.want {
				t.Fatalf("looksLikeLegacyUpgradeBackup(base=%q, file=%q)=%v, want %v", tc.base, tc.fileName, got, tc.want)
			}
		})
	}
}

func TestFindProbeBinaryRuntimeAwareExeSelection(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "cloudhelper-probe-node")
	exe := filepath.Join(root, "cloudhelper-probe-node.exe")

	if err := os.WriteFile(plain, []byte("plain"), 0o644); err != nil {
		t.Fatalf("write plain candidate: %v", err)
	}
	if err := os.WriteFile(exe, []byte("exe"), 0o644); err != nil {
		t.Fatalf("write exe candidate: %v", err)
	}

	selected, err := findProbeBinary(root)
	if err != nil {
		t.Fatalf("findProbeBinary returned error: %v", err)
	}

	if runtime.GOOS == "windows" {
		if selected != exe {
			t.Fatalf("expected windows to select exe candidate, got %q", selected)
		}
		return
	}
	if selected != plain {
		t.Fatalf("expected non-windows to select plain candidate, got %q", selected)
	}
}

func TestNormalizeUpgradeVerifyDurationSec(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "too small", in: 0, want: minUpgradeVerifyDurationSec},
		{name: "lower bound", in: minUpgradeVerifyDurationSec, want: minUpgradeVerifyDurationSec},
		{name: "middle", in: 23, want: 23},
		{name: "too large", in: maxUpgradeVerifyDurationSec + 10, want: maxUpgradeVerifyDurationSec},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeUpgradeVerifyDurationSec(tc.in); got != tc.want {
				t.Fatalf("normalizeUpgradeVerifyDurationSec(%d)=%d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestTrimUpgradeVerifyOutputForLog(t *testing.T) {
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
			got := trimUpgradeVerifyOutputForLog(tc.raw, tc.limit)
			if got != tc.want {
				t.Fatalf("trimUpgradeVerifyOutputForLog(%q, %d)=%q, want %q", string(tc.raw), tc.limit, got, tc.want)
			}
		})
	}
}

func TestProbeUpgradeDownloadRetryDelayCaps(t *testing.T) {
	if got := probeUpgradeDownloadRetryDelay(1); got != 2*time.Second {
		t.Fatalf("retry delay attempt 1=%s, want 2s", got)
	}
	if got := probeUpgradeDownloadRetryDelay(99); got != 15*time.Second {
		t.Fatalf("retry delay should cap at 15s, got %s", got)
	}
}

func TestIsProbeTransientHTTPErrorTreatsUnexpectedEOFAsRetryable(t *testing.T) {
	if !isProbeTransientHTTPError(io.ErrUnexpectedEOF) {
		t.Fatalf("unexpected EOF should be retryable")
	}
}

func TestShouldFallbackProbeUpgradeDownloadToProxy(t *testing.T) {
	if !shouldFallbackProbeUpgradeDownloadToProxy("direct", "https://controller.example.com", io.ErrUnexpectedEOF) {
		t.Fatalf("direct transient download error should fallback to proxy")
	}
	if shouldFallbackProbeUpgradeDownloadToProxy("proxy", "https://controller.example.com", io.ErrUnexpectedEOF) {
		t.Fatalf("proxy mode should not fallback again")
	}
	if shouldFallbackProbeUpgradeDownloadToProxy("direct", "", io.ErrUnexpectedEOF) {
		t.Fatalf("direct mode without controller should not fallback")
	}
	if shouldFallbackProbeUpgradeDownloadToProxy("direct", "https://controller.example.com", errors.New("download failed: 404")) {
		t.Fatalf("non-transient error should not fallback")
	}
}

func TestCopyProbeUpgradeWithProgressFailsAfterIdleTimeout(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	var dst bytes.Buffer
	start := time.Now()
	_, err := copyProbeUpgradeWithProgressAndIdleTimeout(&dst, pr, 20*time.Millisecond, func() {
		_ = pw.CloseWithError(errors.New("idle"))
	}, nil)
	if !errors.Is(err, errProbeUpgradeDownloadIdleTimeout) {
		t.Fatalf("error=%v, want idle timeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("idle timeout took too long: %s", elapsed)
	}
}

func TestDownloadProbeAssetResumeDirect(t *testing.T) {
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
	output := filepath.Join(dir, "probe-node.bin")
	if err := os.WriteFile(output+".part", partial, 0o644); err != nil {
		t.Fatalf("write part file: %v", err)
	}

	if err := downloadProbeAsset(t.Context(), "direct", server.URL, "", nodeIdentity{}, output, nil); err != nil {
		t.Fatalf("downloadProbeAsset returned error: %v", err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("unexpected output content: %q", string(got))
	}
	if _, err := os.Stat(output + ".part"); !os.IsNotExist(err) {
		t.Fatalf("expected part file removed, got err=%v", err)
	}
}

func TestDownloadProbeAssetRetriesUnexpectedEOFAndResumes(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		switch requestCount {
		case 1:
			if got := r.Header.Get("Range"); got != "" {
				t.Fatalf("first request should not use range, got %q", got)
			}
			w.Header().Set("Content-Length", "11")
			_, _ = w.Write([]byte("hello "))
		case 2:
			if got := r.Header.Get("Range"); got != "bytes=6-" {
				t.Fatalf("resume range=%q, want bytes=6-", got)
			}
			w.Header().Set("Content-Length", "5")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("world"))
		default:
			t.Fatalf("unexpected extra request %d", requestCount)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	output := filepath.Join(dir, "probe-node.bin")
	if err := downloadProbeAsset(t.Context(), "direct", server.URL, "", nodeIdentity{}, output, nil); err != nil {
		t.Fatalf("downloadProbeAsset returned error: %v", err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("unexpected output content: %q", string(got))
	}
	if requestCount != 2 {
		t.Fatalf("requestCount=%d, want 2", requestCount)
	}
}

func TestDownloadProbeAssetStreamsThroughController(t *testing.T) {
	partial := []byte("hello ")
	remaining := []byte("proxy")
	var gotPath string
	var gotRange string
	var gotNodeID string
	var gotURL string
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRange = r.Header.Get("Range")
		gotNodeID = r.Header.Get("X-Probe-Node-Id")
		gotURL = r.URL.Query().Get("url")
		w.Header().Set("Content-Length", strconv.Itoa(len(remaining)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(remaining)
	}))
	defer controller.Close()

	dir := t.TempDir()
	output := filepath.Join(dir, "probe-node.bin")
	if err := os.WriteFile(output+".part", partial, 0o644); err != nil {
		t.Fatalf("write part file: %v", err)
	}

	assetURL := "https://github.com/example/repo/releases/download/v1/probe-node"
	if err := downloadProbeAsset(t.Context(), "proxy", assetURL, controller.URL, nodeIdentity{NodeID: "9", Secret: "secret-9"}, output, nil); err != nil {
		t.Fatalf("downloadProbeAsset returned error: %v", err)
	}
	if gotPath != "/api/probe/proxy/download" {
		t.Fatalf("proxy path=%q", gotPath)
	}
	if gotRange != "bytes=6-" {
		t.Fatalf("range=%q", gotRange)
	}
	if gotNodeID != "9" {
		t.Fatalf("node id header=%q", gotNodeID)
	}
	if gotURL != assetURL {
		t.Fatalf("proxied url=%q want=%q", gotURL, assetURL)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != "hello proxy" {
		t.Fatalf("unexpected output content: %q", string(got))
	}
}
