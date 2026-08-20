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
		"cloudhelper-probe-router-linux-arm64",
		"cloudhelper-probe-router-linux-amd64-manifest.json",
		"cloudhelper-probe-router-linux-arm64-manifest.json",
		"mihomo-linux-amd64-compatible-v1.19.29.gz",
		"mihomo-linux-arm64-v1.19.29.gz",
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
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("release workflow still contains standalone Mihomo marker %q", forbidden)
		}
	}
}
