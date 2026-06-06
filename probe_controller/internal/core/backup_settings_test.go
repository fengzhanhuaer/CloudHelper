package core

import (
	"os"
	"path/filepath"
	"testing"
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
