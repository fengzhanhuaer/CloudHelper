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
