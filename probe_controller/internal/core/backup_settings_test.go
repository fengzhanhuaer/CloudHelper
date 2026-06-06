package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestNormalizeBackupSourceDirsForStoreDedupesAndAbsolutizes(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := normalizeBackupSourceDirsForStore([]string{source, source, " "}, "")
	if err != nil {
		t.Fatalf("normalize source dirs failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("source dir count=%d, want 1: %v", len(got), got)
	}
	want, _ := filepath.Abs(source)
	if got[0] != want {
		t.Fatalf("source dir=%q, want %q", got[0], want)
	}
}

func TestNormalizeBackupSourceDirsRejectsBackupOverlap(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	backup := filepath.Join(source, "backup")
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := normalizeBackupSourceDirsForStore([]string{source}, backup); err == nil {
		t.Fatal("expected source/backup overlap to be rejected")
	}
}

func TestNormalizeGoogleDriveFolderPath(t *testing.T) {
	got := normalizeGoogleDriveFolderPath(` /CloudHelper//controller/ `)
	if got != "CloudHelper/controller" {
		t.Fatalf("folder=%q, want CloudHelper/controller", got)
	}
	if empty := normalizeGoogleDriveFolderPath(""); empty != defaultBackupGoogleFolder {
		t.Fatalf("empty folder=%q, want default", empty)
	}
}

func TestParseGoogleOAuthCredentialJSONInstalled(t *testing.T) {
	raw := `{"installed":{"client_id":"client.apps.googleusercontent.com","client_secret":"secret-value"}}`
	clientID, clientSecret, ok := parseGoogleOAuthCredentialJSON(raw)
	if !ok {
		t.Fatalf("expected credential json to parse")
	}
	if clientID != "client.apps.googleusercontent.com" {
		t.Fatalf("client_id=%q", clientID)
	}
	if clientSecret != "secret-value" {
		t.Fatalf("client_secret=%q", clientSecret)
	}
}

func TestNormalizeSubmittedGoogleOAuthCredentialsUsesSavedSecret(t *testing.T) {
	oldStore := Store
	storePath := filepath.Join(t.TempDir(), "store.json")
	Store = NewDataStoreForTest(storePath)
	Store.Data[backupGoogleClientSecretStoreField] = "saved-secret"
	defer func() {
		Store = oldStore
	}()

	clientID := "client.apps.googleusercontent.com"
	clientSecret := "(已保存)"
	normalizeSubmittedGoogleOAuthCredentials(&clientID, &clientSecret)

	if clientSecret != "saved-secret" {
		t.Fatalf("client_secret=%q, want saved-secret", clientSecret)
	}
}

func TestNormalizeSubmittedGoogleOAuthCredentialsJSONOverridesSavedSecret(t *testing.T) {
	oldStore := Store
	storePath := filepath.Join(t.TempDir(), "store.json")
	Store = NewDataStoreForTest(storePath)
	Store.Data[backupGoogleClientSecretStoreField] = "old-secret"
	defer func() {
		Store = oldStore
	}()

	clientID := `{"installed":{"client_id":"new-client.apps.googleusercontent.com","client_secret":"new-secret"}}`
	clientSecret := "(已保存)"
	normalizeSubmittedGoogleOAuthCredentials(&clientID, &clientSecret)

	if clientID != "new-client.apps.googleusercontent.com" {
		t.Fatalf("client_id=%q", clientID)
	}
	if clientSecret != "new-secret" {
		t.Fatalf("client_secret=%q, want new-secret", clientSecret)
	}
}

func TestGoogleTokenExchangeRetryWithoutSecretPolicy(t *testing.T) {
	missingSecretBody := []byte(`{"error":"invalid_client","error_description":"Missing required parameter: client_secret"}`)
	if googleTokenExchangeShouldRetryWithoutSecret(400, missingSecretBody, "secret") {
		t.Fatalf("missing client_secret must not retry without secret")
	}

	notAllowedBody := []byte(`{"error":"invalid_client","error_description":"client_secret is not allowed for this client"}`)
	if !googleTokenExchangeShouldRetryWithoutSecret(400, notAllowedBody, "secret") {
		t.Fatalf("not allowed client_secret should retry without secret")
	}
}

func TestSelectGoogleDriveBackupArchivesToDeleteUsesLocalTimeBuckets(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.Local)
	archives := []googleDriveBackupArchive{}
	add := func(prefix string, base time.Time, count int) {
		for i := 0; i < count; i++ {
			id := fmt.Sprintf("%s-%c", prefix, 'a'+i)
			archives = append(archives, googleDriveBackupArchive{
				ID:      id,
				Name:    backupArchivePrefix + id + ".zip",
				ModTime: base.Add(time.Duration(-i) * time.Minute),
			})
		}
	}

	add("today", now.Add(-1*time.Hour), 4)
	add("yesterday", now.AddDate(0, 0, -1), 4)
	add("lastweek", now.AddDate(0, 0, -7), 4)
	add("lastmonth", now.AddDate(0, -1, 0), 4)
	add("lastyear", now.AddDate(-1, 0, 0), 4)
	add("outside", now.AddDate(-2, 0, 0), 2)

	gotArchives := selectGoogleDriveBackupArchivesToDelete(archives, now)
	got := make([]string, 0, len(gotArchives))
	for _, archive := range gotArchives {
		got = append(got, archive.ID)
	}
	sort.Strings(got)

	want := []string{
		"lastmonth-d",
		"lastweek-d",
		"lastyear-d",
		"outside-a",
		"outside-b",
		"today-d",
		"yesterday-d",
	}
	if len(got) != len(want) {
		t.Fatalf("delete count=%d want=%d got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delete ids=%v want=%v", got, want)
		}
	}
}
