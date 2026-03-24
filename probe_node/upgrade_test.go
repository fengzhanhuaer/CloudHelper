package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
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
			name: "timestamp and bak suffix chain",
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
		{name: "remove timestamp bak chain", base: "probe_node", fileName: "probe_node.bak.20260317084600.bak", want: true},
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
