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
