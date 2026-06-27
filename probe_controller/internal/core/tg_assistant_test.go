package core

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
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
