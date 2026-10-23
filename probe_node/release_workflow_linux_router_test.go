package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseWorkflowDefinesLinuxRouterArtifacts(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test source path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(sourcePath), "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("release workflow YAML is invalid: %v", err)
	}
	content := string(raw)
	for _, marker := range []string{
		"build-probe-router:",
		"go build -tags linux_router",
		"cloudhelper-probe-router-linux-amd64",
		"cloudhelper-probe-router-linux-amd64-manifest.json",
		"mihomo-linux-amd64-compatible-v1.19.29.gz",
		"cloudhelper-probe-node-linux-arm64",
		"cloudhelper-probe-node-android-arm64.apk",
		"Keep Latest 10 Releases",
		"workflow_dispatch:",
		"Explicit release version must use major.minor.patch format",
		"[skip release]",
		"gh api --paginate",
		"select(.draft == true)",
		"reverse | .[10:]",
		"gh release delete",
		"--cleanup-tag --yes",
		"Delete was rate-limited",
	} {
		if !strings.Contains(content, marker) {
			t.Fatalf("release workflow missing %q", marker)
		}
	}
	for _, forbidden := range []string{
		"build-probe-exit-node:",
		"build-probe-exit-node-docker:",
		"go build -tags mihomo_exit",
		"cloudhelper-probe-exit-node-linux-amd64",
		"cloudhelper-probe-exit-node-manifest.json",
		"cloudhelper-probe-exit-node-shell",
		"build-probe-node-docker",
		"docker/setup-buildx-action",
		"docker/login-action",
		"docker/build-push-action",
		"packages: write",
		"ghcr.io/",
		"cloudhelper-probe-router-linux-arm64",
		"mihomo-linux-arm64",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("release workflow still contains retired release marker %q", forbidden)
		}
	}
	for _, action := range []string{
		"actions/checkout@v7",
		"actions/setup-go@v7",
		"actions/setup-java@v5",
		"actions/cache@v6",
		"actions/upload-artifact@v7",
		"actions/download-artifact@v8",
		"android-actions/setup-android@v4",
		"gradle/actions/setup-gradle@v6",
		"softprops/action-gh-release@v3",
	} {
		if !strings.Contains(content, action) {
			t.Fatalf("release workflow missing Node.js 24 action %q", action)
		}
	}
	for _, action := range []string{
		"actions/checkout@v4",
		"actions/setup-go@v5",
		"actions/setup-java@v4",
		"actions/cache@v4",
		"actions/upload-artifact@v4",
		"actions/download-artifact@v4",
		"android-actions/setup-android@v3",
		"gradle/actions/setup-gradle@v4",
		"docker/setup-buildx-action@v3",
		"docker/login-action@v3",
		"docker/build-push-action@v6",
		"softprops/action-gh-release@v2",
	} {
		if strings.Contains(content, action) {
			t.Fatalf("release workflow still contains Node.js 20 action %q", action)
		}
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs yaml.Node `yaml:"needs"`
			Steps []struct {
				Name string `yaml:"name"`
				With struct {
					Files string `yaml:"files"`
				} `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := document.Decode(&workflow); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"prepare-release", "build-probe-controller", "build-probe-node", "build-probe-router", "build-probe-node-android", "build-asus-router-plugins", "create-release"} {
		if _, ok := workflow.Jobs[name]; !ok {
			t.Fatalf("release workflow missing required job %q", name)
		}
	}
	for name, job := range workflow.Jobs {
		var dependencies []string
		switch job.Needs.Kind {
		case yaml.ScalarNode:
			dependencies = []string{job.Needs.Value}
		case yaml.SequenceNode:
			if err := job.Needs.Decode(&dependencies); err != nil {
				t.Fatal(err)
			}
		}
		for _, dependency := range dependencies {
			if _, ok := workflow.Jobs[dependency]; !ok {
				t.Fatalf("job %q depends on missing job %q", name, dependency)
			}
		}
	}
	var files []string
	for _, step := range workflow.Jobs["create-release"].Steps {
		if step.Name == "Create Release" {
			files = strings.Fields(step.With.Files)
		}
	}
	expected := []string{
		"cloudhelper-probe-controller-linux-amd64",
		"cloudhelper-probe-node-linux-amd64",
		"cloudhelper-probe-node-linux-arm64",
		"cloudhelper-probe-node-windows-amd64.exe",
		"cloudhelper-probe-router-linux-amd64",
		"cloudhelper-probe-router-linux-amd64-manifest.json",
		"mihomo-LICENSE",
		"cloudhelper-probe-node-android-arm64.apk",
		"cloudhelper-asus56uv2-jffs.tar.gz",
		"cloudhelper-asus56uv2-jffs.tar.gz.json",
	}
	if len(files) != len(expected) {
		t.Fatalf("release assets=%v, want %d assets", files, len(expected))
	}
	for _, asset := range expected {
		found := false
		for _, file := range files {
			found = found || file == "release-assets/"+asset
		}
		if !found {
			t.Fatalf("missing release asset %q", asset)
		}
	}
}
