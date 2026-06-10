package main

import (
	"archive/zip"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbeSyncScheduleAndRemoteFolder(t *testing.T) {
	if got := normalizeProbeSyncSchedule("daily"); got != probeSyncScheduleDaily {
		t.Fatalf("daily schedule=%q", got)
	}
	if got := normalizeProbeSyncSchedule("每周"); got != probeSyncScheduleWeekly {
		t.Fatalf("weekly schedule=%q", got)
	}
	if got := normalizeProbeSyncSchedule("month"); got != probeSyncScheduleMonthly {
		t.Fatalf("monthly schedule=%q", got)
	}

	remote := probeSyncRemoteFolderPath("CloudHelper/probe_node", "node/a b")
	if remote != "CloudHelper/probe_node/node_a_b" {
		t.Fatalf("remote folder=%q", remote)
	}
}

func TestProbeSyncIsDueBySchedule(t *testing.T) {
	now := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)
	last := now.AddDate(0, 0, -6).UTC().Format(time.RFC3339)
	if probeSyncIsDue(probeSyncSettings{Enabled: true, Schedule: probeSyncScheduleWeekly, LastSuccessAt: last}, now) {
		t.Fatal("weekly sync should not be due before seven days")
	}
	last = now.AddDate(0, 0, -7).UTC().Format(time.RFC3339)
	if !probeSyncIsDue(probeSyncSettings{Enabled: true, Schedule: probeSyncScheduleWeekly, LastSuccessAt: last}, now) {
		t.Fatal("weekly sync should be due after seven days")
	}
	last = now.AddDate(0, -1, 1).UTC().Format(time.RFC3339)
	if probeSyncIsDue(probeSyncSettings{Enabled: true, Schedule: probeSyncScheduleMonthly, LastSuccessAt: last}, now) {
		t.Fatal("monthly sync should not be due before one month")
	}
}

func TestZipProbeSyncSourcesIncludesFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir docs failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatalf("write dir file failed: %v", err)
	}
	filePath := filepath.Join(root, "single.txt")
	if err := os.WriteFile(filePath, []byte("single"), 0o644); err != nil {
		t.Fatalf("write single file failed: %v", err)
	}
	archivePath := filepath.Join(root, "sync.zip")
	if err := zipProbeSyncSources([]string{dir, filePath}, archivePath); err != nil {
		t.Fatalf("zip sync sources failed: %v", err)
	}
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open zip failed: %v", err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["001-docs/a.txt"] {
		t.Fatalf("zip missing directory file, names=%v", names)
	}
	if !names["002-single.txt"] {
		t.Fatalf("zip missing single file, names=%v", names)
	}
}

func TestCreateProbeSyncArchiveUsesConfiguredTempDir(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatalf("write source failed: %v", err)
	}
	tempParent := filepath.Join(root, "zip-temp")
	archivePath, archiveName, err := createProbeSyncArchive([]string{sourceDir}, tempParent, nodeIdentity{NodeID: "node-1"}, time.Now())
	if err != nil {
		t.Fatalf("create archive failed: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(archivePath))
	if !strings.HasPrefix(archivePath, tempParent+string(os.PathSeparator)) {
		t.Fatalf("archive path=%q should be under temp parent=%q", archivePath, tempParent)
	}
	if !strings.HasPrefix(archiveName, "probe-sync-node-1-") || !strings.HasSuffix(archiveName, ".zip") {
		t.Fatalf("archive name=%q", archiveName)
	}
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("archive should exist: %v", err)
	}
}

func TestProbeLocalSyncStatusAndSettingsAPI(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")
	sourceDir := t.TempDir()
	tempDir := t.TempDir()

	unauthorized := doProbeLocalRequest(t, mux, http.MethodGet, "/local/api/sync/status", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("sync/status without session status=%d", unauthorized.Code)
	}

	settingsResp := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/sync/settings", map[string]any{
		"enabled":              false,
		"source_paths":         []string{sourceDir},
		"local_temp_dir":       tempDir,
		"schedule":             "weekly",
		"google_client_id":     "client-id",
		"google_client_secret": "secret",
		"google_folder":        "CloudHelper/custom",
	}, sessionCookie)
	if settingsResp.Code != http.StatusOK {
		t.Fatalf("sync/settings status=%d body=%s", settingsResp.Code, settingsResp.Body.String())
	}
	payload := decodeProbeLocalJSON(t, settingsResp)
	settings, ok := payload["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings payload type=%T", payload["settings"])
	}
	if settings["schedule"] != probeSyncScheduleWeekly {
		t.Fatalf("schedule=%v", settings["schedule"])
	}
	if settings["local_temp_dir"] != tempDir {
		t.Fatalf("local_temp_dir=%v want=%s", settings["local_temp_dir"], tempDir)
	}
	paths, _ := payload["paths"].(map[string]any)
	remote, _ := paths["remote_folder"].(string)
	if !strings.Contains(remote, "CloudHelper/custom/") {
		t.Fatalf("remote folder=%q", remote)
	}
}
