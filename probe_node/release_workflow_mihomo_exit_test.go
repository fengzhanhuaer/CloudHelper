package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseWorkflowDefinesMihomoExitLinuxAMD64Artifacts(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test source path")
	}
	workflowPath := filepath.Join(filepath.Dir(sourcePath), "..", ".github", "workflows", "release.yml")
	raw, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("release workflow YAML is invalid: %v", err)
	}
	content := string(raw)
	for _, marker := range []string{
		"build-probe-exit-node:",
		"build-probe-exit-node-docker:",
		"GOOS: linux",
		"GOARCH: amd64",
		"go build -tags mihomo_exit",
		"cloudhelper-probe-exit-node-linux-amd64",
		"cloudhelper-probe-exit-node-manifest.json",
		"cloudhelper-probe-exit-node-shell",
	} {
		if !strings.Contains(content, marker) {
			t.Fatalf("release workflow missing %q", marker)
		}
	}
}
