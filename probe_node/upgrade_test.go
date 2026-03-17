package main

import "testing"

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
