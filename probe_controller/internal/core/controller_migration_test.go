package core

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZipControllerUserDataDirSkipsRuntimeAndBackupArtifacts(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(source, backupDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "task_history"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		mainStoreFile:                           `{"data":{}}`,
		probeConfigStoreFile:                    `{"probe_nodes":[]}`,
		"root_ca.crt.pem":                       "cert",
		filepath.Join(backupDirName, "old.zip"): "old",
		filepath.Join(".cache", "x"):            "cache",
		filepath.Join("task_history", "1.json"): "history",
		"probe_controller.log":                  "log",
		"cloudhelper.json.tmp":                  "tmp",
	}
	for rel, content := range files {
		path := filepath.Join(source, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	archivePath := filepath.Join(dir, "backup.zip")
	if err := zipControllerUserDataDir(source, archivePath); err != nil {
		t.Fatalf("zipControllerUserDataDir returned error: %v", err)
	}
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{mainStoreFile, probeConfigStoreFile, "root_ca.crt.pem"} {
		if !names[want] {
			t.Fatalf("expected %s in archive, names=%v", want, names)
		}
	}
	for name := range names {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, backupDirName+"/") || strings.HasPrefix(lower, ".cache/") || strings.HasPrefix(lower, "task_history/") || strings.HasSuffix(lower, ".log") || strings.HasSuffix(lower, ".tmp") {
			t.Fatalf("runtime artifact %s should not be archived; names=%v", name, names)
		}
	}
}

func TestWriteControllerMigrationScriptWithArchiveEmbedsData(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "data.zip")
	if err := os.WriteFile(archivePath, []byte("zip-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dir, "migration.sh")
	if err := writeControllerMigrationScriptWithArchive(scriptPath, archivePath); err != nil {
		t.Fatalf("writeControllerMigrationScriptWithArchive returned error: %v", err)
	}
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "__CLOUDHELPER_CONTROLLER_DATA_ARCHIVE_BELOW__") {
		t.Fatalf("script missing archive marker")
	}
	if !strings.Contains(text, "emlwLWJ5dGVz") {
		t.Fatalf("script missing embedded base64 data: %q", text)
	}
	if strings.Contains(text, "MIGRATION_TOKEN") {
		t.Fatalf("self-contained script should not require migration token")
	}
}
