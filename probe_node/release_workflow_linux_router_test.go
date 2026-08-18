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
}
