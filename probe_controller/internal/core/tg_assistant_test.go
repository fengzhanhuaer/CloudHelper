package core

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/gotd/td/tg"
)

func TestBuildTGAssistantAccountViewIncludesSessionToken(t *testing.T) {
	chdirTemp(t)

	accountID := "tg-session-token-test"
	sessionContent := []byte(`{"Version":1,"Data":{"DC":2}}`)
	sessionPath := tgAssistantSessionPath(accountID)
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(sessionPath, sessionContent, 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	view := buildTGAssistantAccountView(tgAssistantAccountRecord{ID: accountID, Phone: "+10000000000"}, 12345)
	want := base64.StdEncoding.EncodeToString(sessionContent)
	if view.SessionToken != want {
		t.Fatalf("session token = %q, want %q", view.SessionToken, want)
	}
}

func TestDecodeTGAssistantSessionToken(t *testing.T) {
	raw := []byte(`{"Version":1,"Data":{"DC":4}}`)
	token := base64.StdEncoding.EncodeToString(raw)
	decoded, err := decodeTGAssistantSessionToken(token)
	if err != nil {
		t.Fatalf("decode session token: %v", err)
	}
	if string(decoded) != string(raw) {
		t.Fatalf("decoded = %q, want %q", decoded, raw)
	}

	if _, err := decodeTGAssistantSessionToken("!!!!"); err == nil {
		t.Fatal("invalid token should fail")
	}
}

func TestBuildTGAssistantTargetsFiltersArchivedDialogs(t *testing.T) {
	targets := buildTGAssistantTargets(
		[]tg.DialogClass{
			&tg.Dialog{Peer: &tg.PeerUser{UserID: 1001}},
			func() *tg.Dialog {
				dialog := &tg.Dialog{Peer: &tg.PeerUser{UserID: 1002}}
				dialog.SetFolderID(tgAssistantArchivedFolderID)
				return dialog
			}(),
		},
		nil,
		[]tg.UserClass{
			&tg.User{ID: 1001, FirstName: "Visible"},
			&tg.User{ID: 1002, FirstName: "Archived"},
		},
	)
	if len(targets) != 2 {
		t.Fatalf("targets=%d, want 2", len(targets))
	}
	if !targets[1].Archived {
		t.Fatal("expected archived dialog to be marked")
	}

	filtered := filterTGAssistantTargets(targets)
	if len(filtered) != 1 {
		t.Fatalf("filtered=%d, want 1", len(filtered))
	}
	if filtered[0].ID != "user:1001" {
		t.Fatalf("filtered[0]=%q, want user:1001", filtered[0].ID)
	}
}

func TestLoadTGAssistantTargetsFiltersArchivedFlag(t *testing.T) {
	chdirTemp(t)

	path := tgAssistantTargetsPath("acct")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir targets dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`[
		{"id":"user:1","name":"visible","type":"user"},
		{"id":"user:2","name":"archived","type":"user","archived":true}
	]`), 0o644); err != nil {
		t.Fatalf("write targets file: %v", err)
	}

	targets, err := loadTGAssistantTargetsFromFile("acct")
	if err != nil {
		t.Fatalf("load targets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets=%d, want 1", len(targets))
	}
	if targets[0].ID != "user:1" {
		t.Fatalf("targets[0]=%q, want user:1", targets[0].ID)
	}
}
